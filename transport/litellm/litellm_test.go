package litellm

import (
	"errors"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/transport"
	"github.com/ChrisVilches/agi/transport/openai"
)

// The gateway is opt-in: with no address set, a caller must be able to tell
// "no gateway wanted" from "gateway misconfigured".
func TestUnsetURLIsNotConfigured(t *testing.T) {
	t.Setenv(EnvURL, "")

	g, err := FromEnv()
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
	t.Setenv(EnvModel, "deepseek-reasoner")
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")
	t.Setenv(openai.EnvModel, "some-other-model")

	g, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() = %v, want nil", err)
	}
	// A trailing slash must not produce "//v1/..." paths later.
	if got := g.BaseURL(); got != "http://gateway.internal:4000" {
		t.Errorf("BaseURL() = %q, want the gateway's address with the slash trimmed", got)
	}
	if got := g.Model(); got != "deepseek-reasoner" {
		t.Errorf("Model() = %q, want %s", got, EnvModel)
	}
}

// Naming no model is the ordinary case, and it gets the shared default.
func TestModelFallsBackToDefault(t *testing.T) {
	t.Setenv(EnvURL, "http://gateway.internal:4000")
	t.Setenv(EnvModel, "")

	g, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() = %v, want nil", err)
	}
	if got := g.Model(); got != openai.DefaultModel {
		t.Errorf("Model() = %q, want the default %q", got, openai.DefaultModel)
	}
}

// A gateway may need no authentication at all.
func TestAPIKeyIsOptional(t *testing.T) {
	t.Setenv(EnvURL, "http://gateway.internal:4000")
	t.Setenv(EnvAPIKey, "")

	if _, err := FromEnv(); err != nil {
		t.Fatalf("FromEnv() without an API key = %v, want nil", err)
	}
}

// What FromEnv returns has to be usable as a backend, whatever it is made of.
func TestGatewaySatisfiesBackend(t *testing.T) {
	t.Setenv(EnvURL, "http://gateway.internal:4000")

	g, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() = %v, want nil", err)
	}
	var _ transport.Backend = g
}
