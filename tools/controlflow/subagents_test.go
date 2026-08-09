package controlflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
func echoRunner(_ context.Context, task string) (string, error) {
	return "answer to " + task, nil
}

func TestSubagentsIsATool(t *testing.T) {
	var _ tools.Tool = NewSubagents(echoRunner)
	var _ tools.Announcer = NewSubagents(echoRunner)
	var _ tools.SystemPrompter = NewSubagents(echoRunner)

	def := NewSubagents(echoRunner).Definition()
	if def.Name != "subagents" {
		t.Errorf("Name = %q, want %q", def.Name, "subagents")
	}
	if def.Description == "" {
		t.Error("Description is empty, and it is how the model learns to call the tool")
	}
}

// Answers come back under their titles, in the order the tasks were given.
func TestCallReturnsAnswersInTheOrderAsked(t *testing.T) {
	s := NewSubagents(echoRunner)
	args := tasksJSON(t,
		Task{Title: "Read the README", Body: "first"},
		Task{Title: "Check the tests", Body: "second"},
		Task{Title: "Count the files", Body: "third"},
	)

	got, err := s.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("Call() = %v, want nil", err)
	}

	want := "## Read the README\nanswer to first\n\n" +
		"## Check the tests\nanswer to second\n\n" +
		"## Count the files\nanswer to third"
	if got != want {
		t.Errorf("Call() = %q, want %q", got, want)
	}
}

// Which subagent finishes first is scheduling, and the same call must not
// produce a different result for it.
func TestOrderDoesNotDependOnWhoFinishesFirst(t *testing.T) {
	// The first task is made to finish last.
	delays := map[string]time.Duration{"slow": 40 * time.Millisecond}
	s := NewSubagents(func(_ context.Context, task string) (string, error) {
		time.Sleep(delays[task])
		return task, nil
	})

	got, err := s.Call(context.Background(), tasksJSON(t,
		Task{Title: "Slow", Body: "slow"},
		Task{Title: "Fast", Body: "fast"},
	))
	if err != nil {
		t.Fatalf("Call() = %v, want nil", err)
	}
	if want := "## Slow\nslow\n\n## Fast\nfast"; got != want {
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

	s := NewSubagents(func(_ context.Context, task string) (string, error) {
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
		return task, nil
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
	s := NewSubagents(func(_ context.Context, task string) (string, error) {
		if task == "doomed" {
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
	if !strings.Contains(got, "## Works\nfine") {
		t.Errorf("Call() = %q, want the answer that did arrive", got)
	}
	if !strings.Contains(got, "gateway exploded") {
		t.Errorf("Call() = %q, want the failure reported under its title", got)
	}
}

// A subagent that says nothing must not leave an empty section the model has
// to guess at.
func TestASilentSubagentSaysSo(t *testing.T) {
	s := NewSubagents(func(context.Context, string) (string, error) { return "  \n ", nil })
	got, err := s.Call(context.Background(), tasksJSON(t, Task{Title: "Quiet", Body: "say nothing"}))
	if err != nil {
		t.Fatalf("Call() = %v, want nil", err)
	}
	if !strings.Contains(got, "no answer") {
		t.Errorf("Call() = %q, want the empty answer called out", got)
	}
}

func TestCallRejectsBadArguments(t *testing.T) {
	many := make([]Task, maxTasks+1)
	for i := range many {
		many[i] = Task{Title: "T", Body: "B"}
	}

	tests := []struct {
		name    string
		args    json.RawMessage
		wantErr string
	}{
		{"not JSON", json.RawMessage("{oh no"), "not valid JSON"},
		{"no tasks at all", json.RawMessage(`{}`), "at least one task"},
		{"an empty list", json.RawMessage(`{"tasks": []}`), "at least one task"},
		{"a task with no title", json.RawMessage(`{"tasks": [{"body": "do it"}]}`), "no title"},
		{"a task with no body", json.RawMessage(`{"tasks": [{"title": "Do it"}]}`), "no body"},
		{"more tasks than allowed", tasksJSON(t, many...), "split the work"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ran := false
			s := NewSubagents(func(context.Context, string) (string, error) {
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
	s := NewSubagents(nil)
	if _, err := s.Call(context.Background(), tasksJSON(t, Task{Title: "T", Body: "B"})); err == nil {
		t.Error("Call() = nil, want an error when there is no way to run a subagent")
	}
}

// Subagents work silently, so the announcement is all the user sees of them:
// it has to name every one.
func TestAnnounceListsEveryTitle(t *testing.T) {
	s := NewSubagents(echoRunner)
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

	one := s.Announce(tasksJSON(t, Task{Title: "Just the one", Body: "a"}))
	if !strings.Contains(one, "1 subagent") || strings.Contains(one, "at once") {
		t.Errorf("Announce() of one task = %q, want it to read as a single subagent", one)
	}
}

// Announce is display only and must never fail.
func TestAnnounceSurvivesBadArguments(t *testing.T) {
	if got := NewSubagents(echoRunner).Announce(json.RawMessage("{oh no")); got != "" {
		t.Errorf("Announce() = %q, want nothing for unparsable arguments", got)
	}
}

// Cancelling the run must reach the subagents, not just the caller.
func TestCallPassesCancellationOn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := NewSubagents(func(ctx context.Context, task string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	go cancel()

	got, err := s.Call(ctx, tasksJSON(t, Task{Title: "Waits", Body: "forever"}))
	if err != nil {
		t.Fatalf("Call() = %v, want the call to return", err)
	}
	if !strings.Contains(got, "context canceled") {
		t.Errorf("Call() = %q, want the cancellation reported", got)
	}
}
