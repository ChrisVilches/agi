# AGENTS.md

Source of truth for agent instructions in this repository. `CLAUDE.md` is a
symlink to this file — edit this file, never the symlink.

## Workflow

- After a significant amount of work, commit the changes. If several changes were applied, commit them in such a way each commit represents a feature or unit of work.
- Write unit tests for anything that can have unit tests. Run tests before committing.
- Run `go install` alongside the commit at the end of every unit of work, so the binary on this machine stays current after every change.

## Conventions

- The module path is `github.com/ChrisVilches/agi`. Use the `github.com/ChrisVilches` namespace for any repository URL or module path in this project.
- The CLI is built on [urfave/cli](https://github.com/urfave/cli) v3, where the root command *is* the application. Command wiring lives in `cli/`.
- LiteLLM (`deploy/litellm/`) is optional. Nothing in the Go module may import or depend on it; any future LiteLLM support belongs behind `transport.Client` and stays opt-in.

## Layout

| Path         | Contents                                                          |
| ------------ | ----------------------------------------------------------------- |
| `main.go`    | Entry point; wires the CLI and maps errors to an exit status.      |
| `cli/`       | Command tree, flags, and command actions.                          |
| `model/`     | Core domain types (messages, conversations) and their validation.  |
| `transport/` | The `Client` interface backends implement, plus request/response.  |
| `deploy/`    | Optional local infrastructure. Not a dependency of the CLI.        |
