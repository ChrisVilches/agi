package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/skills"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/tools/native"
)

// forkWiring is what these tests build agents from. The counter is the run's,
// as it is in a real run, because several of the tests below turn on the
// numbering being shared.
func forkWiring(t *testing.T, found []string) wiring {
	t.Helper()
	w := wiring{
		out:      quiet(),
		client:   &scriptedClient{},
		log:      nolog(),
		base:     "the base instruction",
		maxTurns: defaultMaxTurns,
		agents:   &native.Counter{},
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
	sub, err := workspaceRegistry("", "subagent-1", nil)
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
			has:     []string{native.ForkTool, native.SubagentsTool, "terminate", native.LoadSkillTool, "bash"},
			spawner: true,
		},
		{
			agent:   "fork",
			r:       fork,
			has:     []string{native.SubagentsTool, native.LoadSkillTool, "bash"},
			lacks:   []string{native.ForkTool, "terminate"},
			spawner: true,
		},
		{
			agent: "subagent",
			r:     sub,
			has:   []string{"bash"},
			lacks: []string{native.ForkTool, native.SubagentsTool, "terminate", native.LoadSkillTool},
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
		// no path through it is longer than run -> fork -> subagent.
		if spawns := got[native.ForkTool] || got[native.SubagentsTool]; spawns != c.spawner {
			t.Errorf("a %s spawns = %v, want %v", c.agent, spawns, c.spawner)
		}
	}
}

// A fork's list is the run's minus exactly two, and that has to hold by
// construction rather than by two hand-written lists staying in step.
func TestForkRegistryIsTheRunsMinusTwo(t *testing.T) {
	w := forkWiring(t, []string{"writing"})

	run, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	fork, err := forkRegistry(w, "fork-1", nil)
	if err != nil {
		t.Fatalf("forkRegistry() = %v", err)
	}

	missing := toolNames(run)
	for name := range toolNames(fork) {
		if !missing[name] {
			t.Errorf("a fork has %q, which the run itself does not", name)
		}
		delete(missing, name)
	}
	if len(missing) != 2 || !missing[native.ForkTool] || !missing["terminate"] {
		var left []string
		for name := range missing {
			left = append(left, name)
		}
		t.Errorf("the run withholds %v from a fork, want exactly fork and terminate", left)
	}
}

// Being inside a fork must buy a subagent nothing. The two are built from the
// same call, so this is what would catch a later edit giving one of them more.
func TestASubagentInsideAForkIsAnOrdinarySubagent(t *testing.T) {
	spawned, err := workspaceRegistry("", "subagent-1", nil)
	if err != nil {
		t.Fatalf("workspaceRegistry() = %v", err)
	}
	inFork, err := workspaceRegistry("", "subagent-2", nil)
	if err != nil {
		t.Fatalf("workspaceRegistry() = %v", err)
	}
	if a, b := strings.Join(spawned.Names(), ","), strings.Join(inFork.Names(), ","); a != b {
		t.Errorf("a subagent inside a fork has %s, a subagent of the run has %s", b, a)
	}
}

// The prompt half of the same rule. A fork's prompt is assembled from its own
// registry and never copied from the parent, so it must not explain the two
// tools it does not have — guidance for a tool the reader lacks is the standing
// lie that keeps workspace tools out of a prompt in the first place.
func TestAForkReadsNoGuidanceForToolsItDoesNotHave(t *testing.T) {
	w := forkWiring(t, []string{"writing"})

	fork, err := forkRegistry(w, "fork-1", nil)
	if err != nil {
		t.Fatalf("forkRegistry() = %v", err)
	}
	got := systemPromptFor(w.base, fork, skillsSection(w.found))

	if !strings.Contains(got, w.base) {
		t.Error("a fork does not read the run's own base instruction, which it must")
	}
	if strings.Contains(got, prompt.Fork()) {
		t.Error("a fork is told about the fork tool, which it does not have")
	}
	if strings.Contains(got, prompt.Terminate()) {
		t.Error("a fork is told about the terminate tool, which it does not have")
	}
	if !strings.Contains(got, prompt.Subagents()) {
		t.Error("a fork is not told about the subagents tool, which it does have")
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
	if _, err := loader.Call(context.Background(), json.RawMessage(`{"name":"writing"}`)); err != nil {
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
	if _, err := forked.Call(context.Background(), json.RawMessage(`{"name":"writing"}`)); err == nil {
		t.Error("a fork reloaded a skill whose text is already in the conversation it copied")
	}
	if _, err := forked.Call(context.Background(), json.RawMessage(`{"name":"testing"}`)); err != nil {
		t.Errorf("a fork could not load a skill the parent had not: %v", err)
	}

	// A subagent starts from nothing, so the same skill is new to it.
	fresh := native.NewLoadSkill(w.found, nil)
	if _, err := fresh.Call(context.Background(), json.RawMessage(`{"name":"writing"}`)); err != nil {
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

// contain is what stops a nested agent ending the whole run. converse reads a
// tool's error by type, and a nested agent's error travels that same path, so a
// termination raised inside one would end the run rather than the agent. It
// cannot arise today — terminate is registered for neither — but that is one
// line in one registry, a long way from where the damage would be done.
func TestContainStopsATerminationEscapingANestedAgent(t *testing.T) {
	end := &native.Termination{Reason: "nothing to answer with"}

	_, err := contain("", end)
	if err == nil {
		t.Fatal("contain() let a termination through as success")
	}
	var escaped *native.Termination
	if errors.As(err, &escaped) {
		t.Error("contain() let a termination stay a termination, so it would end the whole run")
	}
	if !strings.Contains(err.Error(), "end the run") {
		t.Errorf("contain() = %v, want it to say what the agent tried to do", err)
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
	if err := runAgent(context.Background(), w.out, nolog(), client, registry, conv, nil, defaultMaxTurns); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	if len(client.sent) != 3 {
		t.Fatalf("the gateway was asked %d times, want 3", len(client.sent))
	}

	// The copy's request is the second. It reads the run's base and not the
	// subagent instruction, because a fork is the agent the user is talking to.
	copied := client.sent[1].Conversation
	if !strings.Contains(copied.System, w.base) {
		t.Error("the copy does not read the run's own base instruction")
	}
	if strings.Contains(copied.System, prompt.Fork()) || strings.Contains(copied.System, prompt.Terminate()) {
		t.Error("the copy is told about tools it does not have")
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
