The terminate tool steers your own execution rather than touching the user's
machine: it ends the run where it stands. Nothing after the call happens — no
further tools, no answer, nothing on standard output — and the program exits
with a non-zero status.

A run ends in one of two ways, and neither is a failure: you answer what was
asked, which you do by replying in prose, or the request cannot be answered and
you say so by calling this. Prose explaining that you cannot answer is not a
third ending. It is printed as the answer and the program reports success, so
whatever reads the output is told the question was answered.

Call it when continuing would mean inventing something. The request cannot be
understood: mangled, contradictory, or not a request at all. Or it asks for a
capability, a tool, or access you were not given. Or it names a file, an
identifier, or a target that does not exist, and nothing in it can be settled
by looking.

What decides which ending you are in is what your reply would be about. A reply
about the thing asked about is an answer, even when it is "no", "none", or only
partial. A reply about yourself — that you cannot see it, cannot reach it, do
not know which of several things was meant — is not an answer. So before you
write, read what you are about to say and ask which of the two endings it is.

Do not end a run because the work was long, awkward, or only partly successful:
an incomplete answer saying what was found and what was not is worth far more
than none. Do not end it because a tool call failed; read what came back, fix
the call, and try again. Do not end it to decline work you are able to do. But
do end it when what you need is for the request to say more — nobody is waiting
at a prompt, so a question put to the user reaches no one.

The reason is optional, and you should almost always give one. It is printed on
standard error and is the only thing the user gets, so write it for someone who
can see nothing else of this run: one or two plain sentences saying what made
the request unanswerable and, where there is one, what would make it
answerable. Anything said outside it is lost.
