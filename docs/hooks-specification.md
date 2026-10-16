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

**It applies to every message agi shows, not only the final answer.** Five
sites print text the model wrote, and all five are hooked:

| Site | What it prints | Kind |
| --- | --- | --- |
| The agent loop | Prose the model wrote alongside its tool calls. | message |
| The end of a run | The final answer. | message |
| The end of a `--schema` run | The prose answer, which is the reasoning there. | message |
| The end of a `--schema` run | The JSON document. | message |
| Above every assistant turn | The deliberation a thinking model returned. | thinking |

Nothing else is. Everything else agi prints — a tool call's announcement, the
`[thinking]` label above the deliberation, the session line, a hook's own
note, the verbose opening block — is agi's own chrome, already styled, and
passing it through a markdown renderer would mangle it.

The last row is the half a hook has to ask for, and 3.2 is about it.

The motivating use is that renderer. The model writes markdown and agi prints
it raw; a hook can pass it through `glow`, or pretty-print a `--schema`
document through `jq`, and the terminal shows something worth reading.
Whether `glow` and `jq` are installed is the hook's business. agi does not
look for them and has no opinion about what a display hook shells out to.

### 3.2 `thinking` says which of the two things the model wrote a hook renders

A thinking model returns the deliberation behind a turn as well as the turn
itself, and agi prints it above the turn under a `[thinking]` label. It is
the model's own words, printed to the user, which is everything the event is
defined over — but it is not the same text as a message and is rarely wanted
laid out the same way, so a `MessageDisplay` hook declares which of the two
it renders:

```json
{ "thinking": true, "command": "./render-thinking.sh" }
```

An absent field is `false`, which is the message: the prose the model wrote
for the reader, which is the only thing this event used to fire on, so every
hook written before the field existed goes on meaning what it meant.

**They are two streams and not one with a flag on it.** A hook is spawned
only for the kind it asked for, and the payload carries no field saying which
kind arrived, because the hook already said. A run that wants both rendered
writes two hooks. Three things follow, and the first is why the choice is in
the manifest rather than left to a branch inside the program:

- A renderer armed to lay out answers is never started over the model talking
  to itself. That hook would otherwise be spawned twice a turn, on text it
  has no layout for, in a run that may print nothing else.
- agi can keep a default over the half nobody claimed. See below.
- Each hook reads one shape of text, which is what makes a hook worth
  writing at all: the two read nothing alike.

**Arming a thinking hook drops agi's own dimming of that block.** With none,
agi prints the deliberation faint, because nothing else was going to and
deliberation is long; with one, the text is printed exactly as the hook
handed it back, and a hook that wants the dimming back emits it itself. The
dimming is a default and not chrome — a run that armed a hook over the
deliberation armed something to decide how it looks, and a default laid
underneath would have every such hook rendering into text agi had already
written escape sequences into.

The `[thinking]` label is chrome and stays, drawn by agi whoever laid the
text out. A hook renders the model's words and is never handed the frame agi
puts them in, which is the rule 3.1 states for every other site. A hook that
hands back nothing prints nothing, label and all: a bare label over an empty
block would mark the turn as having deliberated and then show none of it.

The field belongs to this event alone. Writing it on a `PreToolUse` or
`PostToolUse` hook fails the run when the file is read — a tool hook prints
nothing, so there is no half of the output for it to choose between, and it
is refused rather than ignored for the reason 7.5 gives about a `matcher`
here. It is read strictly: `true`, `false`, or absent, and nothing else,
`null` included.

### 3.3 It runs only at a terminal, and only over what is displayed there

**A display hook runs only when agi's standard output is a terminal.** In a
piped run no display hook is started — not over the answer, and not over the
progress a `--verbose` run sends to standard error. The answer of a piped run
is for another program, and a renderer wanted over it belongs in the pipe,
`agi ... | glow -`, where the user can see it and change it without editing a
manifest. That also keeps the manifest free of combinations that can only do
nothing: a piped run displays no deliberation, so a choice of runs on a
`"thinking": true` hook would have offered one half that never fires.

An earlier version ran display hooks into pipes, on the reasoning that a hook
which redacts or trims a message is worth running there, and let `enabled`
name the kinds of run a hook was for. Both are gone. A program that rewrites
piped output can sit in the pipe as well as in a hook, and a field whose shape
depended on which event it was written under was a rule the user had to
remember.

At a terminal the hook runs over what is displayed, and not over what is not.
Subagents run with every stream discarded, so nothing they print is displayed
and no hook fires for them. A fork's prose is displayed, and is hooked.

### 3.4 A hook cannot ask what agi's output is attached to

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

**agi answers it instead.** A display hook is started only where agi's output
is a terminal (3.3), so a hook that is running is at one.

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
| `enabled` | No | `false` switches the hook off. See 4.4. |
| `thinking` | No | `MessageDisplay` only: render the model's deliberation rather than its message. See 3.2 and 4.5. |
| `command` | Yes | The program to run, or a shell command line. |
| `args` | No | Arguments, which also selects the exec form. See 5.2. |

Note that the two entries above both use the word `command` for two different
things. The one inside `if` is the `bash` tool's own parameter; the one beside
it is the hook program. This is Claude Code's spelling and is kept for that
reason.

Any other field in an entry, and any top-level name that is not an event, fail
the run when the file is read.

### 4.4 `enabled`

`enabled` says whether a hook runs. It is on every event.

| Written | The hook runs |
| --- | --- |
| absent | Yes. This is what every hook meant before the field existed. |
| `true` | Yes. |
| `false` | No. The hook is declared and never runs. |

```json
{ "enabled": false, "matcher": "^bash$", "command": "./scripts/strict.sh" }
```

**It is a yes or a no, and never a choice of runs.** A tool hook runs whether
agi's output is a terminal or not. A display hook runs only at a terminal
(3.3), and `false` is the only thing the field can add to that. An earlier
version took a list of `"tty"` and `"non-tty"` here as well; it is refused now
like any other value that is not a boolean.

**The value is checked strictly**, and a mistake fails the run when the file
is read, with the event and the entry's number in front of it. Anything but
`true` or `false` — a list, a string, a number, an object, a `null` — is
refused with the value quoted back.

**A hook that is off in this run is listed under `--verbose` and said to be
off.** There are two of them: a hook whose `enabled` is `false`, and a display
hook in a run whose output is not a terminal. Leaving either out of the list
would have the user read what the run is armed with and find a hook they wrote
missing from it; listing it unmarked would claim it is armed. They are two of
the dead hooks agi allows — the other is a matcher on a tool agi has and this
run does not, which is 12 — and all are allowed because they are the user's
decision or the event's rule rather than a typo, so the listing says so.

The marks are worded apart, because they are corrected differently:

| Mark | Why the hook cannot fire | How to arm it |
| --- | --- | --- |
| `(off in this run)` | `enabled` is `false`, or it is a display hook and agi's output is not a terminal. | Edit the field, or run agi at a terminal. |
| `(watches nothing in this run)` | The matcher accepts no tool this run registered — nearly always the terminal tools without `--enable-terminals`. | Pass the flag that brings the tool. |

A hook can be both, and the listing shows the first: it is off wherever the
hook is run from, where the matcher is off only until the run is given the
tool.

**What "at a terminal" means is agi's standard output and nothing else.** It
is the same fact that decides where progress goes, read once when the run
starts. It is not the agent's: a subagent's streams are all discarded and a
fork's are marked, and neither changes what agi is attached to. A run started
with no answer about it — which is to say a caller of the package that never
said — is read as not at a terminal, because a display hook is the one that
paints escape sequences, and not running it leaves a message plain rather than
leaving a pipe full of codes.

### 4.5 `thinking`

`thinking` says which of the two things the model writes and agi displays a
`MessageDisplay` hook renders. 3.2 is what it means and why it is a field of
the manifest rather than a branch inside the program; this is what the file
may say.

| Written | The hook renders |
| --- | --- |
| absent | The prose the model wrote for the reader, which is what every hook meant before the field existed. |
| `false` | The same. |
| `true` | The deliberation a thinking model returned, printed above the turn it belongs to. |

**It is the opposite of `enabled` in where it is allowed.** Any hook can be
switched off, so `enabled` is on all three events; there are two kinds of
display only where agi is displaying, so this is on one. A `thinking` written
on a `PreToolUse` or `PostToolUse` hook fails the run when the file is read,
and is refused rather than ignored for the reason 7.5 gives about a `matcher`
on a display hook — a field agi accepted and dropped leaves its author
believing something about their hook that the hook does not do.

**The value is checked strictly.** `true`, `false` or absent; anything else
fails the run with the entry named. `null` is refused among them, and refused
by hand: decoding `null` into a boolean is not an error in Go, it leaves the
boolean alone, so a field written `null` would otherwise read as `false` and
quietly aim the hook at the other half of the output.

A hook with `"thinking": true` is marked in the `--verbose` listing, in the
column a tool hook's matcher sits in, since it is the same thing said about a
different event: what this hook is watching. Two display hooks are otherwise
one line written twice, and they fire on different text.

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

**There is deliberately no field saying whether this is a message or a
deliberation either**, and that one is settled a step earlier: the hook said
so in its `hooks.json` and is spawned only for the kind it asked for, so a
field repeating it would tell every script something it already knows. See
3.2.

**There is deliberately no field saying which of the four message sites this
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

**The tools with a shape are the ones that run a program:** `bash`, and every
tool the user wrote. Every other one — the workspace tools `terminal`,
`mktemp`, `patchfile` and `writefile`, and the native tools — answers with a
string, and for those the field *is* that string:

```json
"tool_response": "everything up-to-date"
```

A program has two output streams, and a guard carried over from Claude Code
reads `.tool_response.stdout`. So a tool that ran one is sent in that
program's shape:

```json
"tool_response": {
  "stdout": "everything up-to-date\n",
  "stderr": "warning: deprecated API\n",
  "interrupted": false,
  "isImage": false
}
```

**`stdout` is the command's standard output, not the whole result.** The
status line agi appends to what the model reads — `exit status 1`, `timed out
after 2m0s` — is agi's sentence about the call rather than anything the
command printed, so it is in neither stream. A guard looking for it should
read the exit status from the command it is guarding, not from this payload.

**`stderr` is the command's standard error.** agi gives the command two pipes
and polls both, so the two really are told apart. That has a price, and section
14.9 is about it: two writes made closer together than a wakeup can no longer
be ordered against each other, where one shared descriptor ordered even those
exactly.

**`interrupted` is `true` for a command agi stopped on its deadline, and for
nothing else.** A call stopped along with the run — a Ctrl-C — never reaches a
hook at all: it is answered as a tool *failure*, and a failed call runs no
`PostToolUse` hook (see 3). So that case is absent from the protocol rather
than misreported in it, which is a narrowing against Claude Code and is listed
as one in section 10.

**`isImage` is always `false`**, and is the one field that is still a
constant. No tool in agi produces an image, and the wire format has no content
block to carry one. It is written out here because a script that branches on
it will never take that branch, and its author is owed the reason.

**There is no `exitCode` field**, and that is deliberate rather than an
oversight. The shape exists in order to *be* Claude Code's, the status is
already in the text the model reads, and two fields that can say the same
thing can come to disagree once a hook has rewritten one of them.

**The two tools answer in one shape for one reason**, which is that both have
two real streams. **A tool that runs no program would need a reason of its
own**, and `terminal` is the instructive case with none: it runs its program
on a pty, where standard input, output and error are one file descriptor by
construction, so the two streams cannot be told apart there even in principle
— that is what a pty is.

**Where the shape lives is a decision too.** It is on the tool, through the
`tools.Responder` interface, and not in the `hooks` package. A guard that had
to know the name `bash` would be a tool special case in the wrong half of the
program. What a program did is a `tools.Execution`, which is the same for
every tool that ran one, and the four field names are the shape; the facts are
shared and the shape is not, which is what lets the next process-backed tool
be shaped by writing one method and copying no capture code. Every tool that
does not implement the interface is shown as a string and replaced with a
string, and nothing about the run changes either way: this is only how a hook
is shown a result it is not otherwise part of.

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

What this event has instead of matching is `thinking`, which is not a weaker
copy of anything: it chooses between two streams rather than filtering one,
and the choice is made before a program is started rather than by a pattern
over text that has already arrived. See 3.2 and 4.5.

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
| No way to switch a hook off without deleting it; a hook cannot learn what the output is attached to either. | `enabled`, on every event: `true` or `false`. A `MessageDisplay` hook runs only where agi's output is a terminal. See 3.3 and 4.4. |
| Nothing hooks the deliberation a thinking model returns. | `thinking` on a `MessageDisplay` hook renders that instead of the message, and drops agi's own dimming of it. See 3.2 and 4.5. |
| `suppressOutput`, `terminalSequence`. | Not implemented. |
| `tool_response.interrupted` is also `true` when the user interrupts a command. | `true` for a timeout only. A call stopped with Ctrl-C is abandoned and answered as a tool failure, which fires no `PostToolUse` hook, so no hook sees that case either way. See 6.1.1. |
| A command's two streams are ordered as the kernel merged them onto one descriptor. | Ordered as the kernel made two pipes readable, which is the same thing for any two writes more than a wakeup apart. Two writes closer together than that tie, and a tie resolves in favour of standard output. See 14.9. |
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
| `tool_response.isImage` | Always `false`. No tool in agi produces an image. See 6.1.1. |
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
  what the tool actually said, on its `tool.output` line.

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
putting it there. `tool.call` keeps the arguments the model wrote,
`tool.output` keeps what the tool produced before any hook saw it, and
`tool.result` keeps what the model finally read, so a chain of rewriting hooks
would otherwise leave every rewrite but the last unaccounted for. A
`MessageDisplay` hook is the case in point: the conversation, the snapshot and
the log all keep the model's own words, so this line is the only record of
what the user actually saw.

A `MessageDisplay` hook's line names no tool and no request, because it
guarded neither. Both fields are left out rather than sent empty, so the
line says what it is — which hook ran, at which moment, and what it came to
— and claims nothing about a call.

**A guarded call leaves the log both halves of what a hook changed**, joined by
the request id. The arguments have `tool.call` for what the model wrote and the
hook's own line for what it left. The output has `tool.output` for what the tool
produced and `tool.result` for what the model finally read, with each hook of a
chain on its own line in between. `tool.output` is written for every call and
not only for a guarded one: a line whose absence meant either that no hook ran
or that one ran and left the output alone would be a line nobody could read.

It is the text the hooks are shown, which for a command is its two streams woven
together under the status line — a string, like every other `content`. This is
the one line where the record of which stream carried which bytes is still alive
and could be written down, and a `stdout`/`stderr` pair is still not what is
written there: the one thing the woven string says and the pair cannot is the
order the two arrived in, which is what that record exists for. 14.11 is the
cost of that.

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
- `enabled` is `true` or `false`;
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
because a matcher that is wrong is wrong on the day the hook is switched back
on, and a mistake found when the folder is read is a mistake found once.

**A hook that loads by that rule and still cannot fire in this run is marked,
not refused.** A matcher on the terminal tools in a run without
`--enable-terminals` is the case: the name is a real tool of agi's, so this is
the user's decision and not their typo, and a decision is obeyed. What is owed
in return is the news, which is the bargain `enabled` already strikes — so
such a hook is listed under `--verbose` and marked `(watches nothing in this
run)`, worded apart from the `(off in this run)` of a hook that is switched
off, because the two are corrected differently: one by editing the field, the
other by passing the flag. See 4.4.

**A hook whose `enabled` is `false` is not refused either**, for the same
reason: a mistake is refused, and a decision is obeyed. Nor is a display hook
in a piped run, which is the event's rule rather than anybody's mistake. The
dead hooks agi allows are therefore these and the matcher above, and all are
marked under `--verbose`. See 4.4.

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

`Message.Thinking` is what selects the hooks, and it is a field of the
message rather than a second entry point because nothing else about the two
kinds differs: the same text through the same sequence to the same place, and
the only question is which hooks were written for this one. `Set.Displays`
answers that question on its own, because the caller has to know whether
anybody claimed a kind *before* laying it out itself — see 3.2 on the
dimming.

The `cli` side hooks the display sites and nowhere else. It is tempting to
put the call inside the progress stream, where everything agi prints already
goes, and that would be wrong: `[bash]` announcements, the `[thinking]`
label, the session line, hook notes and the verbose preamble are agi's own
chrome, and passing them through a markdown renderer would mangle them. The
deliberation under that label is not chrome and is hooked; the label is, and
is not.

It is a `Set` and not a `Guard`, which is what an earlier draft called it.
Guarding is one of the three things a hook does, and the name would have been
wrong for the other two: a hook that rewrites a call's arguments, redacts a
secret out of a result, or adds a line of context is not guarding anything.
Naming the whole after the part is the widening this repository refuses
elsewhere, and it is the reason the native tools are called *native* rather
than *control flow*.

Two error types come back out, and the agent loop tells them apart by type
rather than by reading the message: one refuses the call and lets the agent
carry on, the other stops the agent. The second is contained by
`cli.contain` when the agent is a fork or a subagent, which is the one line
that makes section 8.3 true.

A run given no hooks gets an empty set that passes every call through
untouched, so no caller anywhere has to ask whether there are hooks — the same
bargain `--no-log` already makes with the event log.

**The set is loaded from the folders and then told two things about the run**,
each by a method that returns a copy: `WithSession`, which is the identifier a
hook is sent, and `WithTTY`, which is whether agi's output is a terminal and
so whether the display hooks run. Neither can be settled while the
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
which is worse. The original is still in the log, on the `tool.output` line —
flattened, since that line holds the text the hooks were shown, but the
sections are also in the `subagent.finish` line each subagent wrote for itself.

A replacement is read in the shape the hook was shown — a string for a tool
that answers with one, and the four-field object for a tool that ran a
program, as 6.1.1 says. A value of some other shape is ignored and the tool's
own output stands, which is Claude Code's rule for the same case. The user is
told, because a rewrite that silently did nothing is one its author believes
is working.

A replacement in the four-field object is read in four steps. They look
arbitrary, and they are: section 14.10 names the three requirements whose
collision produces them, and says which pair each step is the compromise of.

1. A value that is not an object does not match. The tool's own output stands
   and the user is told. Fields agi does not know are kept rather than
   refused, because the natural way to write such a hook is to override one
   field of the object it was shown, and under Claude Code that object may
   carry more than these four.
2. **A reply handing both streams back exactly as they were shown has replaced
   nothing**, and the command's own result stands — woven, in order, under its
   status line. This is not a curiosity: taking the object, changing one field
   and printing the whole thing back is how such a hook is usually written, and
   without this rule a hook that changed nothing would still cost the result
   its interleaving.
3. **Anything else is the two joined with a newline, `stdout` first**, with an
   empty one left out. The interleaving is lost, which is the honest
   consequence: agi has no record of when words a hook composed would have been
   written, so the only order it can put them in is the order it has names for.
   Nothing is delimited — a marker agi invented there would be text the model
   has no way to tell from the command's own. **This is all or nothing and not
   per line**: a hook that edits one line of seven costs the other six their
   arrangement too, including lines in the stream it never touched.
4. **The status line is put back underneath.** A hook was never shown it — it
   is agi's sentence about the call and not the command's output — so rewriting
   the output is not a request to drop it, and a model reading a rewritten
   failure should still be told the command failed.

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

### 14.8 A display hook cannot rewrite a piped answer

`MessageDisplay` fires only where agi's output is a terminal (see 3.3), so a
hook that redacts, translates or trims a message does nothing for `agi >
file` or `agi | jq`. That is the price of a renderer never painting escape
sequences into a pipe, and of a manifest with no combination of fields that
can only do nothing.

The rewrite still has a place to go: the pipe. `agi ... | ./redact.sh` does
to the answer what the hook would have done, with one difference worth
knowing — the hook's line in the event log (11.2) records what was printed,
and a program in the pipe leaves no such line. A run that needs the log to
show the rewritten text has to log it itself.

### 14.9 The order of a command's two streams is agi's observation

`stdout` and `stderr` arrive on two pipes, and one reader polls both, so the
order the two are woven into for the model is the order the *kernel* made them
readable in. That is the command's own order for every write that is more than
a wakeup clear of the one before it — measured at well under a tenth of a
millisecond.

**Two writes closer together than that cannot be separated by any reader**, in
this program or any other: both pipes are readable before anything in user
space can look. Such a tie is resolved in favour of standard output, every
time, so that a command at least prints the same way twice.

This is a guarantee being weakened rather than a feature being added. Before
the streams were captured apart, `bash` gave its command one file descriptor
for both, and the kernel ordered even those writes exactly. Telling the two
streams apart costs that. The trade was made because `stderr` was permanently
empty, and a field that is always empty is one every ported guard reads as
"nothing went wrong".

What still holds, and what a hook may rely on:

- **Each stream's own bytes are in exactly the order the command wrote them.**
  Always. `stdout` and `stderr` in the payload are never reordered or
  interleaved with each other.
- **Any pause is a real boundary**, down to well under a tenth of a
  millisecond.
- **No line is ever spliced.** The weaving is by line: each stream's bytes wait
  until the line they are part of is complete, so a read landing mid-line
  cannot drop the other stream into the middle of it. The cost is that a line
  is placed by when it *finished* rather than when it began.

A second effect is older than any of this and worth knowing: a C library fully
buffers standard output when it is a pipe and leaves standard error unbuffered.
A command that does not flush deliberately therefore has its own output
reordered before agi sees any of it, and no capture at either end can undo
that.

### 14.10 Three requirements that do not fit together

Several rules in this document look arbitrary — the tie resolved in favour of
standard output, the status line kept out of both streams and put back
afterwards, a replacement that changed nothing replacing nothing, a replacement
that changed one line collapsing all of them. They are arbitrary. They are what
is left after three requirements that cannot all be met collide, and the honest
thing is to say which three and where each pair breaks, so that a reader can
see the shape of the problem rather than hunt for a principle that is not
there.

The three:

1. **Standard output and standard error must be kept apart**, because a hook
   carried over from Claude Code reads `.tool_response.stderr`, and a field
   that is always empty is one every such hook reads as "nothing went wrong".
2. **The model must read one transcript, in the order things happened**,
   because a warning that arrived between two steps is worth more than knowing
   which descriptor carried it.
3. **A hook may replace what the model reads**, whole, because that is what
   `updatedToolOutput` is for.

No two of them are in trouble alone. All three together have no clean answer.

**(1) against (2).** Keeping the streams apart means two pipes, and two pipes
mean the order is observed rather than imposed: the kernel merges nothing, so
agi sees whichever becomes readable first. Above a wakeup that is the true
order. Below it — two writes microseconds apart — nothing in user space can
tell, and *something* has to be written down. The tie goes to standard output,
every time. There is no reason for standard output over standard error; the
reason is only that a fixed answer makes a command print the same way twice,
where a coin would have the same script read differently from one run to the
next. See 14.9.

**(2) against (3).** The interleaving is a record of when *the program's* bytes
arrived. A hook hands back text that no program ever wrote, so there is no
moment to put it at, and any weave agi produced would be invented timing. So a
replacement that differs is concatenated — standard output, a newline, standard
error — and the interleaving is lost. **This is all or nothing and not per
line**: change one line of seven and the other six lose their arrangement too,
including the lines in the stream the hook never touched. agi cannot do better
without guessing which new line stands where an old one did, and a guess here
is worse than a plain concatenation that is obviously a concatenation.

A rule was considered and rejected: re-weave when the hook happens to hand back
the same number of lines in each stream, substituting line for line. It would
cover redaction, which is the commonest hook there is. It was not taken because
it makes the result depend on whether a hook happened to preserve a line count
— a condition nobody would be told about until it surprised them — and because
it is a second way of reading a replacement, which is a second thing to keep
true. The blunt rule and this paragraph were preferred to the clever rule and a
silence.

**(3) against (2), again.** Because a replacement is read whole, a hook that
takes the object it was shown and prints it straight back would, under the rule
above, flatten a result it did not change. That is the commonest shape of hook
there is. So a reply whose two fields are exactly the two it was shown replaces
nothing at all and the program's own woven result stands. **This is the one
rule here that exists purely to protect requirement (2) from requirement (3)**,
and the only reason it can exist is that agi still holds the record to compare
against.

**(1) against (3).** A hook shown two fields must hand back two fields, and agi
must then turn two fields into the one string the model reads. The join is
standard output first, non-empty parts only, separated by a newline. Nothing is
delimited and nothing is labelled: a marker agi invented would be text the model
has no way to tell from the program's own, so a command that prints `[stderr]`
would become indistinguishable from agi saying it. **In the model-facing text,
stream identity is therefore gone entirely** — a hook returning only
`stderr` produces a result with nothing at all to say it came from standard
error. The identity lives in two places and no third: the execution record
inside agi, and the `tool_response` object a hook is shown.

**Where the status line sits** falls out of the same collision. `exit status 7`
is agi's sentence about the call and not something the program printed, so it
belongs in neither stream and is not in the payload a hook reads. A hook
rewriting the output has therefore not asked for it to be dropped, and it is
put back underneath whatever the hook wrote. The model needs it: nothing else
in the text says a program failed, since standard error is not failure and a
program can fail having printed nothing.

**What a hook author should take from this.** If the ordering matters to you
and you want to edit the output, do not edit the two fields. Build the whole
transcript yourself, put it in `stdout`, and set `stderr` to `""`. There is
then one non-empty part, the join does nothing, and you own both the ordering
and any labelling you want — which is exactly what a hook that renders
`[stdout]` and `[stderr]` sections is doing.

### 14.11 Which stream a byte came from is not kept

A `tools.Execution` holds the two streams apart and keeps the order agi
observed them in. It lives for the length of one call: it is made when the
program runs, shown to each `PostToolUse` hook as `stdout` and `stderr`, and
dropped once the hooks have had it. Nothing downstream holds it.

So of the three accounts agi keeps, none says which bytes a command wrote on
which stream. The conversation and the snapshot hold the one string the model
read. The log holds that string on `tool.result` and the pre-hook one on
`tool.output`, both woven. A reader asking afterwards *was that warning on
standard error?* has no way to find out, and a reader asking *did the weave put
it in the right place?* has no record to check the answer against.

Writing the pair down would not answer either question. `stdout` and `stderr`
side by side say which stream carried a line and lose when it arrived, which is
the half the woven string already keeps and the half that matters — 14.10 (2) is
the requirement, and a reader reconstructing the transcript from the pair would
be inventing the ordering agi went to some trouble to observe. What would answer
both is the chunk record itself, and that is a bigger thing than a field: it is
only meaningful against the two buffers, it has no shape a `content` could take,
and nothing has yet wanted it.

The same gap closes over a rewrite one step earlier. A `PostToolUse` hook is
shown the two streams and replies in the same shape, but its line records the
text after 14.3's join has collapsed it, so the log does not say whether the hook
wrote into `stdout`, `stderr`, or both. That is a narrowing of the same kind and
for the same reason: what agi keeps of a program is what somebody reads, and
nobody has yet needed to read this.
