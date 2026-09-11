# Hooks: specification

This document supersedes `hook-spec.md`, which was written before several
things were settled. Where the two disagree, this one is right.

It says what hooks do, not how to build them. The reasoning behind each
decision, the alternatives that were weighed, and the order of work are in
`hook-plan.md`.

Nothing here is implemented yet.

---

## 1. What a hook is

A hook is a program the user wrote that agi runs at a fixed moment in a run:
before a tool call, and after one. The program is told which tool is about to
run and with what, and it answers with its exit status and, optionally, a JSON
object on standard output.

A hook can let a call through, change its arguments, refuse it, change what
the model reads afterwards, add context to the conversation, or stop the agent
that made the call. agi has no opinion about any of that. The hooks are the user's policy, and a
run given none behaves exactly as it does today.

## 2. This is a flavor of Claude Code

The protocol is Claude Code's, deliberately: the same standard input object,
the same exit statuses, the same JSON output fields. A hook script written for
Claude Code should usually work here. The reference is
<https://code.claude.com/docs/en/hooks>.

It is a flavor and not a copy. agi is a different program — it has no
interactive permission prompt, no plugins, no MCP servers, no transcript file
— so parts of Claude Code's protocol mean nothing here, and a few mean
something slightly different. **Every divergence is listed in section 10, and
every one of them has to appear in the README as well.** A user bringing a
script over is owed the list rather than left to discover it.

## 3. Events

Two, and only two.

| Event | When it runs |
| --- | --- |
| `PreToolUse` | Before a tool call is made. |
| `PostToolUse` | After a tool call has produced a result. |

Both match on the tool's name and on its named parameters.

`PostToolUse` runs only after a call that produced a result. A call that
failed — bad arguments, a tool that refused it — gets no `PostToolUse` hook.
Claude Code has a separate `PostToolUseFailure` event for that case and agi
does not implement it.

An event name in the configuration that is not one of these two fails the run
when the file is read.

## 4. Configuration

### 4.1 The flag

`--hooks DIR` names a folder of hooks. It is repeatable, and the folders are
read in the order they were given, which is the order the hooks run in.

It is a flag rather than an environment variable, for the reason `--tools` is:
a shell that quietly armed a guard on every run would change what the agent is
allowed to do and say nothing about it, and the run it changed would look
exactly like one it had not.

The value is trimmed, and a value that is nothing but whitespace is refused by
the flag itself.

### 4.2 The file

Each folder holds a `manifest.json` at its root — the same filename a folder
of tools uses, because it is the same file. The format has two optional
top-level keys:

```json
{
  "tools": [ ... ],
  "hooks": {
    "PreToolUse":  [ ... ],
    "PostToolUse": [ ... ]
  }
}
```

`--tools DIR` reads the `tools` key. `--hooks DIR` reads the `hooks` key. The
same folder may be given to both flags, and for a folder that holds both that
is the ordinary way to use it.

Giving a folder to only one of the two flags reads only that key. A folder of
tools does not arm guards behind the user's back, and a folder of hooks does
not add tools.

A folder given to `--hooks` whose manifest declares no hooks fails the run,
exactly as a folder given to `--tools` that declares no tools already does:
the user asked for something the folder does not have.

### 4.3 An entry

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "mcp__filesystem__.*|writefile|patchfile",
        "if": {
          "path": "\\.(ts|tsx)$",
          "content": "export default"
        },
        "command": "./scripts/check-exports.sh"
      },
      {
        "matcher": "bash",
        "if": { "command": "^(git push|npm publish)" },
        "command": "./scripts/guard-rails.sh"
      },
      {
        "command": "node",
        "args": ["./scripts/audit.js", "--strict"]
      }
    ]
  }
}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `matcher` | No | A regular expression over the tool's name. |
| `if` | No | One regular expression per named tool parameter. |
| `command` | Yes | The program to run, or a shell command line. |
| `args` | No | Arguments, which also selects the exec form. See 5.2. |

Note that the two entries above both use the word `command` for two different
things. The one inside `if` is the `bash` tool's own parameter; the one beside
it is the hook program. This is Claude Code's spelling and is kept for that
reason.

Any other field in an entry, any other top-level key in the file, and any
unknown event name fail the run when the file is read.

## 5. The program

### 5.1 Where it is

A relative path in `command` is resolved against **the folder the manifest is
in**, never against agi's working directory. `--hooks` is repeatable and the
folders it names will not all sit under the directory agi happens to be
started from.

An absolute path is allowed. Unlike a tool manifest's `path`, a hook program
may live outside its folder: a folder of hooks is a policy for one machine,
not a bundle to hand to somebody else.

### 5.2 How it is started

Two forms, as in Claude Code.

**Shell form** — `command` alone. The string is handed to `sh -c`, so pipes,
globs, `&&` and shell variables all work.

```json
{ "command": "jq -e '.tool_input.command | test(\"^rm\") | not'" }
```

**Exec form** — `command` together with `args`. The program is run directly,
with no shell. Each element of `args` is exactly one argument: nothing is
tokenized, nothing is expanded.

```json
{ "command": "node", "args": ["./scripts/audit.js", "--strict"] }
```

### 5.3 What it inherits

agi's environment and agi's working directory, and nothing else. This is the
protocol user-defined tools already follow, and it is the whole of how a hook
is configured. agi invents no environment variables of its own; everything a
hook needs to know about the run reaches it on standard input.

## 6. The protocol

### 6.1 What the hook reads

A single JSON object on standard input.

```json
{
  "hook_event_name": "PreToolUse",
  "tool_name": "bash",
  "tool_input": { "command": "git push --force", "explanation": "publish" },
  "tool_use_id": "call_01ABC",
  "cwd": "/home/chris/dev/agi",
  "session_id": "/tmp/agi-4f2a1c",
  "agent_id": "subagent-2"
}
```

| Field | Value |
| --- | --- |
| `hook_event_name` | `PreToolUse` or `PostToolUse`. |
| `tool_name` | The tool's name. |
| `tool_input` | The arguments, as the tool will actually receive them. |
| `tool_use_id` | The request id, which is also what the event log correlates by. |
| `tool_output` | `PostToolUse` only. See below. |
| `cwd` | agi's working directory. |
| `session_id` | The session's directory, which is how a run is identified in agi. |
| `agent_id` | `main`, `fork-1`, `subagent-2` — the name the agent records under. |

`tool_input` is what the tool will receive, not necessarily what the model
wrote: a hook earlier in the sequence may already have changed it.

`tool_output` mimics Claude Code's object shape rather than agi's plain
string, so that a script reading `.tool_output.text` finds what it expects:

```json
"tool_output": { "type": "text", "text": "everything up-to-date" }
```

Claude Code's `transcript_path`, `scratchpad_dir`, `permission_mode`,
`effort`, `prompt_id` and `agent_type` are not sent. agi has no such thing,
and a field carrying an invented value would be worse than a missing one.

### 6.2 Exit status

| Status | Meaning |
| --- | --- |
| `0` | Proceed. Standard output is read as JSON when it looks like JSON, and honored. |
| `2` | Block. Standard error is the reason, and the model reads it. |
| anything else | Non-blocking error: the user is shown the reason, the tool runs anyway, the run continues. |

A program that could not be started at all — missing, not executable, or
refused by the kernel — stops the agent that made the call. See section 8.3.

### 6.3 What the hook prints

Standard output is read as JSON when it starts with `{` and ends with `}`,
ignoring whitespace. Anything else is recorded in the log and shown under
`--verbose`, and has no effect on the run. This is where a hook prints its own
progress.

These fields are honored:

| Field | Event | Effect |
| --- | --- | --- |
| `continue: false` | both | Stops the agent that made the call. See 8.3. |
| `stopReason` | both | The message shown when the agent stops. |
| `systemMessage` | both | A warning shown to the user in the progress stream. |
| `hookSpecificOutput.permissionDecision` | pre | `allow` and `defer` proceed; `deny` refuses the call; `ask` is treated as `deny`. |
| `hookSpecificOutput.permissionDecisionReason` | pre | The reason the model is given when a call is refused. |
| `hookSpecificOutput.updatedInput` | pre | A **partial** object overriding named fields of `tool_input`. |
| `hookSpecificOutput.additionalContext` | both | Text put into the conversation beside the tool's result. |
| `decision: "block"` with `reason` | post | Stops the agent that made the call, once it has read the result. See 8.3. |
| `hookSpecificOutput.updatedToolOutput` | post | Replaces what the model reads as the tool's output, **whole**. |

`suppressOutput` and `terminalSequence` are not implemented. Claude Code
ignores the first already, and the second is a terminal feature agi has no use
for. JSON that agi cannot parse, or that fails the shape above, is a
non-blocking error: it is recorded, the user is told, and the run continues.

## 7. Matching

A hook runs when its `matcher` accepts the tool's name **and** its `if`
conditions are satisfied.

### 7.1 `matcher`

A Go regular expression over the tool's name, matched **anywhere in the
name** — so `bash` also matches a tool called `bashlite`. Write `^bash$` for
exactness.

A `matcher` that is absent, empty, or the single character `*` matches every
tool. (`*` alone is not a valid regular expression, and is special-cased for
the same reason Claude Code special-cases it.)

Claude Code's other rule — that a matcher made only of letters, digits,
spaces, commas and pipes is compared literally rather than as a regular
expression — is **not** implemented. Every matcher here is a regular
expression.

### 7.2 `if`

An object whose keys are the tool's **named parameters** and whose values are
regular expressions. Claude Code's `Bash(git push *)` form is deliberately not
implemented: it can only target one argument of one tool, and agi's tools —
user-defined ones especially — take several named parameters.

The conditions are combined with **OR**. The hook runs if at least one of them
matches.

- An `if` that is absent or empty imposes no condition, so the hook runs for
  every call the matcher accepted.
- A parameter the call did not include fails its own condition, and is not an
  error. Another condition may still fire the hook.
- A value that is not a string is matched against its compact JSON. So
  `"timeout": "^12"` matches the number `120`, and an object matches the text
  `{"a":1}`.
- Arguments that are not a JSON object satisfy no condition, so a hook with
  conditions does not run and a hook without them still does. Nothing is
  reported: the tool is about to refuse the call and say why.

### 7.3 Which tools can be matched

Every registered tool, native ones included. A matcher is a regular expression
over a name, and nothing stops it naming `subagents` or `fork`.

### 7.4 Which agents hooks apply to

All of them: the run, a fork, and every subagent. A guard exists to stop
something happening to the machine, and a subagent's `bash` call does the same
damage as the run's.

## 8. Order, and what a hook can stop

### 8.1 Order

Matching hooks run **in sequence**: within a file in the order the entries
appear, and across files in the order `--hooks` named the folders.

They run in sequence rather than in parallel, which is a divergence from
Claude Code, because `updatedInput` chains — each hook's output is the next
one's input — and chaining is meaningless in parallel.

The first hook that refuses a call ends the sequence. The hooks after it never
run, because there is no longer a call for them to inspect.

### 8.2 A refused call

The tool does not run. The reason — `permissionDecisionReason`, or the hook's
standard error — reaches the model as a tool failure, which is exactly what a
failure is for: a call that could not be made. The model gets a turn to
explain itself or try another way, and the run carries on.

### 8.3 Stopping an agent

Three things stop an agent, and only three:

1. The hook program cannot be started: it is missing, not executable, or the
   kernel refuses it.
2. The hook program runs longer than `--hook-timeout`.
3. The hook prints `continue: false`.

The first two are a divergence from Claude Code, which treats both as
non-blocking warnings. The reasoning is the original specification's and it
stands: the executable bit can change while a run is going, so it cannot be
checked once at the start, and a guard that is broken must never fail open —
the call it was there to stop is exactly the call that would otherwise go
through.

**A hook stops the agent whose call it was guarding, and never reaches across
to another one.** What that means depends on which agent that was.

**The run's own agent.** The run ends, the way an interruption already ends
one: the outstanding tool calls are answered as calls that could not be made,
the snapshot is written, the log is closed, and the reason is printed on
standard error with nothing in front of it. This is a clean shutdown and not a
crash — everything said is stored, and the session can be resumed. **The exit
status is 1**, an ordinary failure rather than the 130 an interruption gets,
because the user did not press Ctrl-C.

**A fork or a subagent.** Only that agent ends. Its reason travels back to the
agent that spawned it as the result of the call that spawned it — a tool
failure for a fork, a section saying the subagent failed for a subagent — and
that agent reads it and decides what to do: try the work another way, do it
itself, or give up and say why in its answer.

This is not a special rule invented for hooks. It is what `cli.contain`
already does for the one other thing a nested agent could use to end a run: an
agent the user never spoke to must not end the run, or the user is shown a run
ended over a task they did not set. A hook stop raised inside a fork or a
subagent is contained in the same place and for the same reason.

It costs nothing in safety. The tool never ran, so nothing failed open, and a
parent that goes on to attempt the same work itself fires the same hook and is
stopped by it — this time as the run's own agent, which ends the run. The
guard still wins. It wins a few turns later, having told the model why.

It is worth being clear about how this differs from Ctrl-C, which it
resembles. An interruption cancels the context every agent in the run is
waiting on, so although a subagent's section does say it was interrupted, the
parent never acts on it: the parent was interrupted too. A hook stop cancels
nothing. One agent ends, and its parent carries on with a whole conversation
and a reason to read.

## 9. The timeout

`--hook-timeout` bounds one hook execution: from the moment the process is
spawned to the moment it finishes. The default is 10 seconds and the smallest
value accepted is 1 second, refused by the flag itself before a gateway is
contacted or a session is created.

It is a flag rather than an environment variable for the reason `--max-turns`
is one: a shell that quietly lowered it would make guards fail that would
otherwise have passed.

The bound is per hook execution, not per tool call. A tool call that fires
three hooks may wait three times the timeout.

Claude Code's per-hook `timeout` field is not implemented. An unknown field in
the manifest is refused, so adding one later breaks nobody's existing file.

## 10. Divergences from Claude Code

Every one of these belongs in the README.

| Claude Code | agi |
| --- | --- |
| Many hook events. | Two: `PreToolUse` and `PostToolUse`. |
| Hook types `command`, `http`, `mcp_tool`, `prompt`, `agent`. | A program, and nothing else. |
| `Bash(git push *)` permission-rule filters. | `if`, holding one regular expression per named parameter. |
| A plain matcher is compared literally; only some are regular expressions. | Every matcher is a regular expression. |
| Matching hooks run in parallel. | In sequence, so `updatedInput` can chain. |
| A hook that cannot be started is a warning; the tool runs anyway. | It stops the agent that called the tool. |
| A hook that times out is a warning; the tool runs anyway. | It stops the agent that called the tool. |
| `continue: false` stops Claude entirely. | It stops the agent whose call was guarded. From a fork or a subagent, only that agent ends and its parent reads why; the run ends only when it is the run's own agent. |
| `permissionDecision: "ask"` escalates to the user. | Treated as `deny`. agi has no permission prompt, and a run is as often piped with nobody watching as it is run at a terminal. |
| Relative paths resolve against the working directory. | Against the folder the manifest is in. |
| Per-hook `timeout` field. | One `--hook-timeout` for the run. |
| `suppressOutput`, `terminalSequence`. | Not implemented. |
| `session_id` is an identifier; `transcript_path` points at the transcript. | `session_id` is the session's directory. The other field is not sent. |
| `CLAUDE_PROJECT_DIR` and friends in the environment. | Nothing invented; `cwd` arrives on standard input. |

## 11. What agi records

### 11.1 The conversation

**A hook never rewrites the record.** The assistant turn holds the tool
request exactly as the model wrote it, even when a hook changed the arguments
before the tool saw them. What the user asked and what the model answered are
kept as they arrived; that rule is not relaxed for this.

What a hook can add to the conversation:

- A refused call becomes a tool failure carrying the hook's reason.
- `additionalContext` becomes a harness instruction appended straight after
  the tool's answer. That is what a harness instruction is for — agi speaking
  to the model in its own voice — and it is the one turn type that may later
  be dropped.
- `updatedToolOutput` replaces the text of the tool's answer. When the answer
  was structured — the `subagents` tool answers with one section per subagent
  — the replacement flattens it to a plain result. That is the honest
  consequence of asking for the output to be replaced, and the log still holds
  what the tool actually said.

### 11.2 The log

Three new event kinds:

| Kind | What it records |
| --- | --- |
| `hook.call` | A hook about to run: the event, the program, the tool it is guarding, and the request id that joins it to the `tool.call` line. |
| `hook.result` | What it decided — proceeded, changed the arguments, refused the call, stopped the agent — and how long it took. |
| `hook.failure` | A hook that could not be run at all, or timed out. |

`run.start` also records which hooks the run was armed with, for the reason
the system prompt is recorded: it is assembled from how agi was invoked, a
snapshot deliberately does not keep it, and a run that cannot be reproduced
from its log is a log that failed at its job.

A hook that stopped an agent is recorded under that agent's own name, like
every other line, so which agent a guard actually stopped can be read off the
log rather than inferred. When the agent it stopped was the run's own, the
line explains the outstanding calls answered as abandoned beneath it.

### 11.3 The terminal

A tool call is announced before its hooks run, showing the arguments the model
wrote. Announcing afterwards would leave the screen silent for as long as the
hooks take — up to the timeout — with nothing saying what agi was waiting on.

A second line follows when a hook changed the call or refused it, so that what
the user reads is what actually happened rather than what was asked for.

`systemMessage` is printed in the progress stream. A hook's non-JSON standard
output is printed only under `--verbose`.

A verbose run lists the hooks in its opening block — event, matcher and
program, one line each — beside the model, the system prompt and the tools. A
hook can end a run or silently rewrite what a tool was asked to do, which
makes it exactly the kind of thing somebody watching a run needs to have been
told about beforehand.

## 12. What is checked, and when

**When the folders are read**, before a gateway is contacted or a session is
created — the rule `--tools`, `--skills`, `--sys` and `--schema` already
follow:

- the manifest parses as JSON;
- no unknown top-level key, no unknown event name, no unknown field in an
  entry;
- every regular expression compiles;
- `command` is not empty;
- `args`, where present, goes with a `command` that names a program.

**When the hook runs**, and never before: whether the program exists and can
be executed. This is the specification's own instruction. Permissions change
while a run is going, so a check at the start would be a promise agi cannot
keep, and the only honest check is the attempt itself.

Two identical hooks in two folders are not an error. Both run, one after the
other. The rule that refuses two tools of one name exists because the model
addresses a tool by name and could not tell them apart; nothing addresses a
hook.

## 13. The shape of the mechanism

The hooks are a black box the agent loop calls, with two methods. It runs
every matching hook in sequence and hands back either the processed data or a
stopping error. Matching, running, parsing and deciding all stay inside it:
the agent loop learns nothing about regular expressions, exit statuses or JSON
output fields.

```go
// Before runs the PreToolUse hooks and returns the arguments the tool should
// be called with, which may not be the ones the model wrote.
func (g *Guard) Before(ctx context.Context, c Call) (json.RawMessage, error)

// After runs the PostToolUse hooks and returns the output the model should
// read, which may not be the output the tool produced.
func (g *Guard) After(ctx context.Context, c Call, output string) (string, error)
```

Two error types come back out, and the agent loop tells them apart the way it
already tells a termination from an ordinary failure: one refuses the call and
lets the agent carry on, the other stops the agent. The second is contained by
`cli.contain` when the agent is a fork or a subagent, which is the one line
that makes section 8.3 true.

A run given no hooks gets a guard that passes every call through untouched, so
no caller anywhere has to ask whether there are hooks — the same bargain
`--no-log` already makes with the event log.
