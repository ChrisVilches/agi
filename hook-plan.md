# Hooks: decisions and plan of work

`hooks-specification.md` says what hooks do. This document says why each
decision was taken, which alternatives were weighed, what is still open, and
in what order the work happens. It supersedes nothing — it sits beside the
specification, and the specification wins on any question of behaviour.

Nothing here is implemented yet.

## 1. Where the decisions came from

Three sources, in this order of authority:

1. **`hook-spec.md`**, the user's original. Superseded as a description of the
   feature, but still the reasoning behind the error rules in section 8.3 of
   the specification.
2. **The user's answers while planning**: mimic Claude Code; two events;
   `--hooks DIR` holding `manifest.json`; both the shell and exec forms; paths
   relative to the manifest's folder; other exit statuses handled Claude
   Code's way; `ask` treated as `deny` and documented as a flavor limitation;
   the mechanism as a black box the agent loop calls.
3. **Claude Code's documentation**, <https://code.claude.com/docs/en/hooks>,
   read during planning. It confirmed that a `PreToolUse` hook really can
   change a tool's arguments, through `hookSpecificOutput.updatedInput`, and
   that the override is partial rather than a whole replacement.

## 2. Decisions and the reasoning

Each of these was an open question. The specification states the answer; this
is why.

**The `command` collision.** An entry's `command` is the hook program;
`if.command` is the `bash` tool's own parameter. Two things, one word. Keeping
`command` beats renaming it to `run` or `program`, and beats Claude Code's
nested `{"type": "command", "command": ...}`: mimicry is the standing rule, a
hook author will type `command` without thinking, and nothing is actually
ambiguous — one is a key of `if` and the other a key of the entry. The nesting
exists in Claude Code only to carry `type`, which agi does not have.

**Matchers match anywhere.** Go's regexp does by default, so `bash` also
matches `bashlite`. Anchoring would be safer and is what a careless user
expects, but Claude Code does not anchor, and the surprise is one its users
already live with. Documented, with `^bash$` shown in the README.

**But every matcher is a regular expression.** Claude Code compares a matcher
literally when it is made only of letters, digits, spaces, commas and pipes,
and as a regular expression otherwise. That dual rule is the one place a
smaller, more predictable rule beats mimicry, and the original specification
says so plainly. It is also the divergence most likely to bite somebody moving
a file over, so it gets its own row in the README's table.

**Non-string parameter values are rendered and matched.** The alternative —
only strings can match — quietly ignores a condition the user wrote, which is
the worst behaviour a guard can have. Matching a value's compact JSON is one
rule, never silently fails to fire, and reaches into arguments whose shape agi
does not know.

**A missing parameter is not an error.** Tools have optional parameters, and
OR semantics mean another condition may still fire the hook.

**Hooks run in sequence, not in parallel.** This is a divergence, and the
reason is `updatedInput`: each hook's output is the next one's input, and
chaining is meaningless in parallel. The user asked for a sequence
independently, which settles it.

**Hooks apply to every agent and every tool.** A guard with a hole in it is
not a guard. In the code this means the black box lives in `cli.wiring`, the
one value everything that builds a registry or starts an agent is built from
— which exists precisely so the run, a fork and a subagent cannot be given
different things by accident.

**`updatedToolOutput` flattens a structured answer.** The `subagents` tool
answers with one section per subagent, and that structure survives into the
snapshot. Refusing to rewrite it would mean a hook that works for three tools
and mysteriously does not for a fourth. Flattening is the honest consequence
of asking for the output to be replaced, and the log still holds what the tool
said.

**`additionalContext` becomes a harness instruction.** That is exactly what a
harness instruction is — agi speaking to the model in its own voice — and it
is the only turn type that may later be dropped. Appending it through
`cli.instruct` records it in the same call, so context injected by a hook
cannot be invisible in the log.

**`tool_output` mimics Claude Code's object shape.** agi's tool output is a
string, but two extra fields mean a script written against Claude Code's
documentation reads `.tool_output.text` and finds it.

**What agi sends on standard input.** Only fields with a true twin, under
Claude Code's names. One of them is a compromise worth noticing: `session_id`
carries a directory, which is a small lie, where `session_dir` would be honest
but would not be found by a script written for Claude Code. Mimicry won. Say
so in the README rather than leaving it to be discovered.

**No invented environment variables.** Claude Code exports
`CLAUDE_PROJECT_DIR` and friends. An agi equivalent would mean a new row in
the environment variable table for a value the hook already receives on
standard input as `cwd`.

**No per-hook `timeout` field, for now.** `--hook-timeout` covers the run, and
an unknown field in the manifest is refused, so adding one later breaks
nobody's existing file. Adding it early means two numbers to document and a
precedence rule for when they disagree.

**No duplicate check.** Two tools of one name are refused because the model
addresses a tool by name and could not tell them apart. Nothing addresses a
hook, so two identical hooks are merely two hooks.

## 3. The one hard piece: ending a run from inside a subagent

This needs deciding before any code is written, and it is the only part of the
wiring that is not obvious.

A subagent that fails does not fail the call: its section says "This subagent
failed", and the other subagents' answers are kept. That is deliberate and
right for ordinary failures. But a hook that ends the run must end the run,
not be swallowed into a paragraph the model reads.

Three ways:

**(a) Context cancellation plus a shared reason.** The black box is the run's,
shared by every agent. The first hook that ends the run stores its reason
there and cancels a context the whole run is waiting on. Every agent then
stops the way it already stops for Ctrl-C, and the top of the run replaces the
interruption with the stored reason before reporting it.

**(b) A typed error that propagates.** A sentinel error which `subagents` must
be taught not to swallow and `cli.contain` must be taught to let through.
Honest, but it puts knowledge of hooks into `tools/native`, which today knows
nothing about them.

**(c) Only the agent that made the call ends.** Simplest, and wrong: a guard
that stops one subagent while the other seven carry on is not a guard.

**Chosen: (a).** It reuses the mechanism that already exists for the same
shape of problem, and it keeps `tools/native` ignorant of hooks. Two things
follow. The log needs a line saying a hook ended the run, so the
`run.interrupt` lines beneath it are explained. And the run's reported error
must be the hook's, so the exit status is 1 and the message is the hook's
reason rather than "interrupted".

A tidy-up falls out of it: `cli.abandon` hardcodes the interruption as the
cause. Giving it the cause as an argument makes "a run can be stopped by two
things" visible in the code rather than implied.

## 4. Still open

Small things, to settle while writing the code rather than before it.

**How the black box reports to the log.** It must record what it did, and the
log handle differs per agent — a fork records under its own name. The likely
answer is an observer function given to a copy of the guard, the way `wiring`
already takes a copy of the log and of the output for a fork. The alternative
is for `Before` and `After` to hand back a trace the caller records, which
keeps the black box free of any notion of logging but makes its signature less
simple than the user asked for.

**Whether `cli.abandon` is generalized or a second function is written.** Both
work; which reads better will be clear with the code in front of us.

**Where the flags' reading lives.** `hooksFlag` beside `userToolsFlag` and
`skillsFlag` is the obvious place, but the hooks and the tools now read the
same file, and reading it twice for one folder given to both flags may be
worth avoiding — or may be beneath noticing, since it is a few kilobytes read
once before the run starts.

## 5. Shape of the code

### A new package, `hooks/`

It knows nothing about the CLI, the agent loop, or the event log. It needs a
tool's name and its raw JSON arguments, so it need not import `model` either.

- `hooks.go` — the black box and its two methods.
- `manifest.go` — reading the `hooks` key of a folder's `manifest.json`, and
  everything checked while reading it.
- `match.go` — the matcher and the `if` conditions.
- `run.go` — starting the program, the timeout, and reading what it said.
- `protocol.go` — the JSON sent in and the JSON read back.

The surface, roughly:

```go
// Guard is every hook a run was given.
type Guard struct{ ... }

// Load reads the manifests in dirs.
func Load(dirs []string, timeout time.Duration) (*Guard, error)

// None is a run that was given no hooks: every call passes through untouched.
func None() *Guard

// Call is what a hook is told about.
type Call struct {
    Tool      string
    Arguments json.RawMessage
    RequestID string
    Agent     string
}

func (g *Guard) Before(ctx context.Context, c Call) (json.RawMessage, error)
func (g *Guard) After(ctx context.Context, c Call, output string) (string, error)
```

Two error types come back out:

- `*hooks.Refusal` — this call is refused, the reason goes to the model as a
  tool failure, the run carries on.
- `*hooks.Stop` — the run ends, and the reason is what the user is shown.

`tools/userdefined` already solves most of `run.go`'s problems — running a
program with a timeout, a `WaitDelay` so a child holding the pipes open cannot
outlive the call, truncating what comes back, telling a cancelled run from a
timed-out program. Read it before writing this, and lift the shape rather than
the code: the two protocols differ enough that sharing an implementation would
mean a function with a flag in it.

### Changes in `cli/`

- `--hooks` (repeatable, `Validator: namesPaths`) and `--hook-timeout`
  (`Validator` for the one-second floor).
- `hooksFlag`, reading the folders before a gateway is contacted.
- `wiring` gains the guard.
- `converse` calls `Before` and `After` around `registry.Answer`.
- `abandon` learns that a run can be stopped by something other than Ctrl-C.
- New event kinds; the preamble gains the hooks.

### Changes in `tools/userdefined/`

The manifest decoder refuses unknown fields, so it has to learn that `hooks`
is a key it may see and ignore. That is a one-line change and a test, but it
is the hinge the shared-file decision hangs on — without it, a folder holding
both tools and hooks fails to load as either.

## 6. Order of work

Each step is a commit. Each commit updates `AGENTS.md` in the same breath, per
the repository's own rule, and ends with the tests and `go install`.

1. **`tools/userdefined`**: tolerate a `hooks` key in the manifest. Small, and
   everything else depends on it.
2. **`hooks`**: reading a manifest and matching. Pure functions, easy to test
   hard.
3. **`hooks`**: running a program and the protocol — the two start forms, exit
   statuses, the JSON output fields, the timeout, the two error types. Tested
   with small scripts written into a temporary folder, the way
   `tools/userdefined` is tested.
4. **`hooks`**: the black box on top of those, including the sequencing and
   the chaining of `updatedInput`.
5. **`cli`**: the flags, the wiring, and the calls in the agent loop; the path
   that ends a run, including from inside a subagent.
6. **`cli`**: the log kinds, the terminal lines, and the verbose preamble.
7. **Documentation**: `README.md`, including the divergence table, and
   `nutshell-spec.md` regenerated from its template — "How can a run be
   interrupted?" now has a new answer.

## 7. Documentation owed

- **`AGENTS.md`** — a block of conventions for hooks, a `hooks/` row in the
  layout table, `--hooks` and `--hook-timeout` described where the other flags
  are. No new environment variable, so that table is untouched. Written in the
  same commit as the change it describes, never afterwards.
- **`README.md`** — a section between "Tools you wrote yourself" and "Skills":
  the file, the two events, the protocol, and the divergence table from
  section 10 of the specification. The flavor-not-a-copy point belongs at the
  top of that section, not in a footnote.
- **`nutshell-spec.md`** — regenerated from `nutshell-spec-template.md`, which
  is the user's file and is never modified.
