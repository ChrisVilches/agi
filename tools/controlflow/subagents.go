package controlflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/tools"
)

// minTasks and maxTasks bound one call, and both are refused rather than
// merely discouraged: the model is told what was wrong with the call and gets
// a turn to make a better one.
//
// The minimum is two because one subagent is never worth the round trip — the
// parent would wait on an agent that knows less about the problem than it
// does, for work it could have finished in the time it took to write the
// brief. The maximum is there so a model that decides to fan out cannot open
// an unbounded number of conversations with the gateway at once.
//
// TODO: parameterize these instead of hardcoding them.
const (
	minTasks = 2
	maxTasks = 8
)

// Runner runs one subagent: a fresh agent on a task, returning its final
// answer.
//
// The agent loop lives in the command layer, which already imports this
// package, so the tool is handed the ability to run one rather than reaching
// for it. That also puts the decision of what a subagent may itself do where
// the tools are registered — notably that it may not spawn further subagents,
// which is what keeps a run from recursing without a bottom.
type Runner func(ctx context.Context, brief Brief) (string, error)

// Brief is one subagent's assignment as whatever runs it sees: the task, and
// the number that names this subagent within the run.
//
// The subagent itself reads only the body. The rest is for whatever runs it,
// which has one job the subagent does not: telling several agents working at
// once apart, on screen and in the log.
//
// The numbers are handed out before any subagent starts, in the order the
// tasks were given, and they keep counting across calls: a run that fans out
// twice numbers the second group after the first. Which subagent starts first
// is scheduling, and a number taken from that would put a different agent
// under the same name on the next identical run — the same reason the answers
// come back in the order they were asked for rather than the order they
// arrived.
type Brief struct {
	Task
	// Number names this subagent within the run, from one.
	Number int
}

// Task is one subagent's brief. The title names the work for whoever is
// watching or reading the log; the body is for the subagent, which sees
// nothing else.
type Task struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// SubagentParams is the input schema for the subagents tool.
type SubagentParams struct {
	Tasks []Task `json:"tasks"`
}

// Subagents runs fresh agents on sub-tasks, several at a time.
type Subagents struct {
	run Runner
	// spawned counts the subagents started so far, so that a run which fans
	// out twice does not number the second group after the first.
	spawned atomic.Int64
}

// NewSubagents returns the tool, running subagents through run.
func NewSubagents(run Runner) *Subagents { return &Subagents{run: run} }

// SystemPrompt implements tools.SystemPrompter. When to hand work off is not
// something a model can infer from a schema, so the tool explains itself in
// the system prompt — and only when it has been registered.
func (*Subagents) SystemPrompt() string { return prompt.Subagents() }

// SubagentsTool is the name the tool is registered and called under.
//
// It is exported because whether a run can spawn subagents at all is a
// question asked from outside this package — a run that describes itself has
// to say what a subagent of its would be given, and has nothing to say when
// there are none — and the only true answer is whether a tool of this name was
// registered. A second spelling of it somewhere else would be a second tool as
// far as the registry is concerned, and would go on answering "no" after this
// one was renamed.
const SubagentsTool = "subagents"

// Definition describes the tool to the model. When to reach for a subagent is
// in the system prompt, where the control-flow guidance lives; what is here is
// how to call it.
func (s *Subagents) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: SubagentsTool,
		Description: strings.TrimSpace(fmt.Sprintf(`
Run fresh agents on sub-tasks and return their answers.

A call looks like this, and every call looks like this:

  {"tasks": [
    {"title": "Read the config loader", "body": "Read internal/config in this repository. Report every environment variable it reads and the default each one falls back to. List them; do not summarize."},
    {"title": "Read the CLI flags", "body": "Read cmd/ in this repository. Report every command line flag, what it does, and whether it has a default."}
  ]}

TITLE AND BODY ARE BOTH REQUIRED, ON EVERY TASK. A task missing either one is
refused and no subagent runs, not even for the tasks that were complete.

The title is a label of a few words, shown to the user while the work happens.
The subagent never sees it. The body is the task itself, and it is the only
thing the subagent receives. Putting the work in the title and leaving the body
out is the most common way this call fails: it asks an agent to do something
without telling it what. Never write a task that way, and never leave a body
empty or write it as a restatement of the title — if the body would repeat the
title, the body is not written yet.

AT LEAST %d TASKS, ALWAYS. A call with one task is refused. Not because one is
too few to be useful, but because one is the wrong shape for this tool: every
task in a call is started at the same time and runs alongside the others, and
that is the entire reason the tool exists. With one task there is nothing to run
alongside, and you would sit waiting on an agent that knows less about the
problem than you do, for work you could have finished in the time it took to
brief it. So do not ask whether to hand a piece off; ask whether the work splits
into %d or more pieces that can run at once. If it does not, do the work
yourself. Between %d and %d tasks in a call.

The answers come back together, in the order you listed the tasks, each under
its title.

A subagent is a new agent: it has not seen this conversation, cannot ask you
anything, and returns one answer and nothing else. Write each body as a brief
that stands on its own — what to do, what it needs to know, and what you want
back — and expect nothing to survive but the answer it returns.`,
			minTasks, minTasks, minTasks, maxTasks)),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type": "array",
					"description": fmt.Sprintf("The tasks to run, one subagent each. "+
						"Never fewer than %d: they run at the same time, which is the "+
						"point of the tool, and a call with one task is refused. Every "+
						"task must have both a title and a body.", minTasks),
					"minItems": minTasks,
					"maxItems": maxTasks,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"title": map[string]any{
								"type": "string",
								"description": "Required. A few words naming what this " +
									"subagent is doing, shown to the user while it works. " +
									"It is a label and nothing more: the subagent never " +
									"sees it, so it is not the task and cannot stand in " +
									"for the body.",
							},
							"body": map[string]any{
								"type": "string",
								"description": "Required, and never empty. The task itself, " +
									"and the only thing the subagent receives: it has not " +
									"seen this conversation and can read nothing else. Say " +
									"what to do, what it needs to know to do it, and what " +
									"its answer should contain. A body that only repeats " +
									"the title tells the subagent nothing.",
							},
						},
						"required":             []string{"title", "body"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"tasks"},
			"additionalProperties": false,
		},
	}
}

// parseTasks decodes and validates the model's arguments.
func parseTasks(args json.RawMessage) (SubagentParams, error) {
	var p SubagentParams
	if err := json.Unmarshal(args, &p); err != nil {
		return p, fmt.Errorf("subagents: arguments are not valid JSON: %w", err)
	}
	if len(p.Tasks) < minTasks {
		return p, fmt.Errorf("subagents: this call gave %d task(s) and needs at least %d; "+
			"the tasks in a call run at the same time, so one task has nothing to run "+
			"alongside and buys you nothing — either find the other work that can run "+
			"with it and call again with both, or drop the call and do this piece "+
			"yourself", len(p.Tasks), minTasks)
	}
	if len(p.Tasks) > maxTasks {
		return p, fmt.Errorf("subagents: %d tasks is more than the %d allowed in one call; "+
			"split the work into fewer subagents, or make several calls", len(p.Tasks), maxTasks)
	}
	// A task missing either field fails the whole call rather than being
	// dropped from it. The model asked for work it did not describe, and
	// running the rest would answer a question it did not mean to ask.
	for i, t := range p.Tasks {
		if strings.TrimSpace(t.Title) == "" {
			return p, fmt.Errorf("subagents: task %d has no title; every task needs "+
				"a title of a few words naming the work, and a body saying what the "+
				"work is; call again with both on every task", i+1)
		}
		if strings.TrimSpace(t.Body) == "" {
			return p, fmt.Errorf("subagents: task %d, %q, has no body, so there is "+
				"nothing to send the subagent; the title is a label the subagent "+
				"never sees, and the body is the whole of what it receives — write "+
				"the task out in the body: what to do, what it needs to know, and "+
				"what its answer should contain, then call again", i+1, t.Title)
		}
	}
	return p, nil
}

// Announce lists what is about to be worked on: a count, then every title.
// Subagents run silently — several at once would interleave into nonsense — so
// this list is the only thing the user sees of them, and it is printed before
// the work starts.
func (s *Subagents) Announce(args json.RawMessage) string {
	p, err := parseTasks(args)
	if err != nil {
		return ""
	}

	lines := make([]string, 0, len(p.Tasks)+1)
	lines = append(lines, fmt.Sprintf("%d subagents at once", len(p.Tasks)))
	for _, t := range p.Tasks {
		lines = append(lines, tools.Detail("  - "+strings.TrimSpace(t.Title)))
	}
	return strings.Join(lines, "\n")
}

// Result implements tools.Resulter: it runs every task at once and returns the
// answers as a turn that keeps them apart. Nothing is concatenated here — the
// text the model reads is assembled by the message itself, when it is sent.
func (s *Subagents) Result(ctx context.Context, requestID string, args json.RawMessage) (model.ToolAnswer, error) {
	results, err := s.spawn(ctx, args)
	if err != nil {
		return nil, err
	}
	return model.NewSubagentResults(requestID, results), nil
}

// Call implements tools.Tool, for a caller with no request to answer. The
// registry prefers Result; this renders the same answers as text.
func (s *Subagents) Call(ctx context.Context, args json.RawMessage) (string, error) {
	results, err := s.spawn(ctx, args)
	if err != nil {
		return "", err
	}
	return model.SubagentResults{Results: results}.Render(), nil
}

// spawn runs every task at once and collects what came back.
//
// The results are collected in the order the tasks were given, never the order
// they finished in: which subagent returns first is a matter of scheduling and
// network timing, and the model should not see a different result for the same
// call twice.
func (s *Subagents) spawn(ctx context.Context, args json.RawMessage) ([]model.SubagentResult, error) {
	p, err := parseTasks(args)
	if err != nil {
		return nil, err
	}
	if s.run == nil {
		return nil, errors.New("subagents: no way to run a subagent was configured")
	}

	// Every task is numbered here, before anything starts, so the numbering
	// follows the order the tasks were written and never the order the
	// goroutines below happen to get going. The whole block is claimed at
	// once, so two calls racing cannot interleave their numbers.
	first := int(s.spawned.Add(int64(len(p.Tasks)))) - len(p.Tasks) + 1

	results := make([]model.SubagentResult, len(p.Tasks))
	var wg sync.WaitGroup
	for i, t := range p.Tasks {
		brief := Brief{
			Task:   Task{Title: strings.TrimSpace(t.Title), Body: strings.TrimSpace(t.Body)},
			Number: first + i,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each goroutine owns one slot, so the results need no lock.
			results[i] = model.SubagentResult{
				Title:  brief.Title,
				Answer: s.answer(ctx, brief),
			}
		}()
	}
	wg.Wait()

	return results, nil
}

// answer runs one subagent and returns what to put under its title.
//
// A subagent that fails says so in its own section rather than failing the
// whole call. The others may well have succeeded, and their answers are worth
// having; the model reads what happened and decides whether the missing one
// was essential.
func (s *Subagents) answer(ctx context.Context, brief Brief) string {
	out, err := s.run(ctx, brief)
	if err != nil {
		return "This subagent failed: " + err.Error()
	}
	if out = strings.TrimSpace(out); out == "" {
		return "This subagent returned no answer."
	}
	return out
}
