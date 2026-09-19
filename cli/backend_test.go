package cli

import (
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/transport/litellm"
	"github.com/ChrisVilches/agi/transport/openai"
)

// Naming a backend is what decides which set of variables is read, and each
// backend reads only its own.
func TestOpenBackendPicksTheNamedOne(t *testing.T) {
	t.Setenv(litellm.EnvURL, "http://gateway.internal:4000")
	t.Setenv(litellm.EnvModel, "gateway-model")
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")
	t.Setenv(openai.EnvModel, "direct-model")

	tests := []struct {
		backend string
		wantURL string
		wantMod string
	}{
		{litellm.Name, "http://gateway.internal:4000", "gateway-model"},
		{openai.Name, "https://api.deepseek.com", "direct-model"},
	}
	for _, tt := range tests {
		t.Run(tt.backend, func(t *testing.T) {
			t.Setenv(EnvBackend, tt.backend)

			b, err := openBackend()
			if err != nil {
				t.Fatalf("openBackend() = %v, want nil", err)
			}
			if got := b.BaseURL(); got != tt.wantURL {
				t.Errorf("BaseURL() = %q, want %q", got, tt.wantURL)
			}
			if got := b.Model(); got != tt.wantMod {
				t.Errorf("Model() = %q, want %q", got, tt.wantMod)
			}
		})
	}
}

// Surrounding space arrived from a shell and is never part of a name.
func TestOpenBackendTrimsTheName(t *testing.T) {
	t.Setenv(EnvBackend, "  "+openai.Name+"  ")
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")

	if _, err := openBackend(); err != nil {
		t.Fatalf("openBackend() = %v, want nil", err)
	}
}

// There is no default backend: two exist, they read different variables, and
// guessing would send a user who configured one after the other one's.
func TestOpenBackendRequiresAChoice(t *testing.T) {
	t.Setenv(EnvBackend, "")
	t.Setenv(openai.EnvURL, "https://api.deepseek.com")

	b, err := openBackend()
	if err == nil {
		t.Fatal("openBackend() with no choice = nil error, want an error")
	}
	if b != nil {
		t.Errorf("openBackend() = %v, want nil on error", b)
	}
	for _, want := range []string{EnvBackend, litellm.Name, openai.Name} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

// A misspelled name must say what would have worked.
func TestOpenBackendRejectsAnUnknownName(t *testing.T) {
	t.Setenv(EnvBackend, "anthropic")

	if _, err := openBackend(); err == nil {
		t.Fatal("openBackend() with an unknown name = nil error, want an error")
	} else if !strings.Contains(err.Error(), `"anthropic"`) || !strings.Contains(err.Error(), openai.Name) {
		t.Errorf("error = %v, want it to quote the name and list the choices", err)
	}
}

// The advice reads the same way twice, whatever order a map hands its keys
// over in.
func TestBackendNamesAreOrdered(t *testing.T) {
	want := litellm.Name + ", " + openai.Name
	for range 5 {
		if got := backendNames(); got != want {
			t.Fatalf("backendNames() = %q, want %q", got, want)
		}
	}
}
