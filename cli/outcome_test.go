package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
)

// lastInstruction returns the harness instruction a request ended on, which is
// what the model was actually asked on that attempt.
func lastInstruction(t *testing.T, client *scriptedClient, request int) model.HarnessInstruction {
	t.Helper()
	sent := client.sent[request].Conversation.Messages
	got, ok := sent[len(sent)-1].(model.HarnessInstruction)
	if !ok {
		t.Fatalf("last turn of request %d = %T, want a harness instruction", request, sent[len(sent)-1])
	}
	return got
}

// A completed task is the ordinary ending: nothing is reported, and the
// conversation comes out exactly as it went in.
func TestCompletedAcceptsASuccessAndLeavesOnlyScaffolding(t *testing.T) {
	client := &scriptedClient{replies: replies(`{"success": true}`)}
	conv := answered("User 1 is Ada.")

	var out bytes.Buffer
	if err := completed(context.Background(), prints(humanOutput(&out)), client, conv, ""); err != nil {
		t.Fatalf("completed() = %v, want nil", err)
	}

	// Both shapes are spelled out, since an instruction that described one and
	// qualified it is one a model answers with the wrong document.
	instruction := lastInstruction(t, client, 0)
	for _, want := range []string{`{"success": true}`, `{"success": false, "reason": "..."}`, "standard error"} {
		if !strings.Contains(instruction.Content, want) {
			t.Errorf("instruction = %q, want it to mention %q", instruction.Content, want)
		}
	}

	// Nothing is left to do, so nothing is offered to do it with.
	if len(client.sent[0].Tools) != 0 {
		t.Errorf("tools sent = %+v, want none while asking for the verdict", client.sent[0].Tools)
	}

	assertScaffoldingOnly(t, conv, "User 1 is Ada.")
}

// Text given to --exit-code reaches the model inside the instruction, after
// the general guidance and before the shape of the reply, and is said to
// decide the verdict where it speaks. Without it the instruction is the
// general one and nothing else.
func TestCompletedHandsTheUsersCriterionToTheModel(t *testing.T) {
	const criterion = "success means at least one TODO was found"
	client := &scriptedClient{replies: replies(`{"success": true}`)}
	conv := answered("There are no TODOs.")

	var out bytes.Buffer
	if err := completed(context.Background(), prints(humanOutput(&out)), client, conv, criterion); err != nil {
		t.Fatalf("completed() = %v, want nil", err)
	}

	got := lastInstruction(t, client, 0).Content
	if !strings.Contains(got, "<criterion>\n"+criterion+"\n</criterion>") {
		t.Errorf("instruction = %q, want the criterion between tags", got)
	}
	if !strings.Contains(got, "including where it disagrees with the guidance above") {
		t.Errorf("instruction = %q, want it to say the criterion settles a disagreement", got)
	}
	general, tagged := strings.Index(got, "One test, if the two"), strings.Index(got, "<criterion>")
	shape := strings.Index(got, "Output the raw JSON object")
	if general >= tagged || tagged >= shape {
		t.Errorf("instruction = %q, want the general guidance, then the criterion, then the shape of the reply", got)
	}

	plain := outcomeInstruction("")
	if strings.Contains(plain, "criterion") {
		t.Errorf("the general instruction mentions a criterion: %q", plain)
	}
	if !strings.HasSuffix(plain, outcomeAnswer) || !strings.HasPrefix(plain, outcomeGeneral) {
		t.Errorf("the general instruction is not the two halves joined: %q", plain)
	}
}

// The reason is the whole of what the user gets, so it comes back verbatim and
// is what the error says, with nothing of agi's wrapped around it.
func TestCompletedReportsTheReasonWhenTheTaskWasNot(t *testing.T) {
	const reason = "The repository has no file called config.toml, and nothing in the prompt says which file was meant."
	client := &scriptedClient{replies: replies(`{"success": false, "reason": ` + quote(reason) + `}`)}
	conv := answered("I could not find the file.")

	var out bytes.Buffer
	err := completed(context.Background(), prints(humanOutput(&out)), client, conv, "")

	var short *incomplete
	if !errors.As(err, &short) {
		t.Fatalf("completed() = %v, want an incomplete task", err)
	}
	if short.Reason != reason {
		t.Errorf("reason = %q, want %q", short.Reason, reason)
	}
	if err.Error() != reason {
		t.Errorf("Error() = %q, want the model's own sentence and nothing around it", err.Error())
	}

	assertScaffoldingOnly(t, conv, "I could not find the file.")
}

// A reason alongside a completed task is dropped here rather than carried to
// somewhere that would have to know to ignore it.
func TestCompletedIgnoresAReasonGivenWithSuccess(t *testing.T) {
	client := &scriptedClient{replies: replies(`{"success": true, "reason": "all went well"}`)}
	conv := answered("Done.")

	var out bytes.Buffer
	if err := completed(context.Background(), prints(humanOutput(&out)), client, conv, ""); err != nil {
		t.Fatalf("completed() = %v, want nil", err)
	}
	if len(client.sent) != 1 {
		t.Errorf("requests sent = %d, want the reason ignored rather than corrected", len(client.sent))
	}
}

// A failure with no reason parses and is still useless: the exit status says
// something went wrong and nothing says what. It costs an attempt.
func TestCompletedRejectsAFailureWithNoReason(t *testing.T) {
	client := &scriptedClient{replies: replies(
		`{"success": false}`,
		`{"success": false, "reason": "   "}`,
		`{"success": false, "reason": "The gateway named in AGI_BASE_URL refused every connection."}`,
	)}
	conv := answered("I could not reach it.")

	var out bytes.Buffer
	err := completed(context.Background(), prints(humanOutput(&out)), client, conv, "")

	var short *incomplete
	if !errors.As(err, &short) {
		t.Fatalf("completed() = %v, want an incomplete task", err)
	}
	if !strings.Contains(short.Reason, "refused every connection") {
		t.Errorf("reason = %q, want the one the model finally gave", short.Reason)
	}
	for _, request := range []int{1, 2} {
		if got := lastInstruction(t, client, request).Content; !strings.Contains(got, `field "reason"`) {
			t.Errorf("correction %d = %q, want it to say the reason is required", request, got)
		}
	}
}

// The failure mode worth a test of its own. json.Unmarshal into a struct reads
// an invented field name as a missing success and reports a completed run as a
// failed one, which is the quietest possible way to get the answer backwards.
func TestCompletedRejectsAnInventedFieldRatherThanReadingItAsFailure(t *testing.T) {
	client := &scriptedClient{replies: replies(
		`{"ok": true}`,
		`{"success": true, "status": "fine"}`,
		`{"success": true}`,
	)}
	conv := answered("Done.")

	var out bytes.Buffer
	if err := completed(context.Background(), prints(humanOutput(&out)), client, conv, ""); err != nil {
		t.Fatalf("completed() = %v, want the run to come out completed", err)
	}
	if got := lastInstruction(t, client, 1).Content; !strings.Contains(got, `unexpected field "ok"`) ||
		!strings.Contains(got, `missing field "success"`) {
		t.Errorf("correction = %q, want it to name the invented field and the missing one", got)
	}
	if got := lastInstruction(t, client, 2).Content; !strings.Contains(got, `unexpected field "status"`) {
		t.Errorf("correction = %q, want it to name the invented field", got)
	}
}

// A rejected attempt must come back to the model with the reason, so the next
// one has something to go on.
func TestCompletedFeedsRejectionsBack(t *testing.T) {
	client := &scriptedClient{replies: replies(
		"Sure! The task was completed.", // not JSON at all
		`{"success": "yes"}`,            // the right field, the wrong type
		`{"success": true}`,
	)}
	conv := answered("Done.")

	var out bytes.Buffer
	if err := completed(context.Background(), prints(humanOutput(&out)), client, conv, ""); err != nil {
		t.Fatalf("completed() = %v, want nil", err)
	}
	if len(client.sent) != 3 {
		t.Fatalf("requests sent = %d, want one per attempt", len(client.sent))
	}

	if got := lastInstruction(t, client, 1).Content; !strings.Contains(got, "invalid JSON") {
		t.Errorf("correction = %q, want it to say what was wrong", got)
	}
	second := client.sent[1].Conversation.Messages
	if _, ok := second[len(second)-2].(model.AssistantMessage); !ok {
		t.Errorf("turn before the correction = %T, want the rejected attempt", second[len(second)-2])
	}
	if got := lastInstruction(t, client, 2).Content; !strings.Contains(got, `field "success": want true or false, got a string`) {
		t.Errorf("correction = %q, want the type spelled out", got)
	}

	// A correction says the two shapes again, because an attempt that went
	// wrong is one that did not take them from the instruction above it.
	if got := lastInstruction(t, client, 2).Content; !strings.Contains(got, "there is no third shape") {
		t.Errorf("correction = %q, want it to repeat what the document may be", got)
	}

	assertScaffoldingOnly(t, conv, "Done.")
}

// Every problem in one document is reported at once, so one more attempt can
// fix all of them.
func TestCompletedReportsEveryProblemAtOnce(t *testing.T) {
	client := &scriptedClient{replies: replies(
		`{"done": true, "reason": 7, "note": "x"}`,
		`{"success": true}`,
	)}
	conv := answered("Done.")

	var out bytes.Buffer
	if err := completed(context.Background(), prints(humanOutput(&out)), client, conv, ""); err != nil {
		t.Fatalf("completed() = %v, want nil", err)
	}
	for _, want := range []string{
		`unexpected field "done"`,
		`unexpected field "note"`,
		`missing field "success"`,
		`field "reason": want a string, got a number`,
	} {
		if got := lastInstruction(t, client, 1).Content; !strings.Contains(got, want) {
			t.Errorf("correction = %q, want it to mention %q", got, want)
		}
	}
}

// A turn with no prose is not a verdict; it costs an attempt and is explained.
func TestCompletedTreatsAnEmptyReplyAsAFailedAttempt(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{{ID: "c1", Name: "bash"}}},
		model.NewAssistantMessage(`{"success": true}`),
	}}
	conv := answered("Done.")

	var out bytes.Buffer
	if err := completed(context.Background(), prints(humanOutput(&out)), client, conv, ""); err != nil {
		t.Fatalf("completed() = %v, want nil", err)
	}

	// The tool request is not recorded: there was no tool on offer, and the
	// turn carried no verdict to keep.
	second := client.sent[1].Conversation.Messages
	for i, m := range second {
		if a, ok := m.(model.AssistantMessage); ok && a.WantsTools() {
			t.Errorf("turn %d = %+v, want the tool request left out", i, a)
		}
	}
	if got := lastInstruction(t, client, 1).Content; !strings.Contains(got, "no JSON") {
		t.Errorf("correction = %q, want it to say the reply held no JSON", got)
	}
}

// A model that never produces a readable verdict fails the run. agi cannot say
// whether the work came off, and a run that cannot say that has not succeeded.
func TestCompletedGivesUpAfterMaxAttempts(t *testing.T) {
	bad := make([]string, maxOutcomeAttempts+1)
	for i := range bad {
		bad[i] = `{"success": "maybe"}`
	}
	client := &scriptedClient{replies: replies(bad...)}
	conv := answered("Done.")

	var out bytes.Buffer
	err := completed(context.Background(), prints(humanOutput(&out)), client, conv, "")
	if err == nil {
		t.Fatal("completed() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "10 attempts") {
		t.Errorf("completed() = %v, want it to say how many attempts were spent", err)
	}
	// Giving up is not the model saying the task failed, and the two must not
	// be told apart by reading the message.
	var short *incomplete
	if errors.As(err, &short) {
		t.Errorf("completed() = %v, want a failure rather than a verdict", err)
	}
	var invalid *model.ValidationError
	if !errors.As(err, &invalid) {
		t.Errorf("completed() = %v, want the last rejection wrapped", err)
	}
	if len(client.sent) != maxOutcomeAttempts {
		t.Errorf("requests sent = %d, want the %d-attempt cap", len(client.sent), maxOutcomeAttempts)
	}

	assertScaffoldingOnly(t, conv, "Done.")
}

// A gateway that fails ends the run, and still leaves nothing behind.
func TestCompletedPropagatesTransportError(t *testing.T) {
	client := &scriptedClient{err: errors.New("gateway exploded")}
	conv := answered("Done.")

	var out bytes.Buffer
	err := completed(context.Background(), prints(humanOutput(&out)), client, conv, "")
	if err == nil || !strings.Contains(err.Error(), "gateway exploded") {
		t.Fatalf("completed() = %v, want the transport error", err)
	}
	assertScaffoldingOnly(t, conv, "Done.")
}

// The parse on its own, since it is what decides a process's exit status and
// the cases are quicker to state than to script a conversation for.
func TestParseVerdict(t *testing.T) {
	for _, c := range []struct {
		name      string
		document  string
		completed bool
		reason    string
		problems  []string
	}{
		{name: "completed", document: `{"success": true}`, completed: true},
		{name: "completed with whitespace", document: "  {\"success\": true}\n", completed: true},
		{name: "incomplete", document: `{"success": false, "reason": "no such host"}`,
			reason: "no such host"},
		{name: "reason is trimmed", document: `{"success": false, "reason": "  no such host  "}`,
			reason: "no such host"},
		{name: "completed with a reason drops it", document: `{"success": true, "reason": "fine"}`,
			completed: true},
		{name: "an explicit null reason on a completed task", document: `{"success": true, "reason": null}`,
			problems: []string{`field "reason": want a string, got null`}},

		{name: "not JSON", document: "the task was completed",
			problems: []string{"invalid JSON"}},
		{name: "two documents", document: `{"success": true} {"success": false}`,
			problems: []string{"more than one value"}},
		{name: "an array", document: `[{"success": true}]`,
			problems: []string{`want a JSON object with a "success" field`}},
		{name: "a bare bool", document: `true`,
			problems: []string{`want a JSON object with a "success" field`}},
		{name: "fenced", document: "```json\n{\"success\": true}\n```",
			problems: []string{"invalid JSON"}},
		{name: "missing success", document: `{"reason": "x"}`,
			problems: []string{`missing field "success"`}},
		{name: "success as a string", document: `{"success": "true"}`,
			problems: []string{`field "success": want true or false, got a string`}},
		{name: "success as a number", document: `{"success": 1}`,
			problems: []string{`field "success": want true or false, got a number`}},
		{name: "an invented field", document: `{"success": true, "ok": true}`,
			problems: []string{`unexpected field "ok"`}},
		{name: "incomplete with no reason", document: `{"success": false}`,
			problems: []string{`field "reason": required when "success" is false`}},
		{name: "incomplete with an empty reason", document: `{"success": false, "reason": ""}`,
			problems: []string{`field "reason": required when "success" is false`}},
		{name: "every problem at once", document: `{"ok": 1, "zz": 2}`,
			problems: []string{`unexpected field "ok"`, `unexpected field "zz"`, `missing field "success"`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseVerdict(c.document)
			if len(c.problems) > 0 {
				if err == nil {
					t.Fatalf("parseVerdict(%q) = %+v, want a rejection", c.document, got)
				}
				for _, want := range c.problems {
					if !strings.Contains(describeRejection(err), want) {
						t.Errorf("parseVerdict(%q) = %v, want it to say %q", c.document, err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVerdict(%q) = %v, want it accepted", c.document, err)
			}
			if got.completed != c.completed || got.reason != c.reason {
				t.Errorf("parseVerdict(%q) = %+v, want completed=%v reason=%q",
					c.document, got, c.completed, c.reason)
			}
		})
	}
}

// Unexpected fields are reported in a settled order, so one wrong document
// does not produce a different correction each time it is read.
func TestParseVerdictSortsUnexpectedFields(t *testing.T) {
	const document = `{"success": true, "zebra": 1, "apple": 2, "mango": 3}`
	for range 5 {
		_, err := parseVerdict(document)
		if err == nil {
			t.Fatal("parseVerdict() accepted a document with invented fields")
		}
		want := "- unexpected field \"apple\"\n- unexpected field \"mango\"\n- unexpected field \"zebra\""
		if got := describeRejection(err); got != want {
			t.Fatalf("describeRejection() = %q, want %q", got, want)
		}
	}
}

// Piped, the answer goes to standard output as it always has. The verdict
// changes the exit status and nothing on the pipe, which is the arrangement
// the flag exists for.
func TestExitCodeRunPipedPrintsTheAnswerAndFails(t *testing.T) {
	client := &scriptedClient{replies: replies(
		"I could not find config.toml anywhere under the repository.",
		`{"success": false, "reason": "There is no config.toml in this repository."}`,
	)}

	var out bytes.Buffer
	err := runAgent(context.Background(), pipedOutput(&out), nolog(), client, registryWith(t),
		asked("summarize config.toml"), nil, outcomeCheck{asked: true}, defaultMaxTurns, hooks.None())

	var short *incomplete
	if !errors.As(err, &short) {
		t.Fatalf("runAgent() = %v, want an incomplete task", err)
	}
	if got := out.String(); got != "I could not find config.toml anywhere under the repository.\n" {
		t.Errorf("output = %q, want the answer and nothing else", got)
	}
	// The reason is not on the pipe. main prints it on standard error, which
	// is the one place a piped run is given it.
	if strings.Contains(out.String(), "There is no config.toml") {
		t.Errorf("output = %q, want the reason kept off standard output", out.String())
	}
}

// A completed task is the run agi has always been: the answer, and nothing to
// report.
func TestExitCodeRunPipedSaysNothingWhenTheTaskWasCompleted(t *testing.T) {
	client := &scriptedClient{replies: replies("User 1 is Ada.", `{"success": true}`)}

	var out bytes.Buffer
	if err := runAgent(context.Background(), pipedOutput(&out), nolog(), client, registryWith(t),
		asked("who is user 1?"), nil, outcomeCheck{asked: true}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}
	if got := out.String(); got != "User 1 is Ada.\n" {
		t.Errorf("output = %q, want the answer alone", got)
	}
}

// At a terminal the question is on show like the rest of the run, and the
// outcome is said — but never the reason, which main is about to print on the
// same screen as the run's error.
func TestExitCodeRunAtATerminalShowsTheQuestionAndNotTheReason(t *testing.T) {
	client := &scriptedClient{replies: replies(
		"I could not find it.",
		`{"success": "no"}`,
		`{"success": false, "reason": "There is no config.toml in this repository."}`,
	)}

	var out bytes.Buffer
	err := runAgent(context.Background(), humanOutput(&out), nolog(), client, registryWith(t),
		asked("summarize config.toml"), nil, outcomeCheck{asked: true}, defaultMaxTurns, hooks.None())
	var short *incomplete
	if !errors.As(err, &short) {
		t.Fatalf("runAgent() = %v, want an incomplete task", err)
	}
	for _, want := range []string{
		"I could not find it.",
		"attempt 1 of 10",
		"attempt 2 of 10",
		`field "success": want true or false, got a string`,
		"the task was not completed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, want it to show %q", out.String(), want)
		}
	}
	if strings.Contains(out.String(), "There is no config.toml") {
		t.Errorf("output = %q, want the reason said once, on standard error", out.String())
	}
}

// The question costs a request, so a run that did not ask for it must not make
// one.
func TestRunWithoutExitCodeAsksNothing(t *testing.T) {
	client := &scriptedClient{replies: replies("User 1 is Ada.")}

	var out bytes.Buffer
	if err := runAgent(context.Background(), pipedOutput(&out), nolog(), client, registryWith(t),
		asked("who is user 1?"), nil, outcomeCheck{}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}
	if len(client.sent) != 1 {
		t.Errorf("requests sent = %d, want no verdict asked for", len(client.sent))
	}
}

// With both flags the run has two phases after the loop, and the verdict is
// about the document rather than the prose: with a schema the document is what
// was delivered, so the question is put with the restatement still in front of
// the model. Neither phase takes anything back out; the run does, once.
func TestExitCodeWithSchemaJudgesTheDocument(t *testing.T) {
	const document = `{"id": 1, "name": "Ada"}`
	client := &scriptedClient{replies: replies(
		"User 1 is Ada.",
		document,
		`{"success": true}`,
	)}

	var out bytes.Buffer
	conv := asked("who is user 1?")
	if err := runAgent(context.Background(), pipedOutput(&out), nolog(), client, registryWith(t),
		conv, testSchema(t), outcomeCheck{asked: true}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}
	if got := out.String(); got != document+"\n" {
		t.Errorf("output = %q, want only the document", got)
	}

	// The conversation the verdict was asked against runs on from the prose
	// answer through the restatement to the question.
	asking := client.sent[2].Conversation.Messages
	if len(asking) != 5 {
		t.Fatalf("verdict asked against %d turns, want the prompt, the answer, the restatement, the document and the question: %+v", len(asking), asking)
	}
	if answer, ok := asking[1].(model.AssistantMessage); !ok || answer.Content != "User 1 is Ada." {
		t.Errorf("turn 1 = %+v, want the prose answer", asking[1])
	}
	if !strings.Contains(text(asking[2]), "Restate the answer above") {
		t.Errorf("turn 2 = %+v, want the schema instruction", asking[2])
	}
	if got, ok := asking[3].(model.AssistantMessage); !ok || got.Content != document {
		t.Errorf("turn 3 = %+v, want the accepted document", asking[3])
	}
	if _, ok := asking[4].(model.HarnessInstruction); !ok {
		t.Errorf("turn 4 = %T, want the question", asking[4])
	}

	// Both phases are gone once the run is over, the record ending on the
	// prose answer as it would have without either flag.
	assertUntouched(t, conv, "User 1 is Ada.")
}

// A run that answered and then reported the task incomplete is not in doubt:
// everything said was said, so the conversation is stored and can be picked up
// by the run that tries again.
func TestAnIncompleteTaskLeavesAConversationWorthStoring(t *testing.T) {
	if !ended(&incomplete{Reason: "no such host"}) {
		t.Error("ended() = false for an incomplete task, so its conversation would be thrown away")
	}
	// Giving up on the question is an ordinary failure and is not stored, the
	// way a schema phase that ran out of attempts is not.
	if ended(errors.New("the model did not say whether the task was completed")) {
		t.Error("ended() = true for a failure, so a conversation agi cannot vouch for would be stored")
	}
}

// quote renders s as a JSON string, for building a document in a test.
func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
