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

| Path         | Contents                                                        |
| ------------ | --------------------------------------------------------------- |
| `main.go`    | Entry point; wires the CLI and maps errors to an exit status.     |
| `cli/`       | Command tree, flags, and command actions.                         |
| `model/`     | Core domain types (messages, conversations) and their validation. |
| `transport/` | The `Client` interface backends implement, plus request/response. |
| `deploy/`    | Optional local infrastructure. Not a dependency of the CLI.       |

## Tests

```sh
go test ./...
```

## Optional: LiteLLM proxy

`deploy/litellm/` runs [LiteLLM](https://github.com/BerriAI/litellm) as a Docker
container so several providers can sit behind one OpenAI-compatible endpoint.

**agi does not depend on this.** Nothing in the Go module imports it or talks to
it; it is here for users who want a local proxy. A wrapper that maps agi's
`transport.Client` onto LiteLLM may be added later, and it will stay optional.

```sh
cd deploy/litellm
cp .env.example .env      # then fill in your provider keys
docker compose up -d
curl http://localhost:4000/health/liveliness
```

The proxy listens on `http://localhost:4000` (override with `LITELLM_PORT`).
Clients authenticate with the `LITELLM_MASTER_KEY` from `.env`:

```sh
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer $LITELLM_MASTER_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"opus","messages":[{"role":"user","content":"hi"}]}'
```

Model aliases (`opus`, `sonnet`, `haiku`) are defined in
`deploy/litellm/config.yaml`. Stop it with `docker compose down`.
