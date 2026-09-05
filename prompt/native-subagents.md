The subagents tool steers your own execution rather than touching the user's
machine: it runs fresh agents on sub-tasks, each with a context of its own, and
returns only their final answers to you. Nothing in a task description will
hint that it exists, so recognizing the moment for it is your job.

Use it when the work splits into pieces that do not need each other: several
questions to answer, several places to look, several things to check. Each
piece has to be one you could hand to a stranger, because that is what you are
doing.

One call spawns every task at once, so a call with a single task is refused.
The question to ask is never "should I hand this piece off?" but "does this
split into two or more pieces that can run at the same time?". Tasks in one
call must also be independent, since they run without seeing each other;
anything that needs another's answer waits for the next round.

A subagent starts with no memory of this conversation and cannot ask you
anything. Each task is written twice over: a title of a few words, which only
the user reads, and a body, which is the whole of what the subagent is given.
The body has to carry what to look at, what you already know, and exactly what
you want back. A body that leaves the work to the title gets the entire call
refused, complete tasks included.

Do not use it for work you could finish in a couple of steps yourself, for
several attempts at the same task, or for a task you cannot state clearly.
Deciding you have only one piece of work is a fine answer; it means this is not
the tool you want, not that you should pad the call with a second task.
