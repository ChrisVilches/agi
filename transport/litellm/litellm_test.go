package litellm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/transport"
	"github.com/ChrisVilches/agi/transport/openai"
)

// The gateway is opt-in: with no address set, a caller must be able to tell
// "no gateway wanted" from "gateway misconfigured".
func TestUnsetURLIsNotConfigured(t *testing.T) {
	t.Setenv(EnvURL, "")

	g, err := FromEnv("deepseek-chat")
	if !errors.Is(err, openai.ErrNotConfigured) {
		t.Fatalf("FromEnv() error = %v, want openai.ErrNotConfigured", err)
	}
	if g != nil {
		t.Errorf("FromEnv() gateway = %v, want nil", g)
	}
	// The advice has to name this package's variable, not the one the
	// underlying client reads by default.
	if !strings.Contains(err.Error(), EnvURL) {
		t.Errorf("error = %v, want it to name %s", err, EnvURL)
	}
	if !strings.Contains(err.Error(), Name) {
		t.Errorf("error = %v, want it to name the backend", err)
	}
}

// Each backend reads its own variables and nothing else's: a gateway's address
// and key say nothing about a direct provider's, and the reverse.
func TestFromEnvReadsOnlyItsOwnVariables(t *testing.T) {
	t.Setenv(EnvURL, "http://gateway.internal:4000/")
	t.Setenv(EnvAPIKey, "sk-gateway")
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")

	g, err := FromEnv("deepseek-chat")
	if err != nil {
		t.Fatalf("FromEnv() = %v, want nil", err)
	}
	// A trailing slash must not produce "//v1/..." paths later.
	if got := g.BaseURL(); got != "http://gateway.internal:4000" {
		t.Errorf("BaseURL() = %q, want the gateway's address with the slash trimmed", got)
	}
}

// A gateway's own listing is the spelling it expects back, and the two shapes
// it prints are both names of routes it set up: a model named explicitly in
// its configuration is listed and asked for bare, and one reached through a
// wildcard carries the prefix that routes it. Neither may be rewritten on the
// way through — a rule that added a provider prefix or stripped one would
// break whichever half it was not written for.
func TestModelIsPassedThroughExactly(t *testing.T) {
	t.Setenv(EnvURL, "http://gateway.internal:4000")

	for _, want := range []string{"deepseek-chat", "anthropic/claude-sonnet-5", "moonshot/kimi-k2.5"} {
		g, err := FromEnv(want)
		if err != nil {
			t.Fatalf("FromEnv(%q) = %v, want nil", want, err)
		}
		if got := g.Model(); got != want {
			t.Errorf("Model() = %q, want %q unchanged", got, want)
		}
	}
}

// A gateway may need no authentication at all.
func TestAPIKeyIsOptional(t *testing.T) {
	t.Setenv(EnvURL, "http://gateway.internal:4000")
	t.Setenv(EnvAPIKey, "")

	if _, err := FromEnv("deepseek-chat"); err != nil {
		t.Fatalf("FromEnv() without an API key = %v, want nil", err)
	}
}

// What FromEnv returns has to be usable as a backend, whatever it is made of.
func TestGatewaySatisfiesBackend(t *testing.T) {
	t.Setenv(EnvURL, "http://gateway.internal:4000")

	g, err := FromEnv("deepseek-chat")
	if err != nil {
		t.Fatalf("FromEnv() = %v, want nil", err)
	}
	var _ transport.Backend = g
}

// A run through a gateway carries no reasoning, and the reason is the gateway
// rather than the models behind it: it returns the field under one name and
// wants it back in whatever shape the route it chose takes — a string for a
// provider that spells it the way DeepSeek does, a list of signed blocks for
// an Anthropic route — and the reply does not say which route it took. So
// nothing is read, because there is nothing that could be sent back that is
// right for every route, and reasoning agi kept and could not send would be
// text in the snapshot that no later turn could use.
func TestGatewayRepliesCarryNoReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w,
			`{"choices":[{"message":{"content":"done","reasoning_content":"a long think"}}]}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(EnvURL, srv.URL)
	t.Setenv(EnvAPIKey, "")

	g, err := FromEnv("deepseek-reasoner")
	if err != nil {
		t.Fatalf("FromEnv() = %v, want nil", err)
	}
	resp, err := g.Send(context.Background(), transport.Request{
		Conversation: model.NewConversation("", "hi"),
	})
	if err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if resp.Message.ReasoningContent != "" {
		t.Errorf("reasoning = %q, want none from a gateway", resp.Message.ReasoningContent)
	}
	if resp.Message.Content != "done" {
		t.Errorf("content = %q, want the reply itself untouched", resp.Message.Content)
	}
}
