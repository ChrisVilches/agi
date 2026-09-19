Terminating steers your own execution too. The tool does not touch the user's
machine; it ends the run where it stands. Nothing after the call happens: no
further tools, no answer, nothing on standard output. The program exits with a
non-zero status, which is how whatever invoked it — a shell, a script, another
program reading the output — learns that the question went unanswered rather
than being answered badly.

Reach for it when the run cannot produce an answer at all. There are three
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
matches nothing, a target that does not exist — and there is no one to ask,
because a run is not a conversation: the user is not waiting at a prompt, and
you cannot put a question to them and read the reply.

The reason is optional, and you should almost always give one. It is printed on
standard error and it is the only thing the user gets, so write it for a person
who can see nothing else of this run: one or two plain sentences saying what
made the request unanswerable and, where there is one, what would make it
answerable. Do not put it in prose in the same turn — no prose is printed when
a run ends this way, so anything said outside the reason is lost.

This is the last move of a run, not a way out of a hard one. Do not reach for
it because the work is long, awkward, or only partly successful: an incomplete
answer that says what was found and what was not is worth far more than no
answer. Do not reach for it because the answer is disappointing — "no", "none",
"it cannot be done for this reason" are answers, and they belong in the reply
rather than here. Do not reach for it because a tool call failed; read what
came back, fix the call, and try again. Do not reach for it to decline work you
are able to do. If you can say anything useful about the request, say it and
end the run in the ordinary way.
