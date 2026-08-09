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
- Tools come in three categories, and they differ in how much the model is told about them. **Control-flow tools** (`tools/controlflow`) act on the agent itself — subagents, rewinding, checkpoints — and must explain when and how to use them in the system prompt, by implementing `tools.SystemPrompter`. **Workspace tools** (`tools/workspace`) do ordinary work, can be switched off, and must never appear in the system prompt. **User-defined tools** are described the same way workspace tools are. A tool that may not be registered must not be described in the system prompt, so only control-flow tools implement `SystemPrompter`.
- A workspace or user-defined tool's `Description` is the only thing the model learns about it, so the usage rules belong there rather than in the system prompt.
- A system prompt is assembled from a **base** and whatever the registered tools explain. The base is the swappable half — `prompt.Base()` for an ordinary run, `prompt.Subagent()` for a spawned one — and it must describe nothing agi does, so that replacing it breaks nothing. The control-flow guidance is appended to whichever base is in use, because a model that has not been told what those primitives are for will never reach for them.
- Every prompt lives in `prompt/` as its own Markdown file, embedded with `go:embed`. They are kept apart because they change independently, and kept as text because that is what they are. Embedding is not file discovery: nothing is read from disk at run time, and a prompt is not configuration.
- The control-flow guidance reaches the model only through a registered tool's `SystemPrompt`, never by unconditional concatenation, and it must describe only the primitives that exist. Guidance for a tool the run was not given is a standing lie to the model, which is the same rule that keeps workspace tools out of the prompt.
- A control-flow tool that needs to run an agent is handed a function that does, rather than reaching for the loop: `cli` imports `tools/controlflow`, so the dependency cannot go the other way. `controlflow.Runner` is that function, and supplying it is also where it is decided what a subagent may do — it gets the workspace tools and no control-flow tools, so a run cannot recurse without a bottom.
- One `subagents` call spawns every task at once and returns the answers in the order the tasks were given, never the order they finished in: the same call must not give the model a different result because of scheduling. A subagent that fails reports it in its own section instead of failing the call, so the answers that did arrive are not thrown away. Their progress is discarded — several at once would interleave into nonsense — so the announced list of titles is the whole of what the user sees while they work.
- A conversation is a sequence of typed messages — `UserMessage`, `AssistantMessage`, `ToolResultMessage`, `HarnessInstruction` — not one struct with optional fields. The set is closed by an unexported method on the `Message` interface, so a new kind of turn is added in `model` and nowhere else. Tool requests live inside `AssistantMessage`, because that is how they arrive: one turn may request several tools.
- `model.Conversation` owns the rules about how turns may follow one another, including when a run is finished and which tool requests are outstanding. Logic of that kind belongs there rather than in the agent loop, so the loop only has to do the work in between.
- A turn is either record or scaffolding, and the type says which. The rule that matters is about the record: what the user asked and what the model answered are kept exactly as they arrived, never rewritten and never dropped, only appended to. A `HarnessInstruction` is agi speaking to the model in its own voice — it travels under the `user` role because no provider has one for the harness, and `Render` wraps it as `[Instructions] Follow these instructions: …` so the model does not read it as the user interrupting. Being scaffolding, it is the only turn that reports `Truncatable() == true`, which is permission rather than obligation: an instruction may be dropped once it has served its purpose, or may stand for the whole run.
- `Conversation.DropFrom` is how scaffolding comes back out when a caller decides it should. It removes a tail that must begin at a truncatable turn, taking the exchange that answered it along: those turns exist only because of the instruction and mean nothing without it. The check on the first turn is what stops the record being cut. A caller that means to remove an instruction takes a mark with `Len` before appending it and unwinds on every path out, so a conversation resumed later carries no trace of how the run was steered.
- Stdout is two different channels. At a terminal it is a display for a person, and tool calls, reasons, and the answer all belong there. When it is piped or redirected it is a channel to another program, so it must carry the final answer as plain text and nothing else. The agent loop prints progress through `output.step`, which is discarded when stdout is not a terminal, and the answer through `output.final`, which always reaches stdout.
- `--verbose` while piping is a request for diagnostics, so the progress stream goes to stderr rather than stdout, leaving the data channel clean.
- Whether stdout is a terminal is a separate question from whether color is enabled: `NO_COLOR` at a terminal means plain progress output, not no progress output. Detection lives in `cli.isTerminal`, and routing in `cli.route`, which is kept separate so every branch can be tested without a terminal.
- Terminal styling goes through the helpers in `tools` (`tools.Name`, `tools.Detail`) rather than raw escape codes, so a tool decides which part of its output is a command or a detail, and the styling stays consistent. Color disables itself when output is not a terminal and when `NO_COLOR` is set.
- agi targets only the **intersection** of what mainstream LLM providers support: a conversation of `system`, `user`, and `assistant` messages, and tool calling. That intersection is the whole feature surface, and it is a deliberate ceiling rather than a stage on the way to something richer.
- The reason is that a gateway fronts many providers, and anything outside that core is supported unevenly, behaves differently, or is rejected outright depending on what sits behind the gateway. Building on it would mean either a feature that silently degrades for some providers, or per-provider branching that the `transport.Client` interface exists to avoid.
- In particular, do not add support for **prefix completion** — prefilling the assistant's next turn to steer raw inference — or for **structured output**, meaning schema-constrained or JSON-mode responses. Several current models reject assistant prefill with an error, and structured-output support varies in both syntax and semantics. Where output shape matters, ask for it in the prompt and validate what comes back; where steering matters, use the system prompt or a tool.
- The shape of an answer is therefore asked for and checked locally, never requested from the provider. `model.Schema` is that check: a flat set of named scalar fields (`string`, `bool`, `int`, `decimal` — no nesting, no arrays, nothing optional), parsed from the `--schema` spelling `id:int:"User ID", name:str:"Full name"`, with `ValidateJSON` reporting every way a document fails to match it in a single error so one correction can fix all of them.
- A `--schema` run is the ordinary run plus a second phase, never a different run. The agent loop ends the way it always does, on a prose answer, and only then is the model asked to restate that answer as JSON, up to `maxSchemaAttempts` times, each rejection handed back as a harness instruction saying what was wrong. The reasoning is asked for in prose rather than read off the model's hidden reasoning, which only some providers expose. Attempts that run out fail the run: a wrongly shaped document is worse than none for whatever is reading the pipe. The whole phase is dropped from the conversation before returning — instruction, attempts, and the accepted JSON alike — leaving the prose answer as the last word.
- The prose answer is the deliverable without a schema and the reasoning with one, so with a schema it goes to `output.step` and the JSON to `output.final`. A piped schema run carries the document alone.
- Every run is kept unless the user says otherwise. `session/` stores one in a directory — a fresh one under `TMPDIR` by default, the one `--session` names if it is given, and nowhere at all with `--rm`, which is why `session.Discarded` exists: a session that stores nothing answers every method, so no caller has to ask whether there is anywhere to write. The two flags contradict each other and are refused together.
- Passing the same `--session` directory again is the entire chat mechanism. `Session.Resume` loads the stored conversation and appends the new prompt as the next user turn, so running agi twice is a two-turn conversation and nothing in the agent loop needs to know which it is. The system prompt is recomputed rather than restored, because it describes the tools registered for *this* run.
- A snapshot is agi's own account of a run, not a provider's: a `HarnessInstruction` is stored as a harness instruction, since the user role it travels under is the gateway's business. The file format lives in `session/` and is kept apart from `model` for the same reason the wire format is, and it carries a version so a build that meets a file it does not understand refuses it instead of guessing. The snapshot is written only when a run finished, so a failed run leaves the session at its last complete exchange.
- `transport/` owns every exchange with an external gateway. The `transport.Client` interface is the only thing the rest of the program depends on; each gateway gets a subpackage.
- Gateways are opt-in and never assumed. There is no default gateway URL, so agi contacts one only when its environment variable is set, and the Go module must not take on a dependency on any gateway.
- Running a gateway is the user's concern, not the repository's. Do not add Docker Compose files, container definitions, or other deployment manifests here.

## Environment variables

Every environment variable the application reads. Keep this table complete —
adding one to the code without adding it here is an incomplete change.

| Variable          | Read by            | Required | Meaning                                                                                              |
| ----------------- | ------------------ | -------- | ---------------------------------------------------------------------------------------------------- |
| `AGI_VERBOSE`     | `cli`              | No       | Enables verbose output, equivalent to the `--verbose` / `-v` flag. Any non-empty truthy value enables it. |
| `AGI_MODEL`       | `cli`              | No       | Model the agent runs on. Defaults to `deepseek-chat`.                                                 |
| `LITELLM_URL`     | `transport/litellm` | No      | Base URL of a LiteLLM gateway, for example `http://localhost:4000`. Setting it is what enables the gateway; there is no default. Must be an `http` or `https` URL. |
| `TMPDIR`          | `session`          | No       | Where a run agi names for itself is stored, read by the standard library rather than by agi. Defaults to `/tmp`. Ignored when `--session` names a directory. |
| `DEEPSEEK_KEY`    | `transport/litellm` | No      | Bearer token presented to the gateway. Omit it for a gateway that needs no authentication. Named for the provider key the user keeps in their environment; the gateway decides whether to forward it or substitute its own. |

## Layout

| Path                 | Contents                                                          |
| -------------------- | ----------------------------------------------------------------- |
| `main.go`            | Entry point; wires the CLI and maps errors to an exit status.      |
| `cli/`               | Command tree, flags, command actions, and the agent loop.          |
| `model/`             | Core domain types (messages, conversations, schemas) and their validation. |
| `transport/`         | The `Client` interface gateways implement, plus request/response.  |
| `transport/litellm/` | Client for a LiteLLM gateway, configured from the environment.     |
| `tools/`             | The `Tool` contract and the registry that collects them.           |
| `prompt/`            | The system prompts, one Markdown file each, compiled in with `go:embed`. |
| `tools/controlflow/` | Agent control-flow primitives, which explain themselves in the system prompt. Currently `subagents`. |
| `tools/workspace/`   | Optional tools that do work on the machine. Currently `bash`.      |
| `session/`           | Storing a run in a directory and resuming it, with its own file format. |
