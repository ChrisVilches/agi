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
)

// Name is what this backend calls itself, in its errors and wherever a backend
// is chosen by name.
const Name = "litellm"

// FromEnv builds a gateway client from the environment, on the model it is
// handed. It returns an error wrapping openai.ErrNotConfigured when no gateway
// is configured, so a caller can distinguish "not wanted" from "wanted but
// wrong".
//
// The model is passed through exactly as it was given, with nothing added or
// taken off. A gateway's own listing is the spelling it expects back: a model
// named explicitly in its configuration is listed and asked for bare
// (`deepseek-chat`), and one reached through a wildcard is listed and asked
// for with the prefix that routes it (`anthropic/claude-sonnet-5`). Those two
// shapes look inconsistent and are not — each is the name of a route the
// gateway set up — so a rule here that added a provider prefix or stripped one
// would break whichever half it was not written for.
func FromEnv(model string) (*openai.Client, error) {
	return openai.New(openai.Config{
		BaseURL: os.Getenv(EnvURL),
		APIKey:  os.Getenv(EnvAPIKey),
		Model:   model,
		Name:    Name,
		URLVar:  EnvURL,
		// A run through a gateway carries no reasoning, and the reason is the
		// gateway rather than the models behind it. It returns the field under
		// one name and wants it back in whatever shape the route it chose
		// takes — a string for a provider that spells it the way DeepSeek
		// does, a list of signed blocks for an Anthropic route — so there is
		// no single thing to send back that is right for every route, and
		// which route a request took is not something the reply says. Reading
		// it anyway and sending nothing would leave deliberation in the
		// snapshot that no later turn could use, so it is not read: an
		// assistant turn from a gateway has no reasoning on it, and that is
		// the whole of what this backend promises today.
		Reasoning: false,
	})
}
