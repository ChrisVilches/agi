package litellm

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
			g, err := New(Config{BaseURL: tt.baseURL})
			if err == nil {
				t.Fatalf("New(%q) = nil error, want an error", tt.baseURL)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("New(%q) error = %v, want it to contain %q", tt.baseURL, err, tt.wantErr)
			}
			if g != nil {
				t.Errorf("New(%q) gateway = %v, want nil on error", tt.baseURL, g)
			}
		})
	}
}

// Callers must be able to tell "no gateway wanted" from "gateway misconfigured".
func TestUnsetURLIsErrNotConfigured(t *testing.T) {
	t.Setenv(EnvURL, "")
	t.Setenv(EnvAPIKey, "")

	g, err := FromEnv()
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("FromEnv() error = %v, want ErrNotConfigured", err)
	}
	if g != nil {
		t.Errorf("FromEnv() gateway = %v, want nil", g)
	}
	if Configured() {
		t.Error("Configured() = true with no URL set, want false")
	}
}

func TestFromEnvReadsEnvironment(t *testing.T) {
	t.Setenv(EnvURL, "http://gateway.internal:4000/")
	t.Setenv(EnvAPIKey, "sk-test")

	if !Configured() {
		t.Error("Configured() = false with a URL set, want true")
	}
	g, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() = %v, want nil", err)
	}
	// A trailing slash must not produce "//v1/..." paths later.
	if got := g.BaseURL(); got != "http://gateway.internal:4000" {
		t.Errorf("BaseURL() = %q, want the trailing slash trimmed", got)
	}
}

// An API key is optional; a gateway may be unauthenticated.
func TestAPIKeyIsOptional(t *testing.T) {
	t.Setenv(EnvURL, "https://gateway.internal")
	t.Setenv(EnvAPIKey, "")

	if _, err := FromEnv(); err != nil {
		t.Fatalf("FromEnv() without an API key = %v, want nil", err)
	}
}

// fakeGateway serves a canned chat-completions response and records the last
// request body it received.
func fakeGateway(t *testing.T, status int, body string, captured *wireRequest) *httptest.Server {
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
	return transport.Request{
		Model: "deepseek-chat",
		Conversation: model.Conversation{Messages: []model.Message{
			{Role: model.RoleUser, Content: prompt},
		}},
	}
}

func TestSendReturnsAssistantMessage(t *testing.T) {
	srv := fakeGateway(t, http.StatusOK,
		`{"choices":[{"message":{"role":"assistant","content":"hello there"}}]}`, nil)

	g, err := New(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	resp, err := g.Send(context.Background(), userRequest("hi"))
	if err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if resp.Message.Content != "hello there" {
		t.Errorf("content = %q, want %q", resp.Message.Content, "hello there")
	}
	if resp.Message.Role != model.RoleAssistant {
		t.Errorf("role = %q, want assistant", resp.Message.Role)
	}
}

func TestSendReturnsToolCalls(t *testing.T) {
	toolArgs := `{"command":"df -h"}`
	body := fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":"",`+
		`"tool_calls":[{"id":"call-1","type":"function",`+
		`"function":{"name":"bash","arguments":%s}}]}}]}`, strconv.Quote(toolArgs))
	srv := fakeGateway(t, http.StatusOK, body, nil)

	g, err := New(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	resp, err := g.Send(context.Background(), userRequest("check disk"))
	if err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.Message.ToolCalls))
	}
	call := resp.Message.ToolCalls[0]
	if call.Name != "bash" || call.ID != "call-1" {
		t.Errorf("call = %+v, want bash/call-1", call)
	}
	if string(call.Arguments) != toolArgs {
		t.Errorf("arguments = %s, want the decoded object", call.Arguments)
	}
}

// The API key and the tool definitions must both reach the gateway.
func TestSendAuthenticatesAndSendsTools(t *testing.T) {
	var got wireRequest
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)

	g, err := New(Config{BaseURL: srv.URL, APIKey: "sk-secret"})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	req := userRequest("hi")
	req.System = "be helpful"
	req.Tools = []model.ToolDefinition{{Name: "bash", Description: "run", Parameters: map[string]any{"type": "object"}}}

	if _, err := g.Send(context.Background(), req); err != nil {
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

	g, err := New(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := g.Send(context.Background(), userRequest("hi")); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if !seen {
		t.Fatal("the gateway was never called")
	}
	if authHeader != "" {
		t.Errorf("Authorization = %q, want no header", authHeader)
	}
}

func TestSendReportsGatewayError(t *testing.T) {
	srv := fakeGateway(t, http.StatusUnauthorized,
		`{"error":{"message":"invalid api key","type":"auth_error"}}`, nil)

	g, err := New(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	_, err = g.Send(context.Background(), userRequest("hi"))
	if err == nil {
		t.Fatal("Send() = nil error, want the gateway error")
	}
	for _, want := range []string{"401", "invalid api key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
}

// A gateway may fail with something that is not JSON at all.
func TestSendReportsNonJSONError(t *testing.T) {
	srv := fakeGateway(t, http.StatusBadGateway, "upstream is down", nil)

	g, err := New(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	_, err = g.Send(context.Background(), userRequest("hi"))
	if err == nil || !strings.Contains(err.Error(), "upstream is down") {
		t.Fatalf("error = %v, want the response body quoted", err)
	}
}

func TestSendRejectsEmptyChoices(t *testing.T) {
	srv := fakeGateway(t, http.StatusOK, `{"choices":[]}`, nil)

	g, err := New(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := g.Send(context.Background(), userRequest("hi")); err == nil {
		t.Fatal("Send() = nil error, want a no-choices error")
	}
}

func TestSendRequiresModel(t *testing.T) {
	g, err := New(Config{BaseURL: "http://localhost:4000"})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	req := userRequest("hi")
	req.Model = ""
	if _, err := g.Send(context.Background(), req); err == nil {
		t.Fatal("Send() without a model = nil error, want an error")
	}
}

// A malformed conversation must be caught before a request is made.
func TestSendValidatesConversation(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	t.Cleanup(srv.Close)

	g, err := New(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	req := transport.Request{
		Model:        "m",
		Conversation: model.Conversation{Messages: []model.Message{{Role: "nonsense", Content: "x"}}},
	}
	if _, err := g.Send(context.Background(), req); err == nil {
		t.Fatal("Send() = nil error, want a validation error")
	}
	if called {
		t.Error("the gateway was called despite an invalid conversation")
	}
}

// The gateway must remain usable through the interface the CLI depends on.
func TestGatewaySatisfiesClient(t *testing.T) {
	g, err := New(Config{BaseURL: "http://localhost:4000"})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	var _ transport.Client = g
}
