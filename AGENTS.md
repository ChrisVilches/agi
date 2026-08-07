# AGENTS.md

Source of truth for agent instructions in this repository. `CLAUDE.md` is a
symlink to this file — edit this file, never the symlink.

## Workflow

- After a significant amount of work, commit the changes. If several changes were applied, commit them in such a way each commit represents a feature or unit of work.
- Write unit tests for anything that can have unit tests. Run tests before committing.
- Run `go install` alongside the commit at the end of every unit of work, so the binary on this machine stays current after every change.
- Update this document every time the source code changes, in the same commit as the change, so the two never drift apart. Adding, removing, or repurposing a package, changing the command tree, or changing a convention below all require an edit here.

## Conventions

- The module path is `github.com/ChrisVilches/agi`. Use the `github.com/ChrisVilches` namespace for any repository URL or module path in this project.
- The CLI is built on [urfave/cli](https://github.com/urfave/cli) v3, where the root command *is* the application. Command wiring lives in `cli/`.
- `transport/` owns every exchange with an external gateway. The `transport.Client` interface is the only thing the rest of the program depends on; each gateway gets a subpackage holding both its client and whatever is needed to run it.
- Gateways are opt-in. agi must work with none configured, and the Go module must not take on a dependency on any gateway.

## Layout

| Path                 | Contents                                                              |
| -------------------- | --------------------------------------------------------------------- |
| `main.go`            | Entry point; wires the CLI and maps errors to an exit status.          |
| `cli/`               | Command tree, flags, and command actions.                              |
| `model/`             | Core domain types (messages, conversations) and their validation.      |
| `transport/`         | The `Client` interface gateways implement, plus request/response.      |
| `transport/litellm/` | LiteLLM gateway: its client, and the Docker Compose setup that runs it. |
