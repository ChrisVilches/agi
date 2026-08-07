package litellm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/transport"
)

func TestNewRequiresModel(t *testing.T) {
	g, err := New(Config{BaseURL: "http://localhost:4000"})
	if err == nil {
		t.Fatal("New() without a model = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "no model configured") {
		t.Errorf("New() error = %v, want it to name the missing model", err)
	}
	if g != nil {
		t.Errorf("New() gateway = %v, want nil on error", g)
	}
}

func TestNewAppliesBaseURLDefault(t *testing.T) {
	g, err := New(Config{Model: "opus"})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	if got := g.BaseURL(); got != DefaultBaseURL {
		t.Errorf("BaseURL() = %q, want the default %q", got, DefaultBaseURL)
	}
	if got := g.Model(); got != "opus" {
		t.Errorf("Model() = %q, want %q", got, "opus")
	}
}

// A configured URL with a trailing slash must not produce "//v1/..." paths.
func TestNewTrimsTrailingSlash(t *testing.T) {
	g, err := New(Config{Model: "opus", BaseURL: "http://gateway.internal:4000/"})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	if got := g.BaseURL(); got != "http://gateway.internal:4000" {
		t.Errorf("BaseURL() = %q, want the trailing slash trimmed", got)
	}
}

func TestSendNotImplementedYet(t *testing.T) {
	g, err := New(Config{Model: "opus"})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}

	resp, err := g.Send(context.Background(), transport.Request{})
	if !errors.Is(err, transport.ErrNotImplemented) {
		t.Fatalf("Send() error = %v, want transport.ErrNotImplemented", err)
	}
	if resp != (transport.Response{}) {
		t.Errorf("Send() response = %+v, want the zero Response", resp)
	}
}

// The gateway must remain usable through the interface the CLI depends on.
func TestGatewaySatisfiesClient(t *testing.T) {
	g, err := New(Config{Model: "opus"})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	var c transport.Client = g
	if _, err := c.Send(context.Background(), transport.Request{}); err == nil {
		t.Error("Send() through the interface = nil error, want ErrNotImplemented")
	}
}
