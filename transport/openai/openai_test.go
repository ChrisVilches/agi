package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/transport"
)

// okConfig is a configuration that New accepts, for tests that are about
// something else.
func okConfig(baseURL string) Config {
	return Config{BaseURL: baseURL, Model: "test-model"}
}

func TestNewRejectsBadURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr string
	}{
		{"empty", "", EnvURL + " is not set"},
		{"no scheme", "localhost:4000", "http or https"},
		{"wrong scheme", "ftp://localhost:4000", "http or https"},
		{"no host", "http://", "no host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(okConfig(tt.baseURL))
			if err == nil {
				t.Fatalf("New(%q) = nil error, want an error", tt.baseURL)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("New(%q) error = %v, want it to contain %q", tt.baseURL, err, tt.wantErr)
			}
			if c != nil {
				t.Errorf("New(%q) client = %v, want nil on error", tt.baseURL, c)
			}
		})
	}
}

// A client may be built without a model, because one question an endpoint
// answers does not involve one: listing what it serves. `agi models` has to
// work on a machine that has not chosen a model, since it is the command that
// tells the user what to choose from.
func TestNewAllowsNoModelSoTheCatalogueCanBeListed(t *testing.T) {
	c, err := New(Config{BaseURL: "http://localhost:4000"})
	if err != nil {
		t.Fatalf("New() without a model = %v, want nil", err)
	}
	if got := c.Model(); got != "" {
		t.Errorf("Model() = %q, want empty", got)
	}
}

// Sending a conversation does need one, and that is where it is refused —
// close enough to the request that no client can quietly send an empty model
// name.
func TestSendRefusesWithoutAModel(t *testing.T) {
	c, err := New(Config{BaseURL: "http://localhost:4000"})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	_, err = c.Send(context.Background(), userRequest("hello"))
	if err == nil {
		t.Fatal("Send() without a model = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "no model named") {
		t.Errorf("Send() error = %v, want it to say no model was named", err)
	}
}

// Callers must be able to tell "no backend wanted" from "backend misconfigured".
func TestUnsetURLIsErrNotConfigured(t *testing.T) {
	t.Setenv(EnvURL, "")
	t.Setenv(EnvAPIKey, "")

	c, err := FromEnv("deepseek-flash")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("FromEnv() error = %v, want ErrNotConfigured", err)
	}
	if c != nil {
		t.Errorf("FromEnv() client = %v, want nil", c)
	}
}

func TestFromEnvReadsEnvironment(t *testing.T) {
	t.Setenv(EnvURL, "https://api.deepseek.com/")
	t.Setenv(EnvAPIKey, "sk-test")

	c, err := FromEnv("deepseek-v4-pro")
	if err != nil {
		t.Fatalf("FromEnv() = %v, want nil", err)
	}
	// A trailing slash must not produce "//v1/..." paths later.
	if got := c.BaseURL(); got != "https://api.deepseek.com" {
		t.Errorf("BaseURL() = %q, want the trailing slash trimmed", got)
	}
	if got := c.Model(); got != "deepseek-v4-pro" {
		t.Errorf("Model() = %q, want the model it was handed", got)
	}
}

// The model is the caller's to choose and this package has no opinion about
// it: no default to fall back to, and no variable of its own to read one from.
// Nothing here may put a model name into a run that nobody asked for.
func TestPackageNamesNoModelOfItsOwn(t *testing.T) {
	t.Setenv(EnvURL, "https://api.deepseek.com")

	c, err := FromEnv("  spaced-out  ")
	if err != nil {
		t.Fatalf("FromEnv() = %v, want nil", err)
	}
	if got := c.Model(); got != "spaced-out" {
		t.Errorf("Model() = %q, want it trimmed and otherwise untouched", got)
	}

	// A name carrying a provider prefix is a gateway's own spelling for a
	// route and must survive exactly as given; see litellm.FromEnv.
	c, err = FromEnv("anthropic/claude-sonnet-5")
	if err != nil {
		t.Fatalf("FromEnv() = %v, want nil", err)
	}
	if got := c.Model(); got != "anthropic/claude-sonnet-5" {
		t.Errorf("Model() = %q, want the prefix left alone", got)
	}
}

// An API key is optional; an endpoint may be unauthenticated.
func TestAPIKeyIsOptional(t *testing.T) {
	t.Setenv(EnvURL, "https://api.deepseek.com")
	t.Setenv(EnvAPIKey, "")

	if _, err := FromEnv("deepseek-flash"); err != nil {
		t.Fatalf("FromEnv() without an API key = %v, want nil", err)
	}
}

// A backend that fronts this protocol under another name says so in its
// errors, so a failure points at the thing the user configured.
func TestNameAndURLVarAppearInErrors(t *testing.T) {
	_, err := New(Config{Name: "litellm", URLVar: "LITELLM_URL", Model: "m"})
	if err == nil {
		t.Fatal("New() with no URL = nil error, want an error")
	}
	for _, want := range []string{"litellm", "LITELLM_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
}

// fakeEndpoint serves a canned chat-completions response and records the last
// request body it received.
func fakeEndpoint(t *testing.T, status int, body string, captured *wireRequest) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if captured != nil {
			if err := json.NewDecoder(r.Body).Decode(captured); err != nil {
				t.Errorf("decoding request: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func userRequest(prompt string) transport.Request {
	return transport.Request{Conversation: model.NewConversation("", prompt)}
}

func TestSendReturnsAssistantMessage(t *testing.T) {
	srv := fakeEndpoint(t, http.StatusOK,
		`{"choices":[{"message":{"role":"assistant","content":"hello there"}}]}`, nil)

	c, err := New(okConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	resp, err := c.Send(context.Background(), userRequest("hi"))
	if err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if resp.Message.Content != "hello there" {
		t.Errorf("content = %q, want %q", resp.Message.Content, "hello there")
	}
	if resp.Message.Role() != model.RoleAssistant {
		t.Errorf("role = %q, want assistant", resp.Message.Role())
	}
}

func TestSendReturnsToolCalls(t *testing.T) {
	toolArgs := `{"command":"df -h"}`
	body := fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":"",`+
		`"tool_calls":[{"id":"call-1","type":"function",`+
		`"function":{"name":"bash","arguments":%s}}]}}]}`, strconv.Quote(toolArgs))
	srv := fakeEndpoint(t, http.StatusOK, body, nil)

	c, err := New(okConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	resp, err := c.Send(context.Background(), userRequest("check disk"))
	if err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if len(resp.Message.ToolRequests) != 1 {
		t.Fatalf("tool requests = %d, want 1", len(resp.Message.ToolRequests))
	}
	call := resp.Message.ToolRequests[0]
	if call.Name != "bash" || call.ID != "call-1" {
		t.Errorf("call = %+v, want bash/call-1", call)
	}
	if string(call.Arguments) != toolArgs {
		t.Errorf("arguments = %s, want the decoded object", call.Arguments)
	}
}

// The model comes from the configuration, not from the request, so every call
// a client makes names the model it was configured with.
func TestSendUsesTheConfiguredModel(t *testing.T) {
	var got wireRequest
	srv := fakeEndpoint(t, http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`, &got)

	c, err := New(Config{BaseURL: srv.URL, Model: "deepseek-reasoner"})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := c.Send(context.Background(), userRequest("hi")); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if got.Model != "deepseek-reasoner" {
		t.Errorf("model sent = %q, want the configured model", got.Model)
	}
}

// The API key and the tool definitions must both reach the endpoint.
func TestSendAuthenticatesAndSendsTools(t *testing.T) {
	var got wireRequest
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)

	c, err := New(Config{BaseURL: srv.URL, Model: "deepseek-chat", APIKey: "sk-secret"})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	req := userRequest("hi")
	req.Conversation.System = "be helpful"
	req.Tools = []model.ToolDefinition{{Name: "bash", Description: "run", Parameters: map[string]any{"type": "object"}}}

	if _, err := c.Send(context.Background(), req); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if authHeader != "Bearer sk-secret" {
		t.Errorf("Authorization = %q, want the bearer token", authHeader)
	}
	if len(got.Tools) != 1 || got.Tools[0].Function.Name != "bash" {
		t.Errorf("tools sent = %+v, want the bash definition", got.Tools)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" {
		t.Errorf("messages sent = %+v, want the system prompt first", got.Messages)
	}
}

func TestSendOmitsAuthorizationWithoutKey(t *testing.T) {
	var authHeader string
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		seen = true
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)

	c, err := New(okConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := c.Send(context.Background(), userRequest("hi")); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if !seen {
		t.Fatal("the endpoint was never called")
	}
	if authHeader != "" {
		t.Errorf("Authorization = %q, want no header", authHeader)
	}
}

func TestSendReportsEndpointError(t *testing.T) {
	srv := fakeEndpoint(t, http.StatusUnauthorized,
		`{"error":{"message":"invalid api key","type":"auth_error"}}`, nil)

	c, err := New(okConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	_, err = c.Send(context.Background(), userRequest("hi"))
	if err == nil {
		t.Fatal("Send() = nil error, want the endpoint's error")
	}
	for _, want := range []string{"401", "invalid api key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
}

// An endpoint may fail with something that is not JSON at all.
func TestSendReportsNonJSONError(t *testing.T) {
	srv := fakeEndpoint(t, http.StatusBadGateway, "upstream is down", nil)

	c, err := New(okConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	_, err = c.Send(context.Background(), userRequest("hi"))
	if err == nil || !strings.Contains(err.Error(), "upstream is down") {
		t.Fatalf("error = %v, want the response body quoted", err)
	}
}

func TestSendRejectsEmptyChoices(t *testing.T) {
	srv := fakeEndpoint(t, http.StatusOK, `{"choices":[]}`, nil)

	c, err := New(okConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := c.Send(context.Background(), userRequest("hi")); err == nil {
		t.Fatal("Send() = nil error, want a no-choices error")
	}
}

// A malformed conversation must be caught before a request is made.
func TestSendValidatesConversation(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	t.Cleanup(srv.Close)

	c, err := New(okConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	bad := model.NewConversation("", "hi")
	bad.AddToolResult("ghost", "answers nothing")
	if _, err := c.Send(context.Background(), transport.Request{Conversation: bad}); err == nil {
		t.Fatal("Send() = nil error, want a validation error")
	}
	if called {
		t.Error("the endpoint was called despite an invalid conversation")
	}
}

// The client must remain usable through the interfaces the CLI depends on.
func TestClientSatisfiesBackend(t *testing.T) {
	c, err := New(okConfig("http://localhost:4000"))
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	var _ transport.Client = c
	var _ transport.Backend = c
}

// A model agi was told to use may be one the endpoint does not have, and that
// cannot be known before the request is made. When the refusal quotes the name
// back, the error says where to find one that works — and points at the
// listing rather than printing it, since this is one line at the end of a
// failed run.
//
// The two bodies are what the endpoints really answer, captured from each.
func TestRefusalNamingTheModelSaysWhereToLookForOne(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "provider",
			body: `{"error":{"message":"The supported API model names are deepseek-flash, deepseek-v4-pro, but you passed totally-made-up.","type":"invalid_request_error","code":"invalid_request_error"}}`,
		},
		{
			name: "gateway",
			body: `{"error":{"message":"litellm.BadRequestError: You passed in model=totally-made-up. There are no healthy deployments for this model.","type":"invalid_request_error","code":"400"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakeEndpoint(t, http.StatusBadRequest, tt.body, nil)

			c, err := New(Config{BaseURL: srv.URL, Model: "totally-made-up"})
			if err != nil {
				t.Fatalf("New() = %v, want nil", err)
			}
			_, err = c.Send(context.Background(), userRequest("hi"))
			if err == nil {
				t.Fatal("Send() with an unknown model = nil error, want an error")
			}
			if !strings.Contains(err.Error(), "agi models") {
				t.Errorf("error = %v, want it to point at `agi models`", err)
			}
			// The endpoint's own sentence has to survive: it is the part that
			// says what was actually wrong.
			if !strings.Contains(err.Error(), "totally-made-up") {
				t.Errorf("error = %v, want the endpoint's own message kept", err)
			}
			// Pointing at the listing is not printing it.
			if strings.Contains(err.Error(), "MODEL ") || strings.Contains(err.Error(), "MAX OUTPUT") {
				t.Errorf("error = %v, want no listing printed into it", err)
			}
		})
	}
}

// A refusal that is about something else gets no advice about model names: a
// gateway that cannot reach its provider is not fixed by choosing another one.
func TestRefusalAboutAnythingElseIsLeftAlone(t *testing.T) {
	srv := fakeEndpoint(t, http.StatusUnauthorized,
		`{"error":{"message":"Authentication Fails, Your api key is invalid","type":"authentication_error"}}`, nil)

	c, err := New(Config{BaseURL: srv.URL, Model: "deepseek-flash"})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	_, err = c.Send(context.Background(), userRequest("hi"))
	if err == nil {
		t.Fatal("Send() = nil error, want an error")
	}
	if strings.Contains(err.Error(), "agi models") {
		t.Errorf("error = %v, want no advice about model names on an auth failure", err)
	}
}

// A model agi asked for and the endpoint accepted is never second-guessed:
// `deepseek-chat` is absent from DeepSeek's listing and answers anyway, and a
// run on one must not be refused for not appearing in a catalogue.
func TestAModelMissingFromTheListingIsStillSent(t *testing.T) {
	var sent wireRequest
	srv := fakeEndpoint(t, http.StatusOK,
		`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`, &sent)

	c, err := New(Config{BaseURL: srv.URL, Model: "deepseek-chat"})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	if _, err := c.Send(context.Background(), userRequest("hi")); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if sent.Model != "deepseek-chat" {
		t.Errorf("model sent = %q, want the name agi was given, unchanged", sent.Model)
	}
}

// A reasoning model's deliberation comes back on the turn, and goes out again
// on the next request. The round trip is the whole feature: DeepSeek refuses a
// request carrying tools whose earlier assistant turns have lost it, so a
// client that read it and did not send it back would be a client that could
// not take a second turn.
func TestSendReadsReasoningAndHandsItBack(t *testing.T) {
	var got wireRequest
	srv := fakeEndpoint(t, http.StatusOK,
		`{"choices":[{"message":{"role":"assistant","content":"it is cloudy",`+
			`"reasoning_content":"the tool answered, so I can tell them"}}]}`, &got)

	c, err := New(Config{BaseURL: srv.URL, Model: "deepseek-reasoner"})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	req := userRequest("how's the weather")
	resp, err := c.Send(context.Background(), req)
	if err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	const want = "the tool answered, so I can tell them"
	if resp.Message.ReasoningContent != want {
		t.Fatalf("reasoning returned = %q, want %q", resp.Message.ReasoningContent, want)
	}

	// The turn goes back into the conversation and the run continues, which is
	// when the endpoint has to be shown the reasoning again.
	req.Conversation.AddAssistant(resp.Message)
	req.Conversation.AddUser("and tomorrow?")
	if _, err := c.Send(context.Background(), req); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages sent = %d, want the user turn, the reply and the follow-up", len(got.Messages))
	}
	if got.Messages[1].ReasoningContent != want {
		t.Errorf("reasoning sent back = %q, want %q", got.Messages[1].ReasoningContent, want)
	}
}

// A route that reasons in signed blocks reads back only those, ignoring the
// words beside them, so the blocks have to make the same round trip — and
// arrive as the bytes the endpoint signed, since it verifies them.
func TestSendHandsBackTheShapeTheReplyCameIn(t *testing.T) {
	const blocks = `[{"type":"thinking","thinking":"a long think","signature":"sig/abc+123=="}]`
	var got wireRequest
	srv := fakeEndpoint(t, http.StatusOK,
		`{"choices":[{"message":{"role":"assistant","content":"it is cloudy",`+
			`"reasoning_content":"a long think","thinking_blocks":`+blocks+`}}]}`, &got)

	c, err := New(Config{BaseURL: srv.URL, Model: "anthropic/claude-sonnet-5"})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	req := userRequest("how's the weather")
	resp, err := c.Send(context.Background(), req)
	if err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if string(resp.Message.ReasoningReplay.ThinkingBlocks) != blocks {
		t.Fatalf("blocks returned = %s, want %s", resp.Message.ReasoningReplay.ThinkingBlocks, blocks)
	}

	req.Conversation.AddAssistant(resp.Message)
	req.Conversation.AddUser("and tomorrow?")
	if _, err := c.Send(context.Background(), req); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if string(got.Messages[1].ThinkingBlocks) != blocks {
		t.Errorf("blocks sent back = %s, want %s", got.Messages[1].ThinkingBlocks, blocks)
	}
}

// Nothing says whether a model reasons, so every model is sent to as though it
// might and one that returns nothing costs nothing: the field is left out
// rather than sent empty, so an endpoint that has never heard of it — OpenAI's
// own, whose reasoning lives in an API agi does not speak — is never shown it.
func TestSendOmitsReasoningForAModelThatReturnsNone(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"hello there"}}]}`)
	}))
	t.Cleanup(srv.Close)

	c, err := New(Config{BaseURL: srv.URL, Model: "gpt-5"})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	resp, err := c.Send(context.Background(), userRequest("hi"))
	if err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if resp.Message.ReasoningContent != "" {
		t.Errorf("reasoning = %q, want none for a reply that carried none", resp.Message.ReasoningContent)
	}

	req := userRequest("hi")
	req.Conversation.AddAssistant(resp.Message)
	if _, err := c.Send(context.Background(), req); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	for _, key := range []string{"reasoning_content", "thinking_blocks", "reasoning_items"} {
		if strings.Contains(string(body), key) {
			t.Errorf("request = %s, want no %s key at all", body, key)
		}
	}
}

// There is no client that does not carry reasoning: an endpoint that
// volunteers it is kept, whatever it was configured for, because an endpoint
// that returns the field is an endpoint that reads it back. Nothing has to be
// decided in advance, which is what a name-based rule would have required.
func TestSendKeepsReasoningWhateverTheClientWasBuiltFor(t *testing.T) {
	srv := fakeEndpoint(t, http.StatusOK,
		`{"choices":[{"message":{"content":"done","reasoning_content":"a long think"}}]}`, nil)

	c, err := New(okConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	resp, err := c.Send(context.Background(), userRequest("hi"))
	if err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if resp.Message.ReasoningContent != "a long think" {
		t.Errorf("reasoning = %q, want what the endpoint volunteered", resp.Message.ReasoningContent)
	}
	if resp.Message.Content != "done" {
		t.Errorf("content = %q, want the reply itself untouched", resp.Message.Content)
	}
}

// Reasoning is carried for every endpoint this backend reaches rather than for
// a list of models known to reason: a model name does not say what is behind
// it.
func TestFromEnvCarriesReasoning(t *testing.T) {
	srv := fakeEndpoint(t, http.StatusOK,
		`{"choices":[{"message":{"content":"done","reasoning_content":"a long think"}}]}`, nil)
	t.Setenv(EnvURL, srv.URL)
	t.Setenv(EnvAPIKey, "")

	c, err := FromEnv("deepseek-reasoner")
	if err != nil {
		t.Fatalf("FromEnv() = %v, want nil", err)
	}
	resp, err := c.Send(context.Background(), userRequest("hi"))
	if err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if resp.Message.ReasoningContent != "a long think" {
		t.Errorf("reasoning = %q, want the provider's own, kept", resp.Message.ReasoningContent)
	}
}
