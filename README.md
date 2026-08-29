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

`agi` runs on its own, and also has subcommands:

```sh
agi "summarize this computer's status"   # run the agent on a prompt
agi -v "what distribution is this?"      # also show tool output
agi compact                              # the compact subcommand
agi compact --dry-run
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

`--verbose` / `-v` is a root flag, so it works before or after a subcommand
(`agi -v compact` and `agi compact -v` are equivalent). It can also be set with
`AGI_VERBOSE=1`.

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
