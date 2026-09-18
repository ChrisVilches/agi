# MessageDisplay: a plan

**This is a working paper for work not yet done.** Nothing in it is
implemented. `AGENTS.md` says a document of this kind is deleted when the work
lands rather than rewritten into a description of it — so whoever builds this
should fold what survives into `docs/hooks-specification.md` and `AGENTS.md`,
and delete this file in the same commit.

Everything here was settled in conversation with the user. Where a question is
still open it says so.

## 1. What it is, and why it is wanted

A third hook event, `MessageDisplay`, which is handed a message agi is about
to show the user and may hand back different text to show instead. It changes
**what is printed and nothing else**: the conversation, the snapshot and the
event log all keep the model's own words.

The motivating use is a markdown renderer. The model writes markdown — headings,
fenced code, lists — and agi prints it raw. A hook can pass it through `glow`,
or pretty-print a `--schema` document through `jq`, and the terminal shows
something worth reading. The user's own framing: a hook they would activate
only at a terminal.

It applies to **every message agi shows**, not only the final answer.
Intermediate prose the model writes alongside its tool calls is markdown too,
and is worth rendering.

## 2. Which Claude Code hook to imitate

`MessageDisplay`, and it is a close fit — close enough that agi should take
both the event name and the output field rather than invent either.

Claude Code's documentation states the same constraint agi wants, in its own
words:

> `displayContent` replaces the displayed text on screen. Display-only: the
> transcript and what Claude sees keep the original.

So a hook returns:

```json
{
  "hookSpecificOutput": {
    "hookEventName": "MessageDisplay",
    "displayContent": "…rendered…"
  }
}
```

Worth knowing what was **not** verified: the docs consulted described
`MessageDisplay`'s *output* field but not the full shape of its *input*. The
input field carrying the message text is assumed to be `content` below, and
whoever implements this should check Claude Code's current documentation and
match it if it differs. Getting the input name wrong is the one thing here
that would stop a ported script working.

The other events around an assistant message — `Stop`, `SubagentStop`,
`Notification` — receive context and cannot change any text. There is no
Claude Code event that rewrites the answer for the record, which is why the
display-only shape is not a compromise agi is making but the only shape the
thing has anywhere.

## 3. How close it can stay to Claude Code

Very close. The event name, the output field, the exit statuses, the JSON
envelope, the common input fields and the failure behaviour are all
unchanged. The divergences are the ones agi already has, plus one new one.

| | |
| --- | --- |
| Event name | `MessageDisplay`, unchanged |
| Output field | `hookSpecificOutput.displayContent`, unchanged |
| Exit statuses | unchanged: `0` proceeds, anything else is a non-blocking error |
| `continue: false` | unchanged, and still stops the agent |
| `matcher` and `if` | **new divergence — see 6** |
| When it runs | fires whenever agi displays something, and not when it displays nothing — see 5. Whether this is a divergence is unverified: it depends on whether Claude Code fires the event in its non-interactive mode. |

Exit `2` deserves a note. For a tool event it refuses the call; here there is
nothing to refuse, since the message has already been produced. Treat it as a
non-blocking error: the original text prints. Refusing to display a message
would be a hook that can silence the agent, which nothing asked for.

## 4. Where it hooks, and why not in `output`

It is tempting to put it inside `output.step`, and that would be wrong. Every
line agi prints goes through there: `[bash]` announcements, the session line,
hook notes, the verbose preamble. Those are agi's own chrome, already styled,
and passing them through a markdown renderer would mangle them.

Only four sites print model-authored text, all in `cli/agent.go`:

| Site | What it prints |
| --- | --- |
| `converse`, the `thinking` line | prose the model wrote alongside its tool calls |
| `runAgent`, `out.final(answer)` | the final answer |
| `runAgent`, `out.step(answer)` | the prose answer under `--schema`, which is reasoning there |
| `runAgent`, `out.final(document)` | the JSON document under `--schema` |

Those four call one small helper; nothing else changes. **Hooking the print
site rather than the conversation is what makes display-only true by
construction**: the text is already stored by the time the hook sees it, so
there is no path by which it could edit the record. That is a stronger
guarantee than `PostToolUse` has, where the rule is kept by care rather than
by shape.

Subagents need no thought: they run with `quiet()`, so nothing is displayed
and no hook fires. A fork's prose goes through `output.nested`, is rewritten
by the hook, and is then marked line by line on its way out, which is the
right order.

## 5. It runs whenever something is actually displayed

An earlier draft had agi skip this hook whenever stdout was not a terminal,
reasoning that a `glow` rewrite reaching a pipe would put ANSI escapes into a
machine-readable channel. That was over-reach, and the user was right to
reject it: a display hook need not emit ANSI at all. One that redacts
secrets, translates, or trims a long message is worth running into a pipe,
and agi refusing to run it would be agi deciding what the user's hook is for.

**The rule is instead definitional: the hook fires when agi displays
something, and not when it displays nothing.** A piped run without
`--verbose` sends progress to `io.Discard`, so no intermediate message is
displayed and there is no display to hook. The final answer is written
whatever the destination, so it is always hooked. That is not a policy about
ANSI; it is what the event means.

It follows that a hook which emits escape sequences will corrupt a piped
answer. **That is the hook author's business**, exactly as it is their
business that a `bash` tool can delete a file. agi documents it and does not
police it.

### The part that does not work, and has to be said plainly

The user's plan was for the hook to decide for itself with `[ -t 1 ]`. **It
cannot.** agi captures all three of the hook's standard descriptors — stdin
carries the payload, stdout carries the reply, stderr carries the reason — so
inside the hook they are pipes no matter what agi's own streams are. Measured
with agi running on a real terminal:

```
stdin : not a TTY
stdout: not a TTY
stderr: not a TTY
```

So `[ -t 1 ]` is not merely unreliable, it is always false, and a hook written
around it would take the non-TTY branch forever and never render anything.
Silently — which is the failure this project keeps finding and refusing.

Opening `/dev/tty` does not help either: it reports whether a controlling
terminal exists, which it does for `agi | jq` run from a shell, so it answers
a different question.

Walking up to the parent process and asking what *agi's* streams are attached
to does answer the right question, and is not worth building. It is fragile
today rather than only after some future change, because how many processes
sit in between depends on how the hook author wrote their own `command`:

```
exec form              parent is agi
shell, one command     parent is agi   (sh exec's itself away)
shell, a pipeline      parent is sh    ← agi is a grandparent here
shell, a sequence      parent is agi
```

A hook that climbs one level is right three times out of four and reads the
wrong process the fourth, without saying so. **agi will not do this work, and
will not recommend it.** Anybody determined to try is welcome to experiment
in their own hook; what this document records is that it was considered and
is not the supported answer, so that the next reader does not rediscover it
as a clever idea.

That leaves three honest positions, and the first is taken:

1. **agi tells the hook nothing, fires whenever it displays, and the hook
   assumes it is writing for a terminal.** Closest to Claude Code, adds no
   field, and is what section 3 records.
2. agi adds something like `"tty": true` to the payload. Rejected: Claude
   Code has no such field, and the whole point of this event is to stay close
   enough that a ported script works.
3. agi decides and skips. Rejected above.

**So a display hook may simply assume a terminal**, and should. There is no
detection to write, no branch to get wrong, and nothing agi could tell it that
Claude Code would also tell it. A renderer is written as though somebody is
watching, because that is the only case it is for.

The corollary is the user's and is stated rather than softened: **turning the
hook off for piped work is the user's job**, and a run that pipes with a
renderer still armed gets escape sequences in its output. That is the same
bargain every other hook makes — a `PreToolUse` hook can rewrite a command
into something ruinous, and agi runs it — and it is why the choice belongs per
run rather than per message. In practice that is a second `--resources` folder
holding the renderer, named only when watching, or the `disabled` field
planned for entries, which is the tidier answer and an argument for building
it.

**Worth checking at implementation time**: whether Claude Code fires
`MessageDisplay` at all in its non-interactive mode. If it does not, agi's
definitional rule matches it exactly and the divergence table gains nothing.
This was not verified and should not be assumed either way.

### What this does to `--schema`

The earlier draft claimed the terminal rule settled it. It does not, so the
question stands on its own: a hook may rewrite the JSON document, and under
rule 1 it may do so into a pipe. A hook that pretty-prints with `jq` leaves
valid JSON and nothing downstream notices. A hook that renders it with `glow`
produces something no longer parseable, in the one place agi promises plain
data.

That is the same author's-responsibility line as everywhere else in this
section, and it is where the cost of rule 1 actually lands. It is worth one
sentence in the README beside the `--schema` documentation, because the person
who trips over it will be piping to `jq` and will not connect the two.

The example script in section 7 keeps its `jq`-or-`glow` branch for a reason
unrelated to terminals: a `--schema` document and a prose answer want
different rendering, and the content says which is which. What it does not
have, and should not grow, is a branch on whether anybody is watching.

## 6. `matcher` and `if` have nothing to match

Both are defined over a tool's name and its named parameters, and a
`MessageDisplay` hook has neither.

**Recommended: refuse both when the file is read**, with a message saying
that this event takes no matcher and no conditions. The alternative — quietly
ignoring them — produces the dead-hook silence that `refuseDeadMatchers` was
written to stop, one layer along.

Matching on the message text itself was considered and rejected. The content
already tells a script everything it needs, and a regular expression deciding
whether a renderer runs would be a second, weaker copy of the branch the
script is going to write anyway.

## 7. The payload

Common fields, plus the message:

```json
{
  "hook_event_name": "MessageDisplay",
  "content": "## Findings\n\nThe disk is nearly full…",
  "cwd": "/home/you/project",
  "session_id": "9b3e07c14f2a1c8d",
  "agent_id": "main"
}
```

**There is deliberately no field saying which of the four messages this is.**
An earlier draft had one, on the theory that a script could not otherwise tell
a `--schema` document from prose that happens to contain JSON. That was wrong:
a fenced block contains its backticks, so it never parses as JSON and falls
through to the markdown renderer — which then renders the fence as a fence,
correctly. The content is self-describing, and the branch a script writes is
two cases:

```sh
#!/bin/sh
content=$(jq -r '.content')
if printf '%s' "$content" | jq -e . >/dev/null 2>&1; then
  printf '%s' "$content" | jq .
else
  printf '%s' "$content" | glow -
fi
```

The only message both branches could disagree about is one that is a bare JSON
document with no prose around it, which is either a `--schema` document or a
model that answered with nothing else. Prettifying it is right either way.

A field agi invented would also be a field a ported Claude Code script does
not know to read, which is reason enough on its own.

**Whether `glow` and `jq` are installed is the hook's business, not agi's.**
agi does not look for them, does not mention them, and has no opinion about
what a display hook shells out to.

## 8. What the `input` struct needs

`hooks.input` is unexported and built in exactly one place, `Set.encode`, so
this costs nothing to any caller:

```go
type input struct {
    HookEventName Event  `json:"hook_event_name"`
    CWD           string `json:"cwd,omitempty"`
    SessionID     string `json:"session_id,omitempty"`
    AgentID       string `json:"agent_id,omitempty"`

    // tool events only
    ToolName   string          `json:"tool_name,omitempty"`
    ToolInput  json.RawMessage `json:"tool_input,omitempty"`
    ToolUseID  string          `json:"tool_use_id,omitempty"`
    ToolOutput *payload        `json:"tool_output,omitempty"`

    // MessageDisplay only
    Content string `json:"content,omitempty"`
}
```

One struct where the event name says which fields mean anything, which is the
bargain `eventlog.Event` already makes and defends. `ToolName` and `ToolInput`
gain `omitempty` here — they do not have it today, because both current events
always set them, and without it a `MessageDisplay` payload would carry
`"tool_name": ""`.

A struct per event was considered. It is the wrong trade for a wire format
whose reader switches on `hook_event_name` regardless, and it would be two
types to keep in step for one field each.

## 9. The one thing that must be fixed first

`cli.refuseDeadMatchers` walks every hook in the set and checks its matcher
against the run's tool names. A `MessageDisplay` hook has no tool. One written
without a matcher passes by luck — `Hook.Watches` returns true for an absent
matcher — but one written *with* a matcher is refused, and the message talks
about tools, which has nothing to do with anything.

**Make that function event-aware before adding the event**, not after. It
looks fine until the first person writes a matcher on the new event, and then
it produces a refusal nobody can act on. If 6 is taken — matchers refused
outright for this event — then the two checks meet: `refuseDeadMatchers`
considers only tool events, and a matcher on a `MessageDisplay` hook is
already gone by the time it runs.

## 10. The shape of the work

1. **`cli`**: make `refuseDeadMatchers` consider only tool events. On its own,
   with a test, before anything else.
2. **`hooks`**: the `MessageDisplay` constant, the `events` slice, `omitempty`
   on the two tool fields, `Content` on `input`, and the refusal of `matcher`
   and `if` for this event when the file is read.
3. **`hooks`**: `func (s *Set) OnDisplay(ctx context.Context, content string)
   (string, Report, error)` — the third entry point, beside `Before` and
   `After`. It reuses the whole of the sequencing: hooks run in order, each
   sees what the last one left, `Report` and `Ran` are unchanged, a
   non-blocking error leaves the text alone.
4. **`cli`**: a helper the four print sites call, which returns the text
   unchanged when the set holds no such hook, or when the text is going
   nowhere. `output` already knows the second — `steps` is `io.Discard` in a
   piped run without `--verbose` — but knows it by comparison rather than by
   saying so, and a bool set in `route` would be the honest spelling. That is
   not about terminals: it is so agi does not spawn a renderer for text
   nobody will read.
5. **Documentation**: fold sections 2, 3, 5, 6 and 7 into
   `docs/hooks-specification.md`; add the conventions to `AGENTS.md`; add the
   event and its divergences to both divergence tables and the README. Delete
   this file.

## 11. Tests worth having

The ones that would otherwise rot silently:

- the record is untouched — the conversation and the log hold the model's
  words after a hook rewrote the display
- no hook runs for a message that is displayed nowhere: a piped run without
  `--verbose` discards its progress, and agi should not spawn a renderer for
  text nobody will read
- the final answer **is** hooked when piped, since it is displayed — the
  counterpart of the line above, and the one most likely to be "fixed" by
  somebody who remembers the rejected terminal rule
- a hook that fails, or is not installed, leaves the original text printed
- several hooks chain, each seeing the last one's output
- `matcher` or `if` on a `MessageDisplay` hook is refused when the file is read
- a `MessageDisplay` hook without a matcher is not refused by
  `refuseDeadMatchers`

Each of these should be checked by breaking the code and watching it fail, the
way the ordering and dead-matcher tests were. A test that cannot fail is not
one.
