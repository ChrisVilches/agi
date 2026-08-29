# agi

A CLI tool written in Go.

## Requirements

- Go 1.26 or newer

## Build

```sh
go build -o bin/agi .
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

| Path                 | Contents                                                               |
| -------------------- | ---------------------------------------------------------------------- |
| `main.go`            | Entry point; wires the CLI and maps errors to an exit status.           |
| `cli/`               | Command tree, flags, and command actions.                               |
| `model/`             | Typed messages, the `Conversation` aggregate, and their validation.     |
| `transport/`         | The `Client` interface gateways implement, plus request/response.       |
| `transport/litellm/` | Client for a LiteLLM gateway, configured from the environment.          |
| `tools/`             | The `Tool` contract and the registry that collects them.                |
| `tools/controlflow/` | Agent control-flow primitives (subagents, checkpoints, rewinding).      |
| `tools/workspace/`   | Optional tools that do work on the machine. Currently `bash`.           |
| `tools/userdefined/` | Tools you wrote yourself, read from a folder's `manifest.json`.         |

## Tests

```sh
go test ./...
```

## Configuration

agi is configured through environment variables only. It reads no configuration
files and no dotfiles.

| Variable          | Required | Meaning                                                                    |
| ----------------- | -------- | -------------------------------------------------------------------------- |
| `AGI_VERBOSE`     | No       | Enables verbose output, same as `--verbose` / `-v`.                         |
| `AGI_MODEL`       | No       | Model to run on. Defaults to `deepseek-chat`.                               |
| `LITELLM_URL`     | No       | Base URL of a LiteLLM gateway, e.g. `http://localhost:4000`. No default.    |
| `DEEPSEEK_KEY`    | No       | Bearer token for the gateway. Omit if the gateway needs no authentication.  |

## Optional: LiteLLM gateway

`transport/litellm` implements `transport.Client` against a
[LiteLLM](https://github.com/BerriAI/litellm) gateway, so several providers can
sit behind a single OpenAI-compatible endpoint.

**Gateways are opt-in.** There is no default URL: agi contacts a gateway only
when `LITELLM_URL` is set, and the Go module takes on no LiteLLM dependency —
the client is stdlib only.

```sh
export LITELLM_URL=http://localhost:4000
export DEEPSEEK_KEY=sk-...           # only if your gateway requires one
```

`litellm.FromEnv()` returns a gateway, or an error matching
`litellm.ErrNotConfigured` when `LITELLM_URL` is unset — so callers can tell
"no gateway wanted" from "gateway misconfigured". Running a gateway is outside
the scope of this repository.

Because the gateway speaks the standard OpenAI-compatible chat completions
protocol, `LITELLM_URL` can also point straight at a provider that speaks it.
