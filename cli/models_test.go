package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/transport"
	"github.com/ChrisVilches/agi/transport/openai"
)

// servesModels points the openai backend at a test server answering with the
// given catalogue, which is as much of a backend as this command needs.
func servesModels(t *testing.T, catalogue string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(catalogue)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv(EnvBackend, openai.Name)
	t.Setenv(openai.EnvURL, srv.URL)
	t.Setenv(openai.EnvAPIKey, "sk-test")
}

func TestModelsPrintsTheCatalogue(t *testing.T) {
	servesModels(t, `{"object":"list","data":[
		{"id":"deepseek-v4-pro","owned_by":"deepseek","context_window":1048576,"max_output_tokens":393216},
		{"id":"deepseek-flash","owned_by":"deepseek","context_window":1048576,"max_output_tokens":393216}]}`)

	out, err := run(t, "agi", "models")
	if err != nil {
		t.Fatalf("agi models = %v, want nil", err)
	}
	for _, want := range []string{"MODEL", "deepseek-flash", "deepseek-v4-pro", "1048576", "393216"} {
		if !strings.Contains(out, want) {
			t.Errorf("agi models printed %q, want it to contain %q", out, want)
		}
	}
}

// A gateway hands its catalogue back in whatever order it assembled it, so the
// listing is sorted here: somebody scanning for one name should find it where
// they would look.
func TestModelsAreSortedByName(t *testing.T) {
	servesModels(t, `{"data":[{"id":"zeta"},{"id":"alpha"},{"id":"mu"}]}`)

	out, err := run(t, "agi", "models")
	if err != nil {
		t.Fatalf("agi models = %v, want nil", err)
	}
	alpha, mu, zeta := strings.Index(out, "alpha"), strings.Index(out, "mu"), strings.Index(out, "zeta")
	if !(alpha < mu && mu < zeta) {
		t.Errorf("agi models printed %q, want the names in order", out)
	}
}

// What a backend did not say is shown as an absence and never as a zero, which
// would read as a model that can carry nothing.
func TestModelsShowsWhatTheBackendDidNotSay(t *testing.T) {
	servesModels(t, `{"data":[{"id":"mystery-model"}]}`)

	out, err := run(t, "agi", "models")
	if err != nil {
		t.Fatalf("agi models = %v, want nil", err)
	}
	if strings.Contains(out, "0") {
		t.Errorf("agi models printed %q, want no zero for a field the endpoint omitted", out)
	}
	if !strings.Contains(out, "-") {
		t.Errorf("agi models printed %q, want a dash for the missing fields", out)
	}
}

// Columns are the point of the layout, so the names must all start in the same
// place however long the one above is.
func TestModelsAlignsItsColumns(t *testing.T) {
	servesModels(t, `{"data":[
		{"id":"a","owned_by":"x","context_window":1},
		{"id":"a-very-long-model-name","owned_by":"yyyy","context_window":22}]}`)

	out, err := run(t, "agi", "models")
	if err != nil {
		t.Fatalf("agi models = %v, want nil", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("agi models printed %d lines, want a header and two models: %q", len(lines), out)
	}
	want := strings.Index(lines[0], "OWNED BY")
	for i, line := range lines[1:] {
		// The second column begins where the header's does, whatever is to
		// its left.
		if got := strings.Index(line[want:], strings.Fields(line)[1]); got != 0 {
			t.Errorf("line %d (%q): second column is %d past where the header's is", i+1, line, got)
		}
	}
	for _, line := range lines {
		if strings.HasSuffix(line, " ") {
			t.Errorf("line %q ends in trailing space", line)
		}
	}
}

// An endpoint that answered with nothing is not silently an empty screen: a
// listing that printed nothing cannot be told from one that failed to print.
func TestModelsRefusesAnEmptyCatalogue(t *testing.T) {
	servesModels(t, `{"object":"list","data":[]}`)

	_, err := run(t, "agi", "models")
	if err == nil {
		t.Fatal("agi models against an empty endpoint = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "lists no models") {
		t.Errorf("error = %v, want it to say the endpoint lists none", err)
	}
}

// The command needs a backend for the same reason a run does, and is refused
// the same way when it has none.
func TestModelsNeedsABackend(t *testing.T) {
	t.Setenv(EnvBackend, "")

	_, err := run(t, "agi", "models")
	if err == nil {
		t.Fatal("agi models with no backend = nil error, want an error")
	}
	if !strings.Contains(err.Error(), EnvBackend) {
		t.Errorf("error = %v, want it to name %s", err, EnvBackend)
	}
}

// The command takes nothing, and the likeliest way to arrive with an argument
// is a prompt whose first word is the command's name — which did not run and
// must not look as though it had.
func TestModelsRefusesArguments(t *testing.T) {
	servesModels(t, `{"data":[{"id":"deepseek-chat"}]}`)

	_, err := run(t, "agi", "models", "for", "coding")
	if err == nil {
		t.Fatal("agi models with arguments = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "takes no arguments") {
		t.Errorf("error = %v, want it to say the command takes none", err)
	}
}

// The command list is consulted for the first argument only, so adding a
// subcommand costs exactly one prompt — the word itself, written alone — and
// changes nothing else about how a run is invoked. That claim is the reason
// `models` is a subcommand at all, so it is checked rather than assumed.
func TestSubcommandShadowsOnlyItsOwnWord(t *testing.T) {
	// The root action reads --sys and the subcommand never does, so a run
	// that fails on an unreadable one is a run that reached the agent.
	reachedTheAgent := "reading the system prompt"

	tests := []struct {
		name  string
		args  []string
		stdin string
		agent bool
	}{
		{name: "the word alone", args: []string{"agi", "models"}},
		{name: "the word with flags before it", args: []string{"agi", "-v", "models"}},
		{name: "a prompt beginning with it", args: []string{"agi", "models for coding"}, agent: true},
		{name: "a prompt merely containing it", args: []string{"agi", "which models are best?"}, agent: true},
		{name: "a prompt that is the word, piped in", args: []string{"agi"}, stdin: "models", agent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvBackend, "")

			args := append([]string{tt.args[0], "--sys", "/nonexistent/prompt.md"}, tt.args[1:]...)
			_, err := runStdin(t, tt.stdin, args...)
			if err == nil {
				t.Fatalf("%v = nil error, want an error", args)
			}
			if got := strings.Contains(err.Error(), reachedTheAgent); got != tt.agent {
				t.Errorf("%v = %v; reached the agent = %v, want %v", args, err, got, tt.agent)
			}
		})
	}
}

// The subcommand is in --help, since a word that no longer works as a prompt
// has to be findable as the thing it became.
func TestModelsIsListedInHelp(t *testing.T) {
	out, err := run(t, "agi", "--help")
	if err != nil {
		t.Fatalf("agi --help = %v, want nil", err)
	}
	if !strings.Contains(out, "models") {
		t.Errorf("agi --help printed %q, want it to list the models command", out)
	}
}

// Nothing in a listing is useful without the name, so an empty one is a bug
// worth catching in the renderer and not only in the parser.
func TestWriteModelsKeepsEveryRowNamed(t *testing.T) {
	var out strings.Builder
	models := []transport.Model{{ID: "only-model", OwnedBy: "someone"}}
	if err := writeModels(&out, models); err != nil {
		t.Fatalf("writeModels() = %v, want nil", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("writeModels() printed %d lines, want a header and one model", len(lines))
	}
	if !strings.HasPrefix(lines[1], "only-model") {
		t.Errorf("row = %q, want it to begin with the model's name", lines[1])
	}
}

// Listing models makes one request and runs no agent, so a cancelled one ends
// where a cancelled run would.
func TestModelsHonorsCancellation(t *testing.T) {
	servesModels(t, `{"data":[{"id":"deepseek-chat"}]}`)

	app := NewApp("test")
	var out strings.Builder
	app.Writer = &out
	app.ErrWriter = &out

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := app.Run(ctx, []string{"agi", "models"}); err == nil {
		t.Fatal("agi models with a cancelled context = nil error, want an error")
	}
}
