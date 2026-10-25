# agi

agi is a small LLM agent for the command line, built to behave like any other
unix program. You give it a prompt, it does the work, and it prints the answer.
It reads its input from an argument or a pipe. It writes the answer to standard
output, writes errors to standard error, and reports failure through its exit
status. You can call it from a shell script as easily as `grep` or `awk`, and
you can call it cheaply, many times over.

The idea behind it is to **sprinkle the LLM on top**. Most of a job is better
done by ordinary, deterministic code: it is faster, cheaper and does not make
things up. agi is for the part that really needs a model, which is reading,
judging and writing natural language. It should fit into a pipeline as one more
step, not take the pipeline over. So agi starts with as little as possible: a
short built-in system prompt that you can replace, no project files read behind
your back, and no assumptions about what the task is. Everything else you
choose per run.

The rest follows from that. A session is just a directory, so continuing a
conversation means passing the same directory again. Everything that happened
in a run is written to a log file you can read with `jq`. Your own tools,
hooks and skills live in a folder that you name on the command line. agi works
with any provider, because it only uses what every mainstream provider
supports: chat messages and tool calls.

The story of why and how it was built is in the blog post
[Making My Own AI Agent](https://blog.chrisvilches.com/custom-ai-agent/).

```
$ agi "what linux distribution is this and how much free disk is there? be brief"
I'll check the OS and disk usage.

[bash] Identify the Linux distribution and report free disk space.
  $ cat /etc/os-release | head -5; df -h /

**Arch Linux** (rolling release)
**Free disk on `/`:** 24G available (199G total, 165G used)
```

```
$ agi --schema '[user:str, pid:int]' "top 3 processes by memory" | jq -r '.[].pid'
4711
2280
918
```

## Contents

- [Requirements and build](#requirements-and-build)
- [Configuration](#configuration)
- [Usage](#usage)
- [Terminal and pipe](#terminal-and-pipe)
- [Answering in JSON](#answering-in-json)
- [Exit codes](#exit-codes)
- [Sessions](#sessions)
- [The system prompt](#the-system-prompt)
- [Bringing your own tools, hooks and skills](#bringing-your-own-tools-hooks-and-skills)
- [Subagents and forks](#subagents-and-forks)
- [Terminals](#terminals)
- [Limits](#limits)
- [The event log](#the-event-log)
- [Examples](#examples)
- [Layout](#layout)

## Requirements and build

- Go 1.26.5 or newer.
- Linux. agi does not build on macOS for now, because the terminal tools need
  Linux and are always compiled in. This is a known limitation.

```sh
go install .          # onto your PATH
go build -o bin/agi . # or into the repository (bin/ is ignored by git)
go build -ldflags "-X main.version=0.1.0" -o bin/agi .   # with a version
go test ./...
```

## Configuration

agi is configured with environment variables only. It reads no config files
and no dotfiles. Flags change a single run; variables describe the machine.

| Variable            | Required         | Meaning |
| ------------------- | ---------------- | ------- |
| `AGI_BACKEND`       | Yes              | `litellm` or `openai`. There is no default. |
| `AGI_DEFAULT_MODEL` | Unless `--model` | The model a run uses, spelled exactly as `agi models` prints it. There is no default; agi never picks a model for you. |
| `AGI_VERBOSE`       | No               | Same as `--verbose` / `-v`. |
| `LITELLM_URL`       | For `litellm`    | Base URL of a LiteLLM gateway, e.g. `http://localhost:4000`. |
| `LITELLM_KEY`       | No               | Bearer token for the gateway. |
| `OPENAI_URL`        | For `openai`     | Base URL of an OpenAI-compatible endpoint, e.g. `https://api.deepseek.com`. |
| `OPENAI_API_KEY`    | No               | Bearer token for that endpoint, which need not be OpenAI's own. |
| `TMPDIR`            | No               | Where new sessions and scratch directories are made. Defaults to `/tmp`. |
| `COLORTERM`, `TERM` | No               | Set by your terminal. They decide how colorful file diffs are; see [Diff colors](#diff-colors). |

```sh
# Talking to a provider directly
export AGI_BACKEND=openai
export OPENAI_URL=https://api.deepseek.com
export OPENAI_API_KEY=sk-...
export AGI_DEFAULT_MODEL=deepseek-flash

# Or through a LiteLLM gateway
export AGI_BACKEND=litellm
export LITELLM_URL=http://localhost:4000
export LITELLM_KEY=sk-...          # only if your gateway needs one
export AGI_DEFAULT_MODEL=deepseek-chat
```

Both backends speak the OpenAI Responses protocol, so they are one small
hand-written client with two configurations, and the transport uses only the
standard library. Running a gateway is up to you; `examples/litellm_config.yaml`
is a starting point.

### Models

`agi models` asks your backend what it serves. It does not start a run and
costs nothing:

```
$ agi models
MODEL             OWNED BY   CONTEXT   MAX OUTPUT
deepseek-flash    deepseek   1048576   393216
deepseek-v4-pro   deepseek   1048576   393216

AGI_DEFAULT_MODEL is deepseek-flash, which every run uses unless --model names another
```

The list is sorted by name, and a dash means the endpoint did not say. The
last line is for you, not for pipes: `agi models | awk '{print $1}'` gets only
the names.

`AGI_DEFAULT_MODEL` is the model this machine uses. `--model` picks one for a
single run:

```sh
agi --model deepseek-v4-pro "review this design and be hard on it"
agi --model anthropic/claude-sonnet-5 "summarize this computer's status"
```

Names are sent exactly as you write them. A gateway lists some models bare and
some with a routing prefix; both are fine. agi does not check the name against
the list, because the list is a guide and not a guarantee: aliases work even
though they are not listed, and a gateway can list models your account cannot
use. If the endpoint rejects the name, agi tells you to run `agi models`.

`models` is the only word that cannot be a prompt by itself. `agi "which models
are best?"` and `echo models | agi` both still run the agent.

## Usage

```sh
agi "summarize this computer's status"     # a prompt as one argument
echo "what distribution is this?" | agi    # or on standard input
agi < question.txt
agi -v "what distribution is this?"        # also show tool output
agi --help
```

Leave the argument off and agi reads the prompt from standard input up to EOF.
At a terminal you can type it yourself over several lines and press **Ctrl-D**.
If standard input is a terminal but the output is going to a pipe or a file,
nobody can type, so agi refuses instead of waiting forever. An empty prompt is
refused before anything is sent.

The agent calls the model, runs the tools it asks for (showing the reason it
gave for each) and prints the final answer. Out of the box it has:

- `bash` to run a command,
- `patchfile` to change a file by replacing text that appears exactly once
  (a copy of the file is saved in the session first),
- `writefile` to create a new file (it never overwrites one),
- `mktemp` to get a scratch directory,
- `subagents` and `fork` to split up work (see below),
- and, when asked for, terminals and your own tools.

**Ctrl-C** stops a run cleanly: the request or command in progress is
cancelled, the conversation is saved, and agi exits with status 130. A second
Ctrl-C kills it at once.

## Terminal and pipe

agi checks whether standard output is a terminal and behaves differently:

- **At a terminal**, it shows its work: tool calls, a spinner while it waits,
  the model's reasoning, notes along the way, and then the answer.
- **In a pipe or a file**, standard output carries only the final answer as
  plain text, so the next program gets clean data.

```
$ agi "what linux distribution is this? just the name" | cat
Arch Linux
```

`--verbose` in a pipe means "I want diagnostics", so the progress goes to
standard error and standard output still holds only the answer:

```sh
agi -v "what distribution is this?" 2>run.log > answer.txt
```

Errors are printed on standard error as one plain message, and a failed run
exits non-zero.

### Diff colors

When `patchfile` or `writefile` is called, agi shows the change as a diff with
line numbers. On a terminal that supports 24-bit color (and says so with
`COLORTERM=truecolor`), each changed line is a full-width red or green band,
with the changed words in a stronger shade. A `TERM` with `256color` gets a
close version, and any other terminal gets plain red and green text. Nothing
breaks either way. Some things to know:

- `COLORTERM` is not passed through ssh or sudo by default. Export it on the
  other side if your terminal supports 24-bit color.
- In tmux, add `set -as terminal-features ',*:RGB'`. If tmux, screen or
  `less -R` does not fill erased cells with the current background, a band
  stops where its text ends.
- Long lines that wrap, and later resizes, are not repainted.
- Tabs are shown as spaces, with stops every four columns.

## Answering in JSON

`--schema` asks for the answer as JSON in a shape you describe. Fields are
separated by commas and written `name:type` or `name:type:"description"`. The
types are `str`, `bool`, `int` and `dec` (or `string`, `boolean`, `integer`,
`decimal`). The description tells the model what the field means.

```
$ agi --schema 'distro:str:"Distribution name", free_gb:int:"Free disk in GB"' \
      "what linux distribution is this and how much free disk is there?"
{"distro": "Arch Linux", "free_gb": 24}
```

A number can have an inclusive range, and either end can be left off:
`age:int(0..120)`, `votes:int(0..)`, `discount:dec(..0.5)`.

A string can be limited to a set of values, separated by `|`. The answer has to
be one of them exactly, capital letters included, which makes it easy to branch
on in a script:

```
$ agi --schema 'kind:str(bug|feature|question):"What kind of issue this is"' < issue.txt
{"kind": "bug"}
```

Put brackets around the whole schema to get a list of objects. An empty list is
a valid answer.

```
$ agi --schema '[user:str:"Login name", pid:int:"Process ID"]' \
      "which processes are using the most memory right now? top 3"
[{"user": "chris", "pid": 4711}, {"user": "chris", "pid": 2280}, {"user": "root", "pid": 918}]
```

That is the only nesting allowed. A field always holds one plain value, so
lists inside lists, list fields and brackets around part of the schema are all
refused before anything is sent.

**How it works.** agi does not use any provider's structured-output feature,
because support for it varies too much. The run first answers in prose, as
usual. Then agi asks the model to restate that answer as JSON, checks it
locally, and sends back a list of every problem until it matches, or gives up
after a few tries and fails the run. All of this is then removed from the
conversation, so the saved session ends on the prose answer and the model is
never left thinking it must answer in JSON from now on. At a terminal you see
the prose and the JSON; in a pipe you get only the JSON.

## Exit codes

A normal run exits 0 whenever it produces an answer. That is fine when you
are reading the answer, but in a script "I could not find that file" looks
like a success.

`--exit-code` fixes that. After the run, agi asks the model one more question:
did you actually deliver what was asked for? Yes exits 0. No exits 1, with the
reason on standard error. **The answer is printed either way**; the status
tells the script whether to act on it.

```sh
#!/bin/sh
if summary=$(agi --exit-code "summarize what changed in the last commit"); then
    printf '%s\n' "$summary" | mail -s "today's changes" me@example.com
else
    echo "agi could not do it; see above" >&2
fi
```

The question is about delivery, not quality. Partial results, caveats, and a
true "no" to a yes/no question still count as done. An honest explanation of
why the task could not be done (missing access, a file that does not exist,
"I can't read your mind") counts as not done.

When the general rule does not fit, say what success means. Attach the text
with `=`:

```
$ agi --exit-code="success means at least one TODO was found" "list the TODOs in main.go"
There are no TODO comments in main.go.
At least one TODO was required, and none was found.
$ echo $?
1
```

Without the `=`, the text becomes the prompt. agi catches this when the prompt
is also on the command line, but not when the prompt comes from standard input.

Like `--schema`, the question and its answer are removed from the conversation
before it is saved. The two flags work together, and the verdict can be about
the JSON itself.

## Sessions

Every run is saved in a directory. If you do not name one, agi makes one under
`TMPDIR` and prints it at the start and end of the run, written as the flag you
need to continue:

```
$ agi "analyze this log"
--session /tmp/agi-1b2
... the work, then the answer ...
--session /tmp/agi-1b2

$ agi --session /tmp/agi-1b2 "and which errors repeat the most?"
```

Passing the same directory again is the whole chat mechanism: the saved
conversation is loaded and your new prompt is added to it. This makes it easy
for a script to lead a model through a fixed series of questions, building up
context one step at a time. When you pass `--session` yourself, agi does not
repeat the path back to you.

- `--session DIR` must name a directory that already exists. A typo is an
  error, not a new empty conversation.
- `--rm` deletes the session when the run ends, however it ends.
- Only one run can use a session at a time. A second one is refused at once.
- A session holds the conversation (`conversation.json`), the event log
  (`log.jsonl`), and a copy of every file `patchfile` changed, under
  `backups/`.
- The system prompt is not saved. It is rebuilt from the flags of each run, so
  you can resume a session with different tools or a different prompt.
- A thinking model's reasoning is saved and sent back, but only to the same
  backend and model that produced it.

## The system prompt

The built-in system prompt is short and says nothing about coding or about any
particular kind of task. `--sys` replaces it with your own file:

```sh
agi --sys ~/prompts/librarian.md "file this note"
agi --sys role.md --sys machine.md --sys style.md "what changed today?"
```

The flag can be repeated. The files are joined in the order given, with a
blank line between them. Any file extension works. A missing or empty file
fails the run before anything is sent.

`--sys` replaces only the base. agi still adds short explanations of its own
built-in tools (subagents, fork, skills) after it, and only for the tools the
run actually has. Subagents keep their own prompt.

## Bringing your own tools, hooks and skills

Everything you add to a run lives in a folder, named with `--resources`:

```
~/agi-resources/
  tools.json            the tools you wrote
  bin/fetch-weather.py  one of them
  hooks.json            programs that run around tool calls and output
  guard-rails.sh        one of them
  data-analyzer/
    SKILL.md            a skill, named after its folder
```

```sh
agi --resources ~/agi-resources --resources ./project "what is the weather in Tokyo?"
```

The flag can be repeated, and order is kept. A folder does not need all three
kinds, but one with none of them is refused. Naming a folder brings in
everything in it, hooks included. A `tools.json` holding `[]` or a
`hooks.json` holding `{}` is fine and simply adds nothing. Every mistake in a
folder is reported before anything is sent.

### Tools

A tool is any executable file with a shebang, in any language, listed in the
folder's `tools.json`:

```json
[
  {
    "name": "weather",
    "path": "bin/fetch-weather.py",
    "description": "Report the current weather in a city. Returns a short sentence.",
    "parameters": {
      "type": "object",
      "properties": {
        "city": { "type": "string", "description": "The city to report on." },
        "unit": { "type": "string", "enum": ["celsius", "fahrenheit"] }
      },
      "required": ["city"],
      "additionalProperties": false
    }
  }
]
```

- `description` is the only thing the model learns about the tool, so write it
  well.
- `path` is relative to the folder and must stay inside it.
- `parameters` is JSON Schema, limited to the keywords agi can actually check:
  `type`, `description`, `properties`, `required`, `items`, `enum` and
  `additionalProperties`. A call that does not match is refused before your
  program runs, with every problem listed at once.
- Two tools with the same name, from anywhere, fail the run.

Your program is run directly, not through a shell, with **one argument: the
JSON object of arguments**. It inherits agi's environment and working
directory, and its standard input is empty. The model reads standard output
and standard error woven together in the order they were written, plus a line
saying how the program ended if it did not exit 0. A non-zero exit is not an
error in itself: the model reads what happened and decides what to do.

```python
#!/usr/bin/env python3
import json, sys

args = json.loads(sys.argv[1])
if args["city"] not in STATIONS:
    print(f"no weather station for {args['city']}", file=sys.stderr)
    sys.exit(1)
print(f"{args['city']}: clear, 19 degrees, wind 8 km/h")
```

### Hooks

A hook is a program you wrote that agi runs **before a tool call**
(`PreToolUse`), **after one** (`PostToolUse`), or **just before it prints
something the model wrote** (`MessageDisplay`). It can let a call through,
change its arguments, refuse it, change what the model reads afterwards, add a
note to the conversation, stop the agent, or change what you see on screen.

The protocol a hook program sees is **Claude Code's**, so most hook scripts
written for Claude Code work here unchanged. The `hooks.json` file is agi's
own:

```json
{
  "PreToolUse": [
    {
      "matcher": "^bash$",
      "if": { "command": "^(git push|npm publish)" },
      "command": "./guard-rails.sh"
    }
  ],
  "PostToolUse": [
    { "command": "./redact-secrets.py", "args": ["--strict"] }
  ],
  "MessageDisplay": [
    { "command": "./render-markdown.sh" },
    { "thinking": true, "command": "./render-thinking.sh" }
  ]
}
```

- `matcher` is always a regular expression over the tool name, matched
  anywhere in it, so write `^bash$` for an exact match. A matcher that fits no
  tool agi has fails the run, since a guard that can never fire is worse than
  none.
- `if` holds one regular expression per named parameter, and the hook runs if
  **any** of them matches. This replaces Claude Code's `Bash(git push *)` form,
  which can only look at one argument.
- `command` alone is a shell command line. With `args`, the program is run
  directly. Relative paths are resolved against the folder, and the hook runs
  in that folder, so a folder of hooks can be moved around.
- `enabled: false` switches a hook off without deleting it.
- Hooks run **one after another**, in order, and each sees the call as the one
  before left it. So a hook that rewrites `ls` into `rm -rf /` is still caught
  by a guard watching for `^rm `.
- A hook that cannot start, or that takes longer than `--hook-timeout` (10s by
  default), **stops the agent** instead of being ignored. A broken guard must
  never fail open.

`MessageDisplay` hooks change only what you see. The conversation, the session
and the log keep the model's own words. They run only at a terminal; for a
pipe, put the renderer in the pipe (`agi "..." | glow -`). A display hook with
`"thinking": true` renders the model's reasoning instead of its messages.
`examples/glow-display/` is a ready-made pair.

The full protocol, every difference from Claude Code, and the known
limitations are in [`docs/hooks-specification.md`](docs/hooks-specification.md).

### Skills

A skill is a document about how to do one kind of work. It is a folder holding
a `SKILL.md`, anywhere under a `--resources` folder. Nothing has to be
declared: write the file to add a skill, delete it to remove one.

```markdown
---
name: Analyzing data, and drawing the result
description: Process CSV files and generate data visualizations.
tags: [csv, visualization, pandas]
version: 1.2.0
---

Use pandas, and plot with matplotlib. Write the chart to the working
directory and name it after the column it is about.
```

- The skill's **id** is the name of its folder (`data-analyzer`). Two skills
  with the same id fail the run.
- `name` is a free-form title for people. `name` and `description` are
  required; `tags` and `version` are optional; other fields are ignored.
- The system prompt lists each skill's id, title, description and tags, but
  not its body. The agent calls `loadskill` with an id when it needs one, and
  is told which directory the skill lives in, so paths inside the skill work.
- Files next to `SKILL.md` belong to that skill. A second `SKILL.md` below it
  is an error.

## Subagents and forks

The agent has two ways to split up work:

- **`subagents`** starts several fresh agents at once, each with a short brief
  written by the parent, and returns their answers in the order they were
  asked for. Use it for independent pieces of work. A task can name the skills
  its subagent should get.
- **`fork`** runs a copy of the current agent, with the whole conversation so
  far, on one task, and keeps only its final answer. Use it for a long piece of
  exploration whose details would otherwise fill up the conversation.

A fork can spawn subagents, but cannot fork again. Subagents can do neither.
Subagents run silently; a fork's progress is shown, marked, as it works. A
subagent or fork that fails or is stopped by a hook ends only itself, and its
parent decides what to do next.

## Terminals

`--enable-terminals` gives the agent interactive terminals it can keep open
across calls: an ssh session, a REPL, a program that asks questions, a long job
it watches. It gets `terminal_open`, `terminal_send`, `terminal_read` and
`terminal_close`.

```sh
agi --enable-terminals "ssh into build-01 and tail the deploy log until it says done"
agi --enable-terminals=2 "..."    # at most 2 open at once (default 8)
```

**It is off by default because hooks cannot guard it.** A hook on
`terminal_send` sees the keys, not what they run: a command typed over several
calls, a line from history, anything typed on another machine. If you need a
guard you can trust, run agi somewhere it cannot do harm, such as a container
or a separate user.

Each agent has its own terminals, numbered from 1. They are closed, with
everything running in them, when that agent ends. The screen is read back as
text only, so information shown only through color is lost. Note the `=` in
`--enable-terminals=2`: with a space, the number becomes the prompt.

## Limits

| Flag              | Default | Minimum | Meaning |
| ----------------- | ------- | ------- | ------- |
| `--max-turns`     | 200     | 5       | Exchanges with the model before a run gives up without an answer. Applies to every agent in the run. |
| `--max-subagents` | 8       | 2       | Tasks one `subagents` call may start at once. No upper limit. |
| `--hook-timeout`  | 10s     | 1s      | How long a hook may take before it stops the agent it guards. |

These are flags and not environment variables on purpose: a setting that
silently changes every run from your shell profile would change answers
without anything showing it.

## The event log

Each session directory holds `log.jsonl`: one JSON object per line, in order.
It records everything — the system prompt, every request and where it went,
every tool call and its output, every hook, the model's reasoning, and
everything that was later removed from the conversation. It is only ever
written to a file, never to the screen.

```sh
jq -c 'select(.kind=="tool.call")' /tmp/agi-1b2/log.jsonl
jq -c 'select(.kind=="model.request") | {backend, model, gateway, turn}' log.jsonl
```

- `--no-log` writes nothing.
- `--log-request-body` also records the full JSON body of every request. Use it
  only for debugging: each request carries the whole conversation, so the log
  grows very fast (one 13-turn run went from 22 KB to 413 KB).

## Examples

`examples/` holds things built on agi but not part of it. They are short, not
held to agi's standards, and meant to be read and copied. Deleting the
directory changes nothing about agi.

- **`agi-repl`** — a shell script that turns agi into a chat: it makes a
  session and runs `agi --session DIR` on each line you type. Other flags are
  passed through.
- **`glow-display/`** — a `--resources` folder with two `MessageDisplay`
  hooks that render the answer and the reasoning through `glow`.
- **`litellm_config.yaml`** — a LiteLLM gateway configuration to point agi at.

See [`examples/README.md`](examples/README.md) for details.

## Layout

| Path                    | Contents |
| ----------------------- | -------- |
| `main.go`               | Entry point; maps errors to an exit status. |
| `cli/`                  | Flags, the agent loop, the `--schema` and `--exit-code` phases, and `agi models`. |
| `model/`                | Messages, conversations, schemas, and their validation. |
| `prompt/`               | The built-in prompts, one Markdown file each. |
| `transport/`            | The interfaces backends implement. |
| `transport/openai/`     | The OpenAI Responses protocol and its HTTP client. |
| `transport/litellm/`    | The LiteLLM gateway configuration of that client. |
| `tools/`                | The tool contract, the registry, and running programs. |
| `tools/native/`         | `subagents`, `fork` and `loadskill`. |
| `tools/workspace/`      | `bash`, `mktemp`, `patchfile`, `writefile` and the terminal tools. |
| `tools/workspace/diff/` | The diffs shown for file changes. |
| `tools/userdefined/`    | Your own tools, from `tools.json`. |
| `skills/`               | Finding and reading `SKILL.md` files. |
| `hooks/`                | Your hooks, from `hooks.json`. |
| `terminal/`             | A program on a real PTY, read back as text. Linux only. |
| `session/`              | Session directories, resuming, and the session lock. |
| `eventlog/`             | The JSON-lines event log. |
| `docs/`                 | Long documents about single features. |
| `examples/`             | Things built on agi, not part of it. |
