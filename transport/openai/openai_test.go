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
	return Config{BaseURL: baseURL, Model: "deepseek-chat"}
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

// A request cannot be made without a model, so the configuration is where that
// is caught rather than every call site.
func TestNewRequiresModel(t *testing.T) {
	c, err := New(Config{BaseURL: "http://localhost:4000"})
	if err == nil {
		t.Fatal("New() without a model = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "model") {
		t.Errorf("New() error = %v, want it to mention the model", err)
	}
	if c != nil {
		t.Errorf("New() client = %v, want nil on error", c)
	}
}

// Callers must be able to tell "no backend wanted" from "backend misconfigured".
func TestUnsetURLIsErrNotConfigured(t *testing.T) {
	t.Setenv(EnvURL, "")
	t.Setenv(EnvAPIKey, "")

	c, err := FromEnv()
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
	t.Setenv(EnvModel, "deepseek-reasoner")

	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() = %v, want nil", err)
	}
	// A trailing slash must not produce "//v1/..." paths later.
	if got := c.BaseURL(); got != "https://api.deepseek.com" {
		t.Errorf("BaseURL() = %q, want the trailing slash trimmed", got)
	}
	if got := c.Model(); got != "deepseek-reasoner" {
		t.Errorf("Model() = %q, want the configured model", got)
	}
}

// A run that names no model gets the default rather than an error, so the
// common case needs one variable and not two.
func TestModelFallsBackToDefault(t *testing.T) {
	t.Setenv(EnvURL, "https://api.deepseek.com")
	t.Setenv(EnvModel, "")

	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() = %v, want nil", err)
	}
	if got := c.Model(); got != DefaultModel {
		t.Errorf("Model() = %q, want the default %q", got, DefaultModel)
	}

	t.Setenv(EnvModel, "  spaced-out  ")
	if got := ModelFromEnv(EnvModel); got != "spaced-out" {
		t.Errorf("ModelFromEnv() = %q, want it trimmed", got)
	}
}

// An API key is optional; an endpoint may be unauthenticated.
func TestAPIKeyIsOptional(t *testing.T) {
	t.Setenv(EnvURL, "https://api.deepseek.com")
	t.Setenv(EnvAPIKey, "")

	if _, err := FromEnv(); err != nil {
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
