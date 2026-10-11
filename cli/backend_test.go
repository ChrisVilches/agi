package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ChrisVilches/agi/transport/litellm"
	"github.com/ChrisVilches/agi/transport/openai"
)

// Naming a backend is what decides which set of variables is read, and each
// backend reads only its own. The model is not among them: one variable
// answers that for whichever backend was named.
func TestOpenBackendPicksTheNamedOne(t *testing.T) {
	t.Setenv(litellm.EnvURL, "http://gateway.internal:4000")
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")
	t.Setenv(EnvDefaultModel, "chosen-model")

	tests := []struct {
		backend string
		wantURL string
	}{
		{litellm.Name, "http://gateway.internal:4000"},
		{openai.Name, "https://api.deepseek.com"},
	}
	for _, tt := range tests {
		t.Run(tt.backend, func(t *testing.T) {
			t.Setenv(EnvBackend, tt.backend)

			b, err := openBackend("")
			if err != nil {
				t.Fatalf("openBackend() = %v, want nil", err)
			}
			if got := b.BaseURL(); got != tt.wantURL {
				t.Errorf("BaseURL() = %q, want %q", got, tt.wantURL)
			}
			if got := b.Model(); got != "chosen-model" {
				t.Errorf("Model() = %q, want the one model every backend is given", got)
			}
		})
	}
}

// Surrounding space arrived from a shell and is never part of a name.
func TestOpenBackendTrimsTheName(t *testing.T) {
	t.Setenv(EnvBackend, "  "+openai.Name+"  ")
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")
	t.Setenv(EnvDefaultModel, "deepseek-flash")

	if _, err := openBackend(""); err != nil {
		t.Fatalf("openBackend() = %v, want nil", err)
	}
}

// agi never picks a model for the user. A run that was not told which one to
// use — by the flag or by the variable — stops and says so, rather than
// spending a request and a context window on something nobody chose.
func TestOpenBackendRequiresAModel(t *testing.T) {
	t.Setenv(EnvBackend, openai.Name)
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")
	t.Setenv(EnvDefaultModel, "")

	b, err := openBackend("")
	if err == nil {
		t.Fatal("openBackend() with no model = nil error, want an error")
	}
	if b != nil {
		t.Errorf("openBackend() = %v, want nil on error", b)
	}
	// Both ways of answering are named, since either would have done.
	if !strings.Contains(err.Error(), EnvDefaultModel) {
		t.Errorf("error = %v, want it to name %s", err, EnvDefaultModel)
	}
	if !strings.Contains(err.Error(), "--model") {
		t.Errorf("error = %v, want it to mention --model", err)
	}
	// The user has to be told where to find a name that works.
	if !strings.Contains(err.Error(), "agi models") {
		t.Errorf("error = %v, want it to point at `agi models`", err)
	}
}

// --model is what one run runs on, and it wins over the model the machine
// runs on. Nothing else about the run changes: the name it carries is sent
// the way the variable's would have been.
func TestChosenModelOverridesTheVariable(t *testing.T) {
	t.Setenv(EnvBackend, openai.Name)
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")
	t.Setenv(EnvDefaultModel, "the-machine's-model")

	b, err := openBackend("this-run's-model")
	if err != nil {
		t.Fatalf("openBackend() = %v, want nil", err)
	}
	if got := b.Model(); got != "this-run's-model" {
		t.Errorf("Model() = %q, want the flag's model", got)
	}
}

// The flag is a second way of answering the question and not a second thing to
// answer: given one, a machine that never set the variable runs.
func TestChosenModelIsEnoughOnItsOwn(t *testing.T) {
	t.Setenv(EnvBackend, openai.Name)
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")
	t.Setenv(EnvDefaultModel, "")

	b, err := openBackend("deepseek-flash")
	if err != nil {
		t.Fatalf("openBackend() with only the flag = %v, want nil", err)
	}
	if got := b.Model(); got != "deepseek-flash" {
		t.Errorf("Model() = %q, want the flag's model", got)
	}
}

// A flag that was not given is not an answer, so the variable still answers
// for every run that does not name a model of its own.
func TestVariableAnswersWhenNoModelIsChosen(t *testing.T) {
	t.Setenv(EnvBackend, openai.Name)
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")
	t.Setenv(EnvDefaultModel, "deepseek-flash")

	b, err := openBackend("")
	if err != nil {
		t.Fatalf("openBackend() = %v, want nil", err)
	}
	if got := b.Model(); got != "deepseek-flash" {
		t.Errorf("Model() = %q, want the variable's model", got)
	}
}

// Space around a model name came from a shell, the way it does around a
// backend's, and a name that is nothing but space names nothing.
func TestOpenBackendTrimsTheModel(t *testing.T) {
	t.Setenv(EnvBackend, openai.Name)
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")

	t.Setenv(EnvDefaultModel, "   ")
	if _, err := openBackend(""); err == nil {
		t.Error("openBackend() with a model of only space = nil error, want an error")
	}

	t.Setenv(EnvDefaultModel, "  deepseek-flash  ")
	b, err := openBackend("")
	if err != nil {
		t.Fatalf("openBackend() = %v, want nil", err)
	}
	if got := b.Model(); got != "deepseek-flash" {
		t.Errorf("Model() = %q, want it trimmed", got)
	}

	// A name typed at the flag arrived from a shell as well, and is trimmed in
	// the same place and on the same terms. A flag that is nothing but space
	// never reaches here: its Validator refuses it while the command line is
	// being read, which is where TestModelFlagRefusesNothingButSpace checks it.
	t.Setenv(EnvDefaultModel, "deepseek-flash")
	b, err = openBackend("  deepseek-v4-pro  ")
	if err != nil {
		t.Fatalf("openBackend() = %v, want nil", err)
	}
	if got := b.Model(); got != "deepseek-v4-pro" {
		t.Errorf("Model() = %q, want the flag's model trimmed", got)
	}
}

// A model's name is sent exactly as `agi models` prints it, prefix and all:
// a gateway's listing is the spelling it expects back, and both shapes it
// prints are names of routes it set up.
func TestOpenBackendDoesNotRewriteTheModel(t *testing.T) {
	t.Setenv(EnvBackend, litellm.Name)
	t.Setenv(litellm.EnvURL, "http://gateway.internal:4000")

	for _, want := range []string{"deepseek-chat", "anthropic/claude-sonnet-5"} {
		t.Setenv(EnvDefaultModel, want)

		b, err := openBackend("")
		if err != nil {
			t.Fatalf("openBackend() = %v, want nil", err)
		}
		if got := b.Model(); got != want {
			t.Errorf("Model() = %q, want %q unchanged", got, want)
		}
	}
}

// Listing what a backend serves is the one question that needs no model, and
// it must work on a machine that has not chosen one: it is the command every
// missing-model error sends the user to, so refusing them for want of a model
// would send them in a circle.
func TestOpenEndpointNeedsNoModel(t *testing.T) {
	t.Setenv(EnvBackend, openai.Name)
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")
	t.Setenv(EnvDefaultModel, "")

	b, err := openEndpoint()
	if err != nil {
		t.Fatalf("openEndpoint() with no model = %v, want nil", err)
	}
	if got := b.Model(); got != "" {
		t.Errorf("Model() = %q, want none", got)
	}
}

// It still needs somewhere to ask, though: a listing with no endpoint is the
// same misconfiguration a run with none is.
func TestOpenEndpointStillNeedsABackend(t *testing.T) {
	t.Setenv(EnvBackend, "")

	if _, err := openEndpoint(); err == nil {
		t.Fatal("openEndpoint() with no backend = nil error, want an error")
	}
}

// There is no default backend: two exist, they read different variables, and
// guessing would send a user who configured one after the other one's.
func TestOpenBackendRequiresAChoice(t *testing.T) {
	t.Setenv(EnvBackend, "")
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")

	b, err := openBackend("")
	if err == nil {
		t.Fatal("openBackend() with no choice = nil error, want an error")
	}
	if b != nil {
		t.Errorf("openBackend() = %v, want nil on error", b)
	}
	for _, want := range []string{EnvBackend, litellm.Name, openai.Name} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

// A misspelled name must say what would have worked.
func TestOpenBackendRejectsAnUnknownName(t *testing.T) {
	t.Setenv(EnvBackend, "anthropic")

	if _, err := openBackend(""); err == nil {
		t.Fatal("openBackend() with an unknown name = nil error, want an error")
	} else if !strings.Contains(err.Error(), `"anthropic"`) || !strings.Contains(err.Error(), openai.Name) {
		t.Errorf("error = %v, want it to quote the name and list the choices", err)
	}
}

// The advice reads the same way twice, whatever order a map hands its keys
// over in.
func TestBackendNamesAreOrdered(t *testing.T) {
	want := litellm.Name + ", " + openai.Name
	for range 5 {
		if got := backendNames(); got != want {
			t.Fatalf("backendNames() = %q, want %q", got, want)
		}
	}
}

// The end of the chain: the name in AGI_DEFAULT_MODEL is what a run actually
// asks the endpoint for, unchanged, and it is what the run reports itself as
// being on. Everything above is about choosing a model; this is the claim that
// the choice arrives.
func TestChosenModelReachesTheRequestAndTheReport(t *testing.T) {
	var asked struct {
		mu    sync.Mutex
		model string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		asked.mu.Lock()
		asked.model = body.Model
		asked.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answerReply("done"))
	}))
	t.Cleanup(srv.Close)

	// A gateway's wildcard spelling, which is the shape most at risk of being
	// rewritten on the way through.
	const want = "anthropic/claude-sonnet-5"

	t.Setenv(EnvBackend, openai.Name)
	t.Setenv(openai.EnvURL, srv.URL)
	t.Setenv(EnvDefaultModel, want)
	t.Setenv("TMPDIR", t.TempDir())

	var out bytes.Buffer
	app := NewApp("test")
	app.Writer = &out
	app.ErrWriter = &out
	app.Reader = strings.NewReader("")

	if err := app.Run(context.Background(), []string{"agi", "--verbose", "--rm", "hello"}); err != nil {
		t.Fatalf("run = %v, want nil", err)
	}

	asked.mu.Lock()
	got := asked.model
	asked.mu.Unlock()
	if got != want {
		t.Errorf("model sent to the endpoint = %q, want %q", got, want)
	}
	// --verbose says which model the run is on, and it must be the same one.
	if !strings.Contains(out.String(), want) {
		t.Errorf("verbose output = %q, want it to name %q", out.String(), want)
	}
}

// The same claim for the flag: --model is what the endpoint is asked for, and
// what the run reports itself as being on, over a variable naming something
// else entirely. This is the one that would catch a flag read but never
// handed on — a run that overrode nothing and said nothing about it.
func TestModelFlagReachesTheRequestAndTheReport(t *testing.T) {
	var asked struct {
		mu    sync.Mutex
		model string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		asked.mu.Lock()
		asked.model = body.Model
		asked.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answerReply("done"))
	}))
	t.Cleanup(srv.Close)

	const want = "anthropic/claude-sonnet-5"

	t.Setenv(EnvBackend, openai.Name)
	t.Setenv(openai.EnvURL, srv.URL)
	t.Setenv(EnvDefaultModel, "deepseek-flash")
	t.Setenv("TMPDIR", t.TempDir())

	var out bytes.Buffer
	app := NewApp("test")
	app.Writer = &out
	app.ErrWriter = &out
	app.Reader = strings.NewReader("")

	if err := app.Run(context.Background(), []string{"agi", "--verbose", "--rm", "--model", want, "hello"}); err != nil {
		t.Fatalf("run = %v, want nil", err)
	}

	asked.mu.Lock()
	got := asked.model
	asked.mu.Unlock()
	if got != want {
		t.Errorf("model sent to the endpoint = %q, want the flag's %q", got, want)
	}
	if strings.Contains(out.String(), "deepseek-flash") {
		t.Errorf("verbose output = %q, want nothing about the model it overrode", out.String())
	}
	if !strings.Contains(out.String(), want) {
		t.Errorf("verbose output = %q, want it to name %q", out.String(), want)
	}
}

// A machine that never chose a model runs when the run names one, which is the
// whole of what the flag is for on a machine shared between models. Nothing
// beyond the model is different about such a run, so this goes all the way to
// the endpoint rather than stopping at openBackend.
func TestModelFlagRunsWithoutTheVariable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answerReply("the answer"))
	}))
	t.Cleanup(srv.Close)

	t.Setenv(EnvBackend, openai.Name)
	t.Setenv(openai.EnvURL, srv.URL)
	t.Setenv(EnvDefaultModel, "")
	t.Setenv("TMPDIR", t.TempDir())

	var out bytes.Buffer
	app := NewApp("test")
	app.Writer = &out
	app.ErrWriter = &out
	app.Reader = strings.NewReader("")

	if err := app.Run(context.Background(), []string{"agi", "--rm", "--model", "deepseek-flash", "hello"}); err != nil {
		t.Fatalf("run = %v, want nil", err)
	}
	if got := strings.TrimSpace(out.String()); got != "the answer" {
		t.Errorf("output = %q, want the answer", got)
	}
}

// A value of nothing but space is a quoting accident rather than a way of
// asking for the variable back, and the flag refuses it before a session is
// created or an endpoint contacted.
func TestModelFlagRefusesNothingButSpace(t *testing.T) {
	t.Setenv(EnvBackend, openai.Name)
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")
	t.Setenv(EnvDefaultModel, "deepseek-flash")

	var out bytes.Buffer
	app := NewApp("test")
	app.Writer = &out
	app.ErrWriter = &out
	app.Reader = strings.NewReader("")

	err := app.Run(context.Background(), []string{"agi", "--rm", "--model", "   ", "hello"})
	if err == nil {
		t.Fatal("run with a model of only space = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "names no model") {
		t.Errorf("error = %v, want it to say the value names no model", err)
	}
}

// `agi models` is the command every missing-model error sends the user to, and
// the flag must not have made it a command that needs one: a listing on a
// machine that has chosen nothing, with no --model either, still prints.
//
// A --model given to it is accepted and ignored, the way every root flag is —
// urfave hands a subcommand its parent's flags — and that is not asserted
// here, since it is the library's arrangement rather than anything agi says.
func TestModelsStillRunsWithNoModelAnywhere(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"deepseek-flash","owned_by":"deepseek"}]}`)
	}))
	t.Cleanup(srv.Close)

	t.Setenv(EnvBackend, openai.Name)
	t.Setenv(openai.EnvURL, srv.URL)
	t.Setenv(EnvDefaultModel, "")

	var out bytes.Buffer
	app := NewApp("test")
	app.Writer = &out
	app.ErrWriter = &out
	app.Reader = strings.NewReader("")

	if err := app.Run(context.Background(), []string{"agi", "models"}); err != nil {
		t.Fatalf("agi models = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "deepseek-flash") {
		t.Errorf("output = %q, want the catalogue", out.String())
	}
}
