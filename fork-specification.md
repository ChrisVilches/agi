# The `fork` tool

A specification for a native tool that runs a copy of the agent on one task and
returns only its final answer.

This document is a proposal. Nothing in it is implemented. It is written to be
argued with, and where it decides something that the original sketch left open
it says so and gives the reason.

## 1. What the tool is

`fork` runs a copy of the agent that called it, starting from that agent's
conversation exactly as it stands, on one task, and hands back that copy's
final prose answer as the result of the call. Everything else the copy did —
every file it read, every command it ran, every wrong turn it took — is
discarded.

It exists because agi's record is append-only. A `UserMessage`, an
`AssistantMessage`, a `ToolResultMessage` and a `ToolFailure` all report
`Truncatable() == false`, and `Conversation.DropFrom` refuses a tail that does
not begin at a truncatable turn. That is a deliberate invariant and not an
oversight: what the user asked and what the model answered are kept exactly as
they arrived. It follows that an agent cannot do a long piece of exploratory
work inline and then throw the exploration away. The only way to keep the
conclusion without the rubble is to do the work somewhere else and bring one
answer back, and that somewhere else has to know everything this agent knows or
it is not the same piece of work.

That is the whole of it. A fork is a conversation agi makes, uses, and drops,
in the one place where dropping is not allowed to happen to the record.

## 2. The name

The tool is called `fork`, and not `subtask`.

`subagents` and `subtask` mean the same thing in ordinary English, and a model
holding both descriptions would have to work the difference out from prose
every time. The name has to carry some of that load. In programming, a fork is
a child that is a copy of the parent's state, runs on its own, and whose result
the parent waits for — which is this tool exactly, the waiting included. The
term is not being widened to fit a new case; it already means this.

It also makes the pair legible at a glance. One spawns strangers, the other
copies you.

## 3. The agent tree

A run has three kinds of agent and exactly three levels.

| Agent    | Spawned by        | May spawn           |
| -------- | ----------------- | ------------------- |
| main     | the user          | forks and subagents |
| fork     | main              | subagents           |
| subagent | main or a fork    | nothing             |

The constraints, and why each one is there:

- **A fork cannot fork.** Otherwise "one fork at a time" is untrue and the
  depth is unbounded. The point of the tree is that a model can hold it in its
  head.
- **A subagent cannot fork.** A subagent's context is small and was written for
  it by somebody else; a copy of that small context is a strange thing to want,
  and it would put a fork at the bottom of the tree where nothing needs one.
- **A subagent cannot spawn subagents.** Unchanged from today.
- **Only one fork runs at a time.** This needs no new machinery. `converse`
  iterates `conv.PendingToolRequests()` in order and runs one tool to
  completion before the next, so two `fork` requests in one assistant turn run
  one after the other, and the second copies a conversation that already holds
  the first one's answer.

None of this is stated in any prompt. What an agent may do is settled by which
tools are registered for it and never by wording, which is the rule that
already stops a subagent spawning subagents. A fork simply has no `fork` tool,
the same way it has no tool nobody wrote.

## 4. What a fork is given

### 4.1 The system prompt is rebuilt, not copied

The fork's system prompt is assembled the same way every other agent's is,
through `systemPromptFor(base, registry, skillsSection(found))`, using the
fork's own registry.

It must not be copied from the parent. The parent's prompt contains the
guidance for `fork` and for `terminate`, and the fork holds neither tool.
Carrying that text into the fork would be a prompt describing tools the agent
does not have, which is the standing lie that keeps workspace tools out of the
system prompt in the first place.

**The base is the parent's base, including `--sys`.** This is the opposite of
what a subagent gets, and for the reason the subagent rule gives: what `--sys`
replaces is the character of the agent the user is talking to, and a subagent
is not talked to. A fork *is* the agent the user is talking to — a copy of it —
so it must read the same base, or the answer that comes back was written by
somebody else.

Assembly stays deterministic. The same arguments and the same files produce the
same prompt, for the fork as for the run, which is the invariant that lets a
snapshot store no prompt at all.

### 4.2 The conversation copy

`model.Conversation` gains one method, and it does the whole job:

```go
// Fork returns a conversation a copy of this run can continue from: the same
// turns in a slice of its own, under the system prompt the copy will read,
// with the call that asked for the fork answered, anything asked alongside it
// answered as a call that was not made, and instruction appended as the thing
// the copy is to carry out.
//
// All of that is here rather than in the caller because all of it is a rule
// about how turns may follow one another. A copy that moved on while a call it
// asked for was still outstanding is one a provider may refuse, and a caller
// that had to remember to close those out is a caller that one day will not.
//
// The conversation it was made from is left exactly as it was. The turns are
// values and are shared rather than duplicated; nothing appended to one side
// can reach the other.
func (c *Conversation) Fork(system, requestID, instruction string) *Conversation
```

An earlier draft of this specification had `Fork` copy and nothing else, and
had the fork runner in `cli` close the copy out in three further calls. That
was wrong twice over. It scattered rules about turn order into the agent loop,
which is the one place the repository says they do not belong; and three steps
that must always happen together, in one order, are not three steps.

What each part answers:

- **The fork's own request is answered**, because a provider is entitled to
  refuse a turn that moves on while a call it asked for is outstanding — the
  same rule that makes `AbandonToolRequests` necessary after an interrupt. The
  copy therefore reads that it called a tool it does not itself hold, which is
  the honest record of how it came to exist and is no stranger than a resumed
  session reading its own `terminate` result.
- **Anything asked alongside it is answered as a call that was not made.** That
  is true rather than convenient: `converse` runs pending requests in order, so
  a request still outstanding when the fork starts has not run anywhere yet,
  while a sibling that ran *before* the fork already has its real result in the
  copy. The parent's own conversation is untouched, and runs those siblings for
  real once the fork returns.
- **The instruction is appended last**, as section 4.3 describes.

Two fixed pieces of wording live in `model`, beside `instructionPrefix`,
`failurePrefix` and `resultHeader`, which are the same kind of thing — text a
model reads, produced by agi, fixed for every run:

```go
// forkMade answers the call that asked for a fork, inside the copy that call
// produced. It is one flat line: what this conversation is is explained by the
// instruction that follows, and saying it twice would have the copy read the
// same sentence from two different voices.
const forkMade = "a copy of this run was made; this conversation is that copy"

// errForkUnmade answers the calls asked for alongside a fork, inside the copy.
var errForkUnmade = errors.New("this call was not made in this copy of the run")
```

### 4.3 The task arrives as a harness instruction

The task is appended as a `model.HarnessInstruction`, not as a `UserMessage`.

The user did not write it. The model did, and agi is relaying it, which is what
a harness instruction is for: agi speaking to the model in its own voice,
travelling under the user role because no provider has one for the harness. A
`UserMessage` would be a false record of who said it, and the record is the one
thing agi never rewrites.

It is assembled by `cli.forkInstruction`, beside `schemaInstruction`,
`correctionInstruction` and `describeRejection`. That was an open question in
an earlier draft — whether prose the model reads belongs in `prompt/` instead —
and reading settled it: `cli` already holds three builders of exactly this
kind, and `prompt/` holds system prompts, which this is not. A fourth joins the
three.

```
Everything above is the conversation you were copied from. It is your context
and not your assignment.

Carry out the task below and then answer in prose. That answer is the whole of
what returns to the run you were copied from: nothing else you do here is kept,
and there is nobody to ask, so settle what you can and say plainly in the
answer what you could not.

The task:

<the task, as the model wrote it>
```

It does not open by saying the reader is a copy, because the tool result
immediately above it has just said so. Two sentences making the same
announcement, one from a tool and one from the harness, would read as two
different voices disagreeing about which of them is telling the reader where it
is.

`HarnessInstruction.Render` prefixes the whole thing with `[Instructions]
Follow these instructions: `, as it does for every instruction.

### 4.4 Tools

The fork gets the run's tools minus `fork` and minus `terminate`.

`terminate` is left out for the reason a subagent does not have it. A fork is
one attempt at one piece of work, and letting a discarded attempt end the whole
run would give an experiment the power to kill the thing that ordered it. A
fork that cannot do its task answers in prose saying so, and the parent decides
what that means.

This must hold by construction and not by two hand-written lists, which is the
argument `workspaceRegistry` already makes. So `defaultRegistry` is rewritten
in terms of the fork's list:

```go
// forkRegistry is the tools a fork is given: the work tools, subagents, and
// loadskill where the run has skills.
func forkRegistry(w wiring, agent string, loaded []string) (*tools.Registry, error)

// defaultRegistry is that list plus the two tools only the agent the user is
// talking to may have.
func defaultRegistry(w wiring) (*tools.Registry, error) {
    r, err := forkRegistry(w, eventlog.MainAgent, nil)
    ...
    r.Register(native.NewFork(forkRunner(w)), native.NewTerminate())
}
```

`wiring` is the one value everything here is built from; section 14.4 says what
is in it and why it exists.

A tool added to `forkRegistry` reaches both, and "a fork gets the run's tools
minus two" stays true rather than being true by coincidence until the next tool
is added.

Registering `fork` in `defaultRegistry` also reserves the name against
user-defined tools for free, since `providedNames` builds that registry to find
out what the reserved names are.

### 4.5 Skills

A fork inherits every part of the parent's skill state.

- **Available skills.** The same `[]skills.Skill` the run was given, in the
  same order, so the fork's skills section lists exactly what the parent's
  does.
- **Loaded instructions.** Already in the copied conversation, since a loaded
  skill's text is a tool result like any other.
- **Which skills are loaded.** This has to be handed over explicitly, or the
  fork's `loadskill` would start with an empty set and let the model load a
  skill whose text is already sitting in its context.

So `loadskill` gains a way to be built pre-loaded, and a way to report what it
has loaded:

```go
// NewLoadSkill returns the tool for the skills a run was given, with the names
// in loaded already marked as handed over. A fork passes what its parent had
// loaded, because the instructions came across in the conversation and asking
// for them again would only say twice what is already there.
func NewLoadSkill(found []skills.Skill, loaded []string) *LoadSkill

// Loaded returns the names already handed over, in the order the skills were
// found, so that a fork built from them is built the same way every time.
func (t *LoadSkill) Loaded() []string
```

`Loaded` returns names in `listed` order rather than map order: the fork must be
built deterministically, and a map would order it by hash seed.

The existing two call sites pass `nil`. The fork runner passes the parent
tool's `Loaded()`.

**Subagents spawned inside a fork are unchanged.** They start with no skills,
get only the ones their task names, and have nothing pre-loaded — a fresh
context has nothing in it to have loaded.

## 5. What comes back

One string: the fork's final prose answer, as an ordinary
`model.ToolResultMessage`.

`Fork` implements `tools.Conversational`, the optional interface of section 14,
because it needs two things an ordinary tool is not handed: a copy of the
conversation it was called in, and the request id of the call, so that it can
answer the fork's own outstanding request inside that copy. What it returns is
still `model.NewToolResult(requestID, answer)`.

Nothing wraps the answer. `SubagentResults` needs its `=== subagent result #n
of m: title ===` headers because several answers arrive at once and the model
has to see where one ends; one answer has nothing to be separated from.

A fork that fails, and a fork that ends on an empty answer, both return an
error from the tool. The agent loop turns that into a `ToolFailure`, which is
the right type: there is no answer for the model to read and weigh, only the
fact that there is none. This is not in tension with `bash` reporting a
non-zero exit as an ordinary result. A command's exit status is one more thing
for the model to read; a fork that produced nothing has given it nothing to
read.

It is also why a fork failure is handled differently from a subagent failure. A
failed subagent reports inside its own section because its siblings' answers
are worth keeping. A fork has no siblings.

**A known wart, and what it is not.** `tools.Tool` requires `Definition` and
`Call`, so `Fork` must have a `Call` method. `Call` is handed a context and
arguments and no way to reach a conversation, so it cannot do the tool's job.
It returns an error saying exactly that, and a test asserts the message rather
than leaving it to be discovered.

This is invisible to the model. The `fork` tool it sees works normally: a
request goes to `Registry.Answer`, which prefers `Conversational` and calls
`ResultWith`. `Call` is not on that path, and `Registry.Call` — the only other
way in — has no caller outside tests.

It is also not the same shape as `Subagents.Call`, which is real work:
`Subagents` renders its answers as text instead of as a structured turn, so a
text version honestly exists. What `Fork` is missing is not a rendering choice
but the conversation itself, and there is no version of the call without one.

**The alternative, and why not.** `tools.Tool` could require only
`Definition`, with `Call` made optional the way `Resulter` is. `Fork` would
then have no `Call` at all and the wart would be gone.

It is not worth the trade. Today, registering a tool is a compile-time
guarantee that the model can call it. Narrowing the interface would mean a tool
implementing none of the answering interfaces compiles and is registered and
fails the first time the model calls it — in the middle of a conversation the
user is paying for. That is the class of failure this repository moves to
startup everywhere else: a manifest is checked when its folder is read, `--sys`
is read before a gateway is contacted, a `--schema` that will not parse is
refused before a session exists. Giving up a compile-time guarantee that covers
every tool, in order to delete one method on one tool, runs the wrong way.

So the wart stays, deliberately, and is written down here so the next reader
does not have to rediscover the trade.

## 6. How it is advertised to the model

### 6.1 There is no "when to use which" document, and there cannot be

The convention for native tools is one guidance file per tool, each reaching
the model only when its own tool is registered, and none of them describing a
tool another file provides. A run may register `subagents` and not `fork`, or
`fork` and not `subagents`, and a file that compared them would then be telling
the model about something it does not have.

So the comparison is not written anywhere. Each file is made sharp enough about
its own tool that the choice falls out, which is exactly what the workspace
tools already do: a description "says what its own tool is for and what it is
not for, and the model assembles the comparison from whatever it was actually
given."

Two sentences carry the whole distinction, and neither mentions the other tool:

- `subagents`: **at least two tasks, and each one starts knowing nothing.**
- `fork`: **exactly one task, and it already knows everything you do.**

A model holding both descriptions cannot get this wrong. A model holding one of
them still reads something true.

The rejected alternative is a comparison the registry emits only when both
tools are registered. `loadskill` is already the one exception to guidance
living with its tool, and the convention says plainly that a second one would
be "a pattern with no home."

### 6.2 `subagents` has to stop claiming context as its purpose

This is the part of the change that is not about `fork` at all, and it is the
part that decides whether the model picks correctly.

`prompt/native-subagents.md` currently advertises the tool with two
justifications mixed together: it saves your context, and it runs work in
parallel. Once `fork` exists, both tools save context — so context saving stops
being the thing that tells them apart, and the paragraph that leans on it would
send the model to `subagents` for work that is `fork`'s.

Worse, its examples are now `fork`'s examples: "reading many files to answer a
handful of questions" and "trying approaches that may turn out to be dead ends"
describe one piece of work that depends on what the agent already knows.

Replace the second paragraph of `prompt/native-subagents.md`:

> Use it when the work would otherwise fill your context with material you do
> not need to keep: searching a large codebase, reading many files to answer a
> handful of questions, trying approaches that may turn out to be dead ends.

with:

> Use it when the work splits into pieces that do not need each other: several
> questions to answer, several places to look, several things to check. Each
> piece has to be one you could hand to a stranger, because that is what you
> are doing.

Nothing else in that file changes. Everything else it says — one call spawns
every task, tasks must be independent, a body has to stand on its own — is
about parallelism and strangers already.

### 6.3 `prompt/native-fork.md`

A new file, reaching the model only through `Fork.SystemPrompt`, and written to
the shape the other guidance files keep: what the tool is, when to reach for
it, what a call has to meet, when not to reach for it.

```
The fork tool steers your own execution rather than touching the user's
machine: it runs a copy of you, from this exact point, on one task, and
returns only that copy's final answer.

The copy has everything you have: this whole conversation, the same
instructions, the same tools. It does not report back as it works, and you
cannot talk to it. When it finishes, one answer arrives here as the result of
this call. Everything else it did — every file it read, every command it ran,
every wrong turn it took — is gone.

That is the whole point of it. Reach for it when you want the answer to a
piece of work but not the record of how it was reached: reading a great deal
to settle one question, or trying an approach that may not pan out. You keep
the conclusion and none of the rubble.

It is also the most expensive call you can make. The copy carries this entire
conversation and pays for it again on every turn it takes, and nothing else
happens while it runs. So it earns its cost only when the work is long enough
to be worth hiding. Work you could finish in two or three steps leaves nothing
worth discarding, and you should simply do it here.

One task, and it is written for something that already knows what you know. Do
not brief it. Do not restate the problem, repeat what you have found, or
explain the conversation — it has read all of that, and will read your
repetition as a correction. Write the instruction alone: what to settle, and
what the answer must contain. Be exact about the answer, because the answer is
the only part that survives.
```

Note what the file does **not** say: that a fork cannot fork, and that it has
no `terminate`. Both are settled by registration, and a prompt that forbade
them would be describing tools the reader does not have.

### 6.4 The tool's `Description`

```
Run a copy of yourself on one task and return its answer.

  {"task": "Work out whether the session lock survives the process being killed, and answer yes or no with the reason."}

ONE TASK, AS ONE STRING. Not a list, not an object, not several tasks in one
call. If you have several pieces of work, this is not the tool for them.

The copy starts from this conversation exactly as it stands. It has read
everything you have read and holds the same instructions, so the task is an
instruction and not a briefing. Do not restate the problem and do not repeat
what you have already found: it has all of that, and a repetition reads as a
correction.

Only the copy's final answer comes back, as the result of this call. Nothing
else it does is kept — not the files it reads, not the commands it runs, not
the turns it takes. Say plainly what the answer has to contain, because the
answer is all that survives.

Nothing else happens while it runs, and it re-sends this whole conversation on
every turn it takes, so it is the most expensive call available to you. It
earns that only when the work is long enough that you would not want it here.
```

Schema:

```json
{
  "type": "object",
  "properties": {
    "task": {
      "type": "string",
      "description": "Required, and never empty. The one task for the copy to carry out, written as an instruction to something that has already read this conversation."
    }
  },
  "required": ["task"],
  "additionalProperties": false
}
```

There is deliberately **no `title` field**. `subagents` needs one because the
user cannot see a subagent working and the title is the only thing they get; a
fork is watched live, so the title would be a second name for something already
on the screen. Leaving it out also removes the failure that breaks `subagents`
calls most often, which is the work going into the title and the body being
left empty.

The single string is also what makes the shapes mirror each other. `subagents`
refuses one task; `fork` will not accept an array. The shape of the call the
model can write is itself part of the answer to "which of these do I want".

An empty or whitespace-only `task` is refused with an error the model can act
on, in the manner of every other refusal in `native`.

## 7. Terminal output

A fork is watched. Subagents are silent because several at once would
interleave into nonsense, and that reason does not reach a fork: there is only
ever one, and the parent is blocked while it runs.

So the fork runs with the parent's `output` rather than with `quiet()`, with
two changes:

- `answer` becomes `io.Discard`. The fork's answer is not the run's answer; it
  is a tool result, and the agent loop already prints and logs tool results.
  Letting the fork print it would be saying it twice.
- `steps` is wrapped in a writer that marks every line, so the user can see
  which stream they are reading. `output` gains a `nested()` method returning
  such an output, as a sibling of `quiet()`.

`verbose` is inherited, so a `--verbose` run sees the fork's tool output as it
sees its own.

Three details about the marking writer, because each one is a way to get it
subtly wrong:

- **It cannot be `indent`.** That helper takes a finished block and splits it;
  a fork's progress arrives a line at a time, and a line often arrives across
  several writes. The writer has to hold one bit of state — whether it is at
  the start of a line — and emit the marker when the next byte arrives rather
  than when a newline is seen.
- **A trailing newline must not produce a marked empty line.** `converse`
  prints a bare `"\n"` after every tool call, and a writer that marked eagerly
  would draw a marker on nothing.
- **The marker must be distinguishable from `indent`'s.** `indent` already
  prefixes tool output with a dim `  | `, and the fork's own tool output will
  be indented by it inside the fork before this writer marks it again. If the
  two markers were the same, a fork's first line would be indistinguishable
  from the run's own tool output. A different dim glyph in the same family is
  what is wanted; which glyph is a detail to settle when it is written.

## 8. The log

A fork records everything an ordinary run records, under its own name, in the
run's one log file. Three changes.

**A `fork` field on `eventlog.Event`**, placed immediately after `Agent`:

```go
// Fork reports that the event was recorded inside a fork — by the fork itself
// or by a subagent it spawned. The agent field says which agent; this says
// which side of a fork it was on, which is the one thing a name alone cannot
// tell a reader, since a fork's subagents are numbered in the same sequence as
// the run's own.
Fork bool `json:"fork,omitempty"`
```

Field order is the file format, and this belongs with `Agent` because the two
together are the line's identity and every other field only means something
once that is known.

**Two handles on `eventlog.Log`.** `Log` gains a `fork bool`, stamped by
`Record` alongside `Agent`. `Log.Fork(name)` returns a handle with it set;
`Log.Agent(name)` *preserves* whatever the handle it was called on had, so a
subagent spawned inside a fork carries the flag without anything having to
remember to set it.

**Two event kinds**, `fork.start` and `fork.finish`, mirroring
`subagent.start` and `subagent.finish`: the task and the fork's tool list on
the way in, the answer or the reason and the elapsed time on the way out.

Reusing `subagent.start` for a fork is not an option. `Kind` is what a reader
filters and groups by, and it would be saying a fork is a subagent. These are
two new kinds and not a renaming of agents, which is the thing the sketch ruled
out.

Both mechanisms earn their place. The kinds bracket the fork's work; the flag
marks every line inside it, including its subagents', without a reader having
to track where a bracket opened.

## 9. Names and numbering

Two things are filed under an agent's name: its lines in the log, and the
copies `patchfile` keeps of files it changes, named `<UTC timestamp>-<agent>-<file>`.

**A fork is `fork-1`, `fork-2`, and so on**, counting across the run. A run
that forks twice does not reuse the first one's name, for the reason subagents
count across calls. It is not `main`: a fork's patches are changes whose
reasoning the snapshot does not contain, and being able to tell those copies
apart from the run's own is exactly what the copies are for.

**Subagent numbering becomes run-scoped rather than tool-scoped.** Today the
counter is a field on the `Subagents` tool, and a run has exactly one of those,
so the numbering is already run-scoped — but by accident rather than by
construction. A fork builds a registry of its own and therefore a `Subagents`
tool of its own, whose counter starts at one again, and the accident stops
holding: the fork's first subagent is a second `subagent-1`.

The reason to fix that is not collisions, and it is worth saying so plainly
because that is the obvious argument and it does not hold. A copy is stamped
`20060102T150405.000000000`, so two agents under one name would have to write a
copy of the same file in the same nanosecond — and they never run at the same
time in any case, since the run is blocked while its fork works. The reason is
that two different agents would be filed under one name. In the log a reader is
rescued by the `fork` flag; in `backups/` nothing rescues them, and a listing
would show two `subagent-1` entries belonging to different agents, with no way
to tell which change was whose.

It is also a rule the repository has already written down: the numbering keeps
counting across calls so that a run which fans out twice does not reuse the
first group's names. A fork is one more place those calls come from, so a
per-fork counter would break an existing invariant rather than decline a new
one.

So the counter moves out of the tool and is created once per run:

```go
// Counter hands out the numbers that name spawned agents within a run. It is
// created once and shared by every Subagents tool in the run — the run's own
// and each fork's — because the names it produces are also filenames, and two
// tools counting from one would put two agents under one name.
type Counter struct{ n atomic.Int64 }

// Take claims a block of n numbers and returns the first, so that a call
// numbers its tasks in the order they were written rather than the order the
// goroutines got going.
func (c *Counter) Take(n int) int
```

`native.NewSubagents(run, found, counter)` takes it.

**Forks do not need one, and an earlier draft gave them one anyway.** A run has
exactly one `Fork` tool by construction: a fork has no `fork` tool and neither
has a subagent, so nothing but the run's own registry ever holds one. Its
counter can therefore stay a field on the tool, which is what `Subagents` does
today and the only reason that stopped working for subagents is that a fork
builds a second one. A shared counter for a thing that exists once would be
machinery bought to solve a problem the tree already rules out.

## 10. Interruption, termination, failure

**Interruption needs no new machinery.** A fork runs on the context the whole
run runs on, so Ctrl-C cancels it where it stands. The fork's `converse` calls
`abandon` with the fork's own log, recording `run.interrupt` under the fork's
name with `fork: true`, and returns `ErrInterrupted`. That reaches the parent
loop through `registry.Answer`, where `err != nil && interrupted(ctx)` already
sends the parent to `abandon` too. The parent's outstanding `fork` request is
answered as a call that could not be made, the parent's conversation is stored,
and the run exits 130.

Nothing of the fork is stored, which is correct: a fork's conversation is never
stored, interrupted or not.

**Termination cannot happen in a fork**, because the tool is not registered.
That is true and it is not enough, because of how the error travels.

`converse` decides what a tool's error means by its type:

```go
answer, err := registry.Answer(ctx, req)
var end *native.Termination
if errors.As(err, &end) {
	return "", terminate(lg, conv, turn, req, end)
}
```

A fork's error travels that exact path — `converse` inside the fork, to
`forkRunner`, to `Fork.ResultWith`, to `Registry.Answer`, to the parent's
`converse` — so a `Termination` arising inside a fork would not end the fork.
It would end the whole run, and the user would see a run terminated by an agent
they never spoke to, with a reason written about a task they did not set.

Nothing can trigger it today. But the invariant rests on one line in
`forkRegistry`, and nothing at the place it would matter says so. A rule this
far from the damage is the kind that stops holding the first time somebody adds
a caller — which is the argument `session.Remove` already makes for checking
the directory's prefix rather than trusting the flag.

So **`forkRunner` refuses to let a `Termination` escape**, converting one into
an ordinary error, which the parent then treats as a failed fork. Defence in
depth for a wall that is currently one line thick.

`ErrInterrupted` is deliberately *not* converted. It is the same shape — a
sentinel the parent inspects — but the parent is meant to see this one, which
is how the paragraph above works. The two are told apart by type, which is what
the types are for.

The same coupling exists in `subagentRunner` and is equally unreachable and
equally undefended there. Fixing both is one change and should be one change.

**An ordinary failure inside a fork** — a gateway that does not answer, a reply
agi cannot make sense of — comes back as an error from the tool and becomes a
`ToolFailure` in the parent, which the model reads and decides about on its
next turn. It does not fail the run. The parent may well be able to finish
without whatever the fork was going to find out, and that judgement is the
model's.

## 11. Sessions and snapshots

Nothing changes, and that is worth stating because it is the feature working.

The snapshot holds the parent's conversation: the assistant turn that asked for
the fork, and the one-result turn that answered it. The fork's own turns were
never part of any conversation that gets stored.

The log holds all of them, under the fork's name and with `fork: true`. What
the snapshot drops, the log keeps — which is the existing rule about the two
mechanisms, arriving here as the whole point of the tool rather than as an
exception to anything.

Backups behave as they do everywhere: a `patchfile` call inside a fork writes
its copy to the session's `backups/` under the fork's name, and `--rm` takes
the directory with it at the end of the run.

## 12. `--verbose`

**`preamble` does not change.** It says what the run's own agent was given and
stops there, and a fork is not an exception to that. An earlier draft of this
specification had it grow a third `agentSetup` fed by a `forkPreview`,
alongside the `subagentPreview` that existed at the time. Both of those have
since gone, and the reason they went is the reason a fork must not bring one
back: a preview describes an agent that has not been spawned, which means
describing a hypothetical — and what it showed was an agent this run may never
have. The account of a spawned agent is now printed by the spawned agent, where
there is one to describe.

**A fork says what it was given as it starts**, exactly as a subagent does,
through the same `describeAgent` both go through:

```go
// forkIntro is what a --verbose run says about a fork as it starts: which
// agent this is, the prompt it will read, and the tools it may call.
//
// It is the counterpart of subagentIntro and deliberately not the same
// function. A subagent's block repeats the title the tool already announced,
// because several blocks are written at once and each has to be complete on
// its own; a fork's does not, because nothing can come between the
// announcement of the call and this block, and the task is on the line above.
// What the two share is describeAgent, which is the part that is the same.
func forkIntro(name string, agent agentSetup, explained map[string]string) string
```

The name is the head of the block and the only thing above the prompt, because
the name is the one piece of new information: `fork-1` is what the fork's lines
in the log and the copies its tools keep are filed under, and nothing else on
screen says it.

`explained` comes from `explainedBy` over the registry the fork tool was
registered in, which is the run's own — the same self-reference
`subagentRunner` already uses and `defaultRegistry` already documents as not
being the circle it looks like. So a tool the user read about in the preamble
is named rather than explained again, and what stands out is the one thing the
list is for: which tools this agent has, and which two it does not.

Inside a fork the pattern repeats one level down. `forkRegistry` hands its own
registry to the subagent runner it builds, so a subagent spawned by a fork
compares against the fork's listing rather than the run's. That is the more
recent of the two the user has read, and it needs no special case: it is what
`defaultRegistry` does, in the function `defaultRegistry` is now built from.

**None of this is the run's progress stream.** The intro block is `--verbose`
material, printed once. A fork's ordinary progress is marked and streamed
whether or not `--verbose` is on, per section 7, which is the thing that really
distinguishes a fork from a subagent on screen.

## 13. Changes by file

| File | Change |
| --- | --- |
| `prompt/native-fork.md` | New. The guidance in 6.3. |
| `prompt/native-subagents.md` | Replace the second paragraph, per 6.2. |
| `prompt/prompt.go` | Embed and expose `Fork()`. |
| `tools/native/fork.go` | New. `ForkTool`, `Fork`, `ForkBrief`, `ForkRunner`, `ForkParams`, `Definition`, `SystemPrompt`, `Announce`, `ResultWith`, and a `Call` that refuses. |
| `tools/native/subagents.go` | Take a shared `*Counter` instead of owning `spawned`. |
| `tools/native/counter.go` | New. `Counter`, per section 9. |
| `tools/native/loadskill.go` | `NewLoadSkill(found, loaded)`, and `Loaded()`. |
| `tools/native/native.go` | Package doc: name `fork` among the native tools. |
| `tools/tools.go` | New `Conversational` interface and `Fork` function type, preferred by `Registry.Answer`, which gains a `Fork` parameter. Export `SingleLine`, moved from `tools/workspace/display.go`, now that two packages announce a one-argument call the same way — the argument that put `Truncate` there. |
| `tools/workspace/display.go` | Use `tools.SingleLine`. |
| `model/conversation.go` | `Fork(system, requestID, instruction string) *Conversation`, and the two fixed texts beside the existing ones, per 4.2. |
| `eventlog/event.go` | `Fork bool` after `Agent`; `ForkStart` and `ForkFinish` kinds. |
| `eventlog/eventlog.go` | `Log.fork`; `Log.Fork(name)`; `Agent` preserves the flag; `Record` stamps it. |
| `cli/agent.go` | `wiring`, per 14.4; `forkRegistry` with `defaultRegistry` built on it; `forkRunner`, `forkName`, `forkInstruction`; `converse` passes `conv.Fork` to `registry.Answer`; `forkRunner` and `subagentRunner` stop a `Termination` escaping, per section 10. |
| `cli/output.go` | `output.nested()` and the marking writer, per section 7. |
| `cli/verbose.go` | `forkIntro`, beside `subagentIntro` and through the same `describeAgent`, per section 12. `preamble` and `agentSetup` are unchanged. |
| `AGENTS.md` | Per section 16. |

## 14. Structure

### 14.1 The problem the fork tool creates

A run builds three things, and each one needs the one before it:

1. the **registry**, because a system prompt is assembled from the tools that
   are registered — `systemPromptFor(base, registry, skillsSection(found))`;
2. the **system prompt**, because a conversation is started from one —
   `sess.Resume(system, prompt)`;
3. the **conversation**, which is then what the agent loop works on.

The `fork` tool lives in the first of those and needs the third. That is a real
ordering cycle and not an inconvenience of where the code happens to sit: it
would still be there if `defaultRegistry` moved into `runSession` beside the
other two, because the prompt genuinely is built from the registry and the
conversation genuinely is built from the prompt.

Nothing hands it over at call time either. `tools.Tool.Call` takes a context
and arguments; `tools.Resulter.Result` adds the request id and nothing else.

### 14.2 The answer: an optional interface

The registry already solves this shape of problem four times over. A tool that
can explain itself implements `tools.SystemPrompter`; one with more than a
string to report implements `tools.Resulter`; one with something to say before
or after a call implements `tools.Announcer` or `tools.Reporter`. In each case
the base contract stays as small as it is, the registry prefers the richer
interface when a tool opts into it, and a tool that wants nothing extra is
untouched.

A tool that needs the run's state is the same bargain one step further:

```go
// Fork is the signature of model.Conversation.Fork: it returns a conversation
// a copy of the run can continue from, leaving the original as it was.
type Fork func(system, requestID, instruction string) *model.Conversation

// Conversational is a tool that cannot answer from its arguments alone,
// because what it does depends on the conversation it was called in.
//
// It is handed a function that forks that conversation rather than the
// conversation itself. A tool of this kind has a reason to branch from the
// record and none whatever to write into it: appending to a conversation is
// the agent loop's job, and a tool that could do it as well would be a second
// place the record is written.
//
// Registry.Answer prefers this over Resulter and Resulter over Call, so a tool
// opts in by implementing it and every other tool is unaffected.
type Conversational interface {
	ResultWith(ctx context.Context, fork Fork, requestID string, args json.RawMessage) (model.ToolAnswer, error)
}
```

`Registry.Answer` gains a `Fork` parameter, and `converse` — the only caller
that runs tools — passes `conv.Fork`. The function is the method itself, so
there is no closure to write and nothing between the loop and the tool holds a
conversation, the registry included. An earlier draft had `Answer` take the
`*model.Conversation` and build the closure; it only ever relayed it, and a
registry that holds a conversation it makes no use of is a registry something
will eventually reach through.

The cycle disappears because nothing is captured early any more. The fork tool
is complete the moment it is constructed, exactly like `Subagents` with its
`Runner`, and what it was missing arrives at the only moment it could be
correct: when the call is made.

`native.ForkRunner` is then shaped like `native.Runner`, with a brief rather
than a handful of positional arguments:

```go
// ForkBrief is one fork's assignment as whatever runs it sees.
type ForkBrief struct {
	// Task is the instruction to carry out, as the model wrote it.
	Task string
	// Number names this fork within the run, from one.
	Number int
	// RequestID is the call that asked for the fork, which the copy has to see
	// answered before it can continue.
	RequestID string
	// Fork makes the conversation the copy continues from.
	Fork tools.Fork
}

type ForkRunner func(ctx context.Context, brief ForkBrief) (string, error)
```

The name is `ForkBrief` and not `Brief` because `Brief` belongs to subagents
already. Renaming that for symmetry would churn working code and the document
that describes it, which is a worse trade than an asymmetric pair of names.

### 14.3 Why not the alternatives

**A reference held in `cli`, set once before the loop starts.** This was the
first recommendation and it is worse. It leaves a value that is nil between the
registry being built and the conversation being made, which is a window in
which the tool exists and cannot work; it puts a rule — *set this before the
loop starts* — in a place no compiler checks; and it spreads across two
functions, since `runPrompt` builds the registry and `runSession` makes the
conversation. The optional interface has no window, no rule to remember, and no
extra parameter threaded between functions.

**A setter on the tool, `fork.Use(conv)`.** The same hazard with a shorter
reach. It makes a native tool that is constructed half-built, which is the
thing `NewSubagents` and `NewLoadSkill` are careful not to be.

**A sentinel error carrying the task, handled by `converse`,** in the manner of
`native.Termination`. Rejected on meaning rather than mechanism. `Termination`
is a sentinel because terminate aborts the loop, which is control flow a tool
cannot perform for itself. A fork aborts nothing and returns a value like every
other tool, so giving it terminate's shape would say the two are alike when
they are not. It would also grow `converse`'s signature for every agent,
including the subagents that can never take the branch.

**Passing the conversation itself rather than a `Fork` function.** It works,
and it hands a tool the power to append to the record. The function costs one
line and removes the question.

### 14.4 One value instead of seven arguments

This is the largest piece of tidying the feature forces, and it is
signature-only: nothing it changes is visible to a user.

`defaultRegistry` already takes seven arguments — the output, the backend, the
log, the backup directory, the user's own tools, the run's skills, and the turn
limit — and passes almost all of them straight to `subagentRunner`, which takes
seven of its own. This feature adds two more that both of them need: the
counter that names subagents, and whatever `forkRunner` needs, which is the
same list again. Four functions would then thread eight values, of which seven
are always the same seven, from `runPrompt` down through `runSession`.

That is worth collapsing on its own, but there is a second reason. The claim
this feature rests on is that a fork and a subagent are given the same things
minus what each may not have. Four argument lists that happen to match is the
weakest possible way to hold that true; the next tool added is the one that
gets passed to three of them and forgotten in the fourth.

```go
// wiring is what a run's tools are built from: where its progress goes, the
// backend they talk to, the log they record under, where they keep copies of
// files they change, the tools the user brought, the skills the run was given,
// how many turns an agent may take, and the counter that names the agents it
// spawns.
//
// One value passes through everything that builds a registry or starts an
// agent, so the run, a fork, and a subagent cannot be given different things
// by accident — which is the same reason there is one workspaceRegistry rather
// than a list per agent.
//
// What is deliberately not in it is the registry. A runner is handed the
// registry it should compare its verbose listing against, and which registry
// that is differs by caller: the run's own for a subagent the run spawned, the
// fork's for one a fork spawned. Folding it in here would make that one answer
// for both, and the wrong one for the second.
type wiring struct {
	out      output
	client   transport.Backend
	log      *eventlog.Log
	backups  string
	user     []tools.Tool
	found    []skills.Skill
	maxTurns int
	agents   *native.Counter
}
```

`wiring` is this repository's own word for the job: `cli` is described as where
command wiring lives, and `main.go` as what wires the CLI.

`runPrompt` builds one and passes it down. `defaultRegistry`, `forkRegistry`,
`forkRunner` and `subagentRunner` each take it in place of their seven or eight
arguments, plus whatever is genuinely their own: the registry to compare a
verbose listing against, and, for `forkRegistry`, the agent name and the
already-loaded skills. `providedNames` passes a zero value with a throwaway
counter, as it already passes `quiet()` and a throwaway registry.

### 14.5 What was left alone deliberately

**`cli.terminate`'s two calls.** It does `AddToolResult` and then
`AbandonToolRequests`, which is the same pair `Conversation.Fork` now performs,
and a shared method could express both. It is not extracted here. The
`run.terminate` line it records carries `Messages: conv.Len()`, taken between
the two calls, so merging them would change a number in the log for a reason
that has nothing to do with forking. If a third caller ever wants the pair, it
should be extracted then, and the log line's meaning settled deliberately
rather than as a side effect.

**`forkRegistry` as the name of the shared list.** It names the derived agent
rather than the contents, which reads slightly backwards for a list the run
itself is built from. It is kept because it is true — it is the registry a fork
gets — and because a vaguer name covering both would be a name that says less.

## 15. Tests

### 15.1 What the feature does

- The fork's system prompt is deterministic: the same arguments twice give the
  same prompt, as the existing test asserts for a run.
- The copy has no outstanding tool requests: `PendingToolRequests` is empty and
  `Validate` passes, both when `fork` was the only request and when it was
  requested alongside another tool.
- A sibling request in the copy is a `ToolFailure` with the not-made reason,
  while the parent's own conversation still has it pending.
- The task arrives as a `HarnessInstruction` and not a `UserMessage`.
- `loadskill` in a fork refuses a skill the parent had already loaded, and
  accepts one it had not.
- `Loaded()` returns names in the order the skills were found, whatever order
  they were loaded in.
- Subagent numbering does not restart inside a fork: a run that spawns two
  subagents, then forks, then spawns two more, names them 1 to 4.
- Every event recorded inside a fork carries `fork: true`, including those
  recorded by a subagent the fork spawned; none of the parent's do.
- `fork.start` and `fork.finish` bracket the fork's lines.
- A call with an array, an object, or an empty string for `task` is refused
  with a message naming what was wrong.
- A fork interrupted midway leaves the parent's conversation resumable, with
  the fork request answered as a failure.
- A fork that fails becomes a `ToolFailure` and the parent's run continues.
- `preamble` says nothing about forks, whether or not `fork` is registered: it
  describes the run's own agent and stops.
- `forkIntro` is printed once as a fork starts, only under `--verbose`, and
  names a tool rather than explaining it again when the preamble already
  described it identically.
- `Registry.Answer` still answers a plain `Tool` and a `Resulter` unchanged now
  that it takes a `Fork`, and prefers `Conversational` over both.
- What `Fork` returns is a conversation of its own: appending to it leaves the
  conversation it was made from exactly as it was, turn count included.
- `Fork.Call` refuses with a message naming why, so the wart of section 5 is
  asserted rather than discovered.
- The marking writer puts one marker on a line that arrives in several writes,
  and none on a trailing newline.
- A subagent spawned inside a fork compares its listing against the fork's
  registry, not the run's, so `fork` and `terminate` are not among the tools it
  is told were "explained above".

### 15.2 The tree, asserted rather than assumed

Section 3 says every constraint on the tree is enforced by registration, and
that is true. It is still worth asserting, because a registration line is one
line and nothing about deleting it fails loudly.

The tree is one test rather than several. Start from `defaultRegistry`, and for
every registered tool that can start an agent, build the registry that agent
would get and recur. Assert the whole shape at once:

| Agent    | Has                                  | Does not have                 |
| -------- | ------------------------------------ | ----------------------------- |
| main     | work tools, `subagents`, `fork`, `terminate`, `loadskill` where the run has skills | — |
| fork     | work tools, `subagents`, `loadskill` where the run has skills | `fork`, `terminate` |
| subagent | work tools, `loadskill` where its task named skills | `fork`, `subagents`, `terminate` |

Two properties fall out of that walk and are what the test is really for: no
path through it is longer than main → fork → subagent, and it terminates. A
future edit that registers `fork` inside `forkRegistry` fails on both.

Alongside it:

- A fork's registry is exactly `defaultRegistry` minus `fork` and `terminate`,
  asserted by comparing names, so the two lists cannot drift.
- A subagent spawned inside a fork has exactly the tools of one spawned by the
  run, given the same task skills. Being inside a fork must buy a subagent
  nothing.
- The assembled system prompt of each agent contains the guidance of exactly
  those of its registered tools that implement `SystemPrompter`, and nothing
  else. This is the prompt half of the same rule, and it is what catches a fork
  built from a copied prompt rather than a rebuilt one: such a fork would read
  about `fork` and `terminate` while holding neither.
- A run given no skills produces no `loadskill` at any level, and its
  `subagents` tool offers no `skills` field, in the fork as in the run.
- A user-defined tool named `fork` is refused, with the message worded for
  somebody looking at a manifest — and it is refused in a run that was given no
  skills as well, since the reserved names are agi's whether or not this
  particular run registers the tool. `Registry.Register` refuses it too, as the
  backstop.

### 15.3 Invariants that registration does not protect

These are the interesting ones. Each holds today for a reason other than a
missing tool, so each can be broken by an edit somewhere else entirely.

- **A control-flow sentinel must not escape a nested agent.** `converse`
  inspects the error a tool returned with `errors.As(err, &end)` and ends the
  whole run if it is a `*native.Termination`. A fork's error travels that exact
  path: `converse` inside the fork, to the runner, to `Fork.ResultWith`, to
  `Registry.Answer`, to the parent's `converse`. So a `Termination` arising
  inside a fork would end the run rather than the fork. It cannot arise today
  because `terminate` is not registered there — which means this invariant
  rests on one line in `forkRegistry`, with nothing at the place it matters
  saying so.

  So `forkRunner` converts a `*native.Termination` it receives into an ordinary
  error, and the test hands it a stub that returns one and asserts the parent's
  run continues. `ErrInterrupted` must still pass through untouched, since the
  parent is meant to see it, and a second test asserts that it does. The same
  reasoning applies to `subagentRunner`, where it is equally unreachable and
  equally undefended; fixing both is one change.

- **Only one fork runs at a time.** Nothing enforces this but `converse`
  iterating `PendingToolRequests` and running one tool to completion before the
  next. Test two `fork` requests in one assistant turn: they run in the order
  written, and the second's copy contains the first's answer. A second test
  pins the property the first depends on — that the loop runs a turn's tool
  requests sequentially — so that parallelising it later fails here rather than
  silently.

- **A tool a nested agent does not have may still appear in its history.** A
  resumed session can hold a completed `terminate` or `fork` call from an
  earlier run, and a fork copies that history. So a fork can read a call to a
  tool it does not hold. Assert that the copy is built and validates anyway,
  and that a model calling such a name gets a clean "no tool named" refusal it
  can correct, rather than anything worse.

- **The fork flag is per handle and not sticky.** `Log.Fork` sets it and
  `Log.Agent` preserves it, on handles over one shared sink. Assert that a
  subagent spawned by the run after a fork has finished records `fork: false`,
  and that the run's own lines never carry it, so nothing about the flag leaks
  through the sink.

- **Skill state does not leak downward.** A fork inherits the parent's loaded
  set; a subagent spawned by that fork inherits nothing, because its context is
  fresh. Assert both directions: the fork refuses a skill the parent had
  loaded, and the fork's subagent accepts that same skill.

- **A fork's subagent may be given any of the run's skills**, including ones
  the fork itself never loaded, since `available` is the run's list and not the
  fork's. Assert that, and that a name outside the run's list is refused with
  the same message it gets at the top level.

- **Copies are filed under the agent that made them, at every depth.** A patch
  inside a fork writes a backup named with the fork's name; one made by that
  fork's subagent uses the subagent's name; and the run's own subagents keep
  numbering past both, per section 9.

- **A fork does not inherit the schema phase.** A `--schema` run that forks
  records no `schema.restate` under the fork's name: the fork calls `converse`,
  not `runAgent`, and the schema belongs to the run that answers the user.

- **An interruption unwinds all three levels.** Stop a run while a fork's
  subagent is working, and assert the fork's conversation is abandoned, the
  fork's error reaches the parent, the parent's fork request is answered as a
  failure, and the stored snapshot resumes cleanly.

- **A session resumed after a fork is ordinary.** The snapshot holds the fork
  request and a plain `ToolResultMessage`; resuming decodes it with nothing
  special, validates, and continues.

## 16. `AGENTS.md`

The same commit updates the document. The layout table's `tools/native/` row
names `fork`; the conventions gain entries for:

- The tree of section 3, and that every constraint in it is enforced by
  registration rather than by wording.
- A fork's base being the parent's, `--sys` included, against a subagent's
  being fixed — and the reason, which is that a fork is the agent the user is
  talking to and a subagent is not.
- The system prompt being rebuilt and never copied.
- `defaultRegistry` being `forkRegistry` plus two, as the one list.
- The task being a harness instruction because agi is relaying it.
- `fork` returning a plain result, and a failed fork being a `ToolFailure`
  because there is no answer to read.
- The `fork` flag and the two event kinds, and why a fork is not silent on
  screen when a subagent is.
- A fork describing itself as it starts rather than being previewed, which is
  the rule that already removed the subagent preview: a run must not describe
  an agent it has not spawned.
- That a control-flow sentinel must not escape a nested agent, and that
  `ErrInterrupted` is the one that may — the tree is enforced by registration,
  but this is enforced by the runners and needs saying where the rule lives.
- Agent numbering being run-scoped because the names are also filenames.
- `tools.Conversational` as the fourth optional interface a tool may opt into,
  and that it is handed a fork function rather than the conversation because
  appending to the record is the agent loop's job alone.
- `Conversation.Fork` doing the whole close-out, and why the three steps are
  not three methods.
- `wiring` as the one value a run's tools are built from, and that it exists so
  the run, a fork and a subagent cannot be given different things by accident.

## 17. This has to be documented in detail

More than any feature added to agi so far, `fork` will be misread if it is only
implemented. Three different readers each need something the others do not, and
none of them can work it out from the code.

- **The model** needs to know when to reach for it and when not to, which is
  `prompt/native-fork.md` and the tool's `Description`, and needs `subagents`
  to stop claiming a purpose that is no longer only its own. Section 6 is the
  whole of what the model ever reads, and it is the part where being wrong
  costs the most: a model that reaches for a fork by default has made every run
  several times more expensive, silently.
- **The user** needs `AGENTS.md` to say what the tree is, what a fork inherits,
  what it cannot do, and why its progress appears on screen when a subagent's
  does not. A person watching a run will see output from an agent they did not
  ask for, and nothing else will explain it.
- **Whoever changes this next** needs the reasoning, not the rules. Most of
  what is decided here is decided against a plausible alternative — the prompt
  rebuilt rather than copied, the base inherited where a subagent's is not, the
  instruction a `HarnessInstruction` rather than a `UserMessage`, the close-out
  inside `Conversation.Fork`, the tool handed a function rather than the
  conversation, a counter shared for subagents and not for forks. Every one of
  those reads as an arbitrary choice without its reason, and an arbitrary
  choice is one the next person undoes.

So the work is not finished when the tests pass. It is finished when:

1. `AGENTS.md` carries every entry in section 16, in the same commit as the
   code, as the repository already requires.
2. Each new exported thing — `Conversation.Fork`, `tools.Conversational`,
   `tools.Fork`, `native.Fork`, `ForkBrief`, `ForkRunner`, `Counter`,
   `Log.Fork`, the `Fork` event field, the two kinds — carries a doc comment
   that says what it is **and why it is that rather than the obvious
   alternative**. The existing comments on `native.Brief`, `eventlog.Event` and
   `Conversation.DropFrom` are the standard to match: they argue.
3. The unobvious mechanics carry comments where they are, not only here: the
   ordering cycle in 14.1 beside `wiring`; why `Fork.Call` refuses, beside it;
   why a runner stops a `Termination` and lets `ErrInterrupted` through, beside
   the conversion;
   why `Agent` preserves the fork flag and `Fork` sets it; why the marking
   writer is not `indent`; why `AbandonToolRequests` is called on the copy and
   never on the parent.
4. The two rejected designs that will occur to the next reader — the reference
   held in `cli`, and the sentinel error — are written down somewhere durable
   with the reason. This document is durable enough if it is kept; a paragraph
   in `AGENTS.md` is better for the second, since `native.Termination` sits
   right next to it and invites the comparison.
5. `README.md` gains whatever it says about subagents, said about forks, if it
   says anything at all.

## 18. Not in scope

- **Compacting the context.** The tool that summarizes a conversation in place
  is a separate thing and stays the TODO it is. A fork discards a conversation
  it made; it does not shorten one the user owns.
- **A cap on how many forks a run may make.** `maxTurns` already bounds the
  parent, and each fork's own loop is bounded by it in turn. The existing TODO
  about parameterizing `minTasks`, `maxTasks` and `maxSchemaAttempts` is where
  a limit would go if one is ever wanted.
- **`--schema` inside a fork.** A fork runs `converse` and returns prose, the
  way a subagent does. The schema phase belongs to the run that produces the
  user's answer.
- **Resuming into a fork.** A snapshot holds the parent's conversation; there
  is nothing of a fork in it to resume.
