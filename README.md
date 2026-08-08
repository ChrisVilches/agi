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

The `[bash]` label is colored and the command below it is gray. Both go plain
when output is piped or `NO_COLOR` is set.

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
