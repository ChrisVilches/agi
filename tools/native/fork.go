package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/tools"
)

// ForkRunner runs one fork: a copy of the run that called it, on one task,
// returning that copy's final answer.
//
// It is handed in for the reason Runner is. The agent loop lives in the command
// layer, which already imports this package, so a tool that needs to run an
// agent is given a function that does rather than reaching for one. That also
// puts the decision of what a fork may itself do where the tools are
// registered — notably that it may not fork again, and may not end the run.
type ForkRunner func(ctx context.Context, brief ForkBrief) (string, error)

// ForkBrief is one fork's assignment as whatever runs it sees.
//
// The name is ForkBrief and not Brief because Brief is the subagents tool's
// already. Renaming that for the sake of a symmetric pair would churn working
// code and the document describing it, which is the worse trade.
type ForkBrief struct {
	// Task is the instruction to carry out, as the model wrote it.
	Task string
	// Number names this fork within the run, from one.
	Number int
	// RequestID is the call that asked for the fork. The copy has to see it
	// answered before it can continue, since a provider may refuse a turn that
	// moves on while a call it asked for is outstanding.
	RequestID string
	// Fork makes the conversation the copy continues from. It arrives with the
	// call rather than being held by the tool, because the conversation does
	// not exist when the tool is built: a run assembles its system prompt from
	// its registry and its conversation from that prompt, so the registry
	// comes first.
	Fork tools.Fork
}

// ForkParams is the input schema for the fork tool. One task, as one string:
// the shape of the call is itself part of what tells a model which tool it
// wants.
type ForkParams struct {
	Task string `json:"task"`
}

// Fork runs a copy of the agent that called it on one task.
//
// It exists because agi's record is append-only. Every turn but a harness
// instruction reports Truncatable() == false, so an agent cannot do a long
// piece of exploratory work in its own conversation and then throw the
// exploration away. The only way to keep a conclusion without the rubble is to
// do the work somewhere else and bring one answer back — and that somewhere
// else has to know everything this agent knows, or it is not the same piece of
// work. That is what separates it from a subagent, which starts from nothing.
type Fork struct {
	run ForkRunner
	// forked hands out the numbers that name forks. It is a field here, where
	// the subagents tool's counter is the run's, because a run holds exactly
	// one Fork by construction: a fork has no fork tool and neither has a
	// subagent, so nothing but the run's own registry ever builds one.
	forked Counter
}

// NewFork returns the tool, running forks through run.
func NewFork(run ForkRunner) *Fork { return &Fork{run: run} }

// SystemPrompt implements tools.SystemPrompter. When a copy of the run is the
// right move — and what it costs — is not something a model can infer from a
// schema, so the tool explains itself in the system prompt, and only when it
// has been registered.
func (*Fork) SystemPrompt() string { return prompt.Fork() }

// ForkTool is the name the tool is registered and called under.
//
// It is exported because whether a run can fork at all is asked from outside
// this package, and the only true answer is whether a tool of this name was
// registered. A second spelling of it elsewhere would be a second tool as far
// as the registry is concerned, and would go on answering "no" after this one
// was renamed.
const ForkTool = "fork"

// Definition describes the tool to the model. When to reach for a fork is in
// the system prompt, where a native tool's guidance lives; what is here is how
// to call it.
func (f *Fork) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: ForkTool,
		Description: strings.TrimSpace(`
Run a copy of yourself on one task and return its answer.

  {"task": "Work out whether the session lock survives the process being killed, and answer yes or no with the reason."}

ONE TASK, AS ONE STRING. Not a list, not an object, not several tasks in one
call. If you have several pieces of work, this is not the tool for them.

The copy starts from this conversation exactly as it stands. It has read
everything you have read and holds the same instructions, so the task is an
instruction and not a briefing. Do not restate the problem and do not repeat
what you have already found: it has all of that, and a repetition reads as a
correction.

Only the copy's final answer comes back, as the result of this call. Nothing
else it does is kept — not the files it reads, not the commands it runs, not
the turns it takes. Say plainly what the answer has to contain, because the
answer is all that survives.

Nothing else happens while it runs, and it re-sends this whole conversation on
every turn it takes, so it is the most expensive call available to you. It
earns that only when the work is long enough that you would not want it here.`),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task": map[string]any{
					"type": "string",
					"description": "Required, and never empty. The one task for the " +
						"copy to carry out, written as an instruction to something " +
						"that has already read this conversation. Say what to settle " +
						"and what the answer has to contain; do not explain the " +
						"background, which it already has.",
				},
			},
			"required":             []string{"task"},
			"additionalProperties": false,
		},
	}
}

// Announce implements tools.Announcer: the first line of the task, which is
// what the user needs to see the run go off and do.
//
// A fork is watched, so its own progress follows this line and there is no
// need to show the task in full: what this is for is recognizing the call.
func (f *Fork) Announce(args json.RawMessage) string {
	task, err := f.parse(args)
	if err != nil {
		return ""
	}
	return tools.SingleLine(task)
}

// parse decodes and validates the model's arguments.
func (f *Fork) parse(args json.RawMessage) (string, error) {
	var p ForkParams
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("%s: arguments are not valid JSON, and this call takes "+
			"one object with one string field, task: %w", ForkTool, err)
	}
	task := strings.TrimSpace(p.Task)
	if task == "" {
		return "", fmt.Errorf("%s: no task was given, so there is nothing for the copy "+
			"to do; write the task as one string — what to settle, and what the answer "+
			"has to contain — and call again", ForkTool)
	}
	return task, nil
}

// ResultWith implements tools.Conversational: it forks the conversation the
// call was made in, runs a copy of the agent on the task, and answers with
// what that copy said.
//
// The answer is a plain result. SubagentResults keeps several answers apart
// because several arrive at once; one answer has nothing to be separated from.
func (f *Fork) ResultWith(ctx context.Context, fork tools.Fork, requestID string, args json.RawMessage) (model.ToolAnswer, error) {
	task, err := f.parse(args)
	if err != nil {
		return nil, err
	}
	if f.run == nil {
		return nil, errors.New("fork: no way to run a fork was configured")
	}
	if fork == nil {
		return nil, errors.New("fork: this call was made with no conversation to copy")
	}

	answer, err := f.run(ctx, ForkBrief{
		Task:      task,
		Number:    f.forked.Take(1),
		RequestID: requestID,
		Fork:      fork,
	})
	if err != nil {
		return nil, err
	}
	if answer = strings.TrimSpace(answer); answer == "" {
		return nil, errors.New("fork: the copy produced no answer")
	}
	return model.NewToolResult(requestID, answer), nil
}

// Call implements tools.Tool, and cannot do the work.
//
// The registry registers a tools.Tool, so this method has to exist; its
// signature is a context and arguments, with no way to reach the conversation a
// fork is a copy of. So it refuses, and says why.
//
// Nothing in a run arrives here. Registry.Answer prefers Conversational, which
// this tool implements, and Registry.Call — the only other way in — has no
// caller outside tests. Removing the method would mean narrowing what the
// registry accepts, which would trade a compile-time guarantee covering every
// tool for the deletion of one method on one, and move the failure of a tool
// that answers nothing from the build to the middle of a conversation the user
// is paying for.
//
// It is not the shape Subagents.Call has. That one is real work: it renders the
// same answers as text instead of as a structured turn, so a text version
// honestly exists. What is missing here is not a rendering but the conversation
// itself, and there is no version of this call without one.
func (f *Fork) Call(context.Context, json.RawMessage) (string, error) {
	return "", errors.New("fork: this tool copies the conversation it was called in, " +
		"so it cannot be called without one")
}
