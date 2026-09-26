package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChrisVilches/agi/tools"
)

// tasksJSON builds the arguments a model would send.
func tasksJSON(t *testing.T, tasks ...Task) json.RawMessage {
	t.Helper()
	args, err := json.Marshal(SubagentParams{Tasks: tasks})
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	return args
}

// echoRunner answers with what it was asked, so a test can see which task
// produced which section.
func echoRunner(_ context.Context, brief Brief) (string, error) {
	return "answer to " + brief.Body, nil
}

func TestSubagentsIsATool(t *testing.T) {
	var _ tools.Tool = subagentsWith(echoRunner)
	var _ tools.Announcer = subagentsWith(echoRunner)
	var _ tools.SystemPrompter = subagentsWith(echoRunner)

	def := subagentsWith(echoRunner).Definition()
	if def.Name != "subagents" {
		t.Errorf("Name = %q, want %q", def.Name, "subagents")
	}
	if def.Description == "" {
		t.Error("Description is empty, and it is how the model learns to call the tool")
	}
}

// Answers come back under their titles, in the order the tasks were given.
func TestCallReturnsAnswersInTheOrderAsked(t *testing.T) {
	s := subagentsWith(echoRunner)
	args := tasksJSON(t,
		Task{Title: "Read the README", Body: "first"},
		Task{Title: "Check the tests", Body: "second"},
		Task{Title: "Count the files", Body: "third"},
	)

	got, err := s.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("Call() = %v, want nil", err)
	}

	want := "=== subagent result #1 of 3: Read the README ===\nanswer to first\n\n" +
		"=== subagent result #2 of 3: Check the tests ===\nanswer to second\n\n" +
		"=== subagent result #3 of 3: Count the files ===\nanswer to third"
	if got != want {
		t.Errorf("Call() = %q, want %q", got, want)
	}
}

// Which subagent finishes first is scheduling, and the same call must not
// produce a different result for it.
func TestOrderDoesNotDependOnWhoFinishesFirst(t *testing.T) {
	// The first task is made to finish last.
	delays := map[string]time.Duration{"slow": 40 * time.Millisecond}
	s := subagentsWith(func(_ context.Context, brief Brief) (string, error) {
		time.Sleep(delays[brief.Body])
		return brief.Body, nil
	})

	got, err := s.Call(context.Background(), tasksJSON(t,
		Task{Title: "Slow", Body: "slow"},
		Task{Title: "Fast", Body: "fast"},
	))
	if err != nil {
		t.Fatalf("Call() = %v, want nil", err)
	}
	want := "=== subagent result #1 of 2: Slow ===\nslow\n\n=== subagent result #2 of 2: Fast ===\nfast"
	if got != want {
		t.Errorf("Call() = %q, want the order the tasks were asked in: %q", got, want)
	}
}

// The point of a call with several tasks is that they run at the same time.
func TestSubagentsRunAtTheSameTime(t *testing.T) {
	const n = 4
	var (
		mu      sync.Mutex
		running int
		most    int
	)
	started := make(chan struct{}, n)

	s := subagentsWith(func(_ context.Context, brief Brief) (string, error) {
		mu.Lock()
		running++
		if running > most {
			most = running
		}
		mu.Unlock()
		started <- struct{}{}

		// Hold until every subagent has started, which can only happen if they
		// are not run one after another.
		for i := 0; i < n; i++ {
			<-started
			started <- struct{}{}
			time.Sleep(time.Millisecond)
		}

		mu.Lock()
		running--
		mu.Unlock()
		return brief.Body, nil
	})

	tasks := make([]Task, n)
	for i := range tasks {
		tasks[i] = Task{Title: fmt.Sprintf("Task %d", i), Body: fmt.Sprintf("body %d", i)}
	}
	if _, err := s.Call(context.Background(), tasksJSON(t, tasks...)); err != nil {
		t.Fatalf("Call() = %v, want nil", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if most < n {
		t.Errorf("at most %d subagents ran at once, want all %d", most, n)
	}
}

// One subagent failing must not throw away the answers of the others.
func TestAFailedSubagentIsReportedInItsOwnSection(t *testing.T) {
	s := subagentsWith(func(_ context.Context, brief Brief) (string, error) {
		if brief.Body == "doomed" {
			return "", errors.New("gateway exploded")
		}
		return "fine", nil
	})

	got, err := s.Call(context.Background(), tasksJSON(t,
		Task{Title: "Works", Body: "ok"},
		Task{Title: "Fails", Body: "doomed"},
	))
	if err != nil {
		t.Fatalf("Call() = %v, want the call itself to succeed", err)
	}
	if !strings.Contains(got, "#1 of 2: Works ===\nfine") {
		t.Errorf("Call() = %q, want the answer that did arrive", got)
	}
	if !strings.Contains(got, "gateway exploded") {
		t.Errorf("Call() = %q, want the failure reported under its title", got)
	}
}

// A subagent that says nothing must not leave an empty section the model has
// to guess at.
func TestASilentSubagentSaysSo(t *testing.T) {
	s := subagentsWith(func(context.Context, Brief) (string, error) { return "  \n ", nil })
	got, err := s.Call(context.Background(), tasksJSON(t,
		Task{Title: "Quiet", Body: "say nothing"},
		Task{Title: "Also quiet", Body: "say nothing either"}))
	if err != nil {
		t.Fatalf("Call() = %v, want nil", err)
	}
	if !strings.Contains(got, "no answer") {
		t.Errorf("Call() = %q, want the empty answer called out", got)
	}
}

func TestCallRejectsBadArguments(t *testing.T) {
	many := make([]Task, testMost+1)
	for i := range many {
		many[i] = Task{Title: "T", Body: "B"}
	}

	tests := []struct {
		name    string
		args    json.RawMessage
		wantErr string
	}{
		{"not JSON", json.RawMessage("{oh no"), "not valid JSON"},
		{"no tasks at all", json.RawMessage(`{}`), "at least 2"},
		{"an empty list", json.RawMessage(`{"tasks": []}`), "at least 2"},
		{
			"a task with no title",
			json.RawMessage(`{"tasks": [{"title": "Fine", "body": "ok"}, {"body": "do it"}]}`),
			"no title",
		},
		{
			"a task with no body",
			json.RawMessage(`{"tasks": [{"title": "Fine", "body": "ok"}, {"title": "Do it"}]}`),
			"no body",
		},
		{"only one task", tasksJSON(t, Task{Title: "Alone", Body: "b"}), "at least 2"},
		{"more tasks than allowed", tasksJSON(t, many...), "split the work"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ran := false
			s := subagentsWith(func(context.Context, Brief) (string, error) {
				ran = true
				return "", nil
			})
			got, err := s.Call(context.Background(), tt.args)
			if err == nil {
				t.Fatalf("Call() = %q, want an error", got)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Call() = %q, want it to mention %q", err, tt.wantErr)
			}
			if ran {
				t.Error("a subagent was spawned for a call that should not have been made")
			}
		})
	}
}

// Without a way to run one, the tool must say so rather than return nothing.
func TestCallWithoutARunner(t *testing.T) {
	s := subagentsWith(nil)
	args := tasksJSON(t, Task{Title: "T", Body: "B"}, Task{Title: "U", Body: "C"})
	if _, err := s.Call(context.Background(), args); err == nil {
		t.Error("Call() = nil, want an error when there is no way to run a subagent")
	}
}

// Subagents work silently, so the announcement is all the user sees of them:
// it has to name every one.
func TestAnnounceListsEveryTitle(t *testing.T) {
	s := subagentsWith(echoRunner)
	got := s.Announce(tasksJSON(t,
		Task{Title: "Read the README", Body: "a"},
		Task{Title: "Check the tests", Body: "b"},
	))

	if !strings.Contains(got, "2 subagents") {
		t.Errorf("Announce() = %q, want the number of subagents", got)
	}
	for _, title := range []string{"Read the README", "Check the tests"} {
		if !strings.Contains(got, title) {
			t.Errorf("Announce() = %q, want it to show %q", got, title)
		}
	}
	if strings.Contains(got, "\na") || strings.Contains(got, "\nb") {
		t.Errorf("Announce() = %q, want the titles only, not the task bodies", got)
	}

	// A call too small to be made announces nothing: Announce runs before the
	// call, and there is nothing about to happen to describe.
	if one := s.Announce(tasksJSON(t, Task{Title: "Just the one", Body: "a"})); one != "" {
		t.Errorf("Announce() of a refusable call = %q, want nothing", one)
	}
}

// Announce is display only and must never fail.
func TestAnnounceSurvivesBadArguments(t *testing.T) {
	if got := subagentsWith(echoRunner).Announce(json.RawMessage("{oh no")); got != "" {
		t.Errorf("Announce() = %q, want nothing for unparsable arguments", got)
	}
}

// Cancelling the run must reach the subagents, not just the caller.
func TestCallPassesCancellationOn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := subagentsWith(func(ctx context.Context, brief Brief) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	go cancel()

	got, err := s.Call(ctx, tasksJSON(t,
		Task{Title: "Waits", Body: "forever"},
		Task{Title: "Also waits", Body: "forever too"}))
	if err != nil {
		t.Fatalf("Call() = %v, want the call to return", err)
	}
	if !strings.Contains(got, "context canceled") {
		t.Errorf("Call() = %q, want the cancellation reported", got)
	}
}

// flattened lowercases text and runs it onto one line, so a test can look for
// a phrase without caring where the prose happens to wrap.
func flattened(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// A call with one subagent is never the right move, and the only place that
// can be said is the tool's own description.
func TestDescriptionRulesOutASingleSubagent(t *testing.T) {
	def := subagentsWith(echoRunner).Definition()

	got := flattened(def.Description)
	for _, want := range []string{
		fmt.Sprintf("at least %d tasks", MinTasks),
		"a call with one task is refused",
		"do the work yourself",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("description does not say %q, so a model may spawn just one", want)
		}
	}

	tasks, ok := def.Parameters["properties"].(map[string]any)["tasks"].(map[string]any)
	if !ok {
		t.Fatalf("parameters have no tasks array: %+v", def.Parameters)
	}
	if tasks["minItems"] != MinTasks {
		t.Errorf("minItems = %v, want the schema to agree with the description", tasks["minItems"])
	}
	if desc, _ := tasks["description"].(string); !strings.Contains(flattened(desc), fmt.Sprintf("%d", MinTasks)) {
		t.Errorf("the tasks parameter is described as %q, and does not say how few is too few", desc)
	}
}

// Models were calling this tool with the work written in the title and no body
// at all, which asks an agent to do something without telling it what. Both
// fields are required, and both places that could say so have to say so.
func TestBothFieldsAreInsistedOn(t *testing.T) {
	def := subagentsWith(echoRunner).Definition()

	description := flattened(def.Description)
	for _, want := range []string{"title and body are both required", "the title is a label"} {
		if !strings.Contains(description, want) {
			t.Errorf("description does not say %q, so a model may leave a body out", want)
		}
	}

	items := def.Parameters["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
	props, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("a task has no properties: %+v", items)
	}

	required, ok := items["required"].([]string)
	if !ok || len(required) != 2 {
		t.Fatalf("required = %v, want both fields", items["required"])
	}
	for _, name := range required {
		if _, ok := props[name]; !ok {
			t.Errorf("%q is required but not described", name)
		}
		desc := flattened(props[name].(map[string]any)["description"].(string))
		if !strings.Contains(desc, "required") {
			t.Errorf("the %s is described as %q, and never says it is required", name, desc)
		}
	}

	// The body is the one the model leaves out, so its description has to say
	// what the title is not.
	body := flattened(props["body"].(map[string]any)["description"].(string))
	if !strings.Contains(body, "only thing the subagent receives") {
		t.Errorf("the body is described as %q, and does not say it is all the subagent gets", body)
	}
}

// The words a model reads when it gets the call wrong are its best chance of
// getting it right next time, so they say what to do and not only what failed.
func TestRejectionsSayHowToFixTheCall(t *testing.T) {
	tests := []struct {
		name string
		args json.RawMessage
		want []string
	}{
		{
			"a task with no body",
			json.RawMessage(`{"tasks": [{"title": "Fine", "body": "ok"}, {"title": "Do it"}]}`),
			[]string{"no body", "the title is a label", "call again"},
		},
		{
			"only one task",
			json.RawMessage(`{"tasks": [{"title": "Alone", "body": "b"}]}`),
			[]string{"at the same time", "do this piece yourself"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := subagentsWith(echoRunner).Call(context.Background(), tt.args)
			if err == nil {
				t.Fatal("Call() = nil, want an error")
			}
			got := flattened(err.Error())
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("the model is told %q, which does not mention %q", err, want)
				}
			}
		})
	}
}

// A call with one subagent is refused, and the model is told what to do about
// it rather than merely that it was wrong.
func TestASingleSubagentIsRefused(t *testing.T) {
	ran := false
	s := subagentsWith(func(context.Context, Brief) (string, error) {
		ran = true
		return "", nil
	})

	got, err := s.Call(context.Background(), tasksJSON(t, Task{Title: "On its own", Body: "the only task"}))
	if err == nil {
		t.Fatalf("Call() = %q, want one task refused", got)
	}
	if ran {
		t.Error("a subagent was spawned for a call that should have been refused")
	}
	for _, want := range []string{"at least 2", "yourself"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Call() = %q, want it to mention %q so the model can act on it", err, want)
		}
	}
}

// The runner is given the whole brief, not just the body: the subagent reads
// the body alone, but whatever runs it needs the title and the numbering to
// tell several agents working at once apart.
func TestTheRunnerIsGivenTheWholeBrief(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []Brief
	)
	s := subagentsWith(func(_ context.Context, brief Brief) (string, error) {
		mu.Lock()
		seen = append(seen, brief)
		mu.Unlock()
		return "done", nil
	})

	if _, err := s.Call(context.Background(), tasksJSON(t,
		Task{Title: "  Read the README  ", Body: "read it"},
		Task{Title: "Check the tests", Body: "run them"},
	)); err != nil {
		t.Fatalf("Call() = %v, want nil", err)
	}

	briefs := make(map[string]Brief, len(seen))
	for _, b := range seen {
		briefs[b.Title] = b
	}
	readme, ok := briefs["Read the README"]
	if !ok {
		t.Fatalf("the runner saw %v, want each title, trimmed", briefs)
	}
	if readme.Body != "read it" {
		t.Errorf("the first task arrived with body %q", readme.Body)
	}
	if readme.Number != 1 {
		t.Errorf("the first task was numbered %d, want 1", readme.Number)
	}
	if tests := briefs["Check the tests"]; tests.Number != 2 {
		t.Errorf("the second task was numbered %d, want 2", tests.Number)
	}
}

// A subagent is numbered by where its task was written, never by which
// goroutine got going first, so the same call names the same agents whatever
// the scheduler does with it.
func TestNumberingFollowsTheTasksAndNotTheScheduler(t *testing.T) {
	// The first task is made to start last.
	var (
		mu    sync.Mutex
		order = map[string]int{}
	)
	s := subagentsWith(func(_ context.Context, brief Brief) (string, error) {
		if brief.Body == "slow" {
			time.Sleep(30 * time.Millisecond)
		}
		mu.Lock()
		order[brief.Body] = brief.Number
		mu.Unlock()
		return "done", nil
	})

	if _, err := s.Call(context.Background(), tasksJSON(t,
		Task{Title: "Slow", Body: "slow"},
		Task{Title: "Fast", Body: "fast"},
	)); err != nil {
		t.Fatalf("Call() = %v, want nil", err)
	}
	if order["slow"] != 1 || order["fast"] != 2 {
		t.Errorf("subagents were numbered %v, want the order the tasks were given", order)
	}
}

// The numbering keeps counting across calls, so a run that fans out twice does
// not file the second group's subagents under the first group's names.
func TestNumberingContinuesAcrossCalls(t *testing.T) {
	var (
		mu      sync.Mutex
		numbers []int
	)
	s := subagentsWith(func(_ context.Context, brief Brief) (string, error) {
		mu.Lock()
		numbers = append(numbers, brief.Number)
		mu.Unlock()
		return "done", nil
	})

	args := tasksJSON(t, Task{Title: "One", Body: "a"}, Task{Title: "Two", Body: "b"})
	for i := 0; i < 2; i++ {
		if _, err := s.Call(context.Background(), args); err != nil {
			t.Fatalf("Call() = %v, want nil", err)
		}
	}

	sort.Ints(numbers)
	want := []int{1, 2, 3, 4}
	if len(numbers) != len(want) {
		t.Fatalf("numbers were %v, want %v", numbers, want)
	}
	for i := range want {
		if numbers[i] != want[i] {
			t.Fatalf("numbers were %v, want %v", numbers, want)
		}
	}
}

// The description shows the model a call. A model copies what it is shown, so
// what it is shown has to be a document that would actually be accepted.
func TestTheExampleCallIsValid(t *testing.T) {
	description := subagentsWith(echoRunner).Definition().Description

	start := strings.Index(description, `{"tasks"`)
	end := strings.Index(description, "]}")
	if start < 0 || end < 0 {
		t.Fatal("the description shows no example call")
	}
	example := json.RawMessage(description[start : end+2])

	if !json.Valid(example) {
		t.Fatalf("the example is not valid JSON: %s", example)
	}
	p, err := subagentsWith(nil).parseTasks(example)
	if err != nil {
		t.Fatalf("the example would be refused: %v", err)
	}
	for i, task := range p.Tasks {
		if task.Title == "" || task.Body == "" {
			t.Errorf("example task %d is incomplete: %+v", i+1, task)
		}
	}
}

// testMost is the fan-out cap these tests build the tool with. The number is
// the run's to choose, so the tests pick one rather than reading a default
// that lives in the command layer.
const testMost = 8

// subagentsWith returns the tool with no skills available, which is what most
// of these tests want: a run that was given none accepts none on a task.
func subagentsWith(run Runner) *Subagents { return NewSubagents(run, nil, nil, testMost) }

// The widest call is the run's to decide, so a tool built with a narrower cap
// refuses what a wider one would have accepted, and says both numbers: the
// model is one call away from a call that would work.
func TestTheCapIsTheOneTheToolWasBuiltWith(t *testing.T) {
	const most = 3
	s := NewSubagents(echoRunner, nil, nil, most)

	tasks := make([]Task, most+1)
	for i := range tasks {
		tasks[i] = Task{Title: "T", Body: "B"}
	}

	_, err := s.Call(context.Background(), tasksJSON(t, tasks...))
	if err == nil {
		t.Fatalf("Call() with %d tasks = nil, want a refusal at a cap of %d", len(tasks), most)
	}
	for _, want := range []string{strconv.Itoa(len(tasks)), strconv.Itoa(most)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Call() = %q, want it to name %q", err, want)
		}
	}

	if _, err := s.Call(context.Background(), tasksJSON(t, tasks[:most]...)); err != nil {
		t.Errorf("Call() with %d tasks = %v, want the cap itself accepted", most, err)
	}

	def := s.Definition()
	tasksParam := def.Parameters["properties"].(map[string]any)["tasks"].(map[string]any)
	if tasksParam["maxItems"] != most {
		t.Errorf("maxItems = %v, want the cap the tool was built with, %d", tasksParam["maxItems"], most)
	}
	if !strings.Contains(flattened(def.Description), fmt.Sprintf("between %d and %d tasks", MinTasks, most)) {
		t.Errorf("description does not say how wide a call may be: %q", def.Description)
	}
}

// A caller with no opinion about the cap gets the smallest call the tool
// accepts, rather than a tool whose every call is refused.
func TestACapBelowTheFloorBecomesTheFloor(t *testing.T) {
	for _, most := range []int{0, 1, -4} {
		s := NewSubagents(echoRunner, nil, nil, most)
		tasks := make([]Task, MinTasks)
		for i := range tasks {
			tasks[i] = Task{Title: "T", Body: "B"}
		}
		if _, err := s.Call(context.Background(), tasksJSON(t, tasks...)); err != nil {
			t.Errorf("a tool built with a cap of %d refused the smallest call: %v", most, err)
		}
	}
}
