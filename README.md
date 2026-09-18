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
agi              # root command, no subcommand
agi compact      # the compact subcommand
agi compact --dry-run
agi --help
```

`--verbose` / `-v` is a root flag, so it works before or after a subcommand
(`agi -v compact` and `agi compact -v` are equivalent). It can also be set with
`AGI_VERBOSE=1`.

## Layout

| Path                 | Contents                                                               |
| -------------------- | ---------------------------------------------------------------------- |
| `main.go`            | Entry point; wires the CLI and maps errors to an exit status.           |
| `cli/`               | Command tree, flags, and command actions.                               |
| `model/`             | Core domain types (messages, conversations) and their validation.       |
| `transport/`         | The `Client` interface gateways implement, plus request/response.       |
| `transport/litellm/` | Client for a LiteLLM gateway, configured from the environment.          |

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
| `LITELLM_URL`     | No       | Base URL of a LiteLLM gateway, e.g. `http://localhost:4000`. No default.    |
| `LITELLM_API_KEY` | No       | Bearer token for the gateway. Omit if the gateway needs no authentication.  |

## Optional: LiteLLM gateway

`transport/litellm` implements `transport.Client` against a
[LiteLLM](https://github.com/BerriAI/litellm) gateway, so several providers can
sit behind a single OpenAI-compatible endpoint.

**Gateways are opt-in.** There is no default URL: agi contacts a gateway only
when `LITELLM_URL` is set, and the Go module takes on no LiteLLM dependency —
the client is stdlib only.

```sh
export LITELLM_URL=http://localhost:4000
export LITELLM_API_KEY=sk-...        # only if your gateway requires one
```

`litellm.FromEnv()` returns a gateway, or an error matching
`litellm.ErrNotConfigured` when `LITELLM_URL` is unset — so callers can tell
"no gateway wanted" from "gateway misconfigured". Running a gateway is outside
the scope of this repository; the request mapping onto `/v1/chat/completions` is
not written yet, and `Gateway.Send` currently reports
`transport.ErrNotImplemented`.
