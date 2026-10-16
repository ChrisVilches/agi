# Examples

What is under here is built on agi and is not part of it. Each one is a thing
somebody wanted and went on using, kept in the repository because it is useful
and because a short, working example says more about what agi is like to live
with than a page of documentation does. Most are programs; one is a
configuration file for something agi talks to, and one is a folder to point a
run at.

None of it is production ready, and none of it is held to agi's standards.
These check little, they answer the mistakes their author actually made and
not the ones nobody makes, and they carry no promise to keep working or to
keep their shape. agi does not depend on any of them: delete this directory
and agi builds, tests, and runs exactly as it did.

So read one before you run it — they are short enough for that, deliberately —
and then copy it and change it. A starting point is what each of these is for.

| Path                   | What it is                                                                            |
| ---------------------- | ------------------------------------------------------------------------------------- |
| `agi-repl`             | A shell script that runs agi as a repl: one session, a prompt at a time. No Go in it.   |
| `litellm_config.yaml`  | A LiteLLM gateway for agi to talk to: four routes worth copying, and the command to run it. |
| `glow-display/`        | Two `MessageDisplay` hooks that draw agi's output through `glow`: the answer, and the deliberation behind it. |

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

Enter sends the prompt and only Enter does; the arrow keys go back over what
you have asked.

That distinction is the one piece of the script that is more than a loop
around a flag, and it is there for pastes. A paste of several lines is one
prompt with newlines in it: the newlines are the shape of what was copied, not
you pressing Enter, so a paste sits at the prompt — ending in a newline or
not — until you send it, and until then it can be edited or added to like
anything you typed. What makes that possible is `read -d`, which ends the line
at the carriage return the Enter key sends rather than at the first newline;
read to the newline, a pasted block becomes one prompt and a handful of lines
answering prompts nobody meant to answer. Bracketed paste is the other half,
and it is readline's own: the terminal says where a paste begins and ends, so
the block goes into the line being edited without a keypress being run out of
it, and a tab in a pasted line stays a tab rather than completing a filename.

A line typed while a run is working is the one exception, and it sends on its
own as it always has. The terminal is agi's then rather than readline's, and
the kernel has turned that Return into a newline before the prompt comes back,
which leaves nothing to tell it from one. For a newline you mean to type, at
the prompt, Ctrl-V Ctrl-J puts one in.

Ctrl-C has the meaning it has everywhere else in agi: during a run it stops
that run the way `agi` stops it — the conversation is stored and the prompt
comes back — and at the prompt it quits, as Ctrl-D does. So quitting in the
middle of a run is two presses, one for each.

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

## litellm_config.yaml

A LiteLLM gateway is one endpoint with several providers behind it, speaking
the protocol agi already speaks. agi needs nothing from this file — it sees a
URL and a key, and `AGI_BACKEND=litellm` is the whole of what it knows — so
this is here as a working starting point rather than as something agi reads.

The file carries the `docker run` that starts it, and four routes worth
copying. Three of them are ways of publishing a model, which is really the
only decision a gateway asks you to make:

- **one model under a name you chose**, where `model_name` is what clients ask
  for and `model` is where it goes, so a short local alias can front a long
  provider name;
- **a whole provider through a wildcard**, so a model the provider adds appears
  after a restart with no edit here;
- **a whole provider that model discovery cannot reach**, declared as an
  OpenAI-compatible endpoint so that it can.

That third one exists because of a trap worth knowing before you trust a
listing. A wildcard is a *routing* rule — "anything after this prefix goes
here" — and says nothing about which models exist. What `/v1/models` reports
for one is answered separately, by default from LiteLLM's built-in price map:
a static file that has never heard of your account, so the listing can
advertise models your key cannot reach while omitting ones it can.
`check_provider_endpoint: true` fixes that by asking the provider instead, but
only for providers LiteLLM has a model-info class for; anything else keeps the
static map however you set it.

Which is also why `agi models` is a guide and not a guarantee, and why agi
never checks a model name against it — see **Choosing the model** in the main
README. Copy a name from that listing and it is always spelled the way the
backend wants; whether it will answer is the endpoint's to say.

The fourth pattern is not about publishing a model but about getting a
parameter to one. `kimi-k2.6` returns its own reasoning and then ignores the
reasoning handed back to it: `thinking.keep` decides whether earlier turns'
reasoning becomes context, and it defaults to null, so agi returning all of it
buys nothing. `keep: all` is the fix, and `allowed_openai_params` is what gets
it past the gateway — LiteLLM's Moonshot adapter does not recognize
`thinking`, and `drop_params` discards an unrecognized parameter in silence
rather than refusing it. That silence is the part worth carrying away: a route
can be right in every other respect and still lose the one setting the model
needed.

## glow-display

agi prints what the model writes exactly as the model wrote it, which means
markdown: headings as `#`, emphasis as asterisks, code inside fences. That is
the right default — it is the text, and nothing has been decided for you — but
it is not what anybody wants to read for an hour. `glow-display/` is two hooks
that draw it instead, and it is one suggestion of what to do about that rather
than the suggestion. Nothing here is required and agi is complete without it.

It is a `--resources` folder rather than files to scatter, so there is nothing
to assemble:

```sh
$ agi --resources examples/glow-display "explain the borrow checker"
```

```
glow-display/
├── hooks.json      the two stanzas
├── message.sh      what the model wrote for you
├── thinking.sh     what it worked through to get there
└── thinking.json   the style thinking.sh draws in
```

`glow` and `jq` have to be on the path. They are the hooks' business and not
agi's — agi runs the program `hooks.json` names and reads back what it says —
and a renderer that is missing, that hangs, or that exits badly leaves the
message printed as the model wrote it and tells you what went wrong. The run
is never at risk from any of this: a display hook changes what is printed and
nothing else, and the conversation, the session and the log have all stored
the model's own words before the hook is handed a copy.

### Two hooks, because there are two streams

A thinking model returns the reasoning behind a turn as well as the turn, and
a display hook renders one of the two. `message.sh` has no `thinking` field
and so sees the prose and never the deliberation; `thinking.sh` asks for the
deliberation with `"thinking": true` and sees nothing else. Wanting both drawn
is wanting two hooks, and that is the shape rather than an inconvenience of
it: a renderer armed for your answers is never started over the model talking
to itself, and neither script has to begin by working out which of the two it
just received.

Neither needs to be told to stay out of a pipe. agi runs display hooks only
when its output is a terminal, so `agi > file` gets the model's own words and
no escape sequences, and `agi ... | glow -` is how a piped answer is rendered.
A piped `--verbose` run lists both hooks marked `(off in this run)`, so a
renderer that did not fire says so rather than leaving you to wonder.

### Arming the thinking hook hands you the whole look

With no thinking hook agi prints the deliberation faint, because nothing else
was going to. With one, that dimming is dropped whole — agi will not lay its
own styling over text a hook has already styled, which would mean laying it
over escape sequences — so everything the block looks like is `thinking.sh`'s
from that moment, dimming included.

Which is why `thinking.json` dims the deliberation itself. It is dracula with
two values changed: `document.color` and `list.color` are `#6272A4`, the
palette's own colour for comments and for `hr`, so the body text recedes while
every accent keeps full strength and a heading is still bold purple, `**bold**`
still orange, `` `code` `` still green. Those two lines are the whole
difference from upstream, and they are the knob to turn if it reads too dark
or not dark enough.

It dims by colour and not by the faint attribute because there is no faint to
be had. glamour accepts a `faint` field in a style file and silently emits
nothing for it — `italic` emits its SGR, `faint` emits none — and an escape
smuggled in through a block prefix is closed by glamour's own reset before it
reaches any text. Dimming glow's output from outside fails for the same reason:
every colour it paints ends in a reset, and a reset cancels an enclosing faint.
Three ways round and all three are the same wall, so the colour is the answer.

The `[thinking]` label above the block is agi's own chrome and stays either
way. No hook is ever handed the frame agi puts a message in, only what went
inside it.

### Why one half names a style and the other carries a file

`message.sh` says `glow -s dark` and `thinking.sh` says
`glow -s "$here/thinking.json"`, and the difference is not an oversight. A
style file exists in this folder only where a built-in name could not say what
was wanted, and for the deliberation none could: glow has a `dracula` but no
"dracula with the body dimmed", so that half had to be written out. Having
been written out it is also pinned, and a glow upgrade that retunes the
built-in cannot reach it. The prose half wants nothing changed, so a name does
the job, and a second file would be two hundred lines identical to upstream
bought for a guarantee that half has not asked for.

The consequence is two palettes on the screen rather than one at two
strengths, and that is the part to change first if you dislike it. Point
`message.sh` at a copy of `thinking.json` with the two colour lines taken back
out and the whole screen becomes one scheme, the deliberation subdued inside
it.
