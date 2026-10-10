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
func completed(ctx context.Context, out output, lg *eventlog.Log, client transport.Backend, conv *model.Conversation) error {
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
	instruct(lg, conv, outcomeInstruction())

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

		lg.Record(eventlog.Event{
			Kind:     eventlog.ModelRequest,
			Attempt:  attempt,
			Model:    client.Model(),
			Messages: conv.Len(),
		})

		asked := time.Now()
		reply, err := client.Send(ctx, transport.Request{
			Conversation: conv,
			// No tools: the work is done, and the only thing left to do is say
			// whether it came to anything.
		})
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
		out.reason(reply.Message.ReasoningContent)

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
// It is long for guidance, and every paragraph of it is there because the
// question has one failure mode worth spending words on: a model reports
// failure for work that merely went badly. An incomplete answer, a caveat, a
// question left for the user, a finding nobody wanted — each of those is a
// completed task, and a model that has just written one of them is exactly the
// one about to say so. So the line between the two is drawn twice, once from
// each side, and the shape of the document is spelled out with an example of
// each rather than described, since a model shown the document writes the
// document.
func outcomeInstruction() string {
	return `The work above is finished. Before this run ends, say whether the task you were given was actually completed.

Answer with a single JSON object. There are exactly two shapes it may take:

{"success": true}

{"success": false, "reason": "..."}

The task was completed when the thing asked for was done, or the question asked was answered. That still holds when the result is partial, qualified, or unwelcome. Work you left as a question for the user, follow-up you recommended rather than did, caveats you attached, and parts you judged out of scope are all part of a completed task — an answer saying what was found and what was not is an answer. So is a finding nobody wanted: "no", "none", and "that cannot be done, because ..." are completed tasks, not failures. A task that was hard, long, or awkward is completed if it got done.

The task was not completed when there is nothing to show for the run. The request could not be understood at all. Or it needed a capability, a tool, or access you were not given. Or it named a file, an identifier, or a target that does not exist, and nothing could settle which was meant. Or you attempted the work and it did not come off, so what you wrote above is about yourself rather than about the thing you were asked about.

Before you choose, read what you actually answered above and ask which of those two it is.

The reason is required when success is false and ignored when it is true. It is printed on standard error and is the only thing the person running this will see besides a failed exit status, so write one or two plain sentences saying what stopped the task and, where there is one, what would make it possible.

Output the raw JSON object and nothing else: no prose before or after it, no explanation, and no markdown code fence.`
}

// outcomeCorrection hands a rejected attempt back to the model. Saying what
// was wrong is the whole point of spending another attempt, and the two shapes
// are repeated because an attempt that went wrong is an attempt that did not
// take them from the instruction above.
func outcomeCorrection(rejected error) string {
	return fmt.Sprintf(`That was rejected:
%s

Output a corrected JSON object that fixes every one of those, and nothing else. It is either {"success": true} or {"success": false, "reason": "..."} and there is no third shape.`, describeRejection(rejected))
}
