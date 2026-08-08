// Package litellm exchanges conversations with a LiteLLM gateway.
//
// Using a gateway is opt-in: agi runs with none configured, and the Go module
// takes on no LiteLLM dependency. Configuration comes from the environment
// only — this package reads no configuration files.
package litellm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// gateway that does not require authentication. It is named for the
	// provider key the user keeps in their environment; the gateway decides
	// whether it forwards the key or substitutes its own.
	EnvAPIKey = "DEEPSEEK_KEY"
)

// maxResponseBytes bounds how much of a response body is read, so a misbehaving
// gateway cannot exhaust memory.
const maxResponseBytes = 8 << 20

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
func (g *Gateway) Send(ctx context.Context, req transport.Request) (transport.Response, error) {
	if req.Model == "" {
		return transport.Response{}, errors.New("litellm: request has no model")
	}
	if err := req.Conversation.Validate(); err != nil {
		return transport.Response{}, fmt.Errorf("litellm: %w", err)
	}

	body, err := json.Marshal(encodeRequest(req.Model, req.System, req.Conversation, req.Tools))
	if err != nil {
		return transport.Response{}, fmt.Errorf("litellm: encoding request: %w", err)
	}

	endpoint := g.cfg.BaseURL + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return transport.Response{}, fmt.Errorf("litellm: building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if g.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+g.cfg.APIKey)
	}

	httpResp, err := g.http.Do(httpReq)
	if err != nil {
		return transport.Response{}, fmt.Errorf("litellm: calling %s: %w", endpoint, err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes))
	if err != nil {
		return transport.Response{}, fmt.Errorf("litellm: reading response: %w", err)
	}

	var decoded wireResponse
	// A gateway error may arrive as JSON or as plain text, so decode first and
	// fall back to the raw body when reporting a bad status.
	decodeErr := json.Unmarshal(respBody, &decoded)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		if decodeErr == nil && decoded.Error != nil {
			return transport.Response{}, fmt.Errorf("litellm: %s returned %s: %s",
				endpoint, httpResp.Status, decoded.Error.Error())
		}
		return transport.Response{}, fmt.Errorf("litellm: %s returned %s: %s",
			endpoint, httpResp.Status, snippet(respBody))
	}
	if decodeErr != nil {
		return transport.Response{}, fmt.Errorf("litellm: decoding response: %w", decodeErr)
	}
	if decoded.Error != nil {
		return transport.Response{}, fmt.Errorf("litellm: %s", decoded.Error.Error())
	}
	if len(decoded.Choices) == 0 {
		return transport.Response{}, errors.New("litellm: response contained no choices")
	}

	return transport.Response{Message: decodeMessage(decoded.Choices[0].Message)}, nil
}

// snippet renders a short, single-line excerpt of a response body for errors.
func snippet(b []byte) string {
	const max = 300
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if s == "" {
		return "(empty body)"
	}
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// Compile-time check that Gateway satisfies the transport contract.
var _ transport.Client = (*Gateway)(nil)
