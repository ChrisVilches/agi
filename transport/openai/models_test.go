package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/transport"
)

// The two catalogues agi actually gets back, copied from what each endpoint
// returns rather than written to suit the parser. They are what the one shape
// above this file has to be true of, and they disagree about the context
// window in the way the parser exists to reconcile.
const (
	deepseekCatalogue = `{"object":"list","data":[
		{"id":"deepseek-flash","object":"model","owned_by":"deepseek","name":"DeepSeek-V4.1-Flash",
		 "context_window":1048576,"max_output_tokens":393216,
		 "input_modalities":["text","image"],"output_modalities":["text"],
		 "effort":{"supported_levels":["low","high","max"],"default_level":"high"}},
		{"id":"deepseek-v4-pro","object":"model","owned_by":"deepseek","name":"DeepSeek-V4-Pro",
		 "context_window":1048576,"max_output_tokens":393216,
		 "input_modalities":["text"],"output_modalities":["text"]}]}`

	gatewayCatalogue = `{"data":[
		{"id":"deepseek-chat","object":"model","created":1677610602,"owned_by":"openai","mode":"chat",
		 "max_input_tokens":131072,"max_output_tokens":8192},
		{"id":"moonshot/kimi-k3","object":"model","created":1677610602,"owned_by":"openai","mode":"chat",
		 "max_input_tokens":1048576,"max_output_tokens":1048576}],"object":"list"}`
)

// fakeCatalogue serves a canned model listing and records the request it was
// asked, so a test can check how the listing was fetched as well as what came
// back from it.
func fakeCatalogue(t *testing.T, status int, body string, seen *http.Request) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = *r
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// client is a Client pointed at a test server, for tests about the listing
// rather than about configuration.
func client(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := New(okConfig(baseURL))
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	return c
}

// The whole point of the type is that one listing comes back whichever
// endpoint was asked, so both catalogues are decoded against the same
// expectations — including the two spellings of the context window.
func TestModelsAreOneShapeWhicheverEndpointAnswered(t *testing.T) {
	tests := []struct {
		name      string
		catalogue string
		want      []transport.Model
	}{
		{
			name:      "provider says context_window",
			catalogue: deepseekCatalogue,
			want: []transport.Model{
				{ID: "deepseek-flash", OwnedBy: "deepseek", ContextWindow: 1048576, MaxOutputTokens: 393216},
				{ID: "deepseek-v4-pro", OwnedBy: "deepseek", ContextWindow: 1048576, MaxOutputTokens: 393216},
			},
		},
		{
			name:      "gateway says max_input_tokens",
			catalogue: gatewayCatalogue,
			want: []transport.Model{
				{ID: "deepseek-chat", OwnedBy: "openai", ContextWindow: 131072, MaxOutputTokens: 8192},
				{ID: "moonshot/kimi-k3", OwnedBy: "openai", ContextWindow: 1048576, MaxOutputTokens: 1048576},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakeCatalogue(t, http.StatusOK, tt.catalogue, nil)

			got, err := client(t, srv.URL).Models(context.Background())
			if err != nil {
				t.Fatalf("Models() = %v, want nil", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("Models() returned %d models, want %d: %v", len(got), len(tt.want), got)
			}
			for i, want := range tt.want {
				if got[i] != want {
					t.Errorf("Models()[%d] = %+v, want %+v", i, got[i], want)
				}
			}
		})
	}
}

// The listing is a GET at the versioned path, carrying the key and no body:
// a request that announced a JSON body it does not have would be describing
// itself wrongly to the endpoint.
func TestModelsAsksTheRightRequest(t *testing.T) {
	var seen http.Request
	srv := fakeCatalogue(t, http.StatusOK, deepseekCatalogue, &seen)

	c, err := New(Config{BaseURL: srv.URL, APIKey: "sk-test", Model: "deepseek-chat"})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	if _, err := c.Models(context.Background()); err != nil {
		t.Fatalf("Models() = %v, want nil", err)
	}

	if seen.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", seen.Method)
	}
	if seen.URL.Path != modelsPath {
		t.Errorf("path = %q, want %q", seen.URL.Path, modelsPath)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want the bearer token", got)
	}
	if got := seen.Header.Get("Content-Type"); got != "" {
		t.Errorf("Content-Type = %q, want none on a request with no body", got)
	}
}

// A refusal must name the backend, the address, and whatever the endpoint said
// about it, so the message points at the thing to fix.
func TestModelsReportsARefusal(t *testing.T) {
	srv := fakeCatalogue(t, http.StatusUnauthorized,
		`{"error":{"message":"Authentication Fails","type":"authentication_error"}}`, nil)

	c, err := New(Config{BaseURL: srv.URL, Model: "deepseek-chat", Name: "litellm", URLVar: "LITELLM_URL"})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	_, err = c.Models(context.Background())
	if err == nil {
		t.Fatal("Models() against a refusing endpoint = nil error, want an error")
	}
	for _, want := range []string{"litellm", modelsPath, "401", "Authentication Fails"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
}

// An endpoint in trouble answers in whatever it likes, and the body is quoted
// rather than dropped for not being the protocol's error object.
func TestModelsQuotesANonJSONRefusal(t *testing.T) {
	srv := fakeCatalogue(t, http.StatusBadGateway, "upstream is down", nil)

	_, err := client(t, srv.URL).Models(context.Background())
	if err == nil {
		t.Fatal("Models() = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "upstream is down") {
		t.Errorf("error = %v, want it to quote the body", err)
	}
}

// A 200 carrying an error object is still a failure, and must not be read as a
// catalogue of no models.
func TestModelsReportsAnErrorSentWithAnOKStatus(t *testing.T) {
	srv := fakeCatalogue(t, http.StatusOK, `{"error":{"message":"no providers configured"}}`, nil)

	models, err := client(t, srv.URL).Models(context.Background())
	if err == nil {
		t.Fatalf("Models() = %v, nil error, want an error", models)
	}
	if !strings.Contains(err.Error(), "no providers configured") {
		t.Errorf("error = %v, want the endpoint's message", err)
	}
}

// A body that is not the catalogue at all fails rather than coming back empty,
// which would read as an endpoint that serves nothing.
func TestModelsRejectsAnUndecodableBody(t *testing.T) {
	srv := fakeCatalogue(t, http.StatusOK, "<html>not json</html>", nil)

	_, err := client(t, srv.URL).Models(context.Background())
	if err == nil {
		t.Fatal("Models() on a body that is not JSON = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "decoding response") {
		t.Errorf("error = %v, want it to say what failed", err)
	}
}

// An entry with no name cannot be asked for, so it is left out rather than
// printed as a row nothing can be done with. The rest of the listing stands.
func TestModelsDropsNamelessEntries(t *testing.T) {
	srv := fakeCatalogue(t, http.StatusOK,
		`{"data":[{"id":"  ","owned_by":"nobody"},{"id":"deepseek-chat","owned_by":"deepseek"}]}`, nil)

	got, err := client(t, srv.URL).Models(context.Background())
	if err != nil {
		t.Fatalf("Models() = %v, want nil", err)
	}
	if len(got) != 1 || got[0].ID != "deepseek-chat" {
		t.Errorf("Models() = %+v, want only the named model", got)
	}
}

// An endpoint that really serves nothing is not an error here: it answered.
// What to do about an empty listing is the command's decision, not the
// transport's.
func TestModelsAllowsAnEmptyCatalogue(t *testing.T) {
	srv := fakeCatalogue(t, http.StatusOK, `{"object":"list","data":[]}`, nil)

	got, err := client(t, srv.URL).Models(context.Background())
	if err != nil {
		t.Fatalf("Models() = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Models() = %+v, want nothing", got)
	}
}

// A cancelled listing fails rather than hanging, the way a cancelled run does.
func TestModelsHonorsContextCancellation(t *testing.T) {
	srv := fakeCatalogue(t, http.StatusOK, deepseekCatalogue, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client(t, srv.URL).Models(ctx); err == nil {
		t.Fatal("Models() with a cancelled context = nil error, want an error")
	}
}
