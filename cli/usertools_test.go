package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/session"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/tools/native"
	"github.com/urfave/cli/v3"
)

// toolFolder writes a folder of tools: a manifest naming one tool, and the
// script that runs it. The script is the whole of what the tool does, so a
// test says what it should do in shell and reads the result back.
func toolFolder(t *testing.T, name, body, parameters string) string {
	t.Helper()
	if parameters == "" {
		parameters = `{"type": "object", "properties": {}}`
	}

	dir := t.TempDir()
	manifest := `{"tools": [{
		"name": "` + name + `",
		"path": "run.sh",
		"description": "A tool the user wrote, used in a test.",
		"parameters": ` + parameters + `
	}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing the manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(body), 0o755); err != nil {
		t.Fatalf("writing the script: %v", err)
	}
	return dir
}

// userToolsOf runs the app far enough to read --tools and returns the tools
// the run would have been given. The action is replaced, so nothing is sent
// anywhere and no session is made: the flag is the whole of what is under
// test.
func userToolsOf(t *testing.T, args ...string) ([]tools.Tool, error) {
	t.Helper()

	var got []tools.Tool
	var readErr error
	app := NewApp("test")
	app.Action = func(_ context.Context, cmd *cli.Command) error {
		got, readErr = userToolsFlag(cmd)
		return nil
	}
	if err := app.Run(context.Background(), append([]string{"agi"}, args...)); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	return got, readErr
}

func names(ts []tools.Tool) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Definition().Name)
	}
	return out
}

// Without the flag a run has the tools agi ships and no others.
func TestUserToolsAreAbsentUnlessAskedFor(t *testing.T) {
	got, err := userToolsOf(t, "prompt")
	if err != nil {
		t.Fatalf("userToolsFlag() = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("userToolsFlag() = %v, want no tools", names(got))
	}
}

// The flag is repeatable, and the folders may be given in any order: nothing
// depends on it, since the model is offered tools by name.
func TestUserToolsComeFromEveryFolderNamed(t *testing.T) {
	first := toolFolder(t, "weather", "#!/bin/sh\necho sunny\n", "")
	second := toolFolder(t, "tides", "#!/bin/sh\necho high\n", "")

	got, err := userToolsOf(t, "--tools", second, "--tools", first, "prompt")
	if err != nil {
		t.Fatalf("userToolsFlag() = %v", err)
	}
	if strings.Join(names(got), ",") != "tides,weather" {
		t.Errorf("userToolsFlag() = %v, want the folders read in the order given", names(got))
	}
}

// A mistake in a manifest costs a message rather than a run, so it is found
// here — before a gateway is contacted or a session is created.
func TestUserToolsRefuseABrokenFolder(t *testing.T) {
	dir := toolFolder(t, "weather", "#!/bin/sh\necho sunny\n", "")
	if err := os.Remove(filepath.Join(dir, "run.sh")); err != nil {
		t.Fatalf("removing the script: %v", err)
	}

	if _, err := userToolsOf(t, "--tools", dir, "prompt"); err == nil {
		t.Error("userToolsFlag() = nil, want the missing script refused")
	}

	if _, err := userToolsOf(t, "--tools", t.TempDir(), "prompt"); err == nil {
		t.Error("userToolsFlag() of a folder with no manifest = nil, want an error")
	}

	// A --tools of nothing but spaces never reaches here: the flag refuses it
	// while the command line is read, which TestPathFlagsRefuseABlankValue
	// covers along with every other flag that names a path.
}

// Which of two tools of one name the model would reach is not a question agi
// answers quietly, whether the other one is the user's or agi's own.
func TestUserToolsRefuseANameThatIsTaken(t *testing.T) {
	agis := toolFolder(t, "bash", "#!/bin/sh\necho hello\n", "")
	_, err := userToolsOf(t, "--tools", agis, "prompt")
	if err == nil {
		t.Fatal("userToolsFlag() = nil, want a name agi provides refused")
	}
	if !strings.Contains(err.Error(), "bash") || !strings.Contains(err.Error(), "rename") {
		t.Errorf("userToolsFlag() = %v, want the name and what to do about it", err)
	}

	first := toolFolder(t, "weather", "#!/bin/sh\necho sunny\n", "")
	second := toolFolder(t, "weather", "#!/bin/sh\necho rainy\n", "")
	if _, err := userToolsOf(t, "--tools", first, "--tools", second, "prompt"); err == nil {
		t.Error("userToolsFlag() = nil, want two folders sharing a name refused")
	}
}

// Every tool agi provides is reserved, and the list is asked of the registry
// rather than written down, so a tool added later is reserved by that alone.
//
// The registry compared against is one with skills, because the names agi
// reserves are the ones it can ever register and not the ones this run
// happened to get. A name reserved only for runs that were given skills would
// be a manifest that works until somebody adds --skills to the command.
func TestProvidedNamesAreTheToolsAgiProvides(t *testing.T) {
	provided, err := providedNames()
	if err != nil {
		t.Fatalf("providedNames() = %v", err)
	}
	r, err := defaultRegistry(wiring{out: quiet(), client: &scriptedClient{}, log: nolog(), backups: "", user: nil, found: oneSkill(t), maxTurns: defaultMaxTurns})
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	if !provided[native.LoadSkillTool] {
		t.Errorf("providedNames() does not reserve %q", native.LoadSkillTool)
	}
	for _, name := range r.Names() {
		if !provided[name] {
			t.Errorf("providedNames() does not reserve %q", name)
		}
	}
	if len(provided) != r.Len() {
		t.Errorf("providedNames() reserves %d names, want %d", len(provided), r.Len())
	}
}

// A user-defined tool is a work tool, so it reaches a subagent for the same
// reason bash does: there is one list of them.
func TestUserToolsReachEveryAgent(t *testing.T) {
	dir := toolFolder(t, "weather", "#!/bin/sh\necho sunny\n", "")
	user, err := userToolsOf(t, "--tools", dir, "prompt")
	if err != nil {
		t.Fatalf("userToolsFlag() = %v", err)
	}

	run, err := defaultRegistry(wiring{out: quiet(), client: &scriptedClient{}, log: nolog(), backups: "", user: user, found: nil, maxTurns: defaultMaxTurns})
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	sub, err := workspaceRegistry("", eventlog.MainAgent, user)
	if err != nil {
		t.Fatalf("workspaceRegistry() = %v", err)
	}
	for _, r := range []*tools.Registry{run, sub} {
		if _, ok := r.Lookup("weather"); !ok {
			t.Error("a registry was built without the tool the user brought")
		}
	}

	// It is a work tool in the other way that matters too: the model learns
	// about it from its definition, and the system prompt says nothing.
	if strings.Contains(run.SystemPrompt(), "weather") {
		t.Error("a user-defined tool reached the system prompt")
	}
}

// What a user-defined tool did and what it could not do reach the conversation
// as the two things they are.
func TestUserToolResultsAndFailuresAreStored(t *testing.T) {
	cases := []struct {
		name   string
		script string
		// args are what the model asks the tool for. Empty means a call that
		// matches the tool's parameters.
		args  string
		check func(t *testing.T, conv *model.Conversation)
	}{
		{
			name:   "success",
			script: "#!/bin/sh\necho 'it is sunny in Lima'\n",
			check: func(t *testing.T, conv *model.Conversation) {
				result, ok := conv.Messages[2].(model.ToolResultMessage)
				if !ok {
					t.Fatalf("the answering turn is %T, want a tool result", conv.Messages[2])
				}
				if result.Content != "it is sunny in Lima" {
					t.Errorf("result = %q, want the script's standard output", result.Content)
				}
			},
		},
		{
			name:   "failure",
			script: "#!/bin/sh\necho 'no such city' >&2\nexit 1\n",
			check: func(t *testing.T, conv *model.Conversation) {
				failure, ok := conv.Messages[2].(model.ToolFailure)
				if !ok {
					t.Fatalf("the answering turn is %T, want a tool failure", conv.Messages[2])
				}
				if !strings.Contains(failure.Reason, "no such city") {
					t.Errorf("failure = %q, want the script's standard error", failure.Reason)
				}
			},
		},
		{
			// A call the tool would have refused is refused by agi first, so
			// the script never runs. This one would have succeeded had it
			// run, which is what makes the failure below proof that it did
			// not: the turn would be a result rather than a failure.
			name:   "arguments the tool would have refused",
			script: "#!/bin/sh\necho 'it is sunny somewhere'\n",
			args:   `{"city": 5, "when": "today"}`,
			check: func(t *testing.T, conv *model.Conversation) {
				failure, ok := conv.Messages[2].(model.ToolFailure)
				if !ok {
					t.Fatalf("the answering turn is %T, want a tool failure", conv.Messages[2])
				}
				want := `weather: the arguments do not match the tool's parameters: ` +
					`city must be a string, got a number; when is not a parameter of this tool`
				if failure.Reason != want {
					t.Errorf("failure = %q, want %q", failure.Reason, want)
				}
				// The model reads it marked as a failure rather than as an
				// answer, which is the whole reason the two are separate
				// types.
				if !strings.HasPrefix(failure.Render(), "error: ") {
					t.Errorf("Render() = %q, want it marked as an error", failure.Render())
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := toolFolder(t, "weather", c.script,
				`{"type": "object", "properties": {"city": {"type": "string"}},
				  "required": ["city"], "additionalProperties": false}`)
			user, err := userToolsOf(t, "--tools", dir, "prompt")
			if err != nil {
				t.Fatalf("userToolsFlag() = %v", err)
			}
			registry, err := workspaceRegistry("", eventlog.MainAgent, user)
			if err != nil {
				t.Fatalf("workspaceRegistry() = %v", err)
			}

			args := c.args
			if args == "" {
				args = `{"city": "Lima"}`
			}
			client := &scriptedClient{replies: []model.AssistantMessage{
				{ToolRequests: []model.ToolRequest{{
					ID:        "c1",
					Name:      "weather",
					Arguments: []byte(args),
				}}},
				model.NewAssistantMessage("done"),
			}}

			var out bytes.Buffer
			conv := asked("what is the weather in Lima?")
			if err := runAgent(context.Background(), pipedOutput(&out), nolog(), client, registry, conv, nil, defaultMaxTurns); err != nil {
				t.Fatalf("runAgent() = %v", err)
			}
			c.check(t, conv)
		})
	}
}

// A run given the user's tools says so under --verbose, the same way it
// accounts for every other tool it was given.
func TestVerbosePreambleShowsUserTools(t *testing.T) {
	dir := toolFolder(t, "weather", "#!/bin/sh\necho sunny\n", "")
	user, err := userToolsOf(t, "--tools", dir, "prompt")
	if err != nil {
		t.Fatalf("userToolsFlag() = %v", err)
	}
	registry, err := workspaceRegistry("", eventlog.MainAgent, user)
	if err != nil {
		t.Fatalf("workspaceRegistry() = %v", err)
	}

	sess, err := session.New()
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	defer os.RemoveAll(sess.Dir())

	client := &scriptedClient{replies: []model.AssistantMessage{model.NewAssistantMessage("4")}}
	var out bytes.Buffer
	verbose := output{answer: &out, steps: &out, verbose: true}
	if err := runSession(context.Background(), verbose, nolog(), client, registry, sess,
		prompt.Base(), "what is 2+2?", nil, nil, defaultMaxTurns); err != nil {
		t.Fatalf("runSession() = %v", err)
	}

	if !strings.Contains(out.String(), "weather") {
		t.Error("the preamble did not name the tool the user brought")
	}
	if !strings.Contains(out.String(), "A tool the user wrote") {
		t.Error("the preamble did not say what the tool the user brought is for")
	}
}
