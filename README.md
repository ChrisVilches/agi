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
not be answered exits non-zero. With `--exit-code` a run that answered but did
not get the job done exits non-zero too, saying why on standard error.

## Requirements

- Go 1.26.5 or newer
- Linux. agi does not currently build on macOS: the terminal tools need Linux, and the
  build includes them even when `--enable-terminals` is not used. This is a known
  limitation.

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

Running `agi` runs the agent on a prompt, and everything that varies that run
is a flag on it. The one subcommand is `models`, which is not a run at all.

```sh
agi "summarize this computer's status"   # run the agent on a prompt
agi -v "what distribution is this?"      # also show tool output
agi --model deepseek-v4-pro "..."        # run this one on another model
agi models                               # list what your backend serves
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
(`chmod +x`), and a file that is not valid JSON or has a field agi does not
know:

```
$ agi --resources ~/agi-resources "hello"
/home/you/agi-resources/tools.json: the tool "weather": names /home/you/agi-resources/bin/fetch-weather.py, which is not executable; run chmod +x on it
```

**A file that declares nothing is fine.** A `tools.json` holding `[]`, or a
`hooks.json` holding `{}`, brings nothing and says so — exactly what leaving
the file out says. Emptying a file is a normal way to switch something off for
a while without deleting what you mean to write again. What is refused is a
file agi cannot read as what it claims to be, which is the list above.

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
one is made, and after it has produced a result — and around every message
agi prints. It can let the call through, change its arguments, refuse it,
change what the model reads afterwards, add a note to the conversation, stop
the agent, or change what you see on screen.

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
  ],
  "MessageDisplay": [
    { "command": "./render-markdown.sh" }
  ]
}
```

There are three events. `PreToolUse` runs before a call and can refuse it or
rewrite it; `PostToolUse` runs after one and can rewrite what the model reads;
`MessageDisplay` runs as agi is about to print something the model wrote and
can change what you see. An event name agi does not have fails the run when
the folder is read. So does a hook that could never fire — one whose
`matcher` matches no tool agi has:

```
$ agi --resources ~/agi-resources "tidy up"
the PreToolUse hook /home/you/agi-resources/guard.sh watches ^Bash$, which is
not the name of any tool agi has, so it could never run; the tool names are
bash, fork, loadskill, mktemp, patchfile, subagents, terminal_close,
terminal_open, terminal_read, terminal_send, writefile
```

A guard you believe in and which cannot fire is worse than no guard, and
`^Bash$` is one keystroke from correct.

The names it checks against are every tool agi can give a run, not the ones
this run was given. A matcher on `terminal_send` is fine in a run without
`--enable-terminals`, and a matcher on `fork` is fine though no subagent could
ever set it off: a folder of hooks is yours to carry from run to run, and a
flag about terminals is not a flag about whether your guards load. Such a hook
will not fire in this run, so a `--verbose` run lists it marked `(watches
nothing in this run)` — the same courtesy an `enabled` that names the other
kind of run gets, which is listed as `(off in this run)`.

The same is not checked for `if`: a condition naming a parameter no tool has
is still silent, for the reason given in the specification's known
limitations.

### Rendering what you read: `MessageDisplay`

The model writes markdown — headings, bullets, fenced code — and agi prints
it raw. A display hook is handed the text and hands back what should be
printed instead, so you can pass it through `glow`, or pretty-print a
`--schema` document through `jq`:

```sh
#!/bin/sh
content=$(jq -r '.delta')
if printf '%s' "$content" | jq -e . >/dev/null 2>&1; then
  rendered=$(printf '%s' "$content" | jq .)
else
  rendered=$(printf '%s' "$content" | glow -)
fi
jq -n --arg c "$rendered" \
  '{hookSpecificOutput: {hookEventName: "MessageDisplay", displayContent: $c}}'
```

**It changes what is printed and nothing else.** The conversation, the saved
session and the log all keep the model's own words, so nothing a display
hook does can affect the run — it is handed the text after all three have
stored it. It applies to every message agi shows, not only the final answer:
the prose the model writes alongside its tool calls is markdown too.

A display hook is not a guard and cannot behave like one. It has no
`matcher` and no `if` — writing either fails the run, since there is no tool
to match — and it cannot refuse a message or stop the agent. A renderer that
is missing, that hangs, or that exits badly leaves the message printed as the
model wrote it and tells you what went wrong.

Assume somebody is watching. A hook cannot find out for itself whether agi's
output is a terminal: agi captures its standard streams, so `[ -t 1 ]` inside
one is always false, and agi sends no field saying otherwise. **The
consequence is yours to manage.** agi runs display hooks whenever it
displays something, piped runs included, so a renderer left armed for
`agi > file` or `agi | jq` writes escape sequences into it — and rendering a
`--schema` document with anything but `jq` leaves the pipe carrying what is
no longer JSON. Keep the renderer in a `--resources` folder of its own and
name it only when you are watching.

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

A `PostToolUse` hook gets two more fields: `tool_response`, holding what the
tool answered, and `duration_ms`, how long the call took. `tool_response` has
the tool's own shape, the way it does in Claude Code. **`bash` is the only
tool in agi that has one**, and it is Claude Code's:
`{"stdout": "...", "stderr": "", "interrupted": false, "isImage": false}`.
agi's `bash` gives the command one file descriptor for both streams, so
everything it printed is in `stdout` and `stderr` is always empty.

For **every other tool** — including the ones you wrote — `tool_response` is
simply the string the model reads. A tool of yours is a script with two
streams, but only one of them ever reaches a hook: on exit `0` its result is
standard output alone, and on any other status the call becomes a failure,
which runs no `PostToolUse` hook at all.

A `MessageDisplay` hook gets none of the `tool_` fields. It gets `delta`,
holding the text about to be printed, with `index: 0` and `final: true`
beside it — agi fires the event once per finished message, which is what
Claude Code does when nobody is watching it stream.

`session_id` is a hash of the session's directory: it is the same on every
call of a run and on every later run of that session, and it is safe to put
in a filename, which is what a hook keeping state between calls needs.
`agent_id` is `fork-1` or `subagent-2`, so a guard can hold a subagent to a
stricter rule than the agent you are talking to. It is **left out entirely**
for that agent, which is how Claude Code says the same thing.

The exit status is how a hook answers:

| Status | Meaning |
| --- | --- |
| `0` | Proceed. Standard output is read as JSON if it looks like JSON. |
| `2` | At `PreToolUse`, refuse the call: standard error is the reason and the model reads it. At `PostToolUse` nothing is refused — the tool has already run — and standard error is handed to the model beside the result. |
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
    "updatedToolOutput": { "stdout": "[redacted]", "stderr": "" },
    "additionalContext": "text put into the conversation beside the result",
    "displayContent": "what to print instead, for MessageDisplay"
  }
}
```

`updatedInput` is a **partial** override: it names only the fields it changes
and the rest of the call is kept.

`updatedToolOutput` is **whole**, and it is written in the same shape the
hook was shown — the object above for `bash`, a plain string for every other
tool. A value of some other shape is ignored and the tool's own output
stands, which is what Claude Code does too; you are told, so a rewrite that
did nothing does not look like one that worked.

At `MessageDisplay` the status is read only to notice that something went
wrong, and `displayContent` and `systemMessage` are the whole of what a hook
can have. `continue: false`, a refusal and `additionalContext` are ignored
there, and you are told each was.

### What a hook can stop

Three things stop an agent, and only three: a program that cannot be started,
a program that outruns `--hook-timeout` (10 seconds by default, `1s` at the
least), and a hook that prints `continue: false`. None of them stops anything
at `MessageDisplay`, where there is no call to withhold and nothing to
protect by ending the run.

Stopping **the agent you are talking to** ends the run: the reason goes to
standard error, the conversation is stored so you can resume it, and agi exits
non-zero. Stopping **a fork or a subagent** ends only that agent — its parent
reads why and decides what to do, which may be to try the work another way.
The guard still wins in the end, because a parent attempting the same work
itself fires the same hook.

A refused call is different and much more common: the tool does not run, the
reason reaches the model as a failed call, and the run carries on.

### Where agi differs from Claude Code

The `hooks.json` is agi's own file and looks nothing like Claude Code's
settings. Everything a hook *program* sees and prints is meant to match, so
that a script can be carried from one to the other without being edited.
These are the places it does not.

| Claude Code | agi |
| --- | --- |
| Many hook events. | Three: `PreToolUse`, `PostToolUse` and `MessageDisplay`. |
| A `matcher` on a `MessageDisplay` hook is ignored. | It fails the run when the folder is read: a hook you believe in and which cannot fire is worse than no hook. |
| A `MessageDisplay` hook's `continue`, exit status and refusal are dropped silently. | Dropped too, and you are told each was. |
| Hook types `command`, `http`, `mcp_tool`, `prompt`, `agent`. | A program, and nothing else. |
| `Bash(git push *)` filters. | `if`, one regular expression per named parameter. |
| A plain matcher is compared literally; only some are regular expressions. | Every matcher is a regular expression. |
| Hooks run in parallel. | In sequence, so `updatedInput` can chain. |
| A hook that cannot start, or times out, is a warning. | It stops the agent that called the tool: a guard that is broken must never fail open. At `MessageDisplay` it is a warning, since there is nothing to guard. |
| `continue: false` stops everything. | It stops the agent whose call was guarded; from a fork or a subagent, its parent reads why; from a `MessageDisplay` hook, nothing. |
| `permissionDecision: "ask"` asks you. | Treated as `deny`, and you are told so on screen. agi has no permission prompt, and a run is as often piped with nobody watching. |
| Relative paths resolve against the working directory. | Against the folder the `hooks.json` is in, which is also where the hook runs. |
| A per-hook `timeout` field. | One `--hook-timeout` for the run. |
| `session_id` is an opaque id; `transcript_path` points at the transcript. | `session_id` is a hash of the session's directory. There is no transcript file. |
| `CLAUDE_PROJECT_DIR` and friends in the environment. | Nothing invented; `cwd` arrives on standard input. |
| `suppressOutput`, `terminalSequence`. | Not implemented. |

**Fields agi does not send.** Each belongs to something agi does not have, so
a ported script that reads one will find nothing there, silently:
`transcript_path`, `scratchpad_dir`, `permission_mode`, `prompt_id`,
`effort`, `agent_type`, `mcp_server`, `turn_id`, `message_id`, and
`tool_response.bashEditDiff`. For `bash`, `tool_response.stderr` is always
empty and `interrupted` and `isImage` are always `false`. In a reply,
`classifierContext` and `updatedMCPToolOutput` are accepted and ignored.

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

## Terminals

`--enable-terminals` gives the agent interactive terminals: a bash it can keep
open across calls, type into, and read back. That is what a one-off `bash`
call cannot do — a program that asks questions, an ssh session that has to stay
open, a REPL, vim, top.

```sh
agi --enable-terminals "ssh into build-01 and tail the deploy log until it says done"
```

It is off by default, and **it is not safe in the way `bash` is**. A hook on
`terminal_send` sees the keys the agent types, not what they run: a command
typed across several calls, a line recalled from history, anything typed into a
shell on another machine or inside a REPL. So turning terminals on lets the
agent past every hook you wrote for `bash`. If you need a guard you can rely
on, run agi somewhere the agent cannot do damage — a container, an unprivileged
user, a VM.

The agent gets four tools: `terminal_open`, `terminal_send` (text and named
keys such as `ENTER` or `CTRL_C`), `terminal_read` (the screen, or more lines
back into what scrolled off it), and `terminal_close`. Each agent has terminals
of its own, numbered from 1, and they are all closed — with everything running
in them — when that agent ends: a subagent's when it answers, a fork's when it
returns, the run's when the run ends however it ends. They do not survive into
a resumed session.

Each agent may have up to 8 open at once. To change that, attach a number to
the flag with `=`:

```sh
agi --enable-terminals=2 "..."
```

The agent is not told the number in advance. Hardly any run opens that many,
so rather than spend a sentence of every run's tool descriptions on it, agi
says nothing until the agent asks for one terminal too many and is refused,
told how many it has open and which of them it can close.

The `=` matters: `--enable-terminals 2 "..."` would read the `2` as part of
the prompt. A prompt that is nothing but a number after the bare flag is
refused for that reason:

```
$ agi --enable-terminals 5
the prompt is only "5", which looks like a number meant for --enable-terminals; write it as --enable-terminals=5, or pipe the prompt in on standard input if 5 really is the question
```

Terminals need Linux, and for now so does agi itself; see Requirements.

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

**An `int` or a `dec` can name the range it has to be in**, in parentheses on
the type, with two dots between the ends. Both ends are included, and either
may be left off:

```sh
$ agi --schema 'load:dec(0..1):"1-minute load average per core", procs:int(1..)' \
      "what is the load average per core and how many processes are running?"
{"load": 0.42, "procs": 331}
```

`age:int(0..120)` has a minimum and a maximum, `votes:int(0..)` a minimum
only, `discount:dec(..0.5)` a maximum only, and negative bounds are fine:
`temp:dec(-40..60)`. The model is told the range in the instruction it answers
from, and a number outside it is handed back for correction along with
anything else that was wrong.

A range belongs to a number, so `name:str(1..5)` is refused, as are a
fractional bound on an `int` (`age:int(1.5..3)`), a minimum above its maximum
(`age:int(120..0)`), and a range with neither end (`age:int(..)`). Each is
caught before the gateway is contacted.

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

One thing can put a wrongly shaped one there anyway: a `MessageDisplay` hook.
A display hook is shown the document like any other message, so a renderer
armed for a run you are watching will also rewrite the document of a run you
pipe, and unless it is `jq` the result is no longer JSON. Keep the renderer
in a `--resources` folder you name only when you are watching.

## Saying whether the task was done

An ordinary run exits 0 whenever it produced an answer, and that is the right
default: at a terminal you are reading the answer, and whether it is any good
is a question you can see the answer to. In a script it is not. `"I could not
find that file"` is an answer, and a shell has no way of telling it from the
one you were hoping for.

`--exit-code` is how a script tells them apart. The run works as it always
does and prints the same answer, and then agi asks the model one more question:
was the task you were given actually completed? A yes exits 0. A no exits 1,
and the reason goes to standard error:

```sh
$ agi --exit-code "summarize the release notes in CHANGELOG.md" > notes.txt
There is no CHANGELOG.md in this directory, and nothing in the prompt says
which file was meant.
$ echo $?
1
```

```sh
#!/bin/sh
if summary=$(agi --exit-code "summarize what changed in the last commit"); then
    printf '%s\n' "$summary" | mail -s "today's changes" me@example.com
else
    echo "agi could not do it; see above" >&2
fi
```

**The answer still goes to standard output either way.** A failed run is not a
silent one: whatever the model wrote is on stdout as usual, and the exit status
is what says not to act on it. Nothing is interleaved — the progress stream and
the reason go to standard error, the answer goes to standard output — because
the flag is for a pipe or a script rather than for a screen.

**"Completed" is a low bar, deliberately.** Work left as a question for you,
follow-up you were given rather than done, caveats, parts the model judged out
of scope, and findings you did not want are all completed tasks: an answer
saying what was found and what was not is an answer, and so is "no". The run
exits 1 when there is nothing to show for it — the request could not be
understood, or it needed access the run was not given, or it named something
that does not exist, or the work was attempted and did not come off.

The verdict is asked for in the prompt and read here, never requested from the
provider, so it works the same behind any gateway. It costs one request, which
is why it is a flag: a run nobody is scripting has no use for the answer. At a
terminal the question scrolls past above the answer like the rest of the run;
piped, you see none of it and get the status and the reason. A model that never
produces a readable verdict fails the run — agi cannot say whether the work came
off, and a run that cannot say that has not succeeded.

Neither the question nor the verdict is kept. They are taken back out of the
conversation before the session is stored, so a session is the same whether or
not it was asked, and the only record of the grading is the event log.

It combines with `--schema`: the document goes to stdout as usual and the
status says whether to trust it.

```sh
$ agi --exit-code --schema 'distro:str, free_gb:int' "what distro and how much free disk?"
{"distro": "Arch Linux", "free_gb": 24}
```

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

The one thing that can put anything else on stdout is a `MessageDisplay`
hook, which is shown the answer whatever it is going to. See the warning at
the end of that section.

## Where the conversation is kept

**agi says where, when agi is the one that decided where.** Give it no
`--session` and it makes a directory, and names it at both ends of the run,
written as the flag that continues the conversation — so the line is the
whole of the next command but the prompt:

```
$ agi "analyze this log"
--session /tmp/agi-1b2
... the work, then the answer ...
--session /tmp/agi-1b2
```

It is said twice because by the end the first one has scrolled off, and the
moment the directory is worth having is the moment after the answer. A `--rm`
run says it once, at the top: its directory is gone by the time a closing line
would be read.

**A run given `--session` says neither.** The path would be the one you just
typed, read back from below where you typed it — `session.Open` keeps it
exactly as written — so there is nothing in the line you do not have:

```
$ agi --session /tmp/agi-1b2 "and now what?"
... the work, then the answer ...
```

There is no flag for this, and that is the point. Every program that starts
runs for you — `agi-repl`, a shell function that pins a directory — passes
`--session` by definition, so it is quiet without being told to be, and it is
the one that knows the path and can say it in its own words. The rule is about
what you typed, not about the directory: one agi made an hour ago is yours the
moment you name it.

The line rides the progress stream like everything else here, so a pipe never
carries it and `--verbose` puts it on stderr. Nothing suppresses it for a run
whose directory agi chose — a conversation agi keeps is a conversation agi
tells you where to find.

## The event log

Beside the conversation, every session directory holds `log.jsonl`: one JSON
object per line, in the order things happened. It is the account of a run —
what was asked, what was configured, every exchange with the endpoint, every
tool that ran, and everything later taken back out of the conversation. It
goes to a file and never to a terminal, so nothing it holds can corrupt a pipe
or bury what `--verbose` puts on stderr.

```sh
jq -c 'select(.kind=="tool.call")' /tmp/agi-1b2/log.jsonl
```

`--no-log` is the way to ask for silence, for a run worth keeping but not
worth tracing. The log becomes a pass-through that answers every call and
writes nothing.

Every request says where it went: which backend, which model, which address.

```sh
$ jq -c 'select(.kind=="model.request") | {backend, model, gateway, turn}' log.jsonl
{"backend":"litellm","model":"deepseek-chat","gateway":"http://localhost:4000","turn":1}
```

The backend is there because the address cannot stand in for it. Both backends
are one client under two configurations, so a gateway and a direct endpoint can
be pointed at the same URL and would otherwise be indistinguishable — and it is
on every request rather than once at the top, because a session is resumed by
running agi again against the same directory and nothing stops the second run
from naming a different backend than the first.

## Examples

`examples/` holds programs built on agi that are not part of it: things
somebody wanted and went on using, kept because they are useful and because a
short program that composes agi shows what agi is like to build on. None of it
is production ready or held to agi's standards, and agi depends on none of it —
the directory can be deleted and everything else still builds, tests, and runs.

`examples/agi-repl` is the one there now. It is a shell script that turns
one-shot runs into a conversation you sit in front of: it makes a session
directory, and then asks for a line and runs `agi --session DIR` on it, over
and over.

```sh
$ ./examples/agi-repl -v
agi repl — session /tmp/agi-repl.k3Qd1xZP

agi #1> what distribution is this?
...
agi #2 3.1s 8K> and how much free disk does it have?
```

Every flag you give it is handed to agi as it is written, on every prompt, and
agi knows nothing about any of it: a repl is a directory and a loop around the
command, and the script is the whole of it. `examples/README.md` has the rest —
how the prompt is drawn and why, what the script does not handle, and where you
would start if you built on it.

## Layout

| Path                 | Contents                                                                                 |
| -------------------- | ------------------------------------------------------------------------------------------ |
| `main.go`            | Entry point; wires the CLI and maps errors to an exit status.                                |
| `cli/`               | Command tree, flags, command actions, backend selection, the agent loop, the phases after it, and `agi models`.      |
| `model/`             | Typed messages, the `Conversation` aggregate, schemas, and their validation.                 |
| `prompt/`            | The system prompts, one Markdown file each, compiled in with `go:embed`.                     |
| `transport/`         | The `Client` and `Backend` interfaces backends implement, plus request/response.              |
| `transport/openai/`  | The OpenAI Responses protocol and the HTTP client that speaks it.                      |
| `transport/litellm/` | A LiteLLM gateway's configuration of that client, read from the environment.                  |
| `tools/`             | The `Tool` contract and the registry that collects them.                                      |
| `tools/native/`      | Tools that are part of the agent: `subagents`, `fork`, `loadskill`.                     |
| `skills/`            | Finding the skills under a folder, and reading a `SKILL.md`'s frontmatter.                    |
| `tools/workspace/`   | Optional tools that do work on the machine: `bash`, `mktemp`, `patchfile`, `writefile`, and the terminal tools. |
| `tools/userdefined/` | Tools you wrote yourself, read from a folder's `tools.json`.                                   |
| `session/`           | Storing a run in a directory and resuming it, with the lock that keeps one run to a session.  |
| `eventlog/`          | The run's event log: one JSON object per line, written to the session's directory.            |
| `hooks/`             | Programs of yours that run around every tool call and every message printed, read from a folder's `hooks.json`. |
| `terminal/`          | Running a program on a real PTY and reading back the screen it draws. Linux only. |
| `docs/`              | One long document per feature: what it does, and why it was built that way.                   |
| `examples/`          | Programs built on agi and not part of it, `agi-repl` among them. Nothing here is built, tested, or installed with agi. |

## Tests

```sh
go test ./...
```

## Configuration

agi is configured through environment variables only. It reads no configuration
files and no dotfiles.

| Variable        | Required        | Meaning                                                                                                      |
| --------------- | --------------- | ------------------------------------------------------------------------------------------------------------ |
| `AGI_BACKEND`   | Yes             | Which backend a run talks to, and which endpoint `agi models` asks: `litellm` or `openai`. There is no default; a run that names none is refused. |
| `AGI_DEFAULT_MODEL` | Unless `--model` | The model a run uses when `--model` does not name one, spelled exactly as `agi models` prints it. There is no default: agi never picks a model for you, and a run given neither is refused. `agi models` itself does not need it. |
| `AGI_VERBOSE`   | No              | Enables verbose output, same as `--verbose` / `-v`.                                                            |
| `LITELLM_URL`   | For `litellm`   | Base URL of a LiteLLM gateway, e.g. `http://localhost:4000`. No default. Must be an `http` or `https` URL.      |
| `LITELLM_KEY`   | No              | Bearer token presented to the gateway. Omit it for a gateway that needs no authentication.                     |
| `OPENAI_URL`    | For `openai`    | Base URL of an OpenAI-compatible endpoint, e.g. `https://api.deepseek.com`. No default. `http` or `https`.      |
| `OPENAI_API_KEY` | No             | Bearer token presented to the endpoint `OPENAI_URL` names, which need not be OpenAI's own. Omit it for an endpoint that needs no authentication. |
| `TMPDIR`        | No              | Where a run agi names for itself is kept, and where `mktemp` makes the directories an agent asks for.           |

## Backends

A backend is an endpoint agi talks to, and there is no default one: agi
contacts nothing until it is told where. `AGI_BACKEND` names which to use, and
each reads its own variables and nothing else's, so configuring one says
nothing about the other:

```sh
export AGI_BACKEND=openai
export OPENAI_URL=https://api.deepseek.com
export OPENAI_API_KEY=sk-...
export AGI_DEFAULT_MODEL=deepseek-flash
```

```sh
export AGI_BACKEND=litellm
export LITELLM_URL=http://localhost:4000
export LITELLM_KEY=sk-...            # only if your gateway requires one
export AGI_DEFAULT_MODEL=deepseek-chat
```

The model is not part of a backend's own configuration — one variable answers
it for whichever backend you named, and `--model` overrides that for one run —
so run `agi models` to see the names your backend accepts.

`openai` is any endpoint speaking the OpenAI Responses protocol — the provider
itself, or a model served on your own machine. `litellm` is a
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

### What your backend serves

`agi models` asks the backend `AGI_BACKEND` names what it serves and prints it.
It opens no conversation and spends nothing:

```
$ agi models
MODEL             OWNED BY   CONTEXT   MAX OUTPUT
deepseek-flash    deepseek   1048576   393216
deepseek-v4-pro   deepseek   1048576   393216

AGI_DEFAULT_MODEL is deepseek-flash, which every run uses unless --model names another
```

A gateway answers with everything behind it, under the names you would ask for
them by:

```
$ AGI_BACKEND=litellm agi models
MODEL                       OWNED BY   CONTEXT   MAX OUTPUT
deepseek-chat               openai     131072    8192
moonshot/kimi-k3            openai     1048576   1048576
moonshot/moonshot-v1-128k   openai     131072    131072
```

The two print the same four columns whichever answered, which is less than
either endpoint knows about a model: each reports fields the other has never
heard of, and a listing whose shape depended on your configuration would be
worth reading only once. A dash means that endpoint did not say. The listing is
sorted by name rather than left in the order it arrived, which for a gateway is
the order it happened to assemble its catalogue in.

The line under the listing says which model a run will actually use, since the
name is chosen once and then lives in a shell profile — out of sight exactly
when the catalogue is in front of you. A machine that has chosen nothing is
told so rather than left to find out on its next run:

```
AGI_DEFAULT_MODEL is not set: a run has to be given a model, either by setting it or with --model
```

Nothing is checked against the listing. A name that is not one of the rows —
an alias, or a gateway route — is printed exactly as you set it and not
remarked on, for the reason given [below](#choosing-the-model). The line is
written for a person, so it goes to the screen at a terminal, to standard error
in a piped run that asked for `--verbose`, and nowhere at all in a piped run
that did not: `agi models | awk '{print $1}'` gets the models and nothing else.

### Choosing the model

Listing models does not choose one. `AGI_DEFAULT_MODEL` does, and it takes a
name from that listing **exactly as printed**:

```sh
export AGI_DEFAULT_MODEL=deepseek-flash              # a provider's own name
export AGI_DEFAULT_MODEL=anthropic/claude-sonnet-5   # a gateway route, prefix and all
```

`--model` says the same thing for one run and overrides the variable, which is
the difference between the two: the variable is the model this machine runs on,
and the flag is the model this run runs on — a larger model for a prompt worth
one, a cheaper one for something trivial, or the same prompt put to two models
to compare:

```sh
agi --model deepseek-v4-pro "review this design and be hard on it"
agi --model anthropic/claude-sonnet-5 "summarize this computer's status"
```

There is no default, and agi never picks a model for you. A run given neither
stops before it spends anything:

```
$ agi "hello"
no model chosen: pass --model, or set AGI_DEFAULT_MODEL, naming a model openai serves, as `agi models` lists it
```

A gateway prints two shapes and they only look inconsistent: a model named
explicitly in its configuration is listed and asked for bare, and one reached
through a wildcard carries the prefix that routes it. Both are names of routes
the gateway set up, so agi sends whichever you give it, untouched.

The name is **not** checked against the listing — however you named it, it is
simply used. That is deliberate: `deepseek-chat` is absent from DeepSeek's
catalogue and works anyway, resolving to the model that replaced it, while a
gateway's wildcard can advertise models your account cannot reach. The listing
is a good guide and not a guarantee, so the endpoint is what decides. When it
refuses the name, agi says so and points you back at the listing:

```
$ agi --model totally-made-up "hello"
openai: ... returned 400 Bad Request: invalid_request_error: The supported API model
names are deepseek-flash, deepseek-v4-pro, but you passed totally-made-up.; run
`agi models` to see what openai serves
```

A failure that is about something else — an expired key, a gateway that cannot
reach its provider — is reported as it is, with no advice about model names.

`models` is the one word that cannot be a prompt on its own — `agi models` asks
for the listing rather than the agent. Everything else is untouched: `agi "which
models are best?"` runs the agent as it always has, and `echo models | agi` asks
the agent about the word itself.
