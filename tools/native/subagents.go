package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/skills"
	"github.com/ChrisVilches/agi/tools"
)

// MinTasks is the fewest tasks one call may carry. A call with fewer is
// refused rather than merely discouraged: the model is told what was wrong
// with it and gets a turn to make a better one.
//
// It is two because one subagent is never worth the round trip — the parent
// would wait on an agent that knows less about the problem than it does, for
// work it could have finished in the time it took to write the brief. That is
// a property of the tool and not of the work, which is why, unlike the
// maximum, it is not something a run may change: no job is long enough to make
// a fan-out of one worth making.
//
// It is exported because it is also the smallest cap --max-subagents may be
// given, and that flag is refused in the command layer, where the user can act
// on it. A second spelling of the number over there would be a second floor
// the first time either of them was changed.
const MinTasks = 2

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
	// Skills are the skills this subagent is to be given, already looked up
	// from the names its task listed. They are in the order the run found
	// them, not the order the names were written, so the same set of skills
	// reaches a subagent the same way however the model happened to list them.
	Skills []skills.Skill
}

// Task is one subagent's brief. The title names the work for whoever is
// watching or reading the log; the body is for the subagent, which sees
// nothing else.
//
// SkillNames is what the model writes and not what a subagent gets: the names
// are checked against the skills the run was given and looked up before
// anything is spawned, and what comes out of that is the Brief's Skills.
type Task struct {
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	SkillNames []string `json:"skills"`
}

// SubagentParams is the input schema for the subagents tool.
type SubagentParams struct {
	Tasks []Task `json:"tasks"`
}

// Subagents runs fresh agents on sub-tasks, several at a time.
type Subagents struct {
	run Runner
	// available is every skill the run was given, in the order it found them.
	// A task may name any of them for its subagent and nothing else, so this
	// is both what a name is checked against and what a checked name becomes.
	available []skills.Skill
	// spawned hands out the numbers that name subagents. It belongs to the
	// run rather than to this tool, because a fork builds a second Subagents
	// and two tools counting from one would name two agents alike.
	spawned *Counter
	// most is the largest number of tasks one call may carry, which is the
	// run's to decide: how wide a fan-out is worth making depends on the work
	// and on what the user is willing to have open at the gateway at once, and
	// this tool is the last place that would know either.
	most int
}

// NewSubagents returns the tool, running subagents through run, letting a task
// draw on any of found, naming what it spawns from spawned, and accepting at
// most most tasks in one call.
//
// The skills are passed here rather than reached for when a call arrives,
// because what a subagent may be given is settled by how the run was invoked
// and cannot change partway through it. A run given none accepts no skills on
// a task and does not offer the field.
//
// The counter is passed in for the opposite reason: it is the one thing here
// that is the run's and not this tool's, since a run may hold more than one of
// these. A nil counter gets one of this tool's own, which is right only for a
// caller that will never build a second — a test, or the throwaway registry
// that collects the reserved tool names.
//
// The cap arrives already checked, since --max-subagents refuses anything
// below MinTasks while the command line is read. A caller with no opinion
// about it — the same test, the same throwaway registry — passes nothing and
// gets MinTasks, because a tool has to advertise a width it would accept and
// one that refused every call would describe itself as a tool nobody may use.
func NewSubagents(run Runner, found []skills.Skill, spawned *Counter, most int) *Subagents {
	if spawned == nil {
		spawned = &Counter{}
	}
	if most < MinTasks {
		most = MinTasks
	}
	return &Subagents{run: run, available: found, spawned: spawned, most: most}
}

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
// in the system prompt, where a native tool's guidance lives; what is here is
// how to call it.
func (s *Subagents) Definition() model.ToolDefinition {
	// What a task may say is what a task may say, so the field is offered only
	// where it would be accepted: a run with no skills does not advertise one.
	taskFields := map[string]any{
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
	}
	if len(s.available) > 0 {
		taskFields["skills"] = s.skillsParameter()
	}

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
			MinTasks, MinTasks, MinTasks, s.most)) + s.describeSkills(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type": "array",
					"description": fmt.Sprintf("The tasks to run, one subagent each. "+
						"Never fewer than %d: they run at the same time, which is the "+
						"point of the tool, and a call with one task is refused. Every "+
						"task must have both a title and a body.", MinTasks),
					"minItems": MinTasks,
					"maxItems": s.most,
					"items": map[string]any{
						"type":                 "object",
						"properties":           taskFields,
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

// describeSkills is the part of the description about handing a subagent
// skills, and is empty in a run that was given none.
//
// A run with no skills says nothing about them, the same way it registers no
// tool for loading one: a field the model could fill in and never have
// accepted is worse than no field at all.
func (s *Subagents) describeSkills() string {
	if len(s.available) == 0 {
		return ""
	}
	return "\n\n" + strings.TrimSpace(`
A task may also name skills, and its subagent is given those and only those:

  {"title": "Tidy the export", "body": "...", "skills": ["data-analyzer"]}

A subagent starts with none, and is told nothing about the ones it was not
given. Name the skills that apply to its piece of the work: you are already
deciding what each piece is, so which skills it needs is part of that same
decision and costs you nothing extra to say. The subagent then loads them the
way you load yours, when it gets to the work they are about.

A skill is named here by its id, the one it is listed under in bold and loaded
by, never by the title beside it. The ids must be ones from your own list of
available skills. An id that is not in it is refused and no subagent runs, not
even for the tasks that were right — so do not guess at an id, and do not
invent a skill you would like to exist.`)
}

// skillsParameter is the schema for a task's skills, offered only in a run
// that has skills to offer.
func (s *Subagents) skillsParameter() map[string]any {
	return map[string]any{
		"type": "array",
		"description": "Optional. The skills to give this subagent, by id — the " +
			"name each is listed under in bold, not the title beside it — from " +
			"the skills available to you. It gets these and no others, and " +
			"starts with none if you list none. An id that is not one of yours " +
			"is refused and no subagent runs.",
		"items": map[string]any{"type": "string"},
	}
}

// parseTasks decodes and validates the model's arguments.
func (s *Subagents) parseTasks(args json.RawMessage) (SubagentParams, error) {
	var p SubagentParams
	if err := json.Unmarshal(args, &p); err != nil {
		return p, fmt.Errorf("subagents: arguments are not valid JSON: %w", err)
	}
	if len(p.Tasks) < MinTasks {
		return p, fmt.Errorf("subagents: this call gave %d task(s) and needs at least %d; "+
			"the tasks in a call run at the same time, so one task has nothing to run "+
			"alongside and buys you nothing — either find the other work that can run "+
			"with it and call again with both, or drop the call and do this piece "+
			"yourself", len(p.Tasks), MinTasks)
	}
	if len(p.Tasks) > s.most {
		return p, fmt.Errorf("subagents: %d tasks is more than the %d allowed in one call; "+
			"split the work into fewer subagents, or make several calls", len(p.Tasks), s.most)
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
		if _, err := s.resolveSkills(t.SkillNames); err != nil {
			return p, fmt.Errorf("subagents: task %d, %q: %w", i+1, strings.TrimSpace(t.Title), err)
		}
	}
	return p, nil
}

// resolveSkills turns the ids a task listed into the skills to give its
// subagent, and refuses an id that is not one of the run's own.
//
// An id nobody can resolve fails the whole call rather than being dropped from
// one task, the way a missing title does. The model asked for a subagent that
// knows something, and running it without that is answering a question it did
// not ask; and an id it invented is worth correcting, since the next call would
// invent it again.
//
// What comes back is in the order the run found the skills, not the order the
// ids were written, so the same set reaches a subagent the same way however the
// model listed it. An id repeated within one task is one skill: it is a set
// that was asked for, and there is nothing to be done twice.
func (s *Subagents) resolveSkills(ids []string) ([]skills.Skill, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	wanted := make(map[string]bool, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, fmt.Errorf("names a skill that is nothing but spaces; %s", s.availableSkills())
		}
		if !s.hasSkill(id) {
			return nil, fmt.Errorf("names the skill %q, which is not one of yours%s; %s",
				id, s.titledSkill(id), s.availableSkills())
		}
		wanted[id] = true
	}

	chosen := make([]skills.Skill, 0, len(wanted))
	for _, skill := range s.available {
		if wanted[skill.ID] {
			chosen = append(chosen, skill)
		}
	}
	return chosen, nil
}

// hasSkill reports whether the run was given a skill with that id.
func (s *Subagents) hasSkill(id string) bool {
	for _, skill := range s.available {
		if skill.ID == id {
			return true
		}
	}
	return false
}

// titledSkill reports, as a clause to hang on a refusal, that what was asked
// for is a skill's title rather than its id — the one wrong name the list of
// skills invites, since every skill is shown under both. The loadskill tool
// makes the same correction for the same reason.
func (s *Subagents) titledSkill(asked string) string {
	for _, skill := range s.available {
		if skill.Name == asked {
			return fmt.Sprintf(" (it is the title of the skill whose id is %q, which is "+
				"what to name here)", skill.ID)
		}
	}
	return ""
}

// availableSkills names what a task could have asked for, since a model one
// word away from an id it has is one correction away from the call it meant.
func (s *Subagents) availableSkills() string {
	if len(s.available) == 0 {
		return "this run was given no skills, so a task cannot name any"
	}

	ids := make([]string, 0, len(s.available))
	for _, skill := range s.available {
		ids = append(ids, skill.ID)
	}
	return "the skill ids you can give a subagent are: " + strings.Join(ids, ", ")
}

// Announce lists what is about to be worked on: a count, then every title.
// Subagents run silently — several at once would interleave into nonsense — so
// this list is the only thing the user sees of them, and it is printed before
// the work starts.
func (s *Subagents) Announce(args json.RawMessage) string {
	p, err := s.parseTasks(args)
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
	p, err := s.parseTasks(args)
	if err != nil {
		return nil, err
	}
	if s.run == nil {
		return nil, errors.New("subagents: no way to run a subagent was configured")
	}

	// Every task is numbered here, before anything starts, so the numbering
	// follows the order the tasks were written and never the order the
	// goroutines below happen to get going.
	first := s.spawned.Take(len(p.Tasks))

	results := make([]model.SubagentResult, len(p.Tasks))
	var wg sync.WaitGroup
	for i, t := range p.Tasks {
		// Every name was checked while the call was parsed, so nothing here
		// can be refused any more: a call that was going to fail failed
		// before the first subagent started.
		chosen, err := s.resolveSkills(t.SkillNames)
		if err != nil {
			return nil, err
		}
		brief := Brief{
			Task:   Task{Title: strings.TrimSpace(t.Title), Body: strings.TrimSpace(t.Body)},
			Number: first + i,
			Skills: chosen,
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
