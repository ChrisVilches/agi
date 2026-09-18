// Package litellm exchanges conversations with a LiteLLM gateway.
//
// Using a gateway is opt-in: agi runs with none configured, and the Go module
// takes on no LiteLLM dependency. Configuration comes from the environment
// only — this package reads no configuration files.
package litellm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ChrisVilches/agi/transport"
)

// Environment variables this package reads. Keep the list in AGENTS.md in sync.
const (
	// EnvURL is the base URL of the gateway. Setting it is what enables the
	// gateway; there is no default, so agi never contacts one by accident.
	EnvURL = "LITELLM_URL"
	// EnvAPIKey is the bearer token presented to the gateway. Optional, for a
	// gateway that does not require authentication.
	EnvAPIKey = "LITELLM_API_KEY"
)

// ErrNotConfigured reports that no gateway is configured, which is a normal
// state rather than a failure: callers should fall back to another transport.
var ErrNotConfigured = fmt.Errorf("litellm: %s is not set", EnvURL)

// Config describes how to reach a gateway.
type Config struct {
	// BaseURL of the gateway, such as "http://localhost:4000". Required.
	BaseURL string
	// APIKey is sent as a bearer token. May be empty.
	APIKey string
}

// Gateway is a transport.Client backed by a LiteLLM gateway.
type Gateway struct {
	cfg  Config
	http *http.Client
}

// New validates cfg and returns a Gateway.
func New(cfg Config) (*Gateway, error) {
	if cfg.BaseURL == "" {
		return nil, ErrNotConfigured
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("litellm: invalid %s: %w", EnvURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("litellm: %s must be an http or https URL, got %q", EnvURL, cfg.BaseURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("litellm: %s has no host: %q", EnvURL, cfg.BaseURL)
	}

	return &Gateway{
		cfg:  cfg,
		http: &http.Client{Timeout: 10 * time.Minute},
	}, nil
}

// FromEnv builds a Gateway from the environment. It returns a nil Gateway
// wrapped in ErrNotConfigured when no gateway is configured, so callers can
// distinguish "not wanted" from "wanted but wrong" with errors.Is.
func FromEnv() (*Gateway, error) {
	return New(Config{
		BaseURL: os.Getenv(EnvURL),
		APIKey:  os.Getenv(EnvAPIKey),
	})
}

// Configured reports whether a gateway is configured in the environment.
func Configured() bool { return os.Getenv(EnvURL) != "" }

// BaseURL returns the resolved gateway address.
func (g *Gateway) BaseURL() string { return g.cfg.BaseURL }

// Send implements transport.Client. The model comes from req, so one Gateway
// serves every model the gateway exposes.
//
// The request mapping onto the gateway's OpenAI-compatible
// /v1/chat/completions endpoint is not written yet.
func (g *Gateway) Send(context.Context, transport.Request) (transport.Response, error) {
	return transport.Response{}, transport.ErrNotImplemented
}

// Compile-time check that Gateway satisfies the transport contract.
var _ transport.Client = (*Gateway)(nil)
