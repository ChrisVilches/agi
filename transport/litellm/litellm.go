// Package litellm exchanges conversations with a LiteLLM gateway.
//
// Everything about the gateway lives here: the client that speaks to it and,
// alongside this file, the Docker Compose setup that runs one locally. Using a
// gateway is opt-in — agi works without one, and the Go module pulls in no
// LiteLLM dependency.
package litellm

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ChrisVilches/agi/transport"
)

// DefaultBaseURL is where the bundled docker-compose.yml publishes the gateway.
const DefaultBaseURL = "http://localhost:4000"

// Config describes how to reach a gateway.
type Config struct {
	// BaseURL of the gateway. Defaults to DefaultBaseURL when empty.
	BaseURL string
	// APIKey is sent as a bearer token. For the bundled setup this is the
	// LITELLM_MASTER_KEY from .env. May be empty for an unauthenticated gateway.
	APIKey string
	// Model is the gateway-side model name, such as the "opus" alias defined in
	// config.yaml, not a provider model ID.
	Model string
}

// Gateway is a transport.Client backed by a LiteLLM gateway.
type Gateway struct {
	cfg  Config
	http *http.Client
}

// New validates cfg, applies defaults, and returns a Gateway.
func New(cfg Config) (*Gateway, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("litellm: no model configured")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	return &Gateway{
		cfg:  cfg,
		http: &http.Client{Timeout: 10 * time.Minute},
	}, nil
}

// BaseURL returns the resolved gateway address.
func (g *Gateway) BaseURL() string { return g.cfg.BaseURL }

// Model returns the configured gateway-side model name.
func (g *Gateway) Model() string { return g.cfg.Model }

// Send implements transport.Client.
//
// The request mapping onto the gateway's OpenAI-compatible
// /v1/chat/completions endpoint is not written yet.
func (g *Gateway) Send(context.Context, transport.Request) (transport.Response, error) {
	return transport.Response{}, transport.ErrNotImplemented
}

// Compile-time check that Gateway satisfies the transport contract.
var _ transport.Client = (*Gateway)(nil)
