package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/skills"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/tools/native"
	"github.com/ChrisVilches/agi/transport"
)

// forkWiring is what these tests build agents from. The counter is the run's,
// as it is in a real run, because several of the tests below turn on the
// numbering being shared.
func forkWiring(t *testing.T, found []string) wiring {
	t.Helper()
	w := wiring{
		out:          quiet(),
		client:       &scriptedClient{},
		log:          nolog(),
		base:         "the base instruction",
		maxTurns:     defaultMaxTurns,
		maxSubagents: defaultMaxSubagents,
		agents:       &native.Counter{},
	}
	if len(found) > 0 {
		got, err := skills.Find([]string{skillFolder(t, found...)})
		if err != nil {
			t.Fatalf("Find() = %v", err)
		}
		w.found = got
	}
	return w
}

// forks reports whether an agent can really start a copy of itself, which is
// no longer answered by whether a fork tool is registered: a copy holds one
// that refuses, so that its prompt and its tools are the run's exactly.
func forks(t *testing.T, r *tools.Registry) bool {
	t.Helper()

	got, ok := r.Lookup(native.ForkTool)
	if !ok {
		return false
	}
	f, ok := got.(*native.Fork)
	if !ok {
		t.Fatalf("%q is a %T, want the fork tool", native.ForkTool, got)
	}
	return !f.Nested()
}

// names returns a registry's tool names as a set, for asking what an agent has.
func toolNames(r *tools.Registry) map[string]bool {
	got := make(map[string]bool, r.Len())
	for _, n := range r.Names() {
		got[n] = true
	}
	return got
}

// The tree of section 3: which agent may start which. Every constraint in it is
// enforced by registration rather than by wording, and a registration line is
// one line that nothing fails loudly for deleting — so the whole shape is
// asserted at once, and the walk that asserts it is what proves the tree
// terminates.
func TestAgentTree(t *testing.T) {
	w := forkWiring(t, []string{"writing"})

	run, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	fork, err := forkRegistry(w, "fork-1", nil)
	if err != nil {
		t.Fatalf("forkRegistry() = %v", err)
	}
	sub, err := workspaceRegistry("", "subagent-1", nil, nil)
	if err != nil {
		t.Fatalf("workspaceRegistry() = %v", err)
	}

	for _, c := range []struct {
		agent   string
		r       *tools.Registry
		has     []string
		lacks   []string
		spawner bool
	}{
		{
			agent:   "run",
			r:       run,
			has:     []string{native.ForkTool, native.SubagentsTool, native.LoadSkillTool, "bash"},
			spawner: true,
		},
		{
			// A copy holds fork as well, and it does not run. What it may do
			// is asserted by TestACopyRefusesToFork, where the refusal is;
			// what is asserted here is that it is offered everything, since
			// that is what keeps its prompt the run's.
			agent:   "fork",
			r:       fork,
			has:     []string{native.ForkTool, native.SubagentsTool, native.LoadSkillTool, "bash"},
			spawner: true,
		},
		{
			agent: "subagent",
			r:     sub,
			has:   []string{"bash"},
			lacks: []string{native.ForkTool, native.SubagentsTool, native.LoadSkillTool},
		},
	} {
		got := toolNames(c.r)
		for _, name := range c.has {
			if !got[name] {
				t.Errorf("a %s has no %q", c.agent, name)
			}
		}
		for _, name := range c.lacks {
			if got[name] {
				t.Errorf("a %s has %q, which it must not be able to call", c.agent, name)
			}
		}
		// The tree bottoms out: only the run and a fork can start an agent, so
		// no path through it is longer than run -> fork -> subagent. Holding
		// the fork tool is no longer the same thing as being able to fork, so
		// the one that refuses is not counted — that distinction is the whole
		// of what keeps the depth bounded now.
		if spawns := forks(t, c.r) || got[native.SubagentsTool]; spawns != c.spawner {
			t.Errorf("a %s spawns = %v, want %v", c.agent, spawns, c.spawner)
		}
	}
}

// A fork is offered exactly what the run is offered, and the definitions are
// the same text, so what a provider is sent is byte for byte the run's own.
//
// That is the point of the arrangement and not a detail of it. A copy carries
// the whole conversation forward and re-sends it every turn, so it is the one
// agent whose cost is mostly the turns it inherited — and a provider charges
// less for a prefix it has already seen. One name out of place, or one word of
// one description, and the copy pays full price for all of it.
func TestAForkIsOfferedExactlyWhatTheRunIs(t *testing.T) {
	w := forkWiring(t, []string{"writing"})

	run, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	fork, err := forkRegistry(w, "fork-1", nil)
	if err != nil {
		t.Fatalf("forkRegistry() = %v", err)
	}

	if a, b := run.Names(), fork.Names(); !slices.Equal(a, b) {
		t.Errorf("a fork is offered %v, the run %v", b, a)
	}
	for i, want := range run.Definitions() {
		got := fork.Definitions()[i]
		if !reflect.DeepEqual(got, want) {
			t.Errorf("a fork's %q is not the run's, and the two must be one prefix", want.Name)
		}
	}
}

// Being inside a fork must buy a subagent nothing. The two are built from the
// same call, so this is what would catch a later edit giving one of them more.
func TestASubagentInsideAForkIsAnOrdinarySubagent(t *testing.T) {
	spawned, err := workspaceRegistry("", "subagent-1", nil, nil)
	if err != nil {
		t.Fatalf("workspaceRegistry() = %v", err)
	}
	inFork, err := workspaceRegistry("", "subagent-2", nil, nil)
	if err != nil {
		t.Fatalf("workspaceRegistry() = %v", err)
	}
	if a, b := strings.Join(spawned.Names(), ","), strings.Join(inFork.Names(), ","); a != b {
		t.Errorf("a subagent inside a fork has %s, a subagent of the run has %s", b, a)
	}
}

// The prompt half of the same rule. A fork's prompt is assembled from its own
// registry and never copied from the parent, and it has to come out as the
// run's own text — every section, in the same order, to the byte.
//
// Assembly rather than copying is still what gets it there, and that is worth
// keeping: the prompt says what the registry holds, so the day a copy is given
// something different, the prompt says so and this test fails. A copied prompt
// would go on agreeing with a registry it had stopped describing.
func TestAForkReadsTheRunsOwnPrompt(t *testing.T) {
	w := forkWiring(t, []string{"writing"})

	run, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	fork, err := forkRegistry(w, "fork-1", nil)
	if err != nil {
		t.Fatalf("forkRegistry() = %v", err)
	}
	want := systemPromptFor(w.base, run, skillsSection(w.found))
	got := systemPromptFor(w.base, fork, skillsSection(w.found))

	if got != want {
		t.Errorf("a fork reads a prompt of its own:\n%s\nwant the run's:\n%s", got, want)
	}
	// Named one at a time as well, so a failure says which section went
	// missing rather than printing two pages and leaving it to the reader.
	for name, section := range map[string]string{
		"the run's base instruction": w.base,
		"the fork guidance":          prompt.Fork(),
		"the subagents guidance":     prompt.Subagents(),
	} {
		if !strings.Contains(got, section) {
			t.Errorf("a fork does not read %s, which the run does", name)
		}
	}
}

// Assembling a prompt must be deterministic, or a snapshot could not go on
// storing none. The rule holds for a fork as it does for a run.
func TestAForkSystemPromptIsDeterministic(t *testing.T) {
	w := forkWiring(t, []string{"writing", "testing"})

	var first string
	for i := range 3 {
		r, err := forkRegistry(w, "fork-1", []string{"writing"})
		if err != nil {
			t.Fatalf("forkRegistry() = %v", err)
		}
		got := systemPromptFor(w.base, r, skillsSection(w.found))
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Error("the same arguments assembled two different prompts for a fork")
		}
	}
}

// A run given no skills produces no loadskill at any level, and its subagents
// tool offers no skills field — in a fork as in the run.
func TestAForkInARunWithNoSkillsHasNoLoadSkill(t *testing.T) {
	w := forkWiring(t, nil)

	fork, err := forkRegistry(w, "fork-1", nil)
	if err != nil {
		t.Fatalf("forkRegistry() = %v", err)
	}
	if toolNames(fork)[native.LoadSkillTool] {
		t.Error("a fork in a run with no skills has loadskill")
	}

	sub, ok := fork.Lookup(native.SubagentsTool)
	if !ok {
		t.Fatal("a fork has no subagents tool")
	}
	props, _ := sub.Definition().Parameters["properties"].(map[string]any)
	tasks, _ := props["tasks"].(map[string]any)
	items, _ := tasks["items"].(map[string]any)
	fields, _ := items["properties"].(map[string]any)
	if _, offered := fields["skills"]; offered {
		t.Error("a fork in a run with no skills offers a task a skills field")
	}
}

// fork is agi's, so a user-defined tool may not take the name — and the
// reservation does not depend on whether this particular run registers it.
func TestForkIsAReservedToolName(t *testing.T) {
	provided, err := providedNames()
	if err != nil {
		t.Fatalf("providedNames() = %v", err)
	}
	if !provided[native.ForkTool] {
		t.Errorf("%q is not reserved against a user-defined tool", native.ForkTool)
	}
}

// A copy must be usable as a conversation: the call that asked for the fork
// answered, anything asked alongside it answered too, and nothing left
// outstanding for a provider to object to.
func TestForkedConversationIsComplete(t *testing.T) {
	for _, c := range []struct {
		name     string
		requests []model.ToolRequest
	}{
		{
			name:     "alone",
			requests: []model.ToolRequest{{ID: "a", Name: "fork", Arguments: json.RawMessage(`{}`)}},
		},
		{
			name: "alongside another call",
			requests: []model.ToolRequest{
				{ID: "a", Name: "fork", Arguments: json.RawMessage(`{}`)},
				{ID: "b", Name: "bash", Arguments: json.RawMessage(`{}`)},
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			conv := asked("the question")
			conv.AddAssistant(model.AssistantMessage{Content: "thinking", ToolRequests: c.requests})

			copied := conv.Fork("a fork's prompt", "a", "do the thing")

			if pending := copied.PendingToolRequests(); len(pending) != 0 {
				t.Errorf("the copy has %d outstanding request(s), want none", len(pending))
			}
			if err := copied.Validate(); err != nil {
				t.Errorf("the copy does not validate: %v", err)
			}
			if copied.System != "a fork's prompt" {
				t.Errorf("the copy reads %q, want the prompt it was forked under", copied.System)
			}

			// The instruction is a harness turn and not a user turn: the user
			// did not write it, the model did, and agi is relaying it.
			last, _ := copied.Last()
			instruction, ok := last.(model.HarnessInstruction)
			if !ok {
				t.Fatalf("the copy ends with a %T, want a HarnessInstruction", last)
			}
			if !strings.Contains(instruction.Content, "do the thing") {
				t.Errorf("the instruction = %q, want the task in it", instruction.Content)
			}

			// The conversation it was made from is untouched: the parent still
			// has its own calls to run once the fork returns.
			if got := len(conv.PendingToolRequests()); got != len(c.requests) {
				t.Errorf("the parent has %d pending request(s), want %d", got, len(c.requests))
			}
		})
	}
}

// A call the model asked for alongside the fork was not made in the copy, which
// is what a failure is for — and it is true rather than convenient, since the
// parent runs that call itself once the fork is done.
func TestForkAnswersSiblingCallsAsUnmade(t *testing.T) {
	conv := asked("the question")
	conv.AddAssistant(model.AssistantMessage{ToolRequests: []model.ToolRequest{
		{ID: "a", Name: "fork", Arguments: json.RawMessage(`{}`)},
		{ID: "b", Name: "bash", Arguments: json.RawMessage(`{}`)},
	}})

	copied := conv.Fork("system", "a", "task")

	var made, unmade model.ToolAnswer
	for _, m := range copied.Messages {
		answer, ok := m.(model.ToolAnswer)
		if !ok {
			continue
		}
		switch answer.AnsweredRequest() {
		case "a":
			made = answer
		case "b":
			unmade = answer
		}
	}

	if _, ok := made.(model.ToolResultMessage); !ok {
		t.Errorf("the fork's own call is answered by a %T, want a plain result", made)
	}
	if _, ok := unmade.(model.ToolFailure); !ok {
		t.Fatalf("a call asked for alongside the fork is answered by a %T, want a failure", unmade)
	}
	if !strings.Contains(unmade.Render(), "not made in this copy") {
		t.Errorf("the failure reads %q, want it to say the call was not made here", unmade.Render())
	}
}

// A fork carries the parent's loaded skills across, because their text came
// over in the conversation; a subagent it spawns carries none, because its
// context is new and has nothing in it to have loaded.
func TestForkInheritsLoadedSkillsAndASubagentDoesNot(t *testing.T) {
	w := forkWiring(t, []string{"writing", "testing"})

	run, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	loader, _ := run.Lookup(native.LoadSkillTool)
	if _, err := loader.Call(context.Background(), json.RawMessage(`{"id":"writing"}`)); err != nil {
		t.Fatalf("loading a skill in the run = %v", err)
	}

	if got := loadedSkills(run); len(got) != 1 || got[0] != "writing" {
		t.Fatalf("loadedSkills() = %v, want [writing]", got)
	}

	fork, err := forkRegistry(w, "fork-1", loadedSkills(run))
	if err != nil {
		t.Fatalf("forkRegistry() = %v", err)
	}
	forked, _ := fork.Lookup(native.LoadSkillTool)
	if _, err := forked.Call(context.Background(), json.RawMessage(`{"id":"writing"}`)); err == nil {
		t.Error("a fork reloaded a skill whose text is already in the conversation it copied")
	}
	if _, err := forked.Call(context.Background(), json.RawMessage(`{"id":"testing"}`)); err != nil {
		t.Errorf("a fork could not load a skill the parent had not: %v", err)
	}

	// A subagent starts from nothing, so the same skill is new to it.
	fresh := native.NewLoadSkill(w.found, nil)
	if _, err := fresh.Call(context.Background(), json.RawMessage(`{"id":"writing"}`)); err != nil {
		t.Errorf("a subagent could not load a skill the run had already loaded: %v", err)
	}
}

// The numbering that names spawned agents is the run's, not a tool's, so a fork
// building a Subagents of its own does not start again at one. The names are
// also filenames, and two agents filed under one name cannot be told apart in a
// directory of file copies.
func TestSubagentNumberingDoesNotRestartInsideAFork(t *testing.T) {
	shared := &native.Counter{}
	var seen []int
	for range 4 {
		seen = append(seen, shared.Take(1))
	}
	want := []int{1, 2, 3, 4}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("shared numbering gave %v, want %v", seen, want)
		}
	}

	// A block is claimed at once, so a call numbers its tasks in the order
	// they were written rather than the order their goroutines got going.
	if got := shared.Take(3); got != 5 {
		t.Errorf("Take(3) = %d, want 5", got)
	}
	if got := shared.Take(1); got != 8 {
		t.Errorf("the number after a block of three = %d, want 8", got)
	}
}

// Call has to exist because tools.Tool is what the registry accepts, and it
// cannot do the work: its signature has no way to reach a conversation. It
// refuses and says why, rather than leaving that to be discovered.
func TestForkCallRefuses(t *testing.T) {
	_, err := native.NewFork(nil).Call(context.Background(), json.RawMessage(`{"task":"x"}`))
	if err == nil {
		t.Fatal("Fork.Call() = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "conversation") {
		t.Errorf("Fork.Call() = %v, want it to say it needs the conversation", err)
	}
}

// A malformed call is refused with something the model can act on, and no fork
// is run.
func TestForkRefusesABadCall(t *testing.T) {
	ran := false
	f := native.NewFork(func(context.Context, native.ForkBrief) (string, error) {
		ran = true
		return "answered", nil
	})
	conv := asked("q")
	conv.AddAssistant(model.AssistantMessage{ToolRequests: []model.ToolRequest{{ID: "a", Name: "fork"}}})

	for _, args := range []string{`{"task":""}`, `{"task":"   "}`, `{"task":["a","b"]}`, `{}`, `[]`} {
		if _, err := f.ResultWith(context.Background(), conv.Fork, "a", json.RawMessage(args)); err == nil {
			t.Errorf("ResultWith(%s) = nil error, want a refusal", args)
		}
	}
	if ran {
		t.Error("a refused call ran a fork anyway")
	}
}

// A fork with no conversation to copy refuses rather than working from nothing,
// which is what a caller that passes no fork function gets.
func TestForkRefusesWithoutAConversation(t *testing.T) {
	f := native.NewFork(func(context.Context, native.ForkBrief) (string, error) {
		return "answered", nil
	})
	if _, err := f.ResultWith(context.Background(), nil, "a", json.RawMessage(`{"task":"x"}`)); err == nil {
		t.Error("a fork with no conversation to copy = nil error, want a refusal")
	}
}

// The forks a run takes are numbered across the run, so a run that forks twice
// does not file the second under the first one's name.
func TestForksAreNumberedAcrossTheRun(t *testing.T) {
	var got []int
	f := native.NewFork(func(_ context.Context, brief native.ForkBrief) (string, error) {
		got = append(got, brief.Number)
		return "answered", nil
	})

	conv := asked("q")
	conv.AddAssistant(model.AssistantMessage{ToolRequests: []model.ToolRequest{{ID: "a", Name: "fork"}}})
	for range 3 {
		if _, err := f.ResultWith(context.Background(), conv.Fork, "a", json.RawMessage(`{"task":"x"}`)); err != nil {
			t.Fatalf("ResultWith() = %v", err)
		}
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("fork numbers = %v, want 1 2 3", got)
	}
	if name := forkName(native.ForkBrief{Number: 2}); name != "fork-2" {
		t.Errorf("forkName() = %q, want fork-2", name)
	}
}

// A fork answers with one plain result. SubagentResults keeps several answers
// apart because several arrive at once; one has nothing to be separated from.
func TestForkAnswersWithAPlainResult(t *testing.T) {
	f := native.NewFork(func(_ context.Context, brief native.ForkBrief) (string, error) {
		if brief.Task != "settle it" {
			t.Errorf("the fork was given %q, want the task the model wrote", brief.Task)
		}
		if brief.RequestID != "a" {
			t.Errorf("the fork was given request %q, want a", brief.RequestID)
		}
		return "  the answer  ", nil
	})

	conv := asked("q")
	conv.AddAssistant(model.AssistantMessage{ToolRequests: []model.ToolRequest{{ID: "a", Name: "fork"}}})

	answer, err := f.ResultWith(context.Background(), conv.Fork, "a", json.RawMessage(`{"task":"settle it"}`))
	if err != nil {
		t.Fatalf("ResultWith() = %v", err)
	}
	if _, ok := answer.(model.ToolResultMessage); !ok {
		t.Errorf("a fork answers with a %T, want a plain result", answer)
	}
	if answer.Render() != "the answer" {
		t.Errorf("a fork answered %q, want the copy's answer trimmed", answer.Render())
	}
}

// A fork that produced nothing gives the model nothing to read, so it is a
// failed call rather than an empty answer.
func TestForkWithNoAnswerFails(t *testing.T) {
	f := native.NewFork(func(context.Context, native.ForkBrief) (string, error) { return "   ", nil })
	conv := asked("q")
	conv.AddAssistant(model.AssistantMessage{ToolRequests: []model.ToolRequest{{ID: "a", Name: "fork"}}})

	if _, err := f.ResultWith(context.Background(), conv.Fork, "a", json.RawMessage(`{"task":"x"}`)); err == nil {
		t.Error("a fork that answered nothing = nil error, want a failure")
	}
}

// An interruption is the sentinel that must pass through: a nested agent
// stopped by Ctrl-C was stopped because the run was, and the agent that asked
// abandons its own outstanding calls on the strength of it.
func TestContainLetsAnInterruptionThrough(t *testing.T) {
	if _, err := contain("", ErrInterrupted); !errors.Is(err, ErrInterrupted) {
		t.Errorf("contain() = %v, want the interruption unchanged", err)
	}
	if answer, err := contain("the answer", nil); err != nil || answer != "the answer" {
		t.Errorf("contain() = %q, %v, want an ordinary answer through untouched", answer, err)
	}
}

// Everything recorded inside a fork says so, including what a subagent the fork
// spawned records — that is the one thing a name alone cannot say, since a
// fork's subagents are numbered in the run's own sequence.
func TestEventsInsideAForkAreMarked(t *testing.T) {
	_, lg, read := recorded(t)

	lg.Record(eventlog.Event{Kind: eventlog.RunStart})
	fork := lg.Fork("fork-1")
	fork.Record(eventlog.Event{Kind: eventlog.ForkStart})
	fork.Agent("subagent-1").Record(eventlog.Event{Kind: eventlog.SubagentStart})
	// A subagent the run spawned is not inside a fork, even after one has run.
	lg.Agent("subagent-2").Record(eventlog.Event{Kind: eventlog.SubagentStart})

	want := map[string]bool{"main": false, "fork-1": true, "subagent-1": true, "subagent-2": false}
	for _, line := range read() {
		if inFork, ok := want[line.Agent]; ok && line.Fork != inFork {
			t.Errorf("%s recorded fork=%v, want %v", line.Agent, line.Fork, inFork)
		}
		delete(want, line.Agent)
	}
	if len(want) != 0 {
		t.Errorf("no lines recorded for %v", want)
	}
}

// The marking writer puts one marker on a line however many writes it arrives
// in, and none on a newline that ends one.
func TestNestedOutputMarksLines(t *testing.T) {
	var buf bytes.Buffer
	w := mark(&buf)

	for _, part := range []string{"one ", "line", "\n", "\n", "two\nthree\n"} {
		if _, err := w.Write([]byte(part)); err != nil {
			t.Fatalf("Write() = %v", err)
		}
	}

	want := nestedMarker + "one line\n" +
		"\n" +
		nestedMarker + "two\n" +
		nestedMarker + "three\n"
	if got := buf.String(); got != want {
		t.Errorf("marked output = %q, want %q", got, want)
	}
}

// A fork is watched where a subagent is not, so its progress reaches the
// screen — but its answer does not, since that is a tool result the agent loop
// already prints.
func TestNestedOutputKeepsProgressAndDropsTheAnswer(t *testing.T) {
	var buf bytes.Buffer
	nested := humanOutput(&buf).nested()

	nested.step("working\n")
	nested.final("the answer")

	got := buf.String()
	if !strings.Contains(got, "working") {
		t.Errorf("nested output = %q, want the fork's progress in it", got)
	}
	if strings.Contains(got, "the answer") {
		t.Errorf("nested output = %q, want the fork's answer dropped", got)
	}
}

// A whole run through a fork, driven by a scripted gateway: the model asks for
// a fork, the copy does the work and answers, and that answer comes back as the
// result of the call so the run can finish on it.
//
// What this is really checking is that the copy is a usable conversation. The
// scripted client records what it was sent, so the fork's own request carries
// the parent's turns, the run's base instruction, and none of the guidance for
// the two tools a fork does not have.
func TestARunThroughAFork(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		// The run asks for a fork.
		{Content: "I will look into this separately.", ToolRequests: []model.ToolRequest{
			{ID: "f1", Name: native.ForkTool, Arguments: json.RawMessage(`{"task":"settle the question"}`)},
		}},
		// The copy answers.
		model.NewAssistantMessage("the copy's answer"),
		// The run answers, having read it.
		model.NewAssistantMessage("the run's answer"),
	}}

	w := forkWiring(t, nil)
	w.client = client

	registry, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}

	var out bytes.Buffer
	w.out = humanOutput(&out)
	// The registry has to be rebuilt so the fork runner prints to this output.
	if registry, err = defaultRegistry(w); err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}

	conv := model.NewConversation(systemPromptFor(w.base, registry, ""), "the question")
	if err := runAgent(context.Background(), w.out, nolog(), client, registry, conv, nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	if len(client.sent) != 3 {
		t.Fatalf("the gateway was asked %d times, want 3", len(client.sent))
	}

	// The copy's request is the second. It reads the run's base and not the
	// subagent instruction, because a fork is the agent the user is talking to.
	copied := client.sent[1].Conversation
	if copied.System != client.sent[0].Conversation.System {
		t.Error("the copy was sent a prompt of its own, and must be sent the run's")
	}
	if !strings.Contains(copied.System, w.base) {
		t.Error("the copy does not read the run's own base instruction")
	}
	if err := copied.Validate(); err != nil {
		t.Errorf("the copy sent to the gateway does not validate: %v", err)
	}
	if !strings.Contains(text(copied.Messages[0]), "the question") {
		t.Error("the copy does not carry the conversation it was forked from")
	}

	// The copy's answer comes back as the result of the call, so the run reads
	// it on its next turn and nothing of how it was reached comes with it.
	final := client.sent[2].Conversation
	var sawAnswer, sawWorking bool
	for _, m := range final.Messages {
		text := text(m)
		if strings.Contains(text, "the copy's answer") {
			sawAnswer = true
		}
		if strings.Contains(text, "Carry out the task below") {
			sawWorking = true
		}
	}
	if !sawAnswer {
		t.Error("the run never saw the copy's answer")
	}
	if sawWorking {
		t.Error("the run's conversation carries the copy's scaffolding, which it must not")
	}

	// The run's own conversation is the record: the call, one result, the end.
	if err := conv.Validate(); err != nil {
		t.Errorf("the run's conversation does not validate: %v", err)
	}
	if !strings.Contains(out.String(), "the run's answer") {
		t.Errorf("output = %q, want the run's answer", out.String())
	}
}

// text is whatever a turn says, for a test that only wants to look for a
// phrase. Message itself has no Render — only the turns that answer a tool
// request do — so the two that carry plain content are read directly.
func text(m model.Message) string {
	switch t := m.(type) {
	case model.UserMessage:
		return t.Content
	case model.AssistantMessage:
		return t.Content
	case interface{ Render() string }:
		return t.Render()
	}
	return ""
}

// A fork that fails does not fail the run. The agent that asked reads the
// failure and decides whether it can finish without whatever the copy was going
// to find out — which is its judgement to make, not the harness's.
func TestAFailedForkDoesNotFailTheRun(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{
			{ID: "f1", Name: native.ForkTool, Arguments: json.RawMessage(`{"task":"settle it"}`)},
		}},
		model.NewAssistantMessage("I managed without it."),
	}}

	// A registry whose fork tool always fails, standing in for a gateway that
	// did not answer inside the copy.
	registry := registryWith(t, native.NewFork(
		func(context.Context, native.ForkBrief) (string, error) {
			return "", errors.New("the gateway did not answer")
		}))

	var out bytes.Buffer
	conv := asked("the question")
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client, registry, conv, nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v, want the run to carry on", err)
	}

	var failure model.ToolFailure
	var found bool
	for _, m := range conv.Messages {
		if f, ok := m.(model.ToolFailure); ok {
			failure, found = f, true
		}
	}
	if !found {
		t.Fatal("a failed fork left no failure in the conversation")
	}
	if !strings.Contains(failure.Reason, "gateway did not answer") {
		t.Errorf("the failure reads %q, want why the fork failed", failure.Reason)
	}
	if err := conv.Validate(); err != nil {
		t.Errorf("the conversation does not validate after a failed fork: %v", err)
	}
}

// A fork's work is bracketed in the log under its own name, and everything
// between is marked as having happened inside it — including what a subagent
// the fork spawned records, which is the one thing the name cannot say.
func TestForkIsBracketedInTheLog(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{
			{ID: "f1", Name: native.ForkTool, Arguments: json.RawMessage(`{"task":"settle it"}`)},
		}},
		model.NewAssistantMessage("the copy's answer"),
		model.NewAssistantMessage("the run's answer"),
	}}

	sess, lg, read := recorded(t)
	w := forkWiring(t, nil)
	w.client = client
	w.log = lg
	w.backups = sess.BackupDir()

	registry, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	conv := model.NewConversation("system", "the question")
	if err := runAgent(context.Background(), quiet(), lg, client, registry, conv, nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	var opened, closed bool
	for _, e := range read() {
		switch e.Kind {
		case eventlog.ForkStart:
			opened = true
			if e.Agent != "fork-1" || !e.Fork {
				t.Errorf("fork.start recorded by %q fork=%v, want fork-1 inside a fork", e.Agent, e.Fork)
			}
			if e.Content != "settle it" {
				t.Errorf("fork.start carries %q, want the task", e.Content)
			}
		case eventlog.ForkFinish:
			closed = true
			if e.Content != "the copy's answer" {
				t.Errorf("fork.finish carries %q, want the copy's answer", e.Content)
			}
		}
		// The run's own lines are never marked, whatever the fork did.
		if e.Agent == eventlog.MainAgent && e.Fork {
			t.Errorf("a line recorded by the run itself is marked as inside a fork: %v", e.Kind)
		}
		// Everything the fork recorded is marked, not just its brackets.
		if e.Agent == "fork-1" && !e.Fork {
			t.Errorf("a line recorded by the fork is not marked: %v", e.Kind)
		}
	}
	if !opened || !closed {
		t.Errorf("fork.start seen = %v, fork.finish seen = %v, want both", opened, closed)
	}
}

// A fork is steered by one instruction and nothing else, so reading a fork
// back means reading that instruction. It is recorded in full and under the
// fork's own name: the task alone would leave a reader rebuilding the wrapper
// around it from this repository's source.
func TestAForkRecordsTheInstructionItWasGiven(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{
			{ID: "f1", Name: native.ForkTool, Arguments: json.RawMessage(`{"task":"settle it"}`)},
		}},
		model.NewAssistantMessage("the copy's answer"),
		model.NewAssistantMessage("the run's answer"),
	}}

	sess, lg, read := recorded(t)
	w := forkWiring(t, nil)
	w.client = client
	w.log = lg
	w.backups = sess.BackupDir()

	registry, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	conv := model.NewConversation("system", "the question")
	if err := runAgent(context.Background(), quiet(), lg, client, registry, conv, nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	instructions := only(read(), eventlog.HarnessInstruction)
	if len(instructions) != 1 {
		t.Fatalf("recorded %d instructions, want the fork's one", len(instructions))
	}
	got := instructions[0]
	if got.Agent != "fork-1" || !got.Fork {
		t.Errorf("the instruction was recorded by %q fork=%v, want fork-1 inside a fork", got.Agent, got.Fork)
	}
	if got.Content != forkInstruction("settle it") {
		t.Errorf("the instruction was recorded as %q, want the words the copy read", got.Content)
	}
}

// The preamble describes the run's own agent and stops. It must say nothing
// about a fork, which is the rule that removed the subagent preview: a run may
// not describe an agent it has not spawned, because what it would show is a
// hypothetical one.
func TestPreambleSaysNothingAboutForks(t *testing.T) {
	w := forkWiring(t, nil)
	registry, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}

	var buf bytes.Buffer
	out := humanOutput(&buf)
	out.verbose = true
	preamble(out, &scriptedClient{}, nil, agentSetup{system: "the prompt", registry: registry}, hooks.None(), "")

	got := buf.String()
	// The tool is listed, because the run has it.
	if !strings.Contains(got, "[fork]") {
		t.Error("the preamble does not list the fork tool, which the run has")
	}
	// But no agent is described beyond the run's own.
	if strings.Contains(got, "fork-1") {
		t.Error("the preamble describes a fork, which the run has not spawned")
	}
}

// A fork's block is printed as it starts, headed by the name its log lines and
// file copies are filed under, and it repeats nothing the run's own listing
// already said: a tool is named rather than explained again, and the prompt —
// which is the run's, word for word — is pointed at rather than reprinted.
func TestForkIntroNamesTheAgentAndRepeatsNothing(t *testing.T) {
	w := forkWiring(t, nil)
	run, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	fork, err := forkRegistry(w, "fork-1", nil)
	if err != nil {
		t.Fatalf("forkRegistry() = %v", err)
	}
	parent := agentSetup{system: "the run's prompt", registry: run}

	got := forkIntro("fork-1", agentSetup{system: parent.system, registry: fork}, parent)

	if !strings.Contains(got, "[fork-1]") {
		t.Errorf("the fork's block = %q, want it headed by the agent's name", got)
	}
	if strings.Contains(got, "the run's prompt") {
		t.Error("the fork's block reprints the prompt the user has just read")
	}
	if !strings.Contains(got, "as above") {
		t.Errorf("the fork's block = %q, want it to say which prompt the copy reads", got)
	}
	if !strings.Contains(got, "explained above") {
		t.Error("the fork's block re-explains tools the run's own listing already described")
	}
}

// A copy given a prompt of its own is shown it in full. Nothing does that
// today, and the collapse above must be a comparison rather than an assumption
// — the day the two part company is the day the block has to say so.
func TestForkIntroShowsAPromptThatIsNotTheRuns(t *testing.T) {
	w := forkWiring(t, nil)
	run, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	fork, err := forkRegistry(w, "fork-1", nil)
	if err != nil {
		t.Fatalf("forkRegistry() = %v", err)
	}

	got := forkIntro("fork-1",
		agentSetup{system: "a prompt of the copy's own", registry: fork},
		agentSetup{system: "the run's prompt", registry: run})

	if !strings.Contains(got, "a prompt of the copy's own") {
		t.Errorf("the fork's block = %q, want the prompt it will actually read", got)
	}
}

// Only one fork runs at a time, and nothing enforces that but converse running
// a turn's tool requests one after another. So two forks asked for in one turn
// run in the order they were written, and the second copies a conversation that
// already holds the first one's answer.
//
// The second half is what pins the property the first depends on: if the loop
// were ever made to run a turn's calls at once, this is where it should fail
// rather than in a run where two copies of an agent work on the same files.
func TestTwoForksInOneTurnRunOneAfterTheOther(t *testing.T) {
	var order []string
	var sawFirstAnswer bool

	registry := registryWith(t, native.NewFork(
		func(_ context.Context, brief native.ForkBrief) (string, error) {
			order = append(order, brief.Task)
			// What the copy would start from, had it been run for real.
			copied := brief.Fork("system", brief.RequestID, "do it")
			for _, m := range copied.Messages {
				if strings.Contains(text(m), "first answer") {
					sawFirstAnswer = true
				}
			}
			if brief.Task == "first" {
				return "first answer", nil
			}
			return "second answer", nil
		}))

	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{
			{ID: "f1", Name: native.ForkTool, Arguments: json.RawMessage(`{"task":"first"}`)},
			{ID: "f2", Name: native.ForkTool, Arguments: json.RawMessage(`{"task":"second"}`)},
		}},
		model.NewAssistantMessage("done"),
	}}

	conv := asked("the question")
	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client, registry, conv, nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Errorf("the forks ran %v, want them in the order the model wrote them", order)
	}
	if !sawFirstAnswer {
		t.Error("the second fork did not start from a conversation holding the first one's answer, " +
			"so the two did not run one after the other")
	}
	if err := conv.Validate(); err != nil {
		t.Errorf("the conversation does not validate after two forks: %v", err)
	}
}

// A fork answers in prose and never runs the schema phase: restating an answer
// as JSON belongs to the run that answers the user, not to a copy whose answer
// is read by another agent.
func TestAForkDoesNotInheritTheSchemaPhase(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{
			{ID: "f1", Name: native.ForkTool, Arguments: json.RawMessage(`{"task":"settle it"}`)},
		}},
		model.NewAssistantMessage("the copy's answer"),
		model.NewAssistantMessage("the run's answer"),
		model.NewAssistantMessage(`{"verdict":"yes"}`),
	}}

	sess, lg, read := recorded(t)
	w := forkWiring(t, nil)
	w.client = client
	w.log = lg
	w.backups = sess.BackupDir()

	registry, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	schema, err := model.ParseSchema(`verdict:str:"yes or no"`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v", err)
	}

	conv := model.NewConversation("system", "the question")
	var out bytes.Buffer
	if err := runAgent(context.Background(), pipedOutput(&out), lg, client, registry, conv, schema, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	for _, e := range read() {
		if e.Fork && strings.HasPrefix(string(e.Kind), "schema.") {
			t.Errorf("the fork recorded %v, but the schema phase is the run's alone", e.Kind)
		}
	}
	if got := strings.TrimSpace(out.String()); got != `{"verdict":"yes"}` {
		t.Errorf("a piped schema run printed %q, want the document alone", got)
	}
}

// nestedClient answers whichever agent is asking, telling them apart by what
// they were sent: a subagent reads the subagent instruction, and a fork is the
// one carrying the instruction a copy opens with. The run is whatever is left.
//
// The prompt alone cannot separate the run from a copy of it, and that is the
// arrangement rather than a gap in the test: a copy is offered the same tools
// and reads the same prompt so that the two are one prefix, which is the whole
// of what makes a fork affordable. What differs is the conversation, which is
// where this looks.
//
// It is safe to share because subagents run at the same time as one another,
// which is the whole situation this is here to reproduce.
type nestedClient struct {
	testBackend
	mu   sync.Mutex
	stop context.CancelFunc
	// spawned counts the subagents that got as far as a request.
	spawned int
}

func (c *nestedClient) Send(_ context.Context, req transport.Request) (transport.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case strings.Contains(req.Conversation.System, prompt.Subagent()):
		// A subagent is under way, which is the moment the user presses
		// Ctrl-C. Cancelling is idempotent, so its sibling sees it too.
		c.spawned++
		c.stop()
		return transport.Response{}, context.Canceled
	case isCopy(req.Conversation):
		// The fork, which spawns the two subagents.
		return transport.Response{Message: model.AssistantMessage{
			ToolRequests: []model.ToolRequest{{
				ID: "s1", Name: native.SubagentsTool,
				Arguments: json.RawMessage(`{"tasks":[
					{"title":"One","body":"dig into the first half"},
					{"title":"Two","body":"dig into the second half"}
				]}`),
			}},
		}}, nil
	default:
		// The run itself, which asks for the fork.
		return transport.Response{Message: model.AssistantMessage{
			ToolRequests: []model.ToolRequest{{
				ID: "f1", Name: native.ForkTool,
				Arguments: json.RawMessage(`{"task":"settle it"}`),
			}},
		}}, nil
	}
}

// copyOpening is the first thing forkInstruction says, and the one mark that
// separates a copy's conversation from the run's. The prompt no longer does:
// the two are deliberately the same text, so that a copy continues a prefix the
// provider has already seen.
const copyOpening = "Everything above is the conversation you were copied from"

// isCopy reports whether a conversation is a copy of a run rather than a run.
func isCopy(conv *model.Conversation) bool {
	for _, m := range conv.Messages {
		if strings.Contains(text(m), copyOpening) {
			return true
		}
	}
	return false
}

// Ctrl-C while a fork's subagents are working has to leave the run resumable.
// Three levels unwind at once, and only the outermost conversation is stored,
// so what matters is that the one that survives is complete: nothing
// outstanding, nothing rewritten, and a snapshot that loads back and validates.
func TestAnInterruptInsideAForksSubagentsLeavesTheRunResumable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &nestedClient{stop: cancel}
	sess, lg, read := recorded(t)

	w := forkWiring(t, nil)
	w.client = client
	w.log = lg
	w.backups = sess.BackupDir()

	registry, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	system := systemPromptFor(w.base, registry, "")
	conv := model.NewConversation(system, "the question")

	err = runAgent(ctx, quiet(), lg, client, registry, conv, nil, false, defaultMaxTurns, hooks.None())
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("runAgent() = %v, want the interruption", err)
	}
	if client.spawned == 0 {
		t.Fatal("no subagent got as far as a request, so this did not test what it means to")
	}

	// The conversation that survives is the run's own, and it has to be one a
	// provider will accept: the fork it asked for is answered, and nothing is
	// left outstanding.
	if pending := conv.PendingToolRequests(); len(pending) != 0 {
		t.Errorf("the run has %d outstanding request(s) after the interrupt, want none", len(pending))
	}
	if err := conv.Validate(); err != nil {
		t.Errorf("the interrupted conversation does not validate: %v", err)
	}

	var answer model.ToolAnswer
	for _, m := range conv.Messages {
		if a, ok := m.(model.ToolAnswer); ok && a.AnsweredRequest() == "f1" {
			answer = a
		}
	}
	failure, ok := answer.(model.ToolFailure)
	if !ok {
		t.Fatalf("the fork request is answered by a %T, want a failure", answer)
	}
	if !strings.Contains(failure.Reason, "interrupted") {
		t.Errorf("the failure reads %q, want it to say the run was stopped", failure.Reason)
	}

	// Stored and picked up again, which is the whole point of storing it.
	if err := sess.Save(conv); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	resumed, err := sess.Resume(system, "carry on then")
	if err != nil {
		t.Fatalf("Resume() = %v", err)
	}
	if err := resumed.Validate(); err != nil {
		t.Errorf("the resumed conversation does not validate: %v", err)
	}
	if resumed.Len() != conv.Len()+1 {
		t.Errorf("the resumed conversation has %d turns, want the stored %d plus the new prompt",
			resumed.Len(), conv.Len())
	}
	if pending := resumed.PendingToolRequests(); len(pending) != 0 {
		t.Errorf("the resumed conversation has %d outstanding request(s), want none", len(pending))
	}
	last, _ := resumed.Last()
	if got := text(last); got != "carry on then" {
		t.Errorf("the resumed conversation ends with %q, want the new prompt", got)
	}

	// Every level recorded its own interruption, and the two inside the fork
	// say so — which is what the flag is for, since the names alone cannot.
	stopped := map[string]bool{}
	for _, e := range read() {
		if e.Kind != eventlog.RunInterrupt {
			continue
		}
		stopped[e.Agent] = e.Fork
	}
	if inFork, ok := stopped[eventlog.MainAgent]; !ok || inFork {
		t.Errorf("the run recorded its interruption as %v, want it recorded and not inside a fork", stopped)
	}
	if inFork, ok := stopped["fork-1"]; !ok || !inFork {
		t.Errorf("the fork recorded its interruption as %v, want it recorded and marked", stopped)
	}
	for agent, inFork := range stopped {
		if strings.HasPrefix(agent, "subagent-") && !inFork {
			t.Errorf("%s was interrupted inside a fork but its line is not marked", agent)
		}
	}
}

// A run that forked and finished stores an ordinary conversation: the call, one
// plain result, and the answer. Nothing of the copy is in it, so resuming needs
// nothing special — which is the feature working rather than an exception to
// anything.
func TestASessionResumesAfterAFork(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{
			{ID: "f1", Name: native.ForkTool, Arguments: json.RawMessage(`{"task":"settle it"}`)},
		}},
		model.NewAssistantMessage("the copy's answer"),
		model.NewAssistantMessage("the run's answer"),
	}}

	sess, lg, _ := recorded(t)
	w := forkWiring(t, nil)
	w.client = client
	w.log = lg
	w.backups = sess.BackupDir()

	registry, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	system := systemPromptFor(w.base, registry, "")
	conv := model.NewConversation(system, "the question")

	var out bytes.Buffer
	if err := runAgent(context.Background(), pipedOutput(&out), lg, client, registry, conv, nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}
	if err := sess.Save(conv); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	resumed, err := sess.Resume(system, "and now the other half")
	if err != nil {
		t.Fatalf("Resume() = %v", err)
	}
	if err := resumed.Validate(); err != nil {
		t.Errorf("the resumed conversation does not validate: %v", err)
	}

	// The fork's answer came back as a plain result, so it decodes as one.
	var answer model.ToolAnswer
	for _, m := range resumed.Messages {
		if a, ok := m.(model.ToolAnswer); ok && a.AnsweredRequest() == "f1" {
			answer = a
		}
	}
	if _, ok := answer.(model.ToolResultMessage); !ok {
		t.Fatalf("the stored fork answer decodes as a %T, want a plain result", answer)
	}
	if answer.Render() != "the copy's answer" {
		t.Errorf("the stored fork answer = %q, want the copy's answer", answer.Render())
	}

	// And nothing of how the copy reached it is in the record.
	for _, m := range resumed.Messages {
		if strings.Contains(text(m), "Carry out the task below") {
			t.Error("the stored conversation carries the copy's scaffolding")
		}
	}
}

// A session resumed from an earlier run can hold a completed call to a tool
// this run does not have — one the user brought with --resources last time and
// not this time. A fork copies that history, so a copy can read a call that
// worked and that nothing here could make again.
//
// It has to cope rather than break: the copy is built and validates, and the
// call is refused as a name nothing answers to, which is a correctable mistake
// and not anything worse.
func TestACopyCopesWithACallToAToolThatIsGone(t *testing.T) {
	// What an earlier run left behind: a call to a tool it had, answered
	// before the conversation was stored.
	conv := asked("the first question")
	conv.AddAssistant(model.AssistantMessage{ToolRequests: []model.ToolRequest{
		{ID: "t1", Name: "lookup", Arguments: json.RawMessage(`{"key":"rate"}`)},
	}})
	conv.AddToolResult("t1", "0.5")
	// The next run picks it up and asks for a fork.
	conv.AddUser("the second question")
	conv.AddAssistant(model.AssistantMessage{ToolRequests: []model.ToolRequest{
		{ID: "f1", Name: native.ForkTool, Arguments: json.RawMessage(`{"task":"settle it"}`)},
	}})

	copied := conv.Fork("the fork's prompt", "f1", "do the thing")
	if err := copied.Validate(); err != nil {
		t.Fatalf("a copy carrying a call to a tool the fork lacks does not validate: %v", err)
	}

	var sawLookup bool
	for _, m := range copied.Messages {
		if a, ok := m.(model.AssistantMessage); ok {
			for _, r := range a.ToolRequests {
				if r.Name == "lookup" {
					sawLookup = true
				}
			}
		}
	}
	if !sawLookup {
		t.Fatal("the copy does not carry the earlier call, so this did not test what it means to")
	}

	// Making the same call is refused, and as an ordinary failure: the copy
	// reads it on its next turn and carries on.
	w := forkWiring(t, nil)
	registry, err := forkRegistry(w, "fork-1", nil)
	if err != nil {
		t.Fatalf("forkRegistry() = %v", err)
	}
	_, _, err = registry.Answer(context.Background(), copied.Fork,
		model.ToolRequest{ID: "t2", Name: "lookup", Arguments: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatal("a copy was allowed to call a tool it does not have")
	}
	if !strings.Contains(err.Error(), "lookup") {
		t.Errorf("calling a tool that is gone = %v, want a refusal naming it", err)
	}
}

// The one tool a copy is offered and may not use. It refuses, the refusal does
// not end the copy, and what comes back is an ordinary failure the model reads
// on its next turn and carries on from.
func TestACopyRefusesToFork(t *testing.T) {
	w := forkWiring(t, nil)
	registry, err := forkRegistry(w, "fork-1", nil)
	if err != nil {
		t.Fatalf("forkRegistry() = %v", err)
	}
	conv := asked("the question")
	conv.AddAssistant(model.AssistantMessage{ToolRequests: []model.ToolRequest{
		{ID: "f1", Name: native.ForkTool, Arguments: json.RawMessage(`{"task":"settle it"}`)},
	}})

	req := model.ToolRequest{ID: "f1", Name: native.ForkTool, Arguments: json.RawMessage(`{"task":"settle it"}`)}
	_, _, err = registry.Answer(context.Background(), conv.Fork, req)
	if err == nil {
		t.Fatalf("a copy's %q ran", req.Name)
	}
	if !strings.Contains(err.Error(), "a copy may not copy itself") {
		t.Errorf("a copy's %q = %v, want a refusal saying why", req.Name, err)
	}
}

// A copy is refused whatever it writes in the call, because what is wrong with
// it is not how it was written. Sending it back to fix its arguments would cost
// a second turn on a call that was never going to run.
func TestACopysRefusalDoesNotDependOnTheArguments(t *testing.T) {
	w := forkWiring(t, nil)
	registry, err := forkRegistry(w, "fork-1", nil)
	if err != nil {
		t.Fatalf("forkRegistry() = %v", err)
	}
	conv := asked("the question")
	conv.AddAssistant(model.AssistantMessage{ToolRequests: []model.ToolRequest{
		{ID: "f1", Name: native.ForkTool, Arguments: json.RawMessage(`{"task":""}`)},
	}})

	_, _, err = registry.Answer(context.Background(), conv.Fork,
		model.ToolRequest{ID: "f1", Name: native.ForkTool, Arguments: json.RawMessage(`{"task":""}`)})
	if err == nil {
		t.Fatal("a copy's fork ran on an empty task")
	}
	if !strings.Contains(err.Error(), "a copy may not copy itself") {
		t.Errorf("an empty task in a copy = %v, want the refusal a copy always gets", err)
	}
}

// A fork's subagents may be given any of the run's skills, including ones the
// fork itself never loaded: what a task may name is the run's list, not the
// fork's reading history. A name outside that list is refused as it is at the
// top level.
func TestAForksSubagentsMayBeGivenAnyOfTheRunsSkills(t *testing.T) {
	w := forkWiring(t, []string{"writing", "testing"})

	// One subagents call spawns every task at once, so what they were given is
	// collected under a lock and compared as a set: which of them ran first is
	// scheduling, and nothing a test asserts may depend on it.
	var mu sync.Mutex
	var given []string
	w.client = &scriptedClient{}
	fork, err := forkRegistry(w, "fork-1", []string{"writing"})
	if err != nil {
		t.Fatalf("forkRegistry() = %v", err)
	}

	// Stand in for the runner, so what a subagent was given can be read off.
	spawned := native.NewSubagents(func(_ context.Context, brief native.Brief) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range brief.Skills {
			given = append(given, s.ID)
		}
		return "done", nil
	}, w.found, w.agents, w.maxSubagents)

	// testing is a skill the fork has not loaded; naming it for a subagent is
	// still allowed, because available is the run's list.
	_, err = spawned.Call(context.Background(), json.RawMessage(`{"tasks":[
		{"title":"One","body":"the first half","skills":["testing"]},
		{"title":"Two","body":"the second half","skills":["writing"]}
	]}`))
	if err != nil {
		t.Fatalf("a fork could not give its subagents the run's skills: %v", err)
	}
	sort.Strings(given)
	if !slices.Equal(given, []string{"testing", "writing"}) {
		t.Fatalf("subagents were given %v, want one skill each", given)
	}

	// A name that is not one of the run's fails the whole call, in a fork as
	// anywhere else.
	_, err = spawned.Call(context.Background(), json.RawMessage(`{"tasks":[
		{"title":"One","body":"the first half","skills":["invented"]},
		{"title":"Two","body":"the second half"}
	]}`))
	if err == nil {
		t.Fatal("a fork's subagent was given a skill the run does not have")
	}
	if !strings.Contains(err.Error(), "writing") || !strings.Contains(err.Error(), "testing") {
		t.Errorf("the refusal = %v, want it to name the skills there are", err)
	}
	// The fork still has both tools it should, whatever its subagents named.
	if !toolNames(fork)[native.LoadSkillTool] {
		t.Error("a fork in a run with skills has no loadskill")
	}
}

// A copy of a changed file is filed under the agent that made it, at every
// depth. The run, a fork, and a subagent each patch under their own name, which
// is the only thing telling those copies apart in a directory a person reads.
func TestBackupsAreFiledUnderTheAgentAtEveryDepth(t *testing.T) {
	sess, _, _ := recorded(t)
	w := forkWiring(t, nil)
	w.backups = sess.BackupDir()

	for _, c := range []struct {
		agent string
		build func() (*tools.Registry, error)
	}{
		{eventlog.MainAgent, func() (*tools.Registry, error) { return defaultRegistry(w) }},
		{"fork-1", func() (*tools.Registry, error) { return forkRegistry(w, "fork-1", nil) }},
		{"subagent-2", func() (*tools.Registry, error) {
			return workspaceRegistry(w.backups, "subagent-2", nil, nil)
		}},
	} {
		registry, err := c.build()
		if err != nil {
			t.Fatalf("building %s: %v", c.agent, err)
		}

		file := filepath.Join(t.TempDir(), "notes.txt")
		if err := os.WriteFile(file, []byte("before\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() = %v", err)
		}
		args, err := json.Marshal(map[string]string{
			"path": file, "old_str": "before", "new_str": "after",
		})
		if err != nil {
			t.Fatalf("Marshal() = %v", err)
		}
		if _, err := registry.Call(context.Background(), "patchfile", args); err != nil {
			t.Fatalf("patching as %s: %v", c.agent, err)
		}
	}

	kept, err := os.ReadDir(sess.BackupDir())
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}
	// A copy is named <UTC timestamp>-<agent>-<the file's own name>. The agent
	// is matched inside the name rather than by splitting on the separator,
	// because an agent's name has a hyphen of its own: fork-1-notes.txt cannot
	// be cut back into its parts without already knowing what agents are
	// called. Nothing parses these names — they exist to be read in a
	// directory listing, where the ambiguity costs nothing — so this reads them
	// the way the only reader does.
	var names []string
	for _, e := range kept {
		names = append(names, e.Name())
	}
	if len(names) != 3 {
		t.Errorf("%d copies were kept, want one per agent: %v", len(names), names)
	}
	for _, want := range []string{eventlog.MainAgent, "fork-1", "subagent-2"} {
		if !slices.ContainsFunc(names, func(n string) bool {
			return strings.Contains(n, "-"+want+"-notes.txt")
		}) {
			t.Errorf("no copy is filed under %q; found %v", want, names)
		}
	}
}
