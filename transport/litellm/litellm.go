// Package litellm reaches a LiteLLM gateway.
//
// A gateway speaks the OpenAI chat completions protocol, so there is nothing
// here about how a request is made: transport/openai does that, and this
// package only says where to send it, under which name, and on which model.
// Two hand-written clients for one protocol would drift apart the first time
// either was corrected.
//
// Using a gateway is opt-in: agi runs with none configured, and the Go module
// takes on no LiteLLM dependency. Configuration comes from the environment
// only — this package reads no configuration files.
package litellm

import (
	"os"

	"github.com/ChrisVilches/agi/transport/openai"
)

// Environment variables this package reads. Keep the list in AGENTS.md in sync.
const (
	// EnvURL is the base URL of the gateway. Setting it is what enables the
	// gateway; there is no default, so agi never contacts one by accident.
	EnvURL = "LITELLM_URL"
	// EnvAPIKey is the bearer token presented to the gateway. Optional, for a
	// gateway that does not require authentication. The gateway decides
	// whether it forwards the key or substitutes its own.
	EnvAPIKey = "LITELLM_KEY"
	// EnvModel names the model to ask the gateway for. Optional; the shared
	// default is used when it is unset.
	EnvModel = "LITELLM_MODEL"
)

// Name is what this backend calls itself, in its errors and wherever a backend
// is chosen by name.
const Name = "litellm"

// FromEnv builds a gateway client from the environment. It returns an error
// wrapping openai.ErrNotConfigured when no gateway is configured, so a caller
// can distinguish "not wanted" from "wanted but wrong".
func FromEnv() (*openai.Client, error) {
	return openai.New(openai.Config{
		BaseURL: os.Getenv(EnvURL),
		APIKey:  os.Getenv(EnvAPIKey),
		Model:   openai.ModelFromEnv(EnvModel),
		Name:    Name,
		URLVar:  EnvURL,
	})
}
