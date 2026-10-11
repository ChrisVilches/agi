package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/transport"
)

// capturing is an endpoint that keeps the request body exactly as it arrived,
// rather than decoding it into a wireRequest the way fakeEndpoint does. The
// bytes are the point here: what this file is about is that the body Payload
// renders and the body Send sends are the same thing, and a decode would throw
// away the difference it is looking for.
func capturing(t *testing.T, body *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		read, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request: %v", err)
		}
		*body = read
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply(messageItem("ok")))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Send puts on the wire exactly what Payload renders, because Send is
// implemented in terms of Payload rather than beside it.
//
// This is the assertion the whole arrangement rests on. A log records what
// Payload returns, and what makes that worth recording is that there is one
// renderer rather than two: were these two encodings of the same request, the
// log would hold a plausible body rather than the body, and the day they
// drifted apart would be a day spent debugging a request that was never sent.
func TestSendPutsOnTheWireWhatPayloadRendered(t *testing.T) {
	var sent []byte
	srv := capturing(t, &sent)

	c, err := New(okConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	req := userRequest("what is 2+2?")
	rendered, err := c.Payload(req)
	if err != nil {
		t.Fatalf("Payload() = %v", err)
	}
	if _, err := c.Send(context.Background(), req); err != nil {
		t.Fatalf("Send() = %v", err)
	}

	if len(sent) == 0 {
		t.Fatal("the endpoint received no body, so this proves nothing")
	}
	if string(rendered) != string(sent) {
		t.Errorf("Payload rendered\n %s\nbut Send sent\n %s", rendered, sent)
	}
}

// Payload refuses everything Send refuses, and for the same reasons. It holds
// the checks rather than duplicating them, so a body that renders is a request
// that could have been sent: a caller logging one is never shown a shape the
// endpoint would have rejected out of hand.
func TestPayloadRefusesWhatSendRefuses(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		req  transport.Request
	}{
		{
			name: "no conversation",
			cfg:  okConfig("http://localhost:4000"),
			req:  transport.Request{},
		},
		{
			name: "no model",
			cfg:  Config{BaseURL: "http://localhost:4000"},
			req:  userRequest("hello"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(tt.cfg)
			if err != nil {
				t.Fatalf("New() = %v", err)
			}
			body, err := c.Payload(tt.req)
			if err == nil {
				t.Fatalf("Payload() = %s, want a refusal", body)
			}
			if body != nil {
				t.Errorf("Payload() returned %s alongside its refusal, want nothing", body)
			}
			// Send is handed the same request and refuses it too, which is
			// what makes the one in the log and the one on the wire the same
			// request rather than two that happen to agree.
			if _, err := c.Send(context.Background(), tt.req); err == nil {
				t.Error("Send() = nil, want the refusal Payload gave")
			}
		})
	}
}

// A refusal from Payload names the backend, as every refusal from this client
// does: a message that said only what was wrong would not say which of a
// machine's backends it was wrong about.
func TestPayloadRefusalNamesTheBackend(t *testing.T) {
	c, err := New(Config{BaseURL: "http://localhost:4000", Name: "litellm", URLVar: "LITELLM_URL"})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	_, err = c.Payload(userRequest("hello"))
	if err == nil {
		t.Fatal("Payload() = nil, want a refusal: no model is named")
	}
	if got := err.Error(); !strings.HasPrefix(got, "litellm:") {
		t.Errorf("the refusal is %q, want it to begin with the backend's name", got)
	}
}
