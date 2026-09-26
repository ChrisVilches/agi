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
- **The user interrupts.** Ctrl-C cancels the run. The loop checks for this
  before each turn, before each tool call, and after a failed request, so it
  stops promptly rather than only when a request fails.
- **The turn cap is reached.** `--max-turns` bounds the run, defaulting to 200,
  and reaching it fails with no answer.
- **Something fails.** A request the backend could not complete ends the run.
  A tool that refused a call does not: the refusal is handed back to the model
  as a `ToolFailure` and the loop carries on.

With `--schema`, the loop still ends on a prose answer. Only afterwards is the
model asked to restate that answer as JSON, up to ten attempts; that phase is
not part of the loop.

## When a fork is on-going

A fork is a copy of the run, started from the conversation as it stands, given
one task. Only its final answer comes back; everything else it did is thrown
away. Only one fork runs at a time, and the agent that asked is blocked while
it works.

**What native tools can be executed?**

`subagents`, and `loadskill` when the run was given skills. That is the run's
own set minus two:

- No `fork`, because a fork may not fork. This keeps the tree three levels deep
  at most.
- No `terminate`, because a discarded attempt must not be able to kill the run
  that ordered it. A fork that cannot do its task says so in prose.

Nothing in any prompt says this. It is settled purely by which tools were
registered.

**What skills can be executed?**

All of the run's skills, exactly as the run has them. A fork also inherits
which skills were *already loaded*, because the text of a loaded skill is
sitting in the copied conversation and asking for it again would fetch a
document the fork can already read.

A fork's system prompt is assembled fresh rather than copied, so it describes
only the tools the fork actually holds. Its base prompt, including anything
`--sys` supplied, is the parent's: a fork is the agent the user is talking to,
copied.

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

What a subagent does *not* get is the native tools — no `subagents`, no `fork`,
no `terminate`. That is the whole of why a subagent cannot spawn subagents.

**The skills available are:** only the ones its own task named. Each task in a
`subagents` call may carry a list of skill names; those names are checked
against the skills the run was given, before anything is spawned, and a name
that matches nothing fails the whole call. The subagent then gets a `loadskill`
tool and a skills section built from just those skills. A task that named none
gets no skills and no `loadskill` tool at all, and reads a prompt that says
nothing about skills.

Nothing is marked as already loaded, since a subagent's conversation starts
empty.

## The three levels, in one picture

```
run        →  may fork, may spawn subagents, may terminate
  fork     →  may spawn subagents
    subagent  →  may do neither
  subagent →  may do neither
```

Every one of those limits comes from which tools were registered, never from
wording in a prompt.
