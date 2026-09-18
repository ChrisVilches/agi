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
| `transport/litellm/` | LiteLLM gateway: its client, and the Compose setup that runs one.       |

## Tests

```sh
go test ./...
```

## Optional: LiteLLM gateway

`transport/litellm/` holds everything for talking to a
[LiteLLM](https://github.com/BerriAI/litellm) gateway: the `Gateway` client that
implements `transport.Client`, and the Docker Compose setup that runs one
locally so several providers sit behind a single OpenAI-compatible endpoint.

**Gateways are opt-in.** agi runs with none configured, and the Go module takes
on no LiteLLM dependency — the client is stdlib only. Request mapping onto
`/v1/chat/completions` is not written yet; `Gateway.Send` currently reports
`transport.ErrNotImplemented`.

To run one locally:

```sh
cd transport/litellm
cp .env.example .env      # then fill in your provider keys
docker compose up -d
curl http://localhost:4000/health/liveliness
```

The gateway listens on `http://localhost:4000` (override with `LITELLM_PORT`).
Clients authenticate with the `LITELLM_MASTER_KEY` from `.env`:

```sh
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer $LITELLM_MASTER_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"opus","messages":[{"role":"user","content":"hi"}]}'
```

Model aliases (`opus`, `sonnet`, `haiku`) are defined in
`transport/litellm/config.yaml`. Stop it with `docker compose down`.
