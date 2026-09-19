package controlflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
)

// Termination is what the terminate tool reports: the model has decided the
// run cannot produce an answer, and wants it ended here.
//
// It is an error because that is what it is from the run's point of view —
// nothing will be written to standard output, and whatever invoked agi is
// entitled to hear about it in the way a process hears about anything, through
// an exit status. Being an error is also what carries it out of the tool: a
// tool returns either an answer or an error, and everything between here and
// main already passes errors upward untouched.
//
// It is a type rather than a sentinel because it carries the model's reason,
// and that reason is the whole of what the user is given. Error is the reason
// verbatim, so the line agi prints on standard error is the model's own words
// rather than a paraphrase wrapped around them.
//
// A run that ends this way is not a failed run. Everything said up to the call
// was said, so the conversation is stored like any other and the session can be
// continued — and the terminate call is answered in the conversation before it
// is stored, so a resumed run reads that the last one ended and does not end
// itself again. Tools are not re-run when a session is resumed, so the only
// thing that could repeat the termination is the model asking for it again.
type Termination struct {
	// Reason is why the run cannot be answered, in the model's words, and is
	// empty when it gave none.
	Reason string
}

// noReason is what a run reports when the model ended it without saying why.
// It is a poor showing — the prompt asks for a reason and explains that it is
// the only thing the user gets — but the run still has to say something, since
// the exit status alone does not tell the user what happened.
const noReason = "the run was ended without an answer, and no reason was given"

// Error implements error with the model's reason and nothing around it, so
// that whatever prints it prints what the model meant to say.
func (t *Termination) Error() string {
	if t.Reason == "" {
		return noReason
	}
	return t.Reason
}

// Result is what answers the terminate call in the conversation before it is
// stored. Nothing reads it in this run — the run is over — but a session
// resumed later sends it to the model, which would otherwise see a call it
// made and no sign of what came of it.
func (t *Termination) Result() string {
	if t.Reason == "" {
		return "The run ended here without an answer."
	}
	return "The run ended here without an answer: " + t.Reason
}

// TerminateParams is the input schema for the terminate tool.
type TerminateParams struct {
	Reason string `json:"reason"`
}

// Terminate ends the run without an answer, so that agi exits non-zero.
//
// It deliberately has no say in the exit status. A run that ends this way
// produced no answer, which agi already reports the way it reports any run
// that produced no answer, and a tool choosing its own number would put the
// process's interface in the model's hands.
type Terminate struct{}

// NewTerminate returns the tool.
func NewTerminate() *Terminate { return &Terminate{} }

// SystemPrompt implements tools.SystemPrompter. Which requests are genuinely
// unanswerable — and the many more that only look that way — is not something
// a schema can say, so the tool explains itself in the system prompt, and only
// when it has been registered.
func (*Terminate) SystemPrompt() string { return prompt.Terminate() }

// Definition describes the tool to the model. When ending a run is the right
// move is in the system prompt, where the control-flow guidance lives; what is
// here is how to call it.
func (*Terminate) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: "terminate",
		Description: strings.TrimSpace(`
End this run without an answer, because the request cannot be answered at all.

  {"reason": "The prompt asks for the contents of /etc/hosts on a machine this run has no access to."}

This is the last thing that happens. Nothing after the call runs, no answer is
written, and agi exits with a non-zero status so that whatever invoked it can
tell an unanswered question from a badly answered one.

Call it only when there is nothing to answer with: the request cannot be
understood, or it needs something you were not given, or it names something
that does not exist and there is no one to ask. A long, awkward, or partly
successful task is not one of those — answer with what you have. Neither is a
disappointing answer: "no", "none", and "that cannot be done because ..." are
answers, and they belong in your reply rather than here. A tool call that
failed is not one either; read what came back and correct the call.

The reason is optional and you should give one. It is printed on standard
error and is the only thing the user sees of this run, so write one or two
plain sentences saying what made the request unanswerable and what would make
it answerable. Prose in the same turn is discarded, so nothing outside the
reason reaches anyone.`),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": map[string]any{
					"type": "string",
					"description": "Why the request cannot be answered, in one or two " +
						"plain sentences. Printed on standard error, and the only thing " +
						"the user sees of this run: say what made the request " +
						"unanswerable and, where there is one, what would make it " +
						"answerable. Optional only because a run must be able to end " +
						"even when nothing can be said about why.",
				},
			},
			"additionalProperties": false,
		},
	}
}

// Call implements tools.Tool. It never returns output: either the arguments
// could not be read, which is an ordinary failure the model can correct on the
// next turn, or the run ends here and the Termination carries that upward.
func (*Terminate) Call(_ context.Context, args json.RawMessage) (string, error) {
	var p TerminateParams
	// A call with no arguments at all is a call with no reason, which the tool
	// allows; only arguments that are there and unreadable are a failure.
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("terminate: arguments are not valid JSON: %w", err)
		}
	}
	return "", &Termination{Reason: strings.TrimSpace(p.Reason)}
}
