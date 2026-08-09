package controlflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
)

// maxTasks caps one call, so a model that decides to fan out cannot open an
// unbounded number of conversations with the gateway at once. Over the cap the
// model is told to split the work, which it can do across several calls.
//
// TODO: parameterize this instead of hardcoding it.
const maxTasks = 8

// Runner runs one subagent: a fresh agent on task, returning its final answer.
//
// The agent loop lives in the command layer, which already imports this
// package, so the tool is handed the ability to run one rather than reaching
// for it. That also puts the decision of what a subagent may itself do where
// the tools are registered — notably that it may not spawn further subagents,
// which is what keeps a run from recursing without a bottom.
type Runner func(ctx context.Context, task string) (string, error)

// Task is one subagent's brief. The title is for the user, who sees it while
// the work happens; the body is for the subagent, which sees nothing else.
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
	Base
	run Runner
}

// NewSubagents returns the tool, running subagents through run.
func NewSubagents(run Runner) *Subagents { return &Subagents{run: run} }

// Definition describes the tool to the model. When to reach for a subagent is
// in the system prompt, where the control-flow guidance lives; what is here is
// how to call it.
func (s *Subagents) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: "subagents",
		Description: strings.TrimSpace(fmt.Sprintf(`
Run fresh agents on sub-tasks and return their answers.

Every task in one call is started at the same time and runs alongside the
others, so put independent work in one call rather than making several. The
answers come back together, in the order you listed the tasks, each under its
title. Up to %d tasks in a call.

A subagent is a new agent: it has not seen this conversation, cannot ask you
anything, and returns one answer and nothing else. Write each body as a brief
that stands on its own — what to do, what it needs to know, and what you want
back — and expect nothing to survive but the answer it returns.`, maxTasks)),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type":        "array",
					"description": "The tasks to run, one subagent each.",
					"minItems":    1,
					"maxItems":    maxTasks,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"title": map[string]any{
								"type": "string",
								"description": "A few words naming what this subagent is doing, " +
									"shown to the user while it works. Not the task itself.",
							},
							"body": map[string]any{
								"type": "string",
								"description": "The task, written to be read by an agent that " +
									"knows nothing of this conversation: what to do, what it " +
									"needs to know to do it, and what its answer should contain.",
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
	if len(p.Tasks) == 0 {
		return p, errors.New("subagents: give at least one task")
	}
	if len(p.Tasks) > maxTasks {
		return p, fmt.Errorf("subagents: %d tasks is more than the %d allowed in one call; "+
			"split the work into fewer subagents, or make several calls", len(p.Tasks), maxTasks)
	}
	for i, t := range p.Tasks {
		if strings.TrimSpace(t.Title) == "" {
			return p, fmt.Errorf("subagents: task %d has no title", i+1)
		}
		if strings.TrimSpace(t.Body) == "" {
			return p, fmt.Errorf("subagents: task %q has no body", t.Title)
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
	if n := len(p.Tasks); n == 1 {
		lines = append(lines, "1 subagent")
	} else {
		lines = append(lines, fmt.Sprintf("%d subagents at once", n))
	}
	for _, t := range p.Tasks {
		lines = append(lines, tools.Detail("  - "+strings.TrimSpace(t.Title)))
	}
	return strings.Join(lines, "\n")
}

// Call runs every task at once and returns the answers together.
//
// The answers are assembled in the order the tasks were given, never the order
// they finished in: which subagent returns first is a matter of scheduling and
// network timing, and the model should not see a different result for the same
// call twice.
func (s *Subagents) Call(ctx context.Context, args json.RawMessage) (string, error) {
	p, err := parseTasks(args)
	if err != nil {
		return "", err
	}
	if s.run == nil {
		return "", errors.New("subagents: no way to run a subagent was configured")
	}

	answers := make([]string, len(p.Tasks))
	var wg sync.WaitGroup
	for i, t := range p.Tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each goroutine owns one slot, so the answers need no lock.
			answers[i] = s.answer(ctx, t)
		}()
	}
	wg.Wait()

	sections := make([]string, 0, len(p.Tasks))
	for i, t := range p.Tasks {
		sections = append(sections, "## "+strings.TrimSpace(t.Title)+"\n"+answers[i])
	}
	return strings.Join(sections, "\n\n"), nil
}

// answer runs one subagent and returns what to put under its title.
//
// A subagent that fails says so in its own section rather than failing the
// whole call. The others may well have succeeded, and their answers are worth
// having; the model reads what happened and decides whether the missing one
// was essential.
func (s *Subagents) answer(ctx context.Context, t Task) string {
	out, err := s.run(ctx, t.Body)
	if err != nil {
		return "This subagent failed: " + err.Error()
	}
	if out = strings.TrimSpace(out); out == "" {
		return "This subagent returned no answer."
	}
	return out
}
