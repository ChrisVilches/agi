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

## Tools you wrote yourself

`--tools` adds a folder of your own tools to the run. They can be written in
any language — the only requirements are that each one is an executable file
with a shebang, and that the folder has a `manifest.json` at its root saying
what they are:

```sh
agi --tools ~/agi-tools "what is the weather in Tokyo?"
```

**The flag is repeatable**, and the folders may be given in any order:

```sh
agi --tools ~/agi-tools --tools ./project-tools "what changed today?"
```

### The manifest

```json
{
  "tools": [
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
}
```

The manifest is the whole of what agi knows about the folder: a file that is
not listed is not a tool. `name` and `path` are deliberately separate — what
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
contacted, agi refuses a folder with no `manifest.json`, an entry with no
description, a `path` that names nothing or points outside the folder, a file
without the executable bit (`chmod +x`), and a manifest that is not valid JSON
or has a field agi does not know:

```
$ agi --tools ~/agi-tools "hello"
/home/you/agi-tools/manifest.json: the tool "weather": names /home/you/agi-tools/bin/fetch-weather.py, which is not executable; run chmod +x on it
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

## Skills

A skill is a document you wrote about doing one kind of work — how it is done
here, what the conventions are, what goes wrong. `--skills` makes a folder of
them available to a run, and the agent loads the ones the task calls for.

```sh
agi --skills ~/agi-skills "tidy up the sales export"
agi --skills ~/agi-skills --skills ./project-skills "what changed today?"
```

A skill is a folder holding a `SKILL.md`, and the file is what makes it one.
Nothing has to be declared anywhere: you add a skill by writing it and remove
it by deleting it.

```
~/agi-skills/
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

A `SKILL.md` begins with YAML frontmatter and continues with the instructions:

```markdown
---
name: data-analyzer
description: Process CSV files and generate data visualizations.
tags: [csv, visualization, pandas, analytics]
version: 1.2.0
---

Use pandas, and plot with matplotlib. Write the chart to the working
directory and name it after the column it is about.
```

`name` and `description` are required; `version` and `tags` are not, and
`tags` may be a single string (`tags: csv`) or a list. **The name has to be the
name of the folder the file is in** — a skill is referred to by its name, and
two answers to where a skill lives is one too many. Any other field you put
there is left alone, since a `SKILL.md` is written for whatever reads skills
and not for agi alone.

A mistake costs a message, not a run. Before any gateway is contacted, agi
refuses a folder that is not there, frontmatter that does not parse or is
missing, a name that is not the folder's, a `SKILL.md` nested under another,
and two skills answering to one name — wherever the two folders came from:

```
$ agi --skills ~/agi-skills "hello"
/home/you/agi-skills/team/web-search/SKILL.md: its frontmatter is named "websearch", but it is in a folder called "web-search"; a skill's name is the name of the folder it is in, so rename one to match the other
```

**Skills are summarized in the system prompt and loaded on demand.** The agent
sees each skill's name, description and tags — not its instructions — and calls
the `loadskill` tool with a name when the task calls for it. The tool exists
only in a run that was given skills, it takes a name and never a path, and each
skill loads once, since its instructions stay in the conversation afterwards.
What comes back is the document, under a line saying which directory the skill
is kept in, so that a skill pointing at an example or a script beside it can be
followed: those paths are written relative to the skill's own folder.

**A subagent gets the skills its task names.** When the agent fans work out, it
can list skills on each task, and that subagent is given those and no others:

```json
{"tasks": [
  {"title": "Tidy the export", "body": "...", "skills": ["data-analyzer"]},
  {"title": "Check the figures", "body": "..."}
]}
```

The second subagent above gets no skills and is told nothing about them. A name
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
--max-turns is 1, which is too few to answer anything; the smallest is 5
```

The cap is the run's, and every agent in it is held to the same one — the
subagents included, since raising it for long work means raising it for the
agents doing that work.

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
| `tools/userdefined/` | Tools you wrote yourself, read from a folder's `manifest.json`.                               |
| `session/`           | Storing a run in a directory and resuming it, with the lock that keeps one run to a session.  |
| `eventlog/`          | The run's event log: one JSON object per line, written to the session's directory.            |

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
