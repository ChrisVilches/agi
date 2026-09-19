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
	Base
	run Runner
	// spawned counts the subagents started so far, so that a run which fans
	// out twice does not number the second group after the first.
	spawned atomic.Int64
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
title.

Always give at least two tasks; a call with one is refused. A single subagent
buys you nothing: you would wait on an agent that knows less about the problem
than you do, to do work you could have finished in the time it took to brief
it. If only one piece of work needs doing, do it yourself. Between %d and %d
tasks in a call.

A subagent is a new agent: it has not seen this conversation, cannot ask you
anything, and returns one answer and nothing else. Write each body as a brief
that stands on its own — what to do, what it needs to know, and what you want
back — and expect nothing to survive but the answer it returns.`, minTasks, maxTasks)),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type":        "array",
					"description": "The tasks to run, one subagent each. Always two or more.",
					"minItems":    minTasks,
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
	if len(p.Tasks) < minTasks {
		return p, fmt.Errorf("subagents: give at least %d subagent tasks; "+
			"one subagent is not worth the round trip, so either find the other work "+
			"that can run alongside it or do this piece yourself", minTasks)
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
