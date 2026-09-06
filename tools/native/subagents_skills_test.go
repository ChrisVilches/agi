package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/skills"
)

// spawnedBriefs runs a call and returns the briefs the subagents were given,
// in the order the tasks were listed.
func spawnedBriefs(t *testing.T, s *Subagents, args json.RawMessage) []Brief {
	t.Helper()

	got := make([]Brief, 0, 2)
	done := make(chan Brief, 8)
	s.run = func(_ context.Context, brief Brief) (string, error) {
		done <- brief
		return "done", nil
	}
	if _, err := s.Call(context.Background(), args); err != nil {
		t.Fatalf("Call() = %v", err)
	}
	close(done)
	for brief := range done {
		got = append(got, brief)
	}
	return got
}

// briefFor returns the brief of the subagent with the given title.
func briefFor(t *testing.T, briefs []Brief, title string) Brief {
	t.Helper()
	for _, brief := range briefs {
		if brief.Title == title {
			return brief
		}
	}
	t.Fatalf("no subagent was spawned for %q", title)
	return Brief{}
}

// skillNames renders a brief's skills by the id they were named under, for a
// message about them.
func skillNames(found []skills.Skill) string {
	ids := make([]string, 0, len(found))
	for _, s := range found {
		ids = append(ids, s.ID)
	}
	return strings.Join(ids, ",")
}

// A task names the skills its subagent needs, and gets those and no others.
func TestATaskGivesItsSubagentTheSkillsItNames(t *testing.T) {
	available := skillsIn(t, "writing", "testing", "drawing")
	s := NewSubagents(nil, available, nil, testMost)

	briefs := spawnedBriefs(t, s, json.RawMessage(`{"tasks": [
		{"title": "Write", "body": "write it", "skills": ["writing"]},
		{"title": "Test", "body": "test it", "skills": ["testing", "drawing"]}
	]}`))

	if got := skillNames(briefFor(t, briefs, "Write").Skills); got != "writing" {
		t.Errorf("the first subagent was given %s, want writing", got)
	}
	// The run found them sorted, so "drawing" comes before "testing" whichever
	// way round the task listed them.
	if got := skillNames(briefFor(t, briefs, "Test").Skills); got != "drawing,testing" {
		t.Errorf("the second subagent was given %s, want drawing,testing", got)
	}
}

// A subagent starts with none, so a task that names none gives none.
func TestATaskThatNamesNoSkillsGivesNone(t *testing.T) {
	s := NewSubagents(nil, skillsIn(t, "writing"), nil, testMost)

	briefs := spawnedBriefs(t, s, json.RawMessage(`{"tasks": [
		{"title": "One", "body": "a"},
		{"title": "Two", "body": "b", "skills": []}
	]}`))

	for _, brief := range briefs {
		if len(brief.Skills) != 0 {
			t.Errorf("%q was given %s, want no skills", brief.Title, skillNames(brief.Skills))
		}
	}
}

// The skills come out in the order the run found them, not the order the model
// listed them, so the same set reaches a subagent the same way every time.
func TestTheSkillsOfATaskAreInTheRunsOrder(t *testing.T) {
	s := NewSubagents(nil, skillsIn(t, "alpha", "beta", "gamma"), nil, testMost)

	briefs := spawnedBriefs(t, s, json.RawMessage(`{"tasks": [
		{"title": "One", "body": "a", "skills": ["gamma", "alpha"]},
		{"title": "Two", "body": "b", "skills": ["alpha", "gamma"]}
	]}`))

	for _, brief := range briefs {
		if got := skillNames(brief.Skills); got != "alpha,gamma" {
			t.Errorf("%q was given %s, want alpha,gamma", brief.Title, got)
		}
	}
}

// A set was asked for, and there is nothing to be done twice.
func TestASkillNamedTwiceIsOneSkill(t *testing.T) {
	s := NewSubagents(nil, skillsIn(t, "writing", "testing"), nil, testMost)

	briefs := spawnedBriefs(t, s, json.RawMessage(`{"tasks": [
		{"title": "One", "body": "a", "skills": ["writing", "writing"]},
		{"title": "Two", "body": "b", "skills": ["testing"]}
	]}`))

	if got := skillNames(briefFor(t, briefs, "One").Skills); got != "writing" {
		t.Errorf("the subagent was given %s, want writing once", got)
	}
}

// A name nobody can resolve fails the whole call, the way a missing title
// does: the model asked for a subagent that knows something, and running it
// without that answers a question it did not ask.
func TestAnUnknownSkillNameFailsTheCall(t *testing.T) {
	s := NewSubagents(nil, skillsIn(t, "writing", "testing"), nil, testMost)
	ran := false
	s.run = func(context.Context, Brief) (string, error) {
		ran = true
		return "", nil
	}

	_, err := s.Call(context.Background(), json.RawMessage(`{"tasks": [
		{"title": "Fine", "body": "a", "skills": ["writing"]},
		{"title": "Bad", "body": "b", "skills": ["drawing"]}
	]}`))
	if err == nil {
		t.Fatal("Call() accepted a skill that does not exist")
	}
	if ran {
		t.Error("a subagent was spawned for a call that should not have been made")
	}

	// The message says which task was wrong, which name it got wrong, and
	// what the names actually are.
	for _, want := range []string{"task 2", `"Bad"`, `"drawing"`, "writing", "testing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Call() = %v, want it to mention %s", err, want)
		}
	}
}

// A skill is named here by its id, and a task that reached for the title
// beside it in the list is told which id it meant rather than only refused.
func TestATaskThatNamesASkillsTitleIsCorrected(t *testing.T) {
	s := NewSubagents(echoRunner, skillsIn(t, "writing"), nil, testMost)

	_, err := s.Call(context.Background(), json.RawMessage(`{"tasks": [
		{"title": "One", "body": "a", "skills": ["`+titleOf("writing")+`"]},
		{"title": "Two", "body": "b"}
	]}`))
	if err == nil {
		t.Fatal("Call() accepted a skill named by its title")
	}
	if !strings.Contains(err.Error(), `"writing"`) {
		t.Errorf("Call() = %v, want the id the task meant", err)
	}
}

// A run with no skills offers no field and accepts no name, so a model that
// invents one is told there are none rather than left guessing.
func TestARunWithNoSkillsRefusesATaskThatNamesOne(t *testing.T) {
	s := subagentsWith(echoRunner)

	_, err := s.Call(context.Background(), json.RawMessage(`{"tasks": [
		{"title": "One", "body": "a", "skills": ["writing"]},
		{"title": "Two", "body": "b"}
	]}`))
	if err == nil {
		t.Fatal("Call() accepted a skill in a run that has none")
	}
	if !strings.Contains(err.Error(), "no skills") {
		t.Errorf("Call() = %v, want it to say the run was given none", err)
	}
}

func TestASkillNameOfNothingButSpacesIsRefused(t *testing.T) {
	s := NewSubagents(echoRunner, skillsIn(t, "writing"), nil, testMost)

	_, err := s.Call(context.Background(), json.RawMessage(`{"tasks": [
		{"title": "One", "body": "a", "skills": ["   "]},
		{"title": "Two", "body": "b"}
	]}`))
	if err == nil {
		t.Error("Call() accepted a skill named by nothing but spaces")
	}
}

// A run with no skills says nothing about them, the same way it registers no
// tool for loading one.
func TestTheSkillsFieldIsOfferedOnlyWhenThereAreSkills(t *testing.T) {
	without := subagentsWith(echoRunner).Definition()
	if taskProperties(t, without)["skills"] != nil {
		t.Error("a run with no skills offers a skills field on a task")
	}
	if strings.Contains(without.Description, "skills") {
		t.Errorf("a run with no skills describes them:\n%s", without.Description)
	}

	with := NewSubagents(echoRunner, skillsIn(t, "writing"), nil, testMost).Definition()
	if taskProperties(t, with)["skills"] == nil {
		t.Error("a run with skills offers no skills field on a task")
	}
	if !strings.Contains(with.Description, "skills") {
		t.Errorf("a run with skills does not describe them:\n%s", with.Description)
	}
	// It stays optional: a subagent given none is the ordinary case.
	items := taskItems(t, with)
	required, _ := items["required"].([]string)
	for _, name := range required {
		if name == "skills" {
			t.Error("a task is required to name skills, and it must not be")
		}
	}
}

// taskItems returns the schema of one task in a call.
func taskItems(t *testing.T, def model.ToolDefinition) map[string]any {
	t.Helper()

	properties, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("the parameters have no properties: %v", def.Parameters)
	}
	tasks, ok := properties["tasks"].(map[string]any)
	if !ok {
		t.Fatalf("the parameters have no tasks: %v", properties)
	}
	items, ok := tasks["items"].(map[string]any)
	if !ok {
		t.Fatalf("the tasks have no item schema: %v", tasks)
	}
	return items
}

// taskProperties returns the fields one task may have.
func taskProperties(t *testing.T, def model.ToolDefinition) map[string]any {
	t.Helper()

	fields, ok := taskItems(t, def)["properties"].(map[string]any)
	if !ok {
		t.Fatalf("a task has no properties: %v", taskItems(t, def))
	}
	return fields
}
