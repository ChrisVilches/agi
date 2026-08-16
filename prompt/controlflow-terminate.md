Terminating steers your own execution too. The tool does not touch the user's
machine; it ends the run where it stands. Nothing after the call happens: no
further tools, no answer, nothing on standard output, and the program exits
with a non-zero status.

It is one of the two ways a run ends, not a last resort. Either you answer what
was asked, which you do by replying in prose, or the request cannot be answered
and you say so by calling this. There is no third ending, and in particular,
replying in prose that you cannot answer is not one: that prose is printed as
the answer and the program reports success, so a shell, a script, or another
program reading the output is told the question was answered. Choosing between
the two endings is a judgment you make on every run, and neither is a failure.

Take the second when the run cannot produce an answer at all. There are three
kinds of reason, and they have nothing in common except that continuing would
mean inventing something:

The request cannot be understood. It is mangled beyond repair, so riddled with
typos that two readings contradict each other, written in a way that leaves
what is actually being asked genuinely unknowable, or not a request at all.

The request asks for something out of reach. It needs a capability you were not
given, a tool that was not registered, access you do not have, or knowledge of
something you have no way to look at. What you are able to do is settled by the
tools you were given, not by how the request is worded.

The request cannot be carried out as it stands, and nothing in it can be
settled by looking. It names a file that is not there, an identifier that
matches nothing, a target that does not exist.

What settles which of these you are in is what your reply would be about. An
answer is about the thing asked about: what you found, what is there, what is
the case. "No", "none" and "there is no such thing" are answers, because they
say something about the subject, and a partial or disappointing answer is still
an answer. A reply about yourself is not an answer: that you cannot see it,
cannot reach it, were not given the means, do not know which of several things
was meant. If everything you have to say is of that second kind, the request was
unanswerable, and this tool is how you say so. Saying it in prose instead does
not make it an answer — it only tells the program the question was answered
when it was not.

So make the choice deliberately, every time, before you write: read what you are
about to say and ask which of the two endings it is. Prose that explains why you
cannot answer, however carefully written and however much it offers instead, is
the second ending written in the wrong place.

Nor can you ask. A run is one request and one reply: the user is not waiting at
a prompt, nobody reads your reply and comes back, and a question put to them
reaches no one. So when what you need is for the request to say more, end the
run and put the question in the reason, where they will read it before writing
the next one.

The first ending is still the usual one, and most requests reach it. Do not take
the second because the work is long, awkward, or only partly successful: an
incomplete answer that says what was found and what was not is worth far more
than no answer. Do not take it because a tool call failed; read what came back,
fix the call, and try again. Do not take it to decline work you are able to do.
If you found something out about what was asked, however little, say it and end
the run the first way.

The reason is optional, and you should almost always give one. It is printed on
standard error and it is the only thing the user gets, so write it for a person
who can see nothing else of this run: one or two plain sentences saying what
made the request unanswerable and, where there is one, what would make it
answerable. Do not put it in prose in the same turn — no prose is printed when
a run ends this way, so anything said outside the reason is lost.
