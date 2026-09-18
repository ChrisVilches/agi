# AGENTS.md

Source of truth for agent instructions in this repository. `CLAUDE.md` is a
symlink to this file — edit this file, never the symlink.

## Workflow

- After a significant amount of work, commit the changes. If several changes were applied, commit them in such a way each commit represents a feature or unit of work.
- Write unit tests for anything that can have unit tests. Run tests before committing.
- Run `go install` alongside the commit at the end of every unit of work, so the binary on this machine stays current after every change.
- Update this document every time the source code changes, in the same commit as the change, so the two never drift apart. Adding, removing, or repurposing a package, changing the command tree, adding or renaming an environment variable, or changing a convention below all require an edit here.

## Conventions

- The module path is `github.com/ChrisVilches/agi`. Use the `github.com/ChrisVilches` namespace for any repository URL or module path in this project.
- The CLI is built on [urfave/cli](https://github.com/urfave/cli) v3, where the root command *is* the application. Command wiring lives in `cli/`.
- **Configuration comes from environment variables only. Never use dotfiles.** No `.env`, no `.agirc`, no config file discovery, and no library that reads one. Every setting is an environment variable documented in the table below.
- `transport/` owns every exchange with an external gateway. The `transport.Client` interface is the only thing the rest of the program depends on; each gateway gets a subpackage.
- Gateways are opt-in and never assumed. There is no default gateway URL, so agi contacts one only when its environment variable is set, and the Go module must not take on a dependency on any gateway.
- Running a gateway is the user's concern, not the repository's. Do not add Docker Compose files, container definitions, or other deployment manifests here.

## Environment variables

Every environment variable the application reads. Keep this table complete —
adding one to the code without adding it here is an incomplete change.

| Variable          | Read by            | Required | Meaning                                                                                              |
| ----------------- | ------------------ | -------- | ---------------------------------------------------------------------------------------------------- |
| `AGI_VERBOSE`     | `cli`              | No       | Enables verbose output, equivalent to the `--verbose` / `-v` flag. Any non-empty truthy value enables it. |
| `LITELLM_URL`     | `transport/litellm` | No      | Base URL of a LiteLLM gateway, for example `http://localhost:4000`. Setting it is what enables the gateway; there is no default. Must be an `http` or `https` URL. |
| `DEEPSEEK_KEY`    | `transport/litellm` | No      | Bearer token presented to the gateway. Omit it for a gateway that needs no authentication. Named for the provider key the user keeps in their environment; the gateway decides whether to forward it or substitute its own. |

## Layout

| Path                 | Contents                                                          |
| -------------------- | ----------------------------------------------------------------- |
| `main.go`            | Entry point; wires the CLI and maps errors to an exit status.      |
| `cli/`               | Command tree, flags, and command actions.                          |
| `model/`             | Core domain types (messages, conversations) and their validation.  |
| `transport/`         | The `Client` interface gateways implement, plus request/response.  |
| `transport/litellm/` | Client for a LiteLLM gateway, configured from the environment.     |
