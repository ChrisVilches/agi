# Hooks: specification

This is the whole of what hooks do, and it is the document to read before
changing any of it. The conventions that follow from it, stated shortly, are
in `AGENTS.md`; this is where the reasoning lives.

---

## 1. What a hook is

A hook is a program the user wrote that agi runs at a fixed moment in a run:
before a tool call, after one, and as agi is about to print something the
model wrote. The program is told what the moment is about — which tool is
running and with what, or what the text says — and it answers with its exit
status and, optionally, a JSON object on standard output.

A hook can let a call through, change its arguments, refuse it, change what
the model reads afterwards, add context to the conversation, stop the agent
that made the call, or change what the user sees on screen. agi has no opinion
about any of that. The hooks are the user's policy, and a run given none
behaves exactly as it does today.

The third moment is not like the other two, and section 3 says how. In short:
a `MessageDisplay` hook changes what is printed and nothing else, because the
text reaches it only after the conversation, the snapshot and the log have
all stored it.

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

Three, and only three.

| Event | When it runs |
| --- | --- |
| `PreToolUse` | Before a tool call is made. |
| `PostToolUse` | After a tool call has produced a result. |
| `MessageDisplay` | As agi is about to show the user something the model wrote. |

The first two match on the tool's name and on its named parameters. The third
matches on nothing, because there is nothing there to match: see 7.5.

`PostToolUse` runs only after a call that produced a result. A call that
failed — bad arguments, a tool that refused it — gets no `PostToolUse` hook.
Claude Code has a separate `PostToolUseFailure` event for that case and agi
does not implement it.

An event name in the configuration that is not one of these three fails the
run when the file is read.

### 3.1 `MessageDisplay` is display-only

It is handed text agi is about to print and may hand back different text to
print instead. **It changes what is printed and nothing else.** The
conversation keeps the model's own words, the snapshot stores them, and the
log records them.

That is not a rule kept by care, the way "a hook never rewrites the record"
is kept for `PostToolUse`. It is true by the shape of the mechanism: the hook
runs at the print site, by which point all three have already stored the
text, so there is no path by which a hook of this event could reach any of
them.

It follows that a hook here has nothing to refuse and no agent to stop. The
three things that stop an agent at a tool event exist because a guard that is
broken must never fail open — the call it was there to stop is exactly the
call that would otherwise go through. A message has already happened. There
is nothing to withhold, nothing to fail open into, and ending a run because a
markdown renderer is not installed would destroy the work to protect nothing.
So for this event the exit status, `continue: false`, a permission decision
and `additionalContext` are all accepted and discarded, which is what Claude
Code does with them too — with the difference that agi says so in a note. See
6.4.

**It applies to every message agi shows, not only the final answer.** Four
sites print text the model wrote, and all four are hooked:

| Site | What it prints |
| --- | --- |
| The agent loop | Prose the model wrote alongside its tool calls. |
| The end of a run | The final answer. |
| The end of a `--schema` run | The prose answer, which is the reasoning there. |
| The end of a `--schema` run | The JSON document. |

Nothing else is. Everything else agi prints — a tool call's announcement, the
session line, a hook's own note, the verbose opening block — is agi's own
chrome, already styled, and passing it through a markdown renderer would
mangle it.

The motivating use is that renderer. The model writes markdown and agi prints
it raw; a hook can pass it through `glow`, or pretty-print a `--schema`
document through `jq`, and the terminal shows something worth reading.
Whether `glow` and `jq` are installed is the hook's business. agi does not
look for them and has no opinion about what a display hook shells out to.

### 3.2 It runs when agi displays something, and not when it displays nothing

That is definitional rather than a policy. A piped run without `--verbose`
sends its progress to `io.Discard`, so no intermediate message is displayed
and there is no display to hook. The final answer is written whatever the
destination, so it is always hooked. Subagents need no thought: they run with
every stream discarded, so nothing they print is displayed and no hook fires
for them. A fork's prose is displayed, and is hooked.

**agi deliberately does not skip the hook when stdout is not a terminal.** An
earlier draft did, reasoning that a `glow` rewrite reaching a pipe would put
escape sequences into a machine-readable channel. That is over-reach: a
display hook need not emit escapes at all, and one that redacts secrets,
translates, or trims a long message is worth running into a pipe. Refusing to
run it would be agi deciding what the user's hook is for.

It follows that a hook which emits escape sequences will corrupt a piped
answer, and that a hook which renders a `--schema` document with anything but
`jq` leaves the pipe carrying what is no longer JSON. **That is the hook
author's business**, exactly as it is their business that a `bash` guard can
be written to rewrite a command into something ruinous. agi documents it and
does not police it. Turning the hook off for piped work is the user's, and
`enabled` is how they say so — see 4.4.

### 3.3 A hook cannot ask what agi's output is attached to

It is worth saying plainly, because it is the obvious thing to reach for and
it does not work. `[ -t 1 ]` inside a hook is **always false**. agi captures
all three of a hook's standard descriptors — stdin carries the payload,
stdout carries the reply, stderr carries the reason — so inside the hook they
are pipes whatever agi's own streams are attached to. A hook written around
that test would take the non-terminal branch forever, and silently.

Opening `/dev/tty` does not answer the question either: it reports whether a
controlling terminal exists, which it does for `agi | jq` run from a shell.
Walking up to the parent process and asking what *agi's* streams are attached
to does answer it, and is fragile today rather than after some future change,
because how many processes sit in between depends on how the hook's own
`command` was written — the exec form and a one-command shell line make agi
the parent, a shell pipeline makes it the grandparent. A hook that climbs one
level is right three times in four and reads the wrong process the fourth,
without saying so. **agi does not do this and does not recommend it.**

Nor does agi add a field to the payload saying whether a terminal is there.
Claude Code has no such field, and the whole point of this event is to stay
close enough that a ported script works. A field would also cost a process
per message to be read at all: a renderer that is wrong for this run would
still be started, would still read its payload, and would answer by handing
back the text it was given.

**The manifest says it instead.** `enabled` names the kinds of run a hook is
for, and a hook that is not for this one is never started. It is on every
event rather than on this one, and 4.4 is the whole of it.

## 4. Configuration

### 4.1 The flag

`--resources DIR` names a folder of everything the user brings to a run: the
tools they wrote, the hooks that guard a call, and the skills the agent may
draw on. It is repeatable, and the folders are read in the order they were
given, which is the order the hooks run in.

It is a flag rather than an environment variable: a shell that quietly armed a
guard on every run would change what the agent is allowed to do and say nothing
about it, and the run it changed would look exactly like one it had not.

The value is trimmed, and a value that is nothing but whitespace is refused by
the flag itself. A folder that is not there, or that is not a folder, is
refused before anything is read.

**Naming a folder brings in everything in it, hooks included.** There used to
be three flags — `--tools`, `--skills` and `--hooks` — and one of the things
three flags could say that one cannot is that a folder of tools does not arm
the guards in it until the user asks for them separately. That promise is
gone, deliberately. What replaces it is that the folder is the user's own and
they named it; an opt-in per kind would be three flags again under another
spelling.

### 4.2 The file

Each folder holds a `hooks.json` at its root, beside the `tools.json` its tools
are declared in and the folders its skills are kept in. Its top-level names are
events, and a name that is not one fails the run when the file is read:

```json
{
  "PreToolUse":  [ ... ],
  "PostToolUse": [ ... ]
}
```

A folder need not hold all three kinds. A folder with no `hooks.json` declares
no hooks and is an ordinary folder to name — a folder of skills alone, say. A
folder that holds none of the three is refused: naming one is asking for what
is in it, and a folder with nothing in it is the wrong path far more often than
it is a deliberate request for nothing.

A `hooks.json` that is there and declares nothing — `{}`, or an event with an
empty list — brings no hooks and is perfectly fine. It says what an absent
file says.

It used to fail the run, on the grounds that somebody wrote the file and meant
something by it, and that was wrong on both counts. An empty declaration is
not ambiguous, so nothing afterwards misbehaves quietly; and emptying the file
is how a person turns their guards off for an afternoon without deleting what
they mean to write again. Since one folder carries tools, hooks and skills
together, refusing the run over an empty `hooks.json` also threw away the
tools and skills beside it, which had nothing to do with it.

What still fails the run is a file that cannot be read as hooks at all: a
top-level name that is not an event, an entry with no program, a pattern that
will not compile. Those are mistakes. Declaring nothing is a decision, and
`tools.json` answers it the same way.

The file used to be a `manifest.json` shared with a folder's tools, one
top-level key each. The price of sharing was that each package declared the
*other's* key as a field it read and threw away, because both decoders refuse a
field they do not know and a folder holding both halves would otherwise load as
neither. Two packages that should know nothing about each other each named the
other's word. A filename that says what the file holds is what removed both.

### 4.3 An entry

```json
{
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
```

| Field | Required | Meaning |
| --- | --- | --- |
| `matcher` | No | A regular expression over the tool's name. |
| `if` | No | One regular expression per named tool parameter. |
| `enabled` | No | Which kinds of run the hook is for. See 4.4. |
| `command` | Yes | The program to run, or a shell command line. |
| `args` | No | Arguments, which also selects the exec form. See 5.2. |

Note that the two entries above both use the word `command` for two different
things. The one inside `if` is the `bash` tool's own parameter; the one beside
it is the hook program. This is Claude Code's spelling and is kept for that
reason.

Any other field in an entry, and any top-level name that is not an event, fail
the run when the file is read.

### 4.4 `enabled`

`enabled` says which kinds of run a hook is for. There are two kinds, and they
are told apart by one thing: whether agi's own standard output is a terminal.

| Written | The hook runs |
| --- | --- |
| absent | In every run. This is what every hook meant before the field existed. |
| `true` | In every run. |
| `false` | In none. The hook is declared and never runs. |
| `["tty"]` | Only where agi's output is a terminal. |
| `["non-tty"]` | Only where it is a pipe or a file. |
| `["tty", "non-tty"]` | In both, which is what `true` says. |
| `[]` | In neither, which is what `false` says. |

```json
{
  "MessageDisplay": [
    { "enabled": ["tty"], "command": "glow -" }
  ],
  "PreToolUse": [
    { "enabled": ["non-tty"], "matcher": "^bash$", "command": "./scripts/strict.sh" }
  ]
}
```

**The list form and the boolean form say the same kinds of thing**, which is
why both are read. `false` and `[]` land on the same answer, and so do `true`
and both names listed. The list is there because the question a hook needs
answered is usually not whether it is on: a markdown renderer is wanted at a
terminal and ruinous in a pipe, and a guard that opens an editor or posts to
a chat room is wanted in the unattended run and merely noisy in the attended
one. Both are "enabled", in opposite halves of the world.

**It is on every event**, and not only on `MessageDisplay`, where the need for
it was first felt. A guard is as entitled to the distinction as a renderer is,
and a field that answered the question at one event and not the others would
be a rule the user has to remember the shape of. It is the opposite of
`matcher` and `if`, which are refused where there is nothing to match: there
is a kind of run at every event.

**The value is checked strictly**, and a mistake fails the run when the file
is read, with the event and the entry's number in front of it:

- a value that is not `true`, `false` or a list — a bare `"tty"`, a number,
  an object, a `null` — is refused with the three shapes named;
- a list holding anything but `"tty"` and `"non-tty"` is refused with the two
  names quoted back.

`"TTY"`, `"ttyy"` and `"pipe"` are all refused for the same reason a dead
matcher is. A name agi accepted and ignored would leave a hook that runs in
neither kind of run — loaded, listed, and silent — which is the failure the
whole of 12 exists to prevent.

**A hook that is off in this run is listed under `--verbose` and said to be
off.** Leaving it out of the list would have the user read what the run is
armed with and find a guard they wrote missing from it; listing it unmarked
would claim it is armed. It is one of the two dead hooks agi allows — the
other is a matcher on a tool agi has and this run does not, which is 12 — and
both are allowed because the user asked for them, so the listing says so.

The two marks are worded apart, because they are corrected differently:

| Mark | Why the hook cannot fire | How to arm it |
| --- | --- | --- |
| `(off in this run)` | `enabled` names the other kind of run. | Edit the field, or run agi the other way. |
| `(watches nothing in this run)` | The matcher accepts no tool this run registered — nearly always the terminal tools without `--enable-terminals`. | Pass the flag that brings the tool. |

A hook can be both, and the listing shows the first: `enabled` is off wherever
the hook is run from, where the matcher is off only until the run is given the
tool.

**What `tty` means is agi's standard output and nothing else.** It is the same
fact that decides where progress goes (see 3.2), read once when the run
starts. It is not the agent's: a subagent's streams are all discarded and a
fork's are marked, and neither changes what agi is attached to, so a hook that
is on in a run is on wherever in that run it fires. A run started with no
answer about it — which is to say a caller of the package that never said — is
read as not at a terminal, because the hook written for a terminal is the one
that paints escape sequences, and not running it leaves a message plain rather
than leaving a pipe full of codes.

## 5. The program

### 5.1 Where it is

A relative path in `command` is resolved against **the folder the `hooks.json`
is in**, never against agi's working directory. `--resources` is repeatable
and the folders it names will not all sit under the directory agi happens to
be started from.

An absolute path is allowed. Unlike a `tools.json` entry's `path`, a hook
program may live outside its folder: a folder of hooks is a policy for one
machine, not a bundle to hand to somebody else.

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

**Its working directory is the folder its `hooks.json` is in**, not agi's.
That is the other half of 5.1 rather than a second decision: the exec form's
path can be resolved before the program starts, but a shell command line is
the user's own text and a path may be anywhere inside it, so the only way
`./scripts/guard.sh` can mean the same thing in both forms is for the program
to run where its `hooks.json` lives. It is also what makes a folder of hooks
something that can be moved, copied, or handed to somebody else, which is why
`--resources` is repeatable in the first place.

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
| `hook_event_name` | `PreToolUse`, `PostToolUse` or `MessageDisplay`. |
| `tool_name` | The tool's name. Tool events only. |
| `tool_input` | The arguments, as the tool will actually receive them. Tool events only. |
| `tool_use_id` | The request id, which is also what the event log correlates by. Tool events only. |
| `tool_response` | `PostToolUse` only: what the tool answered. Its shape is the tool's own — see 6.1.1. |
| `duration_ms` | `PostToolUse` only: how long the call took, in milliseconds. |
| `delta` | `MessageDisplay` only: the text agi is about to print. |
| `index` | `MessageDisplay` only, always `0`. See below. |
| `final` | `MessageDisplay` only, always `true`. See below. |
| `cwd` | agi's working directory. |
| `session_id` | An opaque identifier for the run: the first 16 hexadecimal characters of the SHA-256 of the session directory's absolute path. |
| `agent_id` | `fork-1`, `subagent-2` — the name the agent records under. **Absent for the agent the user is talking to.** |

`tool_input` is what the tool will receive, not necessarily what the model
wrote: a hook earlier in the sequence may already have changed it. `delta`
is likewise what will be printed rather than what the model wrote, since an
earlier display hook may already have rewritten it.

**`agent_id` is sent only from inside a fork or a subagent**, which is Claude
Code's rule and the reason agi follows it: that program's own documentation
tells a script to read the field's presence as the test for a subagent. Agi
naming its main agent here — it records under `main` everywhere else — would
have every ported guard read every call as a subagent's.

**The fields that belong to one event are sent only at that event**, so a
`MessageDisplay` payload carries no `tool_name` and a tool payload carries no
`delta`. A field that arrived empty at every other moment would have a script
branching on a call that does not exist.

A `MessageDisplay` payload therefore looks like this:

```json
{
  "hook_event_name": "MessageDisplay",
  "delta": "## Findings\n\nThe disk is nearly full…",
  "index": 0,
  "final": true,
  "cwd": "/home/chris/dev/agi",
  "session_id": "9b3e07c14f2a1c8d"
}
```

**`index` and `final` are constants here, and they are the truthful ones.**
Claude Code fires this event repeatedly as a message streams, numbering the
batches and marking the last. agi fires it once, over a message that is
already complete — which is exactly what Claude Code itself does in a
non-interactive run, where its documentation says the single call carries the
whole message with `index` zero and `final` true. So a renderer that tracks
progress through a message by those two fields behaves correctly under agi
without knowing any of this.

**There is deliberately no field saying which of the four display sites this
is.** An earlier draft had one, on the theory that a script could not
otherwise tell a `--schema` document from prose that happens to contain JSON.
That was wrong: a fenced block contains its backticks, so it never parses as
JSON and falls through to the markdown renderer, which then renders the fence
as a fence. The content is self-describing, and the branch a script writes is
two cases — see 7 for it. The only message both branches could disagree
about is a bare JSON document with no prose around it, which is either a
`--schema` document or a model that answered with nothing else, and
prettifying it is right either way. A field agi invented would also be a
field a ported Claude Code script does not know to read.

Claude Code sends `turn_id` and `message_id` alongside `delta`. agi does not:
it has no such identifiers, and a field carrying an invented value would be
worse than a missing one — which is the rule 10.1 applies to the rest.

**The object has two halves, and the engine reads neither.** `tool_name`,
`tool_input`, `tool_response` and `delta` are what a hook decides *about*.
Everything else is what it decides *in*: which run this is, which agent is
calling, and where. agi's own hook machinery uses none of it — it matches on
the tool's name and arguments, starts a program, and reads what comes back.
Every field here exists for the script, so each one has to answer what a
script would do with it:

| Field | What a script does with it |
| --- | --- |
| `hook_event_name` | Branches, when one program is wired to more than one event. |
| `tool_use_id` | Joins its own audit lines to agi's log, which records the same id as `request_id`; and lets a `PostToolUse` hook recognize the call its `PreToolUse` half already saw. |
| `cwd` | Resolves a relative path in the arguments. A guard on `writefile` cannot judge `src/x.ts` without knowing where that is. |
| `session_id` | Keeps state across calls. A hook is a fresh process every time and remembers nothing, so a guard that wants to warn once per run, or count what the agent has touched, needs a key that is the same on every call of one run and different between runs — and one it can put straight into a filename. |
| `agent_id` | Holds a subagent to a stricter rule than the agent the user is talking to, or labels an audit line with which agent did it. Its absence says the call is the main agent's. |
| `duration_ms` | Notices a call that took long enough to be worth saying something about, without timing it from the outside. |

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

Claude Code's `transcript_path`, `scratchpad_dir`, `permission_mode`,
`effort`, `prompt_id`, `agent_type`, `mcp_server`, `turn_id` and `message_id`
are not sent. agi has no such thing, and a field carrying an invented value
would be worse than a missing one. Section 10.1 lists every one of them, with
what a script that reads one will find instead.

### 6.1.1 The shape of `tool_response`

**`tool_response` has the tool's own shape, not one shape for every tool.**
That is Claude Code's rule rather than a complication agi added: there the
field carries whatever the tool answers with, so its `Write` gives an object
with `filePath` and `type` and its `Bash` gives one with `stdout`, `stderr`,
`interrupted` and `isImage`.

**`bash` is the only tool in agi with a shape.** Every other one — the
workspace tools `terminal`, `mktemp`, `patchfile` and `writefile`, the native
tools, and every tool the user wrote — answers with a string, and for those
the field *is* that string:

```json
"tool_response": "everything up-to-date"
```

A **user-defined tool** is worth saying out loud, because it is a script and
a reader may expect it to be shaped like `bash`. It is not. agi gives such a
script two separate pipes, and then uses only one of them: on exit zero the
tool's result is its standard output alone, and its standard error is
discarded; on any other status the call becomes a tool *failure*, carrying
standard error, and a failed call runs no `PostToolUse` hook at all (see 3).
So a hook is shown a user-defined tool's standard output, as a string, and
has no way to see its standard error in either case.

`bash` is the exception, because `bash` is the tool people write hooks for
and a guard carried over from Claude Code reads `.tool_response.stdout`. It
is sent in that program's shape:

```json
"tool_response": {
  "stdout": "everything up-to-date",
  "stderr": "",
  "interrupted": false,
  "isImage": false
}
```

**Three of those four fields cannot be filled honestly, and are constants.**
They are written out here because a script that branches on one will never
take that branch, and its author is owed the reason.

`stderr` is always empty. agi's `bash` gives the command *one* file
descriptor for both of its streams, so the kernel merges them, in the order
they were written, before agi sees either one. Everything the command printed
is in `stdout`, which is also what the tool's own description promises the
model. Splitting the two would mean two pipes, and two pipes cost that
ordering — which is the more useful half of what a reader of command output
wants. A warning that arrived between two steps is worth more than knowing
which stream it came from.

`interrupted` and `isImage` are always `false`. A tool in agi answers with a
string, so by the time this payload is built there is nothing left that says
the command timed out, and no tool here produces an image.

**None of this generalizes to a second shaped tool**, and the next one added
has its own answer to give. `terminal` is the instructive case: it runs its
program on a pty, where standard input, output and error are one file
descriptor by construction, so the two streams cannot be told apart there
even in principle — that is what a pty is. A user-defined tool is the
opposite case, where agi really does have the two and the protocol throws one
away. Neither is `bash`'s reason, so neither would be `bash`'s shape.

**Where the shape lives is a decision too.** It is on the tool, through the
`tools.Responder` interface, and not in the `hooks` package. A guard that had
to know the name `bash` would be a tool special case in the wrong half of the
program, and the next tool with a shape of its own would have to be added in
two places instead of one. Every tool that does not implement the interface
is shown as a string and replaced with a string, and nothing about the run
changes either way: this is only how a hook is shown a result it is not
otherwise part of.

### 6.2 Exit status

| Status | Meaning |
| --- | --- |
| `0` | Proceed. Standard output is read as JSON when it looks like JSON, and honored. |
| `2` | At `PreToolUse`, block: the tool does not run and standard error is the reason the model reads. At `PostToolUse`, nothing is blocked — see below. |
| anything else | Non-blocking error: the user is shown the reason, the tool runs anyway, the run continues. |

**A status of two means different things at the two tool events, and that
difference is Claude Code's.** Before the call there is something left to
withhold, so it is a refusal. After the call the tool has already run, and a
refusal would be a lie about what happened; the status only carries the
hook's standard error to the model, beside the result it already has. The
same guard filed at the wrong event therefore fails loudly in one direction
and harmlessly in the other, which is the right way round.

An earlier build of agi read the status the same way at both events, which
made a `PostToolUse` hook exiting two end the run and report it as
interrupted. That was a bug rather than a divergence, and it is gone.

A program that could not be started at all — missing, not executable, or
refused by the kernel — stops the agent that made the call. See section 8.3.

**A hook killed by a signal has no status of its own**, and is read as the
status a shell reports for the same death: 128 plus the signal, so a hook
killed with `SIGTERM` is 143. That lands it among the statuses nothing was
planned for, which is the only safe reading. Zero would have agi look for a
reply in the output of a program that never got to print one and let the call
through as though a guard had approved it, and two would refuse a call nobody
decided to refuse.

**None of that is true at `MessageDisplay`**, where every status is read only
to notice that something went wrong: the message is printed as it stood and
the user is told. There is nothing to block, and a program that could not be
started stops nothing. Claude Code ignores the status at this event too. A
hook that exited badly and printed a `displayContent` anyway is still
honored, since the status is not a decision here and the field is.

### 6.3 What the hook prints

Standard output is read as JSON when it starts with `{` and ends with `}`,
ignoring whitespace. Anything else is recorded in the log and shown under
`--verbose`, and has no effect on the run. This is where a hook prints its own
progress.

These fields are honored:

| Field | Event | Effect |
| --- | --- | --- |
| `continue: false` | pre, post | Stops the agent that made the call. See 8.3. |
| `stopReason` | pre, post | The message shown when the agent stops. |
| `systemMessage` | all | A warning shown to the user in the progress stream. |
| `hookSpecificOutput.permissionDecision` | pre | `allow` and `defer` proceed; `deny` refuses the call; `ask` is treated as `deny`, and the user is told it was (see 14.4). |
| `hookSpecificOutput.permissionDecisionReason` | pre | The reason the model is given when a call is refused. |
| `hookSpecificOutput.updatedInput` | pre | A **partial** object overriding named fields of `tool_input`. |
| `hookSpecificOutput.additionalContext` | pre, post | Text put into the conversation beside the tool's result. |
| `decision: "block"` with `reason` | post | Stops the agent that made the call, once it has read the result. See 8.3. |
| `hookSpecificOutput.updatedToolOutput` | post | Replaces what the model reads as the tool's output, **whole**, and in the tool's own shape — see 6.1.1. |
| `hookSpecificOutput.displayContent` | display | Replaces the text agi is about to print, **whole**. Nothing else changes. |

`suppressOutput` and `terminalSequence` are not implemented. Claude Code
ignores the first already, and the second is a terminal feature agi has no use
for. JSON that agi cannot parse, or that fails the shape above, is a
non-blocking error: it is recorded, the user is told, and the run continues.

An empty `displayContent` is a hook deliberately printing nothing, and is
honored as such. Leaving the field out is a hook that had nothing to say
about the text, which is what an observing hook — one that only logs — does.

### 6.4 What a `MessageDisplay` hook asks for and does not get

`displayContent` and `systemMessage` are the whole of what it can have.
`continue: false`, `decision: "block"`, a permission decision and
`additionalContext` are all accepted and discarded, as they are in Claude
Code.

**agi differs in saying so.** Each of them produces a note in the progress
stream: the hook asked for something a display hook cannot do, and it was
ignored. That is the rule an `ask` decision already follows (see 14.4).
Quietly doing something other than what a hook asked is how somebody comes
to believe their guard behaves one way when it behaves another, and the
likeliest cause of any of these is a hook filed under the wrong event — a
script that refuses a message and is never told is a script whose author
will look everywhere but here.

Only the first such request is named. A reply doing two of them at once is
one confused script, and listing every way it was confused would bury the
news that none of it happened.

## 7. Matching

A tool hook runs when its `matcher` accepts the tool's name **and** its `if`
conditions are satisfied. A `MessageDisplay` hook has neither and runs every
time: see 7.5.

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

A `MessageDisplay` hook belongs to every agent too, and in practice sees the
run's messages and a fork's. A subagent displays nothing, so nothing of its
is hooked.

### 7.5 `MessageDisplay` takes no `matcher` and no `if`

Both are defined over a tool: one is a pattern for its name, the other a
pattern per named parameter. A display hook has neither. **Writing either on
one fails the run when the file is read**, with the field named and the
reason given.

Claude Code accepts a matcher here and silently ignores it. agi refuses it,
because ignoring it produces exactly the dead-hook silence that refusing an
unfirable matcher exists to stop: somebody who wrote `"matcher": "^bash$"` on
a display hook believes it renders only a `bash` result, and it renders
everything. It is a divergence, and it is the one place this event is not
Claude Code's exactly.

Matching on the message text itself was considered and rejected. The content
already tells a script everything it needs, and a regular expression deciding
whether a renderer runs would be a second, weaker copy of the branch the
script is going to write anyway.

## 8. Order, and what a hook can stop

### 8.1 Order

Matching hooks run **in sequence**: within a file in the order the entries
appear, and across files in the order `--resources` named the folders. The
display hooks chain the same way, each shown what the one before it left, so
a run may pass a message through a renderer and then through something that
trims it.

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

**All three are read differently at `MessageDisplay`, where none of them
stops anything.** A renderer that is not installed, one that hangs, and one
that asks to end the run are each a note in the progress stream, and the
message is printed as the model wrote it. The reasoning is 3.1's: the three
exist so that a broken guard does not fail open, and a display hook is
guarding nothing. Everything below this line is about the tool events.

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
a `hooks.json` is refused, so adding one later breaks nobody's existing file.

## 10. Divergences from Claude Code

Every one of these belongs in the README.

**The line is drawn at the manifest.** `hooks.json` is agi's own file and
looks nothing like Claude Code's settings, and that is deliberate. Everything
a hook *program* sees or prints is not agi's to redesign: the fields on
standard input, their names and shapes, the exit statuses and what they mean,
and the reply a hook prints. The goal is that a script can be carried from
one program to the other and back without being edited. So the table below
divides into differences a script cannot observe, which are free, and
differences it can, which need an argument — and the only ones left in the
second group are the three stops of 8.3, the `ask` substitution, and where a
relative path resolves.

| Claude Code | agi |
| --- | --- |
| Many hook events. | Three: `PreToolUse`, `PostToolUse` and `MessageDisplay`. |
| A `matcher` on a `MessageDisplay` hook is silently ignored. | It fails the run when the file is read, with the field named. A guard that cannot fire must never load quietly. |
| A `MessageDisplay` hook's `continue`, exit status and refusal are discarded silently. | Discarded too, and the user is told each was, in a note. |
| Hook types `command`, `http`, `mcp_tool`, `prompt`, `agent`. | A program, and nothing else. |
| `Bash(git push *)` permission-rule filters. | `if`, holding one regular expression per named parameter. |
| A plain matcher is compared literally; only some are regular expressions. | Every matcher is a regular expression. |
| Matching hooks run in parallel. | In sequence, so `updatedInput` can chain. |
| A hook that cannot be started is a warning; the tool runs anyway. | It stops the agent that called the tool — at a tool event. At `MessageDisplay` it is a note and the message is printed. |
| A hook that times out is a warning; the tool runs anyway. | The same: it stops the agent at a tool event, and is a note at `MessageDisplay`. |
| `continue: false` stops Claude entirely. | It stops the agent whose call was guarded. From a fork or a subagent, only that agent ends and its parent reads why; the run ends only when it is the run's own agent. From a `MessageDisplay` hook it stops nothing. |
| `permissionDecision: "ask"` escalates to the user. | Treated as `deny`, and the user is told the substitution was made. agi has no permission prompt, and a run is as often piped with nobody watching as it is run at a terminal. |
| Relative paths resolve against the working directory. | Against the folder the `hooks.json` is in. |
| A hook runs in the project directory. | It runs in the folder its `hooks.json` is in, which is what makes the line above true for a shell command line too. `cwd` on standard input says where agi is. |
| Per-hook `timeout` field. | One `--hook-timeout` for the run. |
| No way to say which runs a hook is for; a hook cannot learn what the output is attached to either. | `enabled`, on every event: `true`, `false`, or a list of `"tty"` and `"non-tty"`. See 4.4. |
| `suppressOutput`, `terminalSequence`. | Not implemented. |
| `session_id` is a short opaque identifier; `transcript_path` points at the transcript. | `session_id` is a short opaque identifier too — a hash of the session's directory — so a ported script uses it unchanged. Where the session lives is not sent, and there is no transcript file to point at: a snapshot and a log are not one. |
| `CLAUDE_PROJECT_DIR` and friends in the environment. | Nothing invented; `cwd` arrives on standard input. |

### 10.1 What a ported script will not find

These are the fields Claude Code sends that agi does not, and the fields it
honors that agi ignores. Each one belongs to something agi does not have, so
none of them can be fixed by agreeing to send it: a field carrying an
invented value is worse than a missing one, because a guard that reads it
then makes a decision on a number nobody computed.

**A script that reads one of these is a script that will not work here**, and
that is the point of listing them. The absence is silent — agi cannot know a
hook wanted a field it never asked for — so this table is the only warning
its author gets.

| Field | What agi has instead |
| --- | --- |
| `transcript_path` | Nothing. There is no transcript file: a snapshot holds what was said and a log holds what was done, and neither is the other. |
| `scratchpad_dir` | Nothing. agi has no scratchpad. A hook that wants working space makes its own, keyed by `session_id`. |
| `permission_mode` | Nothing. agi has no permission modes and no prompt; see 14.4. |
| `prompt_id` | Nothing. agi has no per-prompt identifier. `session_id` is the run, and `tool_use_id` is the call. |
| `effort` | Nothing. agi has no effort setting. |
| `agent_type` | Nothing. agi's subagents are numbered, not typed, so there is no name to send. `agent_id` carries `subagent-2`. |
| `mcp_server` | Nothing. agi has no MCP. |
| `turn_id`, `message_id` | Nothing. agi numbers its turns but has no identifier of this kind for either. |
| `tool_response.bashEditDiff` | Nothing. agi does not record what a command changed under git. |
| `tool_response.stderr` | Always the empty string for `bash`, and absent for every other tool. See 6.1.1 for why the two streams cannot be told apart. |
| `tool_response.interrupted`, `.isImage` | Always `false`. See 6.1.1. |
| `classifierContext` in a reply | Accepted and ignored. It feeds Claude Code's auto-mode classifier, which agi has no equivalent of. |
| `updatedMCPToolOutput` in a reply | Accepted and ignored. There are no MCP tools to replace the output of; `updatedToolOutput` covers every tool agi has. |
| `suppressOutput`, `terminalSequence` in a reply | Accepted and ignored. Claude Code ignores the first already; the second is a terminal feature agi has no use for. |

The three in the reply are *accepted* rather than refused, which is section
6.3's rule and not a special case: unknown fields in a reply are ignored,
because a reply is written for Claude Code's protocol and a field agi does
not know is a field another program reads.

## 11. What agi records

### 11.1 The conversation

**A hook never rewrites the record.** The assistant turn holds the tool
request exactly as the model wrote it, even when a hook changed the arguments
before the tool saw them. What the user asked and what the model answered are
kept as they arrived; that rule is not relaxed for this.

**A `MessageDisplay` hook cannot touch it at all**, and that is stronger than
the rule above rather than an instance of it. The rule above is kept by
nobody writing the line that would break it; this one is kept by where the
hook runs. The text reaches it at the print site, by which point the
conversation holds it, the snapshot has stored it and the log has recorded
it, so there is no path from a display hook to any of the three.

What a hook can add to the conversation:

- A refused call becomes a tool failure carrying the hook's reason.
- `additionalContext` becomes a harness instruction appended straight after
  the tool's answer. That is what a harness instruction is for — agi speaking
  to the model in its own voice — and it is the one turn type that may later
  be dropped.
- A `PostToolUse` hook that exited two has its standard error put into the
  conversation the same way `additionalContext` is, since that is what Claude
  Code does with it and agi has no second channel to the model. A hook that
  used both gets one instruction holding both, a paragraph apart.
- `updatedToolOutput` replaces the text of the tool's answer. When the answer
  was structured — the `subagents` tool answers with one section per subagent
  — the replacement flattens it to a plain result. That is the honest
  consequence of asking for the output to be replaced, and the log still holds
  what the tool actually said.

### 11.2 The log

Two event kinds:

| Kind | What it records |
| --- | --- |
| `hook.result` | One hook having run and decided something — proceeded, changed the arguments or the message, refused the call, stopped the agent — with the event it ran at, the program, the tool it was guarding, the request id that joins it to the `tool.call` line, how long it took, and, for a hook that rewrote something, what it left. |
| `hook.failure` | A hook that went wrong without deciding anything: a program that could not be run, one that timed out, a status that means nothing, output that claimed to be a reply and would not parse. |

There is no `hook.call`. A hook is run by a mechanism that hands back an
account of what it did, and there is no moment in between for a line saying
one is about to run.

A line that says `"decided": "rewrote"` carries what the hook left behind, and
carries it exactly as the hook wrote it — escape sequences and control codes
included, since a hook that paints a message is one whose whole work is those
codes. The encoder escapes a control character rather than writing it out, so
the line is still one JSON object. Rewritten arguments go in `arguments`, the
field the arguments of a call already go in, so a reader joining a rewrite to
the `tool.call` above it reads the two the same way; everything else a hook
can rewrite is text, and text goes in `content`. A line that decided anything
else carries neither field.

That text is on the hook's own line and nowhere else, which is the point of
putting it there. `tool.call` keeps the arguments the model wrote and
`tool.result` keeps what the model finally read, so a chain of rewriting hooks
would otherwise leave every rewrite but the last unaccounted for. A
`MessageDisplay` hook is the case in point: the conversation, the snapshot and
the log all keep the model's own words, so this line is the only record of
what the user actually saw — and for a piped `--schema` run, the only record
of what the pipe actually carried.

A `MessageDisplay` hook's line names no tool and no request, because it
guarded neither. Both fields are left out rather than sent empty, so the
line says what it is — which hook ran, at which moment, and what it came to
— and claims nothing about a call.

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
hook can stop an agent, silently rewrite what a tool was asked to do, or
change what the user is shown, which makes it exactly the kind of thing
somebody watching a run needs to have been told about beforehand. A display
hook has no matcher and nothing is printed where one would be, rather than
the `*` that stands for "every tool": there is no tool it declined to name. A
hook that cannot fire in this run is listed and marked, and the two reasons
are marked apart; the table in 4.4 is the whole of it.

That block also carries the run's `session_id`, beside the session directory
it was made from. The id is opaque by design, so this is the one place the two
are shown together, and it is what somebody holding a leftover state file
named after an id needs in order to find out which conversation made it. It is
printed only when the run has hooks: with none, nothing will ever see the id.

## 12. What is checked, and when

**When the folders are read**, before a gateway is contacted or a session is
created — the rule `--resources`, `--sys` and `--schema` already
follow:

- the `hooks.json` parses as JSON;
- no top-level name that is not an event, and no unknown field in an
  entry;
- every regular expression compiles;
- `command` is not empty;
- `args`, where present, goes with a `command` that names a program;
- no `matcher` and no `if` on an event that has no tool to match;
- `enabled` is `true`, `false`, or a list holding nothing but the two names
  agi knows;
- the matcher accepts the name of at least one tool agi has.

**A hook that could never fire is refused**, which is the last of those. A
matcher accepting no tool at all — `^Bash$` where the tool is `bash` — loads
perfectly well, is listed under `--verbose`, and lets the call it was written
to guard go straight through. Nothing else about the run would ever say so,
and a guard the user believes in and which cannot fire is worse than no guard,
so it is refused with the matcher quoted back and the tool names listed beside
it.

It is checked against every name agi can register, plus the tools these
folders brought, and deliberately not against the narrower set this
invocation ends up with. What it is looking for is a name that is nothing — a
misspelling — and a tool the user switched off is not that. A subagent holds
fewer tools than the run, so a hook watching `fork` is live even though no
subagent could ever set it off; `--enable-terminals` is the same thing one
level up, so a hook watching `terminal_send` is live in a run that left
terminals off. The alternative was tried and removed: it made one folder of
hooks load or fail the run on a flag about something else, which is the trap
agi already refuses to set for a `tools.json`, whose reserved names have never
depended on the flag either. It is checked whatever the hook's `enabled` says,
because a matcher that is wrong is wrong in the run the hook *is* for, and a
mistake found when the folder is read is a mistake found once rather than on
the next run of the other kind.

**A hook that loads by that rule and still cannot fire in this run is marked,
not refused.** A matcher on the terminal tools in a run without
`--enable-terminals` is the case: the name is a real tool of agi's, so this is
the user's decision and not their typo, and a decision is obeyed. What is owed
in return is the news, which is the bargain `enabled` already strikes — so
such a hook is listed under `--verbose` and marked `(watches nothing in this
run)`, worded apart from the `(off in this run)` of a hook whose `enabled`
names the other kind of run, because the two are corrected differently: one by
editing the field, the other by passing the flag. See 4.4.

**A hook that cannot fire because its `enabled` is for the other kind of run
is not refused either**, for the same reason: a mistake is refused, and a
decision is obeyed. The two dead hooks agi allows are therefore this one and
the matcher above, and both are marked under `--verbose`. See 4.4.

**It is asked only of a hook whose event is about a tool call.** A
`MessageDisplay` hook has no tool, and one written with a matcher is already
gone by the time this runs — refused above, in a message about matchers
rather than about the tools this run has, which is the message somebody who
wrote one can act on. One written without a matcher would otherwise pass this
check by luck, since an absent matcher accepts every name whatever the event,
and a check that holds by luck is a check that stops holding without anything
saying so.

**The same question about a hook's conditions is deliberately not asked.** An
`if` naming a parameter no matching tool declares — `comand` for `command` —
is the same kind of dead hook and is left alone, because the answer depends on
the parameters of whichever tools the matcher accepts, and some tools declare
none or allow any. The check would be right for some tools and quiet for
others, and one rule that always holds is worth more than two told apart by
what a schema happens to say. It is a known limitation; see 14.7.

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
three methods on it — one before a tool runs, one after, and one before text
is printed. Each runs every matching hook in sequence and hands back the
processed data, a report of what the hooks did, and for the tool events a
stopping error. Matching, running, parsing and deciding all stay inside: the
agent loop learns nothing about regular expressions, exit statuses or JSON
output fields.

```go
// Before runs the PreToolUse hooks and returns the arguments the tool should
// be called with, which may not be the ones the model wrote.
func (s *Set) Before(ctx context.Context, c Call) (json.RawMessage, Report, error)

// After runs the PostToolUse hooks and returns the output the model should
// read, which may not be the output the tool produced.
func (s *Set) After(ctx context.Context, c Call, output string) (string, Report, error)

// OnDisplay runs the MessageDisplay hooks and returns the text agi should
// print, which may not be the text the model wrote.
func (s *Set) OnDisplay(ctx context.Context, m Message) (string, Report)
```

`OnDisplay` returns no error, and that is the signature saying what 3.1
says: nothing a display hook does can stop anything, so there is nothing for
a caller about to print text to decide. Everything that went wrong is a note
in the report.

It takes a `Message` rather than a `Call` because it is not a call: nothing
is about to happen to the machine, nothing can be refused, and there is no
tool, no arguments and no request to correlate with. Widening `Call` to cover
both would be naming the whole after the part, which is the mistake `Set`
was renamed to avoid.

The `cli` side hooks the four print sites and nowhere else. It is tempting
to put the call inside the progress stream, where everything agi prints
already goes, and that would be wrong: `[bash]` announcements, the session
line, hook notes and the verbose preamble are agi's own chrome, and passing
them through a markdown renderer would mangle them.

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

**The set is loaded from the folders and then told two things about the run**,
each by a method that returns a copy: `WithSession`, which is the identifier a
hook is sent, and `WithTTY`, which is whether agi's output is a terminal and
so which hooks an `enabled` leaves out. Neither can be settled while the
folders are read — the session does not exist yet, and where the output is
going is not the folder's business — and reading the folders early is what
makes a mistake in a manifest cost a message rather than a conversation.
Nothing else about a set changes once a run is going, which is why both are
copies and neither is a setter.

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

A replacement is read in the shape the hook was shown — a string for a tool
that answers with one, and the four-field object for `bash`, as 6.1.1 says. A
value of some other shape is ignored and the tool's own output stands, which
is Claude Code's rule for the same case. The user is told, because a rewrite
that silently did nothing is one its author believes is working. Where a
`bash` replacement writes into both `stdout` and `stderr`, the two are joined
with a newline: this tool has one stream to hand back, and a hook that wrote
into both meant both to be read. Nothing is delimited — a marker agi invented
there would be text the model has no way to tell from the command's own.

### 14.4 A guard cannot escalate to a person

`permissionDecision: "ask"` is treated as `deny`, because agi has no
interactive permission prompt and a run is as often piped with nobody watching
as it is run at a terminal. A hook that wanted a human decision gets the safe
half of it. See section 6.3.

**The substitution is said out loud rather than made quietly.** The user is
told, in the progress stream, that the hook asked to put the call to a person
and that it was refused instead. Doing something other than what a hook asked
without saying so is how somebody comes to believe their guard behaves one way
when it behaves another, which is the same failure a dead matcher produces and
is worth the same care.

It is a note and not a failure: the hook is not broken, the call really is
refused, and the model is told the hook's own reason as it would be for any
refusal. What the note adds is the one thing the model's copy does not say —
that a decision the author wanted a person to make was made for them.

Being a note, it travels the way every note does: to the screen at a terminal,
to standard error under `--verbose`, and nowhere at all in a piped run that
did not ask for diagnostics. That last case is the limitation inside the
limitation, and it is the same one every note has.

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

### 14.7 A condition naming a parameter nothing has is still silent

A matcher that accepts no tool is refused (see 12). A condition naming a
parameter that no matching tool declares is not, and it produces the same
dead hook: `{"matcher": "^bash$", "if": {"comand": "^rm "}}` loads, is
listed, and never fires, because `bash` has a `command` and nothing called
`comand`.

It is left alone rather than checked because the check could not be made to
hold everywhere. Whether a parameter exists is a question about the tools the
matcher accepts, and a tool may declare no properties at all, or allow
additional ones, in which case any name is legitimate and agi has nothing to
compare against. A rule that refused the typo for some tools and shrugged at
it for others would be worse than the silence, because it would read as a
guarantee.

What reduces the damage is that the matcher half is now checked, and the two
typos are usually made together — a user who wrote `comand` for a tool that
does not exist is stopped by 12. The one that gets through is a correct
matcher with a misspelled condition.

### 14.8 A display hook armed for a piped run corrupts what it writes

`MessageDisplay` fires whenever agi displays something, which includes the
answer of a run that is piped or redirected (see 3.2). A renderer left armed
for such a run puts escape sequences onto the channel another program is
reading, and one that renders a `--schema` document with anything but `jq`
leaves that channel carrying what is no longer JSON. The person who trips
over it will be piping to `jq` and will not connect the two.

Skipping the hook when stdout is not a terminal was tried and rejected: a
display hook need not emit escapes at all, and one that redacts, translates
or trims is worth running into a pipe. Letting the hook decide for itself
cannot work either, for the reasons in 3.3. Adding a field saying whether
anybody is watching would break the one thing this event is for, which is
that a ported Claude Code script works unchanged.

So the choice belongs per run rather than per message, and it is the user's:
keep the renderer in a `--resources` folder of its own and name it only when
watching. It is the same bargain every other hook makes — a `PreToolUse`
hook can rewrite a command into something ruinous, and agi runs it.
