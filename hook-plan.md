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
Claude Code's names.

`session_id` took four drafts. The wrong turns are worth keeping, because
each was wrong for a different reason and the reasons are the design.

A run in agi is identified by its session directory. Claude Code's
`session_id` is a short opaque string. Something has to give.

1. **The whole path, under `session_id`.** Unique and honest, but it breaks
   the one pattern every state-keeping hook script is built around —
   `FLAG_FILE="/tmp/notified_${SESSION_ID}.flag"` — because the value has
   slashes in it.
2. **The directory's last segment, with the full path beside it in
   `session_dir`.** Short and filename-safe, and wrong: a last segment is not
   unique, so `~/work/notes` and `~/play/notes` would have shared an id and a
   hook keeping state under it would have silently mixed two conversations.
   It also put one fact in two fields, which this repository avoids
   everywhere else, for the reason `transport.Request` carries no model name.
3. **The whole absolute path, slug-encoded, `/` written as `-`.** Unique,
   filename-safe, legible, and with a real precedent: Claude Code stores a
   project's own directory that way, as `-home-chris-dev-agi`. Two things
   against it. The length is unbounded, so a deep path inside a script's own
   prefix can approach a filesystem's limit. And the precedent is for the
   wrong thing — Claude Code slugs a *project directory* for its own storage,
   where a person browses it, while its `session_id` is an opaque UUID. A
   ported script expects the second.
4. **A hash of the absolute path.** Chosen.

**Settled: the first 16 hexadecimal characters of the SHA-256 of the session
directory's absolute, cleaned path.** Fixed length, filename-safe, collision-
free for any plausible number of sessions at 64 bits, the same on every call
of a run and on every later run of that session, and shaped like the value a
ported script expects. The path is not sent at all: nothing asked for it, and
a field nobody needed is harder to take back than one nobody has yet.

What it costs is legibility — a leftover state file named after an id says
nothing about which conversation made it. That is paid back in the `--verbose`
opening block, which prints the id beside the directory it came from, and
nowhere else. Not the event log: that records the directory already, and the
id is a plain function of it.

The lesson worth carrying is narrower than "send the real value", which is
what draft 3 was written under. Deriving a value for another program's
clients is fine. Losing information while deriving it is not, and draft 2's
defect was uniqueness rather than derivation.

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

## 3. A hook stopping a nested agent

This looked like the hard part and is not, once the right precedent is
noticed. It is written up here because the wrong answer was very nearly taken.

The question: a hook stops an agent, and that agent is a subagent or a fork.
Does the whole run end?

The first plan said yes, on the grounds that a guard which stops one subagent
while the other seven carry on is not a guard. That was wrong twice over.

It was wrong about safety. The guarded tool never ran, so nothing failed open.
The other subagents are not running unguarded — they are running guarded, and
the same hook stops each of them the moment it matches one of their calls.
Blocking a call is not the same as permitting it.

And it was wrong about the repository. agi has already answered this question
once, for the only other thing a nested agent could use to end a run.
`cli.contain` catches a `*native.Termination` raised inside a fork or a
subagent and turns it into text the parent reads, and its reasoning is exactly
the reasoning here: otherwise the user is shown "a run ended by an agent they
never spoke to, giving a reason about a task they did not set". A hook stop
has the same shape and gets the same treatment, in the same function.

**Decided: a hook stops the agent whose call it guarded, and nothing else.**
The run ends only when that agent is the run's own. A fork's stop comes back
as the tool failure answering the `fork` call; a subagent's comes back as its
section of the results. The parent then reads a reason and decides — try the
work another way, do it itself, or give up and say so in its answer — which is
what an agent is for.

The guard still wins in the end. A parent that responds by attempting the same
work itself fires the same hook, and is stopped by it as the run's own agent,
which ends the run. It costs a few turns and buys the model a chance to
explain what happened.

Two consequences for the implementation:

- **No shared state and no context cancellation.** The earlier plan wanted
  both, to carry a stop from a subagent up to the top. Neither is needed: the
  error travels the path every other nested failure already travels. A
  subagent's error is already turned into its section by
  `Subagents.answer`; a fork's error already becomes a tool failure in
  `converse` when the context has not been cancelled, which for a hook stop it
  has not been.
- **`cli.abandon` does not need generalizing** for nested agents. It still
  needs the cause as an argument, or a sibling function, for the one case that
  does end a run: a hook stopping the run's own agent, where the reason on
  standard error must be the hook's and the exit status 1 rather than 130.

Worth being exact about one thing, because the resemblance to Ctrl-C is
misleading. An interruption cancels the context every agent is waiting on. A
subagent's section does then say it was interrupted, but the parent never acts
on it, because the parent was interrupted in the same instant. A hook stop
cancels nothing: one agent ends, and the parent carries on with a whole
conversation and something to read.

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

**Where the session's path is made absolute.** `session.Open` keeps the
directory as the user typed it, so `Session.Dir` can be relative, and the hook
payload needs it absolute. Two ways: resolve it where the payload is built,
which touches nothing else; or have the `session` package store the absolute
path from the start, which would also make the log's `session` field and the
line printed at the top of a run absolute. The second is arguably an
improvement in its own right — `./chat` in a log is worth less than the whole
path — but it changes behaviour outside this feature, so it is a separate
question and should not ride along on a hooks commit. Start with the first.

**Where the flags' reading lives.** `hooksFlag` beside `userToolsFlag` and
`skillsFlag` is the obvious place, but the hooks and the tools now read the
same file, and reading it twice for one folder given to both flags may be
worth avoiding — or may be beneath noticing, since it is a few kilobytes read
once before the run starts.

## 4a. The revamp this is built against, and does not wait for

The user intends to merge `--tools` and `--skills` into a single
`--resources DIR`. One folder would then hold a `tools.json`, a `skills.json`,
and — by the time it happens — a `hooks.json`, so that a user keeps one folder
of everything they bring to a run instead of naming the same folder to three
flags.

**Hooks are not built against that shape.** They go into `manifest.json`'s
`hooks` key now, as section 4 of the specification describes, and they move
with everything else when the revamp comes. Building against a flag that does
not exist yet would be guessing at a format nobody has written down, and the
repository's standing rule is that machinery for a future that has not
arrived is machinery nobody can test.

What the work owes the revamp is a **TODO comment beside the flags in
`cli/root.go`**, naming `--resources` and the three files, so that whoever
does it finds the three flags in one place with a note saying they were always
meant to be one. That is the same treatment the compaction subcommand and
Windows file locking already get: a TODO where the change would be made, and
nothing else anywhere.

One thing to carry into that work when it happens: a folder given to
`--resources` would hold hooks and tools together, so the question of whether
naming a folder arms its guards comes back. Today the answer is that `--tools`
does not read the `hooks` key, and a merged flag cannot keep that answer —
naming the folder would have to arm everything in it. That is a decision for
the revamp and not a reason to change anything now.

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

- `--hooks` (repeatable, `Validator: namesPaths`) and `--hook-timeout` (a
  `DurationFlag`, with a `Validator` for the one-second floor). A TODO beside
  them for the `--resources` revamp of section 4a.
- `hooksFlag`, reading the folders before a gateway is contacted.
- `wiring` gains the guard.
- `converse` calls `Before` and `After` around `registry.Answer`, and tells a
  refusal from a stop the way it already tells a termination from an ordinary
  failure.
- `contain` catches a stop raised inside a fork or a subagent, beside the
  termination it already catches.
- `abandon`, or a sibling of it, learns that a run can be stopped by something
  other than Ctrl-C: the reason is the hook's and the exit status is 1.
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
