# agi in a nutshell

A short, human-friendly summary of how a run behaves. It is filled in from the
template in `nutshell-spec-template.md`, and it is deliberately incomplete:
`AGENTS.md` is the full account, and this document only answers the questions
that are asked most often.

## Agent (general)

**When does a loop end?**

The agent loop runs one turn after another until one of these happens:

- **The model answers.** A reply that asks for no tools is the final answer,
  and the loop returns it. An answer that is empty after trimming fails the run
  instead.
- **The model ends the run.** The `terminate` tool stops the run where it
  stands, with no answer on stdout and a non-zero exit status.
- **The user interrupts.** Ctrl-C cancels the run, and the loop stops at the
  next point it checks.
- **The turn cap is reached.** `--max-turns` bounds the run, defaulting to 200,
  and reaching it fails with no answer.
- **Something fails.** A request the backend could not complete ends the run.
  A tool that refused a call does not: the refusal is handed back to the model
  as a failure and the loop carries on.

With `--schema`, the loop still ends on a prose answer. Only afterwards is the
model asked to restate that answer as JSON, up to ten attempts; that phase is
not part of the loop.

## Programming

**What's the concept of "registry" used in the app?**

A registry is the set of tools one agent has, and nothing else decides what an
agent may do. It is `tools.Registry`: a map from a tool's name to the tool,
which refuses two tools of the same name because the model addresses them by
name and could not tell them apart.

Everything about tools goes through it:

- the list of tool definitions sent to the provider, always in name order, so
  the request is stable;
- the system prompt's middle part, gathered from the tools that can explain
  themselves;
- announcing a call before it runs and reporting what it produced;
- running a call and building the turn that answers it.

Each agent gets its own registry, and they are built from one another rather
than written out separately. The work tools are one list, the fork's list is
that plus `subagents` and `loadskill`, and the run's list is the fork's plus
`fork` and `terminate`. This is why "a fork gets the run's tools minus two" is
true by construction: there are no two hand-written lists that could fall out
of step.

## Interruptions

**How can a run be interrupted?**

Ctrl-C. The first one cancels the context that every part of the run is
waiting on, so the request in flight and any command a tool is running stop
where they are. The run then finishes the way it always does: the conversation
is stored, the log is closed, and agi exits 130 instead of 1 so a shell can
tell an interruption from a failure. Signal handling goes back to the operating
system immediately, so a second Ctrl-C kills agi outright and nothing is
stored.

The loop checks for the interruption before each turn, before each tool call,
and after a request that came back with an error, so it stops promptly rather
than only when something happens to fail.

This is separate from the model calling `terminate`, which is the model ending
a run it cannot answer, not the user stopping one.

**What happens when a bash tool call is interrupted?** (for example a long
`sleep`)

The command is killed along with the run. `bash` does not report what it had
printed so far and does not report the exit status a signal leaves behind:
that would describe a command that ran and went wrong, when really the call
could not be made. The pending request is answered in the conversation with a
tool failure reading `error: interrupted`.

**What happens when a fork is interrupted?**

The same. The fork's own loop stops, the fork returns nothing, and the `fork`
request in the parent's conversation is answered with `error: interrupted`.
Whatever the fork managed to do is lost, which is true of a fork that finished
normally as well — only its final answer ever comes back.

**What happens when a subagent is interrupted?**

This one differs, because a subagent failing is not the same as the call
failing. Every subagent stops, and the `subagents` call still returns an
ordinary result: one section per task, in the order the tasks were given, each
reading `This subagent failed: interrupted`. The run then stops on its next
check.

**How is any of it restored?**

The same way in all three cases. Every request left outstanding is answered
with a failure first, because a conversation that moves on while a call is
still unanswered is one a provider may reject. The snapshot is then written —
an interrupted run is stored, unlike a failed one, since everything that was
said really was said. Resuming appends the new prompt as the next user turn
after all of that. Nothing is re-run, so the model sees the interrupted call,
reads that it was interrupted, and decides for itself whether to try again.

## Native tools

**List all the native tools.**

`subagents`, `fork`, `terminate`, `loadskill`.

**When does an agent, subagent, fork (or any variant) get or not get any
native tool?**

```
run        →  subagents, fork, terminate, loadskill
  fork     →  subagents, loadskill
    subagent  →  loadskill
  subagent →  loadskill
```

- A **fork** has no `fork`, so it cannot fork, which keeps only one fork
  running at a time and the tree three levels deep at most. It has no
  `terminate`, so a discarded attempt cannot kill the run that ordered it.
- A **subagent** has no native tool but `loadskill`. That is the whole of why
  it cannot spawn subagents.
- `loadskill` exists only where there are skills to load: the run and a fork
  have it when the run was given skills at all, and a subagent has it only
  when its own task named some.

None of this is stated in any prompt. What an agent may do is settled purely
by which tools were registered for it.

**How are native tools explained in the prompts? In the system prompts, or
simply in the tool array passed to the model provider?**

Both. Every tool, of every kind, reaches the provider in the tool array with
its description. Native tools are explained in the system prompt *as well*,
because a model cannot work out from a description alone when to reach for
something that acts on the run itself.

The explanation only ever reaches a model that holds the tool. Three of them
carry their own text and contribute it when they are registered;
`loadskill`'s explanation is assembled alongside the list of this run's skills,
since the list is not the tool's to write. A prompt describing a tool the
reader does not have would be a standing lie, which is the same rule that keeps
workspace tools and user-defined tools out of the system prompt entirely —
those are described by their tool definition and nothing more.

**Can they be disabled by the user?**

No. There is no flag that removes a tool. The one thing the user affects is
`loadskill`, indirectly: a run given no `--skills` has no skills, so the tool
is not registered and nothing in the prompt mentions skills.

## When a fork is on-going

A fork is a copy of the run, started from the conversation as it stands, given
one task. Only its final answer comes back. Only one fork runs at a time, and
the agent that asked is blocked while it works. Unlike a subagent, a fork is
watched on screen: its progress streams out with a marker showing whose it is.

**What native tools can be executed?**

`subagents`, and `loadskill` when the run has skills. See the table above for
why `fork` and `terminate` are missing.

**What skills can be executed?**

All of the run's skills, exactly as the run has them. A fork also inherits
which skills were *already loaded*, because the text of a loaded skill is
sitting in the copied conversation and asking for it again would fetch a
document the fork can already read.

A fork's system prompt is assembled fresh rather than copied, so it describes
only the tools the fork actually holds. Its base prompt, including anything
`--sys` supplied, is the parent's: a fork is the agent the user is talking to,
copied.

**After a run with a fork finished, a snapshot is created, but when restoring
it, can we see the history inside the fork?**

No. The fork works on a copy of the conversation, and the original is left
exactly as it was; nothing the fork did is written back. What the snapshot
holds is the parent's side of it: the `fork` call, and one tool result holding
the answer the fork returned. Everything in between — the fork's turns, its
tool calls, its reasoning — is gone.

The log is where that history lives. A fork's lines are all marked as being
inside a fork, including those of any subagent it spawned, so a run can be read
back in full there. This is the usual division: the snapshot exists to continue
a conversation, the log to explain one.

## When a subagent is spawned, what does it inherit?

A subagent starts from nothing: an empty conversation, and the task text the
spawning agent wrote for it. Several run at the same time, their answers come
back in the order the tasks were given, and their progress is not shown on
screen — though each one records everything in the log under its own name.

**The system prompt is:** `prompt/subagent.md` instead of the ordinary base.
It tells the agent that its reply is final, read by a machine and not a person,
so it must not ask questions or offer to do more. `--sys` does not reach it:
what `--sys` replaces is the character of the agent the user is talking to, and
nobody talks to a subagent. Apart from the base, the prompt is assembled the
same way as any other — base, then whatever the registered tools explain, then
the skills.

**The workspace tools are:** the same ones, with nothing added and nothing
taken away. `bash`, `mktemp`, `patchfile`, `writefile`. There is one list of
work tools that every agent is built from, so a tool added there reaches a
subagent too.

**The user defined tools are inherited in what way:** in exactly the same way,
because they are part of that same one list. A tool the user brought with
`--tools` is a work tool in every respect, so a subagent gets it for the same
reason it gets `bash`.

**The skills available are:** only the ones its own task named. Each task in a
`subagents` call may carry a list of skill names; those names are checked
against the skills the run was given, before anything is spawned, and a name
that matches nothing fails the whole call. The subagent then gets a `loadskill`
tool and a skills section built from just those skills. A task that named none
gets no skills and no `loadskill` tool at all, and reads a prompt that says
nothing about skills.

Nothing is marked as already loaded, since a subagent's conversation starts
empty.
