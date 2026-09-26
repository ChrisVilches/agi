# agi

An LLM agent that runs on the command line. Give it a prompt and it works on
this machine — running commands, reading and writing files, spawning subagents
of its own — and prints an answer, as prose or as JSON in a shape you asked
for. It talks to any OpenAI-compatible endpoint, directly or through a LiteLLM
gateway, named in environment variables; everything that varies one run is a
flag on it.

Every run is a unix process and behaves like one: the prompt can come from an
argument or standard input, the answer goes to standard output and is the only
thing there when it is piped, errors go to standard error, and a run that could
not be answered exits non-zero.

## Requirements

- Go 1.26.5 or newer

## Build

```sh
go install .          # onto your PATH
go build -o bin/agi . # or into the repository, which is ignored by git
```

Stamp a version into the binary with:

```sh
go build -ldflags "-X main.version=0.1.0" -o bin/agi .
```

## Usage

There are no subcommands: running `agi` runs the agent on a prompt, and
everything else is a flag on that run.

```sh
agi "summarize this computer's status"   # run the agent on a prompt
agi -v "what distribution is this?"      # also show tool output
agi --help
```

The prompt is a single argument, so quote it. The agent calls the model,
runs whatever tools it asks for — printing the reason it gave for each —
and prints the final answer:

```
$ agi "what linux distribution is this and how much free disk is there? be brief"
I'll check the OS and disk usage.

[bash] Identify the Linux distribution and report free disk space.
  $ cat /etc/os-release | head -5; df -h /

**Arch Linux** (rolling release)
**Free disk on `/`:** 24G available (199G total, 165G used)
```

The `[bash]` label is colored and the command below it is gray.

## The prompt on standard input

Leave the argument off and agi reads the prompt from standard input instead,
up to EOF. It is the same prompt either way, so every flag still applies:

```sh
echo "what distribution is this?" | agi
agi --schema 'cores:int:"CPU cores"' < question.txt

agi <<'EOF'
Summarize this computer's status.
Keep it to three lines.
EOF
```

That includes typing it yourself. Run `agi` with no argument, type the prompt
over as many lines as you like, and press **Ctrl-D** to send it:

```
$ agi
summarize this computer's status
keep it to three lines
^D
```

The input is trimmed, and a prompt that is empty after trimming is refused
before any gateway is contacted.

## Your own system prompt

A run starts from a base instruction — the agent's character, and the first
thing in the system prompt — and `--sys` replaces it with a file of your own:

```sh
agi --sys ~/prompts/librarian.md "file this note"
```

Any file will do, whatever it is called: what is sent to the model is the
file's text, so the extension is not looked at.

**The flag can be given more than once**, which is how an instruction written
in parts stays in parts. The files are joined in the order you wrote them, one
blank line between each, so every file arrives as its own paragraph:

```sh
agi --sys role.md --sys machine.md --sys style.md "what changed today?"
```

Nothing is inserted between them, and the order is never rearranged.

What the flag replaces is the base and only the base: whatever the tools of the
run have to explain about themselves is still appended after it, and a subagent
the run spawns keeps the instruction written for it. A file that is empty,
missing, or unreadable fails the run before any gateway is contacted:

```
$ agi --sys ~/prompts/typo.md "hello"
reading the system prompt: open /home/you/prompts/typo.md: no such file or directory
```

## What you bring to a run

Everything you bring to a run lives in a folder, and one flag names it:

```sh
agi --resources ~/agi-resources "what is the weather in Tokyo?"
```

A folder may hold three kinds of thing, and it need not hold all of them:

```
~/agi-resources/
  tools.json            the tools you wrote, described (see below)
  bin/fetch-weather.py  one of them
  hooks.json            the guards that run around every tool call
  guard-rails.sh        one of them
  data-analyzer/
    SKILL.md            a skill, named after the folder it is in
```

**The flag is repeatable**, and the order is kept:

```sh
agi --resources ~/agi-resources --resources ./project "what changed today?"
```

Order decides two things. Hooks run in sequence, and one hook's output is the
next one's input; and the skills are listed to the model in this order. Within
a folder, each kind keeps the order it was written in — the order `tools.json`
lists a tool, the order `hooks.json` lists a hook, the order the filesystem
sorts a skill's folder.

**Naming a folder brings in everything in it**, hooks included. There is no
way to take the tools of a folder without its guards: the folder is yours and
you named it.

A folder that is not there is refused, and so is one that holds none of the
three — naming a folder is asking for what is in it, and an empty one is the
wrong path far more often than it is a deliberate request for nothing.

## Tools you wrote yourself

Tools can be written in any language. The only requirements are that each one
is an executable file with a shebang, and that the folder has a `tools.json`
at its root saying what they are.

### tools.json

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
        "unit": {
          "type": "string",
          "enum": ["celsius", "fahrenheit"],
          "description": "Temperature unit."
        }
      },
      "required": ["city"],
      "additionalProperties": false
    }
  }
]
```

The file is the whole of what agi knows about the folder's tools: a script that
is not listed is not a tool. `name` and `path` are deliberately separate — what
the script is called on disk is your business, and it is usually not what the
tool should be called when a model reads a list of them. `description` is the
only thing the model is ever told about the tool, so it is worth writing well.
`parameters` is a JSON Schema object, the same one a call is checked against.

### What a tool receives, and what it says back

A tool is run directly — never through a shell — with **one argument: the JSON
object of arguments the model produced**, already checked against the schema.
It inherits agi's environment and working directory, so an API key is exported
the way agi's own configuration is, and its standard input is empty.

**The exit status decides which stream the model reads:**

| Exit status | What the model is given            | How it is stored |
| ----------- | ---------------------------------- | ---------------- |
| `0`         | standard output                    | a tool result    |
| anything else | standard error, or standard output when standard error is empty | a tool failure |

So write something either way: what you did, or why you could not.

```python
#!/usr/bin/env python3
import json, sys

args = json.loads(sys.argv[1])
city = args["city"]
if city not in STATIONS:
    print(f"no weather station for {city}", file=sys.stderr)
    sys.exit(1)
print(f"{city}: clear, 19 degrees, wind 8 km/h")
```

### What is checked before the run starts

A mistake in a folder costs a message, not a run. Before any gateway is
contacted, agi refuses an entry with no description, a `path` that names
nothing or points outside the folder, a file without the executable bit
(`chmod +x`), a `tools.json` that declares nothing, and one that is not valid
JSON or has a field agi does not know:

```
$ agi --resources ~/agi-resources "hello"
/home/you/agi-resources/tools.json: the tool "weather": names /home/you/agi-resources/bin/fetch-weather.py, which is not executable; run chmod +x on it
```

**Two tools of the same name fail the run**, wherever the two came from —
another folder, or agi itself. The model addresses a tool by its name and could
not tell them apart, so deduplicating them is yours to do: rename one, or leave
one of the folders out.

Only the part of JSON Schema agi can check is accepted in `parameters` —
`type`, `description`, `properties`, `required`, `items`, `enum` and
`additionalProperties` — because agi will not advertise a constraint it does
not enforce. A call that does not match is refused before your script runs, and
the model is told everything that was wrong with it at once.

## Hooks

A hook is a program you wrote that agi runs around every tool call — before
one is made, and after it has produced a result. It can let the call through,
change its arguments, refuse it, change what the model reads afterwards, add a
note to the conversation, or stop the agent.

```sh
agi --resources ~/agi-resources "tidy up the logs"
```

**The protocol is Claude Code's, on purpose.** A hook script written for that
program usually works here unchanged. It is a flavor of it rather than a copy,
though, and the table at the end of this section lists every place the two
differ — worth reading before you carry a script over.

### hooks.json

A folder's hooks are declared in a `hooks.json` at its root, whose top-level
names are the events:

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
  ]
}
```

There are two events. `PreToolUse` runs before a call and can refuse it or
rewrite it; `PostToolUse` runs after one and can rewrite what the model reads.
An event name agi does not have fails the run when the folder is read.

### Which calls a hook runs for

`matcher` is a regular expression over the tool's **name**. It matches
anywhere in the name, so `bash` also matches a tool called `bashlite` — write
`^bash$` for exactness. Leaving it out, writing it empty, or writing `*` all
mean every tool.

`if` holds one regular expression per **named parameter** of the tool, and
they are combined with **or**: the hook runs if at least one matches. This is
where agi departs from Claude Code's `Bash(git push *)` form, which can only
ever target one argument of one tool — agi's tools, and yours especially, take
several named parameters.

A parameter the call did not include simply fails its own condition. A value
that is not a string is matched against its JSON, so `"timeout": "^12"`
matches the number `120`.

Hooks run **in sequence**, in the order the folders were named and the order
each `hooks.json` lists them, and each is shown the call as the one before it
left it. A hook that rewrites a call is judged by the guards after it, so a
hook turning `ls` into `rm -rf /` does not slip past a guard watching for
`^rm `.

### What a hook runs

`command` alone is a shell command line, so pipes and globs work:

```json
{ "command": "jq -e '.tool_input.command | test(\"^rm\") | not'" }
```

`command` with `args` runs the program directly, with no shell, each argument
exactly as written:

```json
{ "command": "node", "args": ["./scripts/audit.js", "--strict"] }
```

A relative path is resolved against **the folder the `hooks.json` is in**, not
your working directory, and the program **runs in that folder** — so a folder
of hooks is something you can move, copy, or hand to someone else. A hook that
wants your project reads the `cwd` field described below and goes there
itself.

### What a hook reads, and what it says back

Your program reads one JSON object on standard input:

```json
{
  "hook_event_name": "PreToolUse",
  "tool_name": "bash",
  "tool_input": { "command": "git push --force" },
  "tool_use_id": "call_01ABC",
  "cwd": "/home/you/project",
  "session_id": "9b3e07c14f2a1c8d",
  "agent_id": "subagent-2"
}
```

`tool_output` is there too for `PostToolUse`, as `{"type": "text", "text":
"..."}`. `session_id` is a hash of the session's directory: it is the same on
every call of a run and on every later run of that session, and it is safe to
put in a filename, which is what a hook keeping state between calls needs.
`agent_id` is `main`, `fork-1`, `subagent-2`, so a guard can hold a subagent
to a stricter rule than the agent you are talking to.

The exit status is how a hook answers:

| Status | Meaning |
| --- | --- |
| `0` | Proceed. Standard output is read as JSON if it looks like JSON. |
| `2` | Refuse the call. Standard error is the reason, and the model reads it. |
| anything else | A hook that went wrong: you are told, and the call goes ahead. |

On exit `0` a hook can print a JSON object to ask for more:

```json
{
  "continue": false,
  "stopReason": "the branch is protected",
  "systemMessage": "a warning shown to you, never to the model",
  "hookSpecificOutput": {
    "permissionDecision": "deny",
    "permissionDecisionReason": "not on main",
    "updatedInput": { "command": "git push" },
    "updatedToolOutput": { "type": "text", "text": "[redacted]" },
    "additionalContext": "text put into the conversation beside the result"
  }
}
```

`updatedInput` is a **partial** override: it names only the fields it changes
and the rest of the call is kept.

### What a hook can stop

Three things stop an agent, and only three: a program that cannot be started,
a program that outruns `--hook-timeout` (10 seconds by default, `1s` at the
least), and a hook that prints `continue: false`.

Stopping **the agent you are talking to** ends the run: the reason goes to
standard error, the conversation is stored so you can resume it, and agi exits
non-zero. Stopping **a fork or a subagent** ends only that agent — its parent
reads why and decides what to do, which may be to try the work another way.
The guard still wins in the end, because a parent attempting the same work
itself fires the same hook.

A refused call is different and much more common: the tool does not run, the
reason reaches the model as a failed call, and the run carries on.

### Where agi differs from Claude Code

| Claude Code | agi |
| --- | --- |
| Many hook events. | Two: `PreToolUse` and `PostToolUse`. |
| Hook types `command`, `http`, `mcp_tool`, `prompt`, `agent`. | A program, and nothing else. |
| `Bash(git push *)` filters. | `if`, one regular expression per named parameter. |
| A plain matcher is compared literally; only some are regular expressions. | Every matcher is a regular expression. |
| Hooks run in parallel. | In sequence, so `updatedInput` can chain. |
| A hook that cannot start, or times out, is a warning. | It stops the agent: a guard that is broken must never fail open. |
| `continue: false` stops everything. | It stops the agent whose call was guarded; from a fork or a subagent, its parent reads why. |
| `permissionDecision: "ask"` asks you. | Treated as `deny`. agi has no permission prompt, and a run is as often piped with nobody watching. |
| Relative paths resolve against the working directory. | Against the folder the `hooks.json` is in, which is also where the hook runs. |
| A per-hook `timeout` field. | One `--hook-timeout` for the run. |
| `session_id` is an opaque id; `transcript_path` points at the transcript. | `session_id` is a hash of the session's directory. There is no transcript file. |
| `CLAUDE_PROJECT_DIR` and friends in the environment. | Nothing invented; `cwd` arrives on standard input. |
| `suppressOutput`, `terminalSequence`. | Not implemented. |

The whole of it, including the places the behaviour is imperfect and stays
that way, is in `docs/hooks-specification.md`.

## Skills

A skill is a document you wrote about doing one kind of work — how it is done
here, what the conventions are, what goes wrong. The skills kept under a
`--resources` folder are available to the run, and the agent loads the ones the
task calls for.

```sh
agi --resources ~/agi-resources "tidy up the sales export"
agi --resources ~/agi-resources --resources ./project "what changed today?"
```

A skill is a folder holding a `SKILL.md`, and the file is what makes it one.
Nothing has to be declared anywhere — there is no `skills.json` beside the
other two — so you add a skill by writing it and remove it by deleting it.

```
~/agi-resources/
  data-analyzer/
    SKILL.md            the skill "data-analyzer"
    examples/           anything else it needs, however deeply nested
  team/
    web-search/
      SKILL.md          the skill "web-search"
```

The folders on the way down are just places skills are kept, so
`team/web-search/SKILL.md` is the skill `web-search` and there is no skill
called `team`. Everything under a skill belongs to that skill, so a second
`SKILL.md` below one is an error rather than another skill.

### A skill has an id and a title

**The id is the name of the folder the `SKILL.md` is in**, and it is the
skill's identity: it is what the agent loads a skill by, what a subagent's task
names, and the one thing no two skills may share. The folder is where the skill
is, so the id cannot drift from it.

**No two skills may have the same id**, and that is checked across everything
the run was given — every `--resources` folder is read first, and the ids are
compared once at the end. Which of two `data-analyzer` folders should win is
not a question agi answers quietly:

```
$ agi --resources ~/agi-resources --resources ./project "hello"
two skills are in a folder called "data-analyzer", one in /home/you/agi-resources/data-analyzer and one in /home/you/project/data-analyzer; rename one of the folders, or leave out one of the folders they were found under, since a skill is loaded by the name of the folder it is in and these could not be told apart
```

**The title is the `name` in the frontmatter, and it is free-form.** It is prose
written to be read — a sentence saying what the skill is about — and nothing is
required of it: two skills may carry the same title, and one that only repeats
the folder's name costs nothing either.

A `SKILL.md` begins with YAML frontmatter and continues with the instructions:

```markdown
---
name: Analyzing data, and drawing the result
description: Process CSV files and generate data visualizations.
tags: [csv, visualization, pandas, analytics]
version: 1.2.0
---

Use pandas, and plot with matplotlib. Write the chart to the working
directory and name it after the column it is about.
```

`name` and `description` are required; `version` and `tags` are not, and
`tags` may be a single string (`tags: csv`) or a list. Any other field you put
there is left alone, since a `SKILL.md` is written for whatever reads skills
and not for agi alone.

A mistake costs a message, not a run. Before any gateway is contacted, agi
refuses a folder that is not there, frontmatter that does not parse or is
missing, a `SKILL.md` nested under another, and two skills sharing an id —
wherever the two folders came from.

**Skills are summarized in the system prompt and loaded on demand.** The agent
sees each skill's id, title, description and tags — not its instructions — and
calls the `loadskill` tool with an **id** when the task calls for it. The tool
exists only in a run that was given skills, it takes an id and never a path or a
title, and each skill loads once, since its instructions stay in the
conversation afterwards. A call that gives a title instead is refused and told
the id it meant, since that is the one wrong name the list invites. What comes
back is the document, under a line saying which directory the skill is kept in,
so that a skill pointing at an example or a script beside it can be followed:
those paths are written relative to the skill's own folder.

**A subagent gets the skills its task names.** When the agent fans work out, it
can list skill ids on each task, and that subagent is given those and no others:

```json
{"tasks": [
  {"title": "Tidy the export", "body": "...", "skills": ["data-analyzer"]},
  {"title": "Check the figures", "body": "..."}
]}
```

The second subagent above gets no skills and is told nothing about them. An id
that is not one of the run's skills is refused and nothing is spawned, not even
for the tasks that were right.

## How long a run may go on

A run is bounded by how many exchanges with the model it may spend before it
gives up without an answer. The default is 200, which is high enough that only
a run gone wrong — an agent calling the same tool over and over, say — ever
reaches it.

`--max-turns` changes it, for work that really does take longer, or to cut a
run short while you are trying something out:

```sh
agi --max-turns 500 "refactor every test in this folder"
agi --max-turns 20 "what distribution is this?"
```

The smallest cap is 5. A turn is one exchange, and the first of them is rarely
the answer: the agent calls a tool, reads what came back, and often corrects a
call that went wrong. A smaller number could only ever end in giving up, so it
is refused before the gateway is contacted:

```
$ agi --max-turns 1 "hello"
invalid value "1" for flag -max-turns: too few turns to answer anything in; the smallest is 5
```

The cap is the run's, and every agent in it is held to the same one — the
subagents included, since raising it for long work means raising it for the
agents doing that work.

## How wide a run may fan out

When the agent hands work off, one `subagents` call carries several tasks and
runs them all at the same time. `--max-subagents` is how many it may carry,
and the default is 8:

```sh
agi --max-subagents 20 "summarize every file in this folder, one agent each"
agi --max-subagents 2 "compare these two configs"
```

There is no upper limit. A wide fan-out costs conversations open with the
gateway at once, which is yours to spend, so agi does not second-guess a number
you typed.

The smallest is 2. A call with one task has nothing to run alongside it, which
is the entire point of the tool, so a cap of one would be a tool no call could
ever be made to:

```
$ agi --max-subagents 1 "read these four files"
invalid value "1" for flag -max-subagents: too few subagents for a call to be worth making; the smallest is 2
```

Like the turn cap, this one is the run's: a fork spawns subagents under the
same number.

## Answering in JSON

`--schema` asks for the answer as JSON in a shape you describe. Fields are
comma-separated and each one is written `name:type` or
`name:type:"description"`. The types are `str`, `bool`, `int` and `dec`, each
of which also has a long spelling (`string`, `boolean`, `integer`, `decimal`).
The description is optional, but it is what the model is told the field means,
so it is worth writing:

```sh
$ agi --schema 'distro:str:"Distribution name", free_gb:int:"Free disk in GB"' \
      "what linux distribution is this and how much free disk is there?"
{"distro": "Arch Linux", "free_gb": 24}
```

**Wrap the whole schema in brackets to ask for a list instead**, when the
answer has one entry per thing rather than one set of facts. The fields inside
describe a single entry, and the answer is an array of them:

```sh
$ agi --schema '[user:str:"Login name", pid:int:"Process ID"]' \
      "which processes are using the most memory right now? top 3"
[{"user": "chris", "pid": 4711}, {"user": "chris", "pid": 2280}, {"user": "root", "pid": 918}]
```

An empty array is a valid answer: it means the run found nothing to list.

There is **one list at most, and it goes around the whole schema**. A field
always holds a single scalar, so none of these can be written:

```sh
agi --schema '[id:int, names:str[]]'      # a field holding a list
agi --schema '[[id:int], [name:str]]'     # a list of lists
agi --schema 'id:int, [name:str]'         # brackets around part of the schema
```

Each is rejected before the gateway is contacted, with a message saying so.

The shape is asked for in the prompt and checked here, never requested from the
provider, so it works the same behind any gateway. agi lets the run answer in
prose first and then asks the model to restate that answer as JSON, retrying
with the reason whenever what came back does not match. At a terminal the prose
reasoning is shown and the JSON is the final answer; piped, stdout carries the
JSON alone. A run that never produces a matching document fails rather than
printing a wrongly shaped one.

## Piping

At a terminal, agi shows its work. Piped or redirected, stdout carries **only
the final answer, as plain text** — progress is for a person watching, and
another program reading the output should get clean data:

```sh
$ agi "what linux distribution is this? just the name" | cat
Arch Linux
```

`--verbose` while piping is a request for diagnostics, so the progress stream
goes to stderr and stdout still carries only the answer:

```sh
$ agi -v "what distribution is this?" 2>run.log > answer.txt
```

`--verbose` / `-v` can also be set with `AGI_VERBOSE=1`.

## Layout

| Path                 | Contents                                                                                 |
| -------------------- | ------------------------------------------------------------------------------------------ |
| `main.go`            | Entry point; wires the CLI and maps errors to an exit status.                                |
| `cli/`               | Command tree, flags, command actions, backend selection, and the agent loop.                 |
| `model/`             | Typed messages, the `Conversation` aggregate, schemas, and their validation.                 |
| `prompt/`            | The system prompts, one Markdown file each, compiled in with `go:embed`.                     |
| `transport/`         | The `Client` and `Backend` interfaces backends implement, plus request/response.              |
| `transport/openai/`  | The OpenAI chat completions protocol and the HTTP client that speaks it.                      |
| `transport/litellm/` | A LiteLLM gateway's configuration of that client, read from the environment.                  |
| `tools/`             | The `Tool` contract and the registry that collects them.                                      |
| `tools/native/`      | Tools that are part of the agent: `subagents`, `terminate`, `loadskill`.                     |
| `skills/`            | Finding the skills under a folder, and reading a `SKILL.md`'s frontmatter.                    |
| `tools/workspace/`   | Optional tools that do work on the machine: `bash`, `mktemp`, `patchfile`, `writefile`.       |
| `tools/userdefined/` | Tools you wrote yourself, read from a folder's `tools.json`.                                   |
| `session/`           | Storing a run in a directory and resuming it, with the lock that keeps one run to a session.  |
| `eventlog/`          | The run's event log: one JSON object per line, written to the session's directory.            |
| `hooks/`             | Programs of yours that run around every tool call, read from a folder's `hooks.json`.          |
| `docs/`              | One long document per feature: what it does, and why it was built that way.                   |

## Tests

```sh
go test ./...
```

## Configuration

agi is configured through environment variables only. It reads no configuration
files and no dotfiles.

| Variable        | Required        | Meaning                                                                                                      |
| --------------- | --------------- | ------------------------------------------------------------------------------------------------------------ |
| `AGI_BACKEND`   | Yes             | Which backend a run talks to: `litellm` or `openai`. There is no default; a run that names none is refused.     |
| `AGI_VERBOSE`   | No              | Enables verbose output, same as `--verbose` / `-v`.                                                            |
| `LITELLM_URL`   | For `litellm`   | Base URL of a LiteLLM gateway, e.g. `http://localhost:4000`. No default. Must be an `http` or `https` URL.      |
| `LITELLM_KEY`   | No              | Bearer token presented to the gateway. Omit it for a gateway that needs no authentication.                     |
| `LITELLM_MODEL` | No              | Model to ask the gateway for. Defaults to `deepseek-chat`.                                                     |
| `OPENAI_URL`    | For `openai`    | Base URL of an OpenAI-compatible endpoint, e.g. `https://api.deepseek.com`. No default. `http` or `https`.      |
| `OPENAI_KEY`    | No              | Bearer token presented to the endpoint. Omit it for an endpoint that needs no authentication.                  |
| `OPENAI_MODEL`  | No              | Model the agent runs on. Defaults to `deepseek-chat`, which suits the default base prompt.                                                          |
| `TMPDIR`        | No              | Where a run agi names for itself is kept, and where `mktemp` makes the directories an agent asks for.           |

## Backends

A backend is an endpoint agi talks to, and there is no default one: agi
contacts nothing until it is told where. `AGI_BACKEND` names which to use, and
each reads its own variables and nothing else's, so configuring one says
nothing about the other:

```sh
export AGI_BACKEND=openai
export OPENAI_URL=https://api.deepseek.com
export OPENAI_KEY=sk-...
```

```sh
export AGI_BACKEND=litellm
export LITELLM_URL=http://localhost:4000
export LITELLM_KEY=sk-...            # only if your gateway requires one
```

`openai` is any endpoint speaking the OpenAI chat completions protocol — the
provider itself, or a model served on your own machine. `litellm` is a
[LiteLLM](https://github.com/BerriAI/litellm) gateway, which speaks the same
protocol with several providers behind it. They are two configurations of one
hand-written client rather than two clients, and the Go module takes on no
dependency on either: the transport is stdlib only. Running a gateway is
outside the scope of this repository.

Naming no backend is refused with the list of names, and so is a backend whose
address is not set, in a message naming the variable to fix:

```
$ agi "hello"
no backend chosen: set AGI_BACKEND to one of litellm, openai
```

agi targets only what every mainstream provider agrees on: a conversation of
system, user and assistant messages, and tool calling. Prefix completion and
structured output are deliberately not used, which is why `--schema` asks for a
shape in the prompt and checks the answer here.
