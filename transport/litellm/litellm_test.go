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

// gatewayReply is a reply in the protocol every route answers in, whatever it
// fronts: the thinking as an item of its own, then what the model said.
func gatewayReply(thinking string) string {
	return `{"id":"resp_1","status":"completed","output":[` +
		`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"a long think"}],` +
		`"encrypted_content":"` + thinking + `"},` +
		`{"type":"message","id":"msg_1","role":"assistant",` +
		`"content":[{"type":"output_text","text":"done"}]}]}`
}

// A run through a gateway carries reasoning on the same terms as a run against
// a provider, and nothing here says so: the client reads whatever reasoning a
// reply carried and hands the same back, and that rule needed no help to cover
// a gateway.
//
// Which route the gateway chose stops being a question in this protocol, which
// is the one thing it settled that the protocol before it could not: a route's
// own shape — prose, signed blocks, items — is the gateway's business on the
// far side, and what reaches agi is reasoning items whichever route produced
// them.
func TestGatewayRepliesCarryReasoning(t *testing.T) {
	for _, route := range []string{"deepseek-chat", "anthropic/claude-sonnet-5", "openai/gpt-5"} {
		t.Run(route, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, gatewayReply("gAAAA"))
			}))
			t.Cleanup(srv.Close)
			t.Setenv(EnvURL, srv.URL)
			t.Setenv(EnvAPIKey, "")

			g, err := FromEnv(route)
			if err != nil {
				t.Fatalf("FromEnv() = %v, want nil", err)
			}
			resp, err := g.Send(context.Background(), transport.Request{
				Conversation: model.NewConversation("", "hi"),
			})
			if err != nil {
				t.Fatalf("Send() = %v, want nil", err)
			}
			if resp.Message.ReasoningContent != "a long think" {
				t.Errorf("reasoning = %q, want what the gateway returned", resp.Message.ReasoningContent)
			}
			if resp.Message.ReasoningReplay.Empty() {
				t.Error("replay = empty, want the item the gateway will read back")
			}
			if resp.Message.Content != "done" {
				t.Errorf("content = %q, want the reply itself untouched", resp.Message.Content)
			}
		})
	}
}

// What a route replied in is what it is handed back, which is the whole of how
// a gateway run takes a second turn: the thinking is the route's own payload,
// and agi neither reads inside it nor reshapes it on the way past.
func TestGatewayHandsTheReplayBackOnTheNextTurn(t *testing.T) {
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, gatewayReply("gAAAAAB+/x=="))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(EnvURL, srv.URL)
	t.Setenv(EnvAPIKey, "")

	g, err := FromEnv("anthropic/claude-sonnet-5")
	if err != nil {
		t.Fatalf("FromEnv() = %v, want nil", err)
	}
	req := transport.Request{Conversation: model.NewConversation("", "hi")}
	resp, err := g.Send(context.Background(), req)
	if err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}

	req.Conversation.AddAssistant(resp.Message)
	req.Conversation.AddUser("and now?")
	if _, err := g.Send(context.Background(), req); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if !strings.Contains(string(sent), `"encrypted_content":"gAAAAAB+/x=="`) {
		t.Errorf("request = %s, want the route's own thinking handed back intact", sent)
	}
}
