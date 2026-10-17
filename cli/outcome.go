package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/transport"
)

// maxOutcomeAttempts bounds how many times the model is asked whether the task
// was completed before the run gives up.
//
// It is the same number as maxSchemaAttempts and is deliberately a second
// constant rather than one shared by both. The two phases ask for different
// documents and would be tuned apart the day either turned out to need it, and
// a single constant would make a change meant for one of them silently change
// the other.
//
// TODO: parameterize this instead of hardcoding it.
const maxOutcomeAttempts = 10

// incomplete is what a run reports when the model said the task was not
// completed. It is the whole of what --exit-code adds to a run that answered:
// the answer still goes to standard output, and this reaches the user on
// standard error with agi exiting 1.
//
// It is an error because that is what it is from the process's point of view,
// and because being one is what carries it: everything between here and main
// already passes errors upward untouched. Error is the model's reason verbatim
// and nothing around it, so the line agi prints is the model's own sentence
// rather than a paraphrase wrapped in agi's voice.
//
// It is a type rather than a sentinel because it carries that reason, and
// because ended has to tell it from an ordinary failure: a run that answered
// and then reported the task incomplete is not in doubt, so its conversation
// is stored and can be resumed.
type incomplete struct {
	// Reason is what stopped the task, in the model's words. It is never
	// empty: an attempt that reported failure without saying why is rejected
	// like any other unreadable verdict.
	Reason string
}

// Error implements error with the model's reason and nothing around it.
func (i *incomplete) Error() string { return i.Reason }

// verdict is what the model said about the run when it was asked.
type verdict struct {
	// completed is whether the task was done.
	completed bool
	// reason is what stopped it, and is empty for a completed task — the
	// model may give one anyway, and it is dropped here rather than carried
	// to a caller that would have to know to ignore it.
	reason string
}

// completed asks the model whether the task it has just finished was actually
// completed, and reports what it said. It is the --exit-code phase, and it is
// the ordinary run plus a question rather than a different run: the agent loop
// has already ended on an answer, and that answer is printed whatever comes
// back from here.
//
// Nil means the task was completed and agi exits 0. An *incomplete means the
// model said it was not, and carries the reason. Anything else is a run that
// could not be asked at all — the gateway did not answer, the user stopped it,
// or the attempts ran out — which exits 1 as any failure does, because a run
// that cannot say whether it succeeded has not succeeded.
//
// Nothing it appends survives. The instruction, every attempt, and the verdict
// itself are scaffolding, dropped before returning so that a run later resumed
// from this conversation finds the answer as the last word and no trace of the
// question that followed it. That is what makes the flag free of the record:
// a session is the same whether or not it was asked.
// It is handed the agent's hooked for the reason restate is: an attempt is an
// assistant turn, the deliberation behind it is shown above it, and showing
// it means reaching whatever display hook the run armed over thinking. The
// verdict itself is still agi's own question and agi's own word for the
// outcome, so none of that is hooked.
//
// criterion is what the user said counts as completed for this task, through
// --exit-code="TEXT", and is empty for the general question.
func completed(ctx context.Context, shown hooked, client transport.Backend, conv *model.Conversation, criterion string) error {
	out, lg := shown.out, shown.log
	mark := conv.Len()
	defer func() {
		// Removing turns from the conversation is itself an event. They will
		// not be in the snapshot — that is the point of dropping them — so
		// this line is the only place the run accounts for them.
		lg.Record(eventlog.Event{
			Kind:    eventlog.Drop,
			From:    mark,
			Dropped: conv.Len() - mark,
		})
		// The mark is where the instruction below goes, so the dropped tail
		// always starts at a harness instruction and this cannot fail.
		_ = conv.DropFrom(mark)
	}()

	lg.Record(eventlog.Event{Kind: eventlog.OutcomeAsk})
	instruct(lg, conv, outcomeInstruction(criterion))

	// Why the last attempt could not be read, reported to the model and, if
	// the attempts run out, to the user.
	var rejected error

	for attempt := 1; attempt <= maxOutcomeAttempts; attempt++ {
		// The phase is scaffolding and the deferred drop above takes it back
		// out whichever way this returns, so an interrupted run stores the
		// answer the loop ended on and nothing of the question.
		if interrupted(ctx) {
			return abandon(lg, conv, 0)
		}

		out.step("%s %s\n", tools.Name("exit-code"),
			tools.Detail(fmt.Sprintf("asking whether the task was completed (attempt %d of %d)", attempt, maxOutcomeAttempts)))

		req := transport.Request{
			Conversation: conv,
			// No tools: the work is done, and the only thing left to do is say
			// whether it came to anything.
		}
		recordRequest(lg, client, req, eventlog.Event{Attempt: attempt})

		asked := time.Now()
		done := shown.out.waiting("inference")
		reply, err := client.Send(ctx, req)
		done()
		if err != nil {
			lg.Record(eventlog.Event{
				Kind:    eventlog.ModelResponse,
				Attempt: attempt,
				Reason:  err.Error(),
				Elapsed: eventlog.Since(asked),
			})
			if interrupted(ctx) {
				return abandon(lg, conv, 0)
			}
			return err
		}
		lg.Record(eventlog.Event{
			Kind:             eventlog.ModelResponse,
			Attempt:          attempt,
			Content:          reply.Message.Content,
			ReasoningContent: reply.Message.ReasoningContent,
			Elapsed:          eventlog.Since(asked),
		})

		// An attempt is an assistant turn like any other, so whatever thinking
		// went into it is shown above it.
		shown.reason(ctx, reply.Message.ReasoningContent)

		// Only the prose is kept. A turn that asks for a tool has misread the
		// instruction, and there is no tool on offer to answer it with.
		document := strings.TrimSpace(reply.Message.Content)
		lg.Record(eventlog.Event{Kind: eventlog.OutcomeAttempt, Attempt: attempt, Content: document})

		var said verdict
		if document == "" {
			rejected = errors.New("the reply contained no JSON at all")
		} else {
			conv.AddAssistant(model.NewAssistantMessage(document))
			said, rejected = parseVerdict(document)
			if rejected == nil {
				return report(out, lg, attempt, document, said)
			}
		}

		lg.Record(eventlog.Event{
			Kind:    eventlog.OutcomeRejected,
			Attempt: attempt,
			Reason:  describeRejection(rejected),
		})
		out.step("%s\n", indent(describeRejection(rejected)))
		instruct(lg, conv, outcomeCorrection(rejected))
	}

	return fmt.Errorf("the model did not say whether the task was completed in %d attempts; last rejection: %w",
		maxOutcomeAttempts, rejected)
}

// report records the verdict, says it on the progress stream, and turns it
// into what the run returns.
//
// The line it prints says the outcome and never the reason. At a terminal the
// progress stream and standard error are the same screen, and main is about to
// print the reason there as the run's error; saying it here as well would say
// it twice, which is the rule abandon follows for an interruption. A piped run
// drops this line and keeps the reason, which is the arrangement the flag
// exists for.
func report(out output, lg *eventlog.Log, attempt int, document string, said verdict) error {
	if said.completed {
		lg.Record(eventlog.Event{
			Kind:    eventlog.OutcomeCompleted,
			Attempt: attempt,
			Content: document,
		})
		out.step("%s\n", indent("the task was completed"))
		return nil
	}
	lg.Record(eventlog.Event{
		Kind:    eventlog.OutcomeIncomplete,
		Attempt: attempt,
		Content: document,
		Reason:  said.reason,
	})
	out.step("%s\n", indent("the task was not completed"))
	return &incomplete{Reason: said.reason}
}

// outcomeFields are the two keys a verdict may have. They are listed rather
// than inferred from a struct's tags because the same list is what an
// unexpected key is checked against, and a reader of either should not have to
// find the other.
var outcomeFields = map[string]bool{"success": true, "reason": true}

// parseVerdict reads one verdict document.
//
// It is strict in the way Schema.ValidateJSON is strict, and for the stronger
// reason: a schema document is handed to whoever is reading the pipe, while
// this one decides a process's exit status. A field name the model invented
// has to be refused rather than ignored, because json.Unmarshal into a struct
// would read {"ok": true} as a missing success and report a completed run as a
// failed one — the quietest way imaginable to get the answer backwards.
//
// Every problem with a document is reported together, so one correction can
// fix all of them. A reason is required when the task was not completed, since
// the exit status alone tells the user nothing and the reason is the whole of
// what they get; a reason given alongside a completed task is dropped here,
// because the model is told it is ignored and a caller should not have to
// remember that too.
func parseVerdict(raw string) (verdict, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	var document any
	if err := dec.Decode(&document); err != nil {
		return verdict{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return verdict{}, errors.New("invalid JSON: more than one value")
	}
	object, ok := document.(map[string]any)
	if !ok {
		return verdict{}, errors.New(`want a JSON object with a "success" field`)
	}

	var problems []string

	// Map order is unspecified, so unexpected fields are reported sorted to
	// keep the message the same from run to run.
	var unexpected []string
	for name := range object {
		if !outcomeFields[name] {
			unexpected = append(unexpected, name)
		}
	}
	sort.Strings(unexpected)
	for _, name := range unexpected {
		problems = append(problems, fmt.Sprintf("unexpected field %q", name))
	}

	var said verdict
	if value, present := object["success"]; !present {
		problems = append(problems, `missing field "success"`)
	} else if done, isBool := value.(bool); !isBool {
		problems = append(problems,
			fmt.Sprintf(`field "success": want true or false, got %s`, jsonKind(value)))
	} else {
		said.completed = done
	}

	if value, present := object["reason"]; present {
		if text, isText := value.(string); !isText {
			problems = append(problems,
				fmt.Sprintf(`field "reason": want a string, got %s`, jsonKind(value)))
		} else {
			said.reason = strings.TrimSpace(text)
		}
	}

	// A failed run with no reason is the one shape that parses and is still
	// useless: the exit status says something went wrong and nothing says
	// what. It is rejected rather than filled in with a sentence of agi's,
	// which would read as the model's own and say nothing either.
	if len(problems) == 0 && !said.completed && said.reason == "" {
		problems = append(problems,
			`field "reason": required when "success" is false, and it is what the user is shown`)
	}

	if len(problems) > 0 {
		return verdict{}, &model.ValidationError{Problems: problems}
	}
	// A reason given for a completed task is dropped here; the model is told
	// it is ignored, and nothing downstream should have to know that.
	if said.completed {
		said.reason = ""
	}
	return said, nil
}

// jsonKind names what a JSON value turned out to be, for a message telling the
// model what it wrote where something else was wanted.
func jsonKind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "true or false"
	case string:
		return "a string"
	case float64, json.Number:
		return "a number"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	return "something else"
}

// outcomeInstruction is what the model is told once the answer is in.
//
// It asks one question — did the person get the thing they asked for? — and
// is long because the model has to be stopped from answering a different one.
// Left to itself it grades the answer rather than the delivery, and an honest,
// correct, well written account of why a thing could not be done scores as a
// success on every count but the only one that matters here: the person asking
// got nothing they asked for. That is the failure mode the length is spent on,
// and it is spent in one direction. A model that has just explained a limit is
// exactly the one about to call the explanation a completed task, so the
// instruction says in as many words that explaining an obstacle is not
// clearing it, and that it makes no difference whether the obstacle was the
// model's or the world's.
//
// This was not always its premise. The instruction used to spend its length
// guarding the other side — against a model reporting failure for work that
// merely went badly — and said in passing that "that cannot be done, because
// ..." was a completed task. That sentence was the bug rather than an
// imprecision in it: asked to read a mind, to predict a lottery, or to decide
// the halting problem, the model wrote the true answer, matched it against
// that clause, and exited 0. The guard it replaced is still here, because the
// error it was written for is real; what changed is that it is now the narrow
// clause and not the broad one. A true negative completes a task only where
// the request was a question such an answer settles, which is what separates
// "there are no bugs in this file" from "no program can decide that".
//
// The carve-outs it keeps — partial work, caveats, a question left for the
// user, scope the model declined — are the ones that survived the rewrite
// against a set of probe prompts. So is the test in the last paragraph, which
// is there because a model shown how to tell two cases apart does better than
// one shown only the rule.
//
// Both shapes of the document are spelled out with an example of each rather
// than described, since a model shown the document writes the document.
//
// Making the comparison structural was tried and does nothing. An earlier
// attempt had the model write what was asked for and what was delivered as
// fields of the document, before the verdict and in that order; it filled them
// in accurately — "asked for: a Go program that decides whether an arbitrary
// program halts", "delivered: an explanation that no such program exists" —
// and then said success. The model was never failing to see the mismatch. It
// was classifying it the way it had been told to, which is why this is prose
// and not another field.
//
// A criterion the user gave is inserted after all of that and before the
// paragraph about the reason, so the general guidance is read first and the
// user's words are read as the last thing said about what completed means —
// which is what lets them settle it where the two disagree. That is the point
// of the flag's text: it exists for the tasks the general question gets
// wrong, and words that could only add to it could not correct it. Where the
// user's words are silent the general guidance still holds, since a criterion
// is usually one sentence about one thing. The text goes between tags rather
// than under a heading, because it is the user's prose dropped into the
// model's and a Markdown heading would dissolve into either. Without one the
// instruction is exactly what it was before the flag took text.
func outcomeInstruction(criterion string) string {
	if criterion == "" {
		return outcomeGeneral + "\n\n" + outcomeAnswer
	}
	return outcomeGeneral + "\n\n" + fmt.Sprintf(`For this task, what counts as completed is defined as follows:

<criterion>
%s
</criterion>

This definition decides the verdict wherever it applies, including where it disagrees with the guidance above. Where it says nothing, the guidance above still applies. When success is false, the reason says which part of this definition was not met.`, criterion) + "\n\n" + outcomeAnswer
}

// outcomeGeneral is the general question, which every --exit-code run is
// asked; see outcomeInstruction.
const outcomeGeneral = `The work above is finished. Before this run ends, say whether the task you were given was actually completed.

Answer with a single JSON object. There are exactly two shapes it may take:

{"success": true}

{"success": false, "reason": "..."}

Read the original request again, then read what you actually wrote in answer to it. The question is not whether the run went well, or whether your answer was honest, correct and useful. It is narrower: did the person get the thing they asked for?

They did when the thing asked for was produced, or when a question was answered with a fact about the subject it asked about. That holds when the result is partial, qualified, or unwelcome. Work you left as a question for the user, follow-up you recommended rather than did, caveats you attached, and parts you judged out of scope are all part of a completed task. A true negative is an answer: "no", "none", "there were none", "it is already the case" complete a task whenever the request was a question such an answer settles. A task that was hard, long, or awkward is completed if it got done.

They did not when what you wrote is about you and your situation rather than about the subject you were asked about. An answer whose substance is that you could not do it did not do it — however true, well judged and well written that explanation is. It makes no difference whether the obstacle was yours or the world's: a capability, a tool or an access you were not given; a target that does not exist; a request nobody could have understood; a thing nobody can do, by you or by anyone; or work you attempted that did not come off. Explaining why a thing is impossible is not the same as producing it. This is the case to watch, because a careful explanation of a limit is the kind of answer you are most likely to credit as a success when the person asking got nothing they asked for.

One test, if the two are hard to tell apart: cross out every sentence of your answer that is about you, your tools, your access, or what is possible. If what remains is the thing that was asked for, the task was completed. If almost nothing remains, it was not.`

// outcomeAnswer closes the instruction: what the reason is for and how the
// document is written. It comes last whether or not a criterion was given,
// so the shape of the reply is the last thing read before writing it.
const outcomeAnswer = `The reason is required when success is false and ignored when it is true. It is printed on standard error and is the only thing the person running this will see besides a failed exit status, so write one or two plain sentences saying what stopped the task and, where there is one, what would make it possible.

Output the raw JSON object and nothing else: no prose before or after it, no explanation, and no markdown code fence.`

// outcomeCorrection hands a rejected attempt back to the model. Saying what
// was wrong is the whole point of spending another attempt, and the two shapes
// are repeated because an attempt that went wrong is an attempt that did not
// take them from the instruction above.
func outcomeCorrection(rejected error) string {
	return fmt.Sprintf(`That was rejected:
%s

Output a corrected JSON object that fixes every one of those, and nothing else. It is either {"success": true} or {"success": false, "reason": "..."} and there is no third shape.`, describeRejection(rejected))
}
