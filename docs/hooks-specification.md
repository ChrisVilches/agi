# Hooks: specification

This is the whole of what hooks do, and it is the document to read before
changing any of it. The conventions that follow from it, stated shortly, are
in `AGENTS.md`; this is where the reasoning lives.

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

Section 14 is the other list, and the two are not the same. A divergence is a
decision: agi does this differently and means to. A known limitation is a
place where the behaviour is imperfect and stays that way because fixing it
would cost more than it is worth. Both are written down; neither is an
apology.

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

**This arrangement is known to be temporary**, and a TODO says so beside the
flags in `cli/root.go`. The intended shape is one `--resources DIR` flag
naming a folder that holds a `tools.json`, a `skills.json`, and by then
presumably a `hooks.json`, so that a user keeps one folder of everything they
bring to a run rather than naming the same folder to three flags. Hooks are
built against `manifest.json` now and move with the rest when that happens.

One thing to carry into that work when it comes: a folder given to
`--resources` would hold hooks and tools together, so the question of whether
naming a folder arms its guards comes back. Today the answer is that `--tools`
does not read the `hooks` key. A merged flag cannot keep that answer — naming
the folder would have to arm everything in it — which is a decision for the
revamp and not a reason to change anything now.

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

### 5.3 What it inherits, and where it runs

agi's environment, and nothing else. This is the protocol user-defined tools
already follow, and it is the whole of how a hook is configured. agi invents
no environment variables of its own; everything a hook needs to know about
the run reaches it on standard input.

**Its working directory is the folder its manifest is in**, not agi's. That
is the other half of 5.1 rather than a second decision: the exec form's path
can be resolved before the program starts, but a shell command line is the
user's own text and a path may be anywhere inside it, so the only way
`./scripts/guard.sh` can mean the same thing in both forms is for the program
to run where its manifest lives. It is also what makes a folder of hooks
something that can be moved, copied, or handed to somebody else, which is why
`--hooks` is repeatable in the first place.

This is a divergence from Claude Code, which runs a hook in the project
directory. What it costs is that a hook wanting to look at the project has to
go there itself — `cd "$(jq -r .cwd)"`, using the `cwd` field of 6.1, which
carries agi's own working directory and exists for exactly this.

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
  "session_id": "9b3e07c14f2a1c8d",
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
| `session_id` | An opaque identifier for the run: the first 16 hexadecimal characters of the SHA-256 of the session directory's absolute path. |
| `agent_id` | `main`, `fork-1`, `subagent-2` — the name the agent records under. |

`tool_input` is what the tool will receive, not necessarily what the model
wrote: a hook earlier in the sequence may already have changed it.

**The object has two halves, and the engine reads neither.** `tool_name`,
`tool_input` and `tool_output` are what a hook decides *about*. Everything
else is what it decides *in*: which run this is, which agent is calling, and
where. agi's own hook machinery uses none of it — it matches on the tool's
name and arguments, starts a program, and reads what comes back. Every field
here exists for the script, so each one has to answer what a script would do
with it:

| Field | What a script does with it |
| --- | --- |
| `hook_event_name` | Branches, when one program is wired to both events. |
| `tool_use_id` | Joins its own audit lines to agi's log, which records the same id as `request_id`; and lets a `PostToolUse` hook recognize the call its `PreToolUse` half already saw. |
| `cwd` | Resolves a relative path in the arguments. A guard on `writefile` cannot judge `src/x.ts` without knowing where that is. |
| `session_id` | Keeps state across calls. A hook is a fresh process every time and remembers nothing, so a guard that wants to warn once per run, or count what the agent has touched, needs a key that is the same on every call of one run and different between runs — and one it can put straight into a filename. |
| `agent_id` | Holds a subagent to a stricter rule than the agent the user is talking to, or labels an audit line with which agent did it. |

A field that could not answer that question would not be here.

**`session_id` is an opaque identifier and not a path**, and that is the whole
of what a hook needs. The pattern it exists for is the one every
state-keeping hook script is written around:

```sh
SESSION_ID=$(echo "$INPUT_JSON" | jq -r '.session_id // empty')
FLAG_FILE="/tmp/notified_${SESSION_ID}.flag"
```

Such a script needs the value to be unique per run, the same on every call of
one run, and safe inside a filename. It does not need to know where the
session is, any more than a Claude Code script does.

The three properties come from how it is built. It is a hash, so it is a
fixed sixteen characters of hexadecimal whatever the path was, which no
filename length limit can be troubled by, and it carries no `/` to break the
line above. It is a hash **of the whole absolute path**, so it cannot
collide: a user keeping `~/work/notes` and `~/play/notes` has two ids, where a
scheme built from the directory's last segment would have given them one and
silently mixed two conversations' state. And it is a plain function of the
path, so it is the same on every call and the same on every later run of that
same session, which is what makes state kept under it worth keeping.

**The path is resolved to an absolute one, and cleaned, before it is
hashed.** `--session` takes any directory the user likes, and they may type it
relatively, so the same string can name two directories and two strings can
name one. Both have to be settled before the hash, and they fail in opposite
ways:

- Two runs, one string. `cd ~/work && agi --session ./chat` and
  `cd ~/play && agi --session ./chat` are two different conversations. Hashing
  `./chat` would give them one id, and a hook keeping state under it would mix
  them — the same silent defect the directory's last segment had, arriving by
  a different road. This is the one that matters, because nothing about it
  looks wrong from the outside.
- One run, two strings. `agi --session ./chat` from `~/dev` and
  `agi --session ~/dev/chat` are the same conversation, and would otherwise
  get two ids, so a hook's "warn once per run" would warn twice. Annoying
  rather than harmful.

Cleaning takes care of the spellings that differ only in punctuation — a
trailing slash, a `.` segment, a doubled separator — so `~/chat` and `~/chat/`
are one id.

**Symbolic links are not resolved**, and that is the one case left where a
directory can have two ids: a session reached as `~/chat` and as the
`/mnt/data/chat` it points at hashes differently. Resolving them would mean
asking the filesystem, which can fail and which would make the id depend on
the state of the machine rather than on how agi was invoked. The cost of
leaving it is that a hook's per-run state splits in two, which is the
harmless direction above. Worth knowing, not worth the machinery: agi's own
lock already treats the two as one session, because `flock` is taken on the
directory itself and not on the name used to reach it.

**The session's location is deliberately not sent.** An earlier draft sent the
path as well, so that a hook could read the run's event log while it was going
— which is possible, since the log is appended to and never rewritten. Nothing
asked for it, and the rule above is that a field has to say what a script
would do with it. A hook that one day needs the directory can be given one
then; a field nobody needed is harder to take back than one nobody has yet.

For the same reason nothing here invites a hook to write into a session
directory. It holds the conversation and the record of how it was produced. A
hook that wants scratch space makes its own, the way the `mktemp` tool does.

Because the id is opaque, a person looking at a leftover state file cannot see
which session it belonged to. That is what the identifier being opaque costs,
and it is paid back by `--verbose`, which prints the id in its opening block
beside the session's directory, so the two can be read together. It is not put
in the event log: the log records the directory already, and the id is a plain
function of it.

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

**Whether a hook matches is asked when its turn comes, and not before.** A
`PreToolUse` hook can rewrite the arguments, so the call a later hook judges
is not necessarily the one the model wrote, and its `matcher` and `if` are
tested against the call as it now stands.

This matters in both directions, and the first is why it is a rule rather
than an implementation detail. A hook that rewrote `ls` into `rm -rf /` would
otherwise walk it straight past the guard whose condition is `^rm `, because
that guard was ruled out before the rewrite happened — one hook could be used
to smuggle a call past another. The second direction is the ordinary one: a
hook that rewrites a dangerous call into a safe one takes it out of the reach
of the guards written for the dangerous version, which is exactly what such a
rewrite is for.

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

**It takes a duration**, so `--hook-timeout 30s` and `--hook-timeout 1m` both
work. The two caps agi already has, `--max-turns` and `--max-subagents`, are
plain numbers because they count things that have no unit. This one measures
time, and a bare `30` would leave the user to guess whether it meant seconds
or milliseconds.

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
| A hook runs in the project directory. | It runs in the folder its manifest is in, which is what makes the line above true for a shell command line too. `cwd` on standard input says where agi is. |
| Per-hook `timeout` field. | One `--hook-timeout` for the run. |
| `suppressOutput`, `terminalSequence`. | Not implemented. |
| `session_id` is a short opaque identifier; `transcript_path` points at the transcript. | `session_id` is a short opaque identifier too — a hash of the session's directory — so a ported script uses it unchanged. Where the session lives is not sent, and there is no transcript file to point at: a snapshot and a log are not one. |
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
hook can stop an agent or silently rewrite what a tool was asked to do, which
makes it exactly the kind of thing somebody watching a run needs to have been
told about beforehand.

That block also carries the run's `session_id`, beside the session directory
it was made from. The id is opaque by design, so this is the one place the two
are shown together, and it is what somebody holding a leftover state file
named after an id needs in order to find out which conversation made it. It is
printed only when the run has hooks: with none, nothing will ever see the id.

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

Every hook a run was given is one value, `hooks.Set`, and the agent loop calls
two methods on it — one before a tool runs and one after. Each runs every
matching hook in sequence and hands back either the processed data or a
stopping error. Matching, running, parsing and deciding all stay inside:
the agent loop learns nothing about regular expressions, exit statuses or JSON
output fields.

```go
// Before runs the PreToolUse hooks and returns the arguments the tool should
// be called with, which may not be the ones the model wrote.
func (s *Set) Before(ctx context.Context, c Call) (json.RawMessage, error)

// After runs the PostToolUse hooks and returns the output the model should
// read, which may not be the output the tool produced.
func (s *Set) After(ctx context.Context, c Call, output string) (string, error)
```

It is a `Set` and not a `Guard`, which is what an earlier draft called it.
Guarding is one of the three things a hook does, and the name would have been
wrong for the other two: a hook that rewrites a call's arguments, redacts a
secret out of a result, or adds a line of context is not guarding anything.
Naming the whole after the part is the widening this repository refuses
elsewhere, and it is the reason the native tools are called *native* rather
than *control flow*.

Two error types come back out, and the agent loop tells them apart the way it
already tells a termination from an ordinary failure: one refuses the call and
lets the agent carry on, the other stops the agent. The second is contained by
`cli.contain` when the agent is a fork or a subagent, which is the one line
that makes section 8.3 true.

A run given no hooks gets an empty set that passes every call through
untouched, so no caller anywhere has to ask whether there are hooks — the same
bargain `--no-log` already makes with the event log.

## 14. Known limitations

These are places where the behaviour is imperfect and known to be, as opposed
to section 10, which lists deliberate differences from Claude Code. Each is a
trade that was made on purpose, with what it costs written next to it.

### 14.1 A symbolic link gives one session two identifiers

`session_id` is a hash of the session directory's absolute path, and symbolic
links in that path are **not** resolved. So a session reached as `~/chat` on
Monday and as the `/mnt/data/chat` it points at on Tuesday is one
conversation with two identifiers, and a hook keeping state under that key
keeps two piles of it. A guard that warns once per run warns twice.

Resolving the links would fix it and is not done, because it would mean
asking the filesystem a question that can fail, and because the identifier
would then depend on the state of the machine rather than on how agi was
invoked — which every other part of a run's identity avoids.

What it does not affect is which conversation is which. agi's own lock is an
`flock` on the directory itself and not on the name used to reach it, so two
spellings are one session everywhere it matters; it is only the identifier
handed to hooks that splits.

The failure is in the harmless direction. Two *different* sessions can never
share an identifier, whatever they are called and wherever they are reached
from, because the whole absolute path goes into the hash.

### 14.2 A snapshot does not show that a hook changed a call

When a `PreToolUse` hook rewrites a call's arguments, the conversation keeps
the request exactly as the model wrote it, because the record of what was
said is never edited. The tool then runs with different arguments. So a
snapshot read on its own shows a call and a result that do not quite explain
each other.

The event log holds both, under one request id, which is the log's whole
advantage over the snapshot as an account of what happened. Anybody asking
what a run actually did should read the log; anybody asking what was said
should read the snapshot. That division is deliberate and predates hooks.

### 14.3 Replacing a structured result flattens it

`updatedToolOutput` replaces a tool's output with text. Most tools answer with
text anyway, but `subagents` answers with a structured turn holding one
section per subagent, and that structure is what reaches the snapshot. A hook
that rewrites such a result turns it into a plain one.

Refusing to rewrite those would mean a hook that works for every tool but one,
which is worse. The original is still in the log.

### 14.4 A guard cannot escalate to a person

`permissionDecision: "ask"` is treated as `deny`, because agi has no
interactive permission prompt and a run is as often piped with nobody watching
as it is run at a terminal. A hook that wanted a human decision gets the safe
half of it. See section 6.3.

### 14.5 A stopped nested agent costs turns before the run stops

A hook that stops a fork or a subagent stops only that agent, and its parent
decides what to do next. A parent that responds by attempting the same work
itself fires the same hook and is stopped in turn, which does end the run — so
a guard meant to stop everything may take a few turns to do it, and the model
spends those turns discovering what the guard already knew.

The alternative is a nested agent ending a run the user never asked it to end.
Section 8.3 has the argument in full.

### 14.6 A slow hook is paid for once per hook, not once per call

`--hook-timeout` bounds one hook execution. A tool call matched by three hooks
can therefore wait three times the timeout before the tool runs at all, and
nothing caps the total. Hooks run in sequence, which is what makes
`updatedInput` able to chain, and the price of a sequence is that its waits
add up.
