package litellm

import (
	"context"
	"errors"
	"strings"
	"testing"

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

func TestSendNotImplementedYet(t *testing.T) {
	g, err := New(Config{BaseURL: "http://localhost:4000"})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}

	resp, err := g.Send(context.Background(), transport.Request{Model: "opus"})
	if !errors.Is(err, transport.ErrNotImplemented) {
		t.Fatalf("Send() error = %v, want transport.ErrNotImplemented", err)
	}
	if resp != (transport.Response{}) {
		t.Errorf("Send() response = %+v, want the zero Response", resp)
	}
}

// The gateway must remain usable through the interface the CLI depends on.
func TestGatewaySatisfiesClient(t *testing.T) {
	g, err := New(Config{BaseURL: "http://localhost:4000"})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	var c transport.Client = g
	if _, err := c.Send(context.Background(), transport.Request{}); err == nil {
		t.Error("Send() through the interface = nil error, want ErrNotImplemented")
	}
}
