# Examples

What is under here is built on agi and is not part of it. Each one is a
program somebody wanted and went on using, kept in the repository because it
is useful and because a short program that composes agi says more about what
agi is like to build on than a page of documentation does.

None of it is production ready, and none of it is held to agi's standards.
These check little, they answer the mistakes their author actually made and
not the ones nobody makes, and they carry no promise to keep working or to
keep their shape. agi does not depend on any of them: delete this directory
and agi builds, tests, and runs exactly as it did.

So read one before you run it — they are short enough for that, deliberately —
and then copy it and change it. A starting point is what each of these is for.

| Path       | What it is                                                                            |
| ---------- | ------------------------------------------------------------------------------------- |
| `agi-repl` | A shell script that runs agi as a repl: one session, a prompt at a time. No Go in it.   |

## agi-repl

`agi-repl` turns one-shot runs into a conversation you sit in front of. It
makes a session directory, and then asks for a line and runs `agi --session
DIR` on it, over and over:

```sh
$ ./agi-repl -v
agi repl — session /tmp/agi-repl.k3Qd1xZP

agi #1> what distribution is this?
...
agi #2 3.1s 8K> and how much free disk does it have?
```

Everything you give the script is handed to agi as it is written, on every
prompt — `agi-repl --enable-terminals --max-turns 400` is that run, each time
round. The exceptions are the three the script answers itself: `--help` is its
own usage, and `--session` and `--rm` are refused, because the session is what
the script is for. Where it puts that session is `TMPDIR` like everything
else, and it is kept when you quit, with the directory printed so the
conversation can be continued or read later.

Each run is an `agi --session DIR`, so none of them names the directory on its
way past: the script printed it when it started and prints it again when it
quits, which is the whole of what you need and twice is enough.

The prompt is magenta, and that colour is the one thing on the screen the
script paints rather than agi. A repl borrows the terminal it was started
from, and a prompt that looks like the shell prompt two lines above it reads
as the shell — the line you type a command at, not a question at a model. It
goes plain wherever the colour would not be seen anyway: `NO_COLOR`, a dumb
terminal, or a prompt that is not going to a terminal at all.

Beside the name are three figures, and they are what the script knows without
asking agi anything. The number is which prompt this is, counting from one; it
wears a `#` so that it is read as a count, since nothing else would tell it
apart from the figures after it — `agi 3 1.2s 12K` offers three numbers and
says what only two of them are. The time is how long the last run took, by the
shell's clock rather than agi's — it is there whether or not the run got far
enough to write anything down. The size is how big the stored conversation has
become, which is the nearest thing to a measure of how full the context is:
agi counts no tokens anywhere, so there is no better figure to print, and the
file is asked how many bytes it is rather than read.

The prompt is that one colour from end to end, figures and all, and a failure
mark is the only thing that breaks the span. Two earlier attempts at setting
the figures apart say why nothing does. Faint was the first, and the attribute
weakens whatever foreground it is given, so beside a bold magenta name the one
part of the prompt that changes became the one part hardest to read. Leaving
them unpainted was the second and was worse in its own way: a prompt written
half in a colour and half in none reads as two things that happened to land on
the same line, where what is wanted is one line that belongs to the script.

A run that ended badly is marked before the angle bracket, because the prompt
is what comes back after a screenful of output has scrolled past and the
ending may have gone up with it. `^C` is a run you stopped, which is an
ordinary thing to do here and so is not drawn as a failure; `!` in red is a
run that failed, with the status after it when it was neither.

The log in the session directory is no part of this. It is agi's own account
of a run, written for someone inspecting one afterwards, and a prompt that
quietly read it would turn a record into an interface — agi could not then
change what it writes there without breaking the script sitting beside it.

The session line the script prints at either end is dim — the same faint agi's
own `--session` line is written in, because it is the same line doing the same
job. It is the only thing the script says about itself before the first
prompt: what Enter does and what Ctrl-C does are what a repl is, and `--help`
has them for whoever goes looking.

Enter sends a line, so there is no multi-line prompt; the arrow keys go back
over what you have asked. Ctrl-C has the meaning it has everywhere else in
agi: during a run it stops that run the way `agi` stops it — the conversation
is stored and the prompt comes back — and at the prompt it quits, as Ctrl-D
does. So quitting in the middle of a run is two presses, one for each.

agi itself knows nothing about any of this. A repl is a directory and a loop
around the command, and the script is the whole of it.

### What it does not handle

The two flags it refuses are matched by name, against a list written by hand.
It glances at the arguments rather than reading them: it does not know that
`--` ends the flags, and it cannot tell a flag from the value of one. Going
further would mean keeping a copy of agi's flags in the script, to go stale
the day one is added, in exchange for catching mistakes nobody makes.
Everything else you mistype is agi's to refuse, on the first prompt and in its
own words — after the session directory has been made and the banner printed.

That is the weight the job is worth in a script this size, and it is the kind
of thing to settle differently if you build on it. The loop at the top is
where you would start.
