// Package openai speaks the OpenAI chat completions protocol over HTTP.
//
// That protocol is the whole of what agi needs from a provider — a list of
// messages, a list of tool definitions, one reply — and it is what OpenAI,
// DeepSeek, and a LiteLLM gateway all accept, so one client serves every
// endpoint agi talks to. It is written out here rather than taken from a
// vendor SDK on purpose: the surface agi uses is a few hundred lines of JSON
// structs and a POST, and a code-generated SDK would be both the largest
// dependency in the module and a standing invitation to reach past the
// intersection of provider features that AGENTS.md caps this project at.
//
// Configuration comes from the environment only — this package reads no
// configuration files.
package openai

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
	// EnvURL is the base URL of the endpoint, such as "https://api.deepseek.com".
	// Setting it is what enables this backend; there is no default, so agi
	// never contacts a provider by accident.
	EnvURL = "OPENAI_URL"
	// EnvAPIKey is the bearer token presented to the endpoint. Optional, for
	// an endpoint that needs no authentication — a model served on the local
	// machine, say.
	EnvAPIKey = "OPENAI_KEY"
	// EnvModel names the model to run on. Optional; DefaultModel is used when
	// it is unset.
	EnvModel = "OPENAI_MODEL"
)

// DefaultModel is the model a backend runs on when its environment names none.
// It is the model agi is developed and tested against.
//
// It is DeepSeek's plain endpoint rather than its thinking one, which is a
// choice about the pair rather than about the model: a default model is only
// ever run alongside the default base prompt, and prompt.Base() asks the agent
// to think on the page. A model with nowhere else to think does that, and the
// thinking stays in the conversation for the turns that follow. A thinking
// model has somewhere else, and what goes there is discarded at the end of the
// turn — so the instruction buys less than it is written to buy. Both halves
// can be swapped, and swapping the other one may well reverse this.
const DefaultModel = "deepseek-chat"

// Name is what this backend calls itself, in its errors and wherever a backend
// is chosen by name. A client whose configuration names nothing else reports
// itself as this, which is the plain OpenAI-compatible case.
const Name = "openai"

// maxResponseBytes bounds how much of a response body is read, so a
// misbehaving endpoint cannot exhaust memory.
const maxResponseBytes = 8 << 20

// The paths agi asks an endpoint for, both of them versioned. A provider that
// also serves the unversioned spelling serves this one too, and a gateway
// serves only this one, so one spelling reaches every endpoint agi talks to.
const (
	completionsPath = "/v1/chat/completions"
	modelsPath      = "/v1/models"
)

// ErrNotConfigured reports that a backend has no address to talk to, which is
// a normal state rather than a failure: agi contacts nothing until it is told
// where. It is a sentinel so callers can tell "not configured" from
// "configured wrongly" with errors.Is.
var ErrNotConfigured = errors.New("no endpoint address is configured")

// Config describes how to reach an OpenAI-compatible endpoint.
//
// Name and URLVar carry no information the client needs to make a request:
// they exist so that a failure says which backend failed and which environment
// variable to fix. A gateway that speaks this protocol under another name —
// LiteLLM does — sets them to its own, and its errors then read as its own
// rather than as this package's.
type Config struct {
	// BaseURL of the endpoint, such as "https://api.deepseek.com". Required.
	BaseURL string
	// APIKey is sent as a bearer token. May be empty.
	APIKey string
	// Model is the provider-side model name every request runs on. Required;
	// callers reading the environment fall back to DefaultModel.
	Model string
	// Name prefixes this client's errors. Empty means "openai".
	Name string
	// URLVar is the environment variable BaseURL came from, named in a
	// configuration error so the user knows what to correct. Empty means
	// EnvURL.
	URLVar string
}

// Client is a transport.Backend backed by an OpenAI-compatible endpoint.
type Client struct {
	cfg  Config
	http *http.Client
}

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	if cfg.Name == "" {
		cfg.Name = Name
	}
	if cfg.URLVar == "" {
		cfg.URLVar = EnvURL
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("%s: %s is not set: %w", cfg.Name, cfg.URLVar, ErrNotConfigured)
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid %s: %w", cfg.Name, cfg.URLVar, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%s: %s must be an http or https URL, got %q", cfg.Name, cfg.URLVar, cfg.BaseURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%s: %s has no host: %q", cfg.Name, cfg.URLVar, cfg.BaseURL)
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("%s: no model named", cfg.Name)
	}
	cfg.Model = strings.TrimSpace(cfg.Model)

	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 10 * time.Minute},
	}, nil
}

// FromEnv builds a Client from this package's environment variables. It
// returns an error wrapping ErrNotConfigured when no endpoint is configured,
// so a caller can distinguish "not wanted" from "wanted but wrong".
func FromEnv() (*Client, error) {
	return New(Config{
		BaseURL: os.Getenv(EnvURL),
		APIKey:  os.Getenv(EnvAPIKey),
		Model:   ModelFromEnv(EnvModel),
	})
}

// ModelFromEnv reads a model name from the named environment variable, falling
// back to DefaultModel. Every backend spelling of "which model" goes through
// it, so the default is one value rather than one per package.
func ModelFromEnv(key string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return DefaultModel
}

// BaseURL returns the resolved endpoint address.
func (c *Client) BaseURL() string { return c.cfg.BaseURL }

// Model returns the model every request runs on.
func (c *Client) Model() string { return c.cfg.Model }

// Send implements transport.Client.
func (c *Client) Send(ctx context.Context, req transport.Request) (transport.Response, error) {
	if req.Conversation == nil {
		return transport.Response{}, fmt.Errorf("%s: request has no conversation", c.cfg.Name)
	}
	if err := req.Conversation.Validate(); err != nil {
		return transport.Response{}, fmt.Errorf("%s: %w", c.cfg.Name, err)
	}

	body, err := json.Marshal(encodeRequest(c.cfg.Model, req.Conversation, req.Tools))
	if err != nil {
		return transport.Response{}, fmt.Errorf("%s: encoding request: %w", c.cfg.Name, err)
	}

	respBody, err := c.call(ctx, http.MethodPost, completionsPath, body)
	if err != nil {
		return transport.Response{}, err
	}

	var decoded wireResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return transport.Response{}, fmt.Errorf("%s: decoding response: %w", c.cfg.Name, err)
	}
	if decoded.Error != nil {
		return transport.Response{}, fmt.Errorf("%s: %s", c.cfg.Name, decoded.Error.Error())
	}
	if len(decoded.Choices) == 0 {
		return transport.Response{}, fmt.Errorf("%s: response contained no choices", c.cfg.Name)
	}

	return transport.Response{Message: decodeMessage(decoded.Choices[0].Message)}, nil
}

// call makes one request to the endpoint and hands back the body of a
// successful reply.
//
// Everything both of this client's requests do identically is here — the
// bearer token, the cap on how much of a reply is read, and what a refusal
// reads like — so that the two differ only in where they go and what they
// decode. A body of nil is a request that carries none, which is also what
// decides whether a content type is declared: a GET announcing that it is
// sending JSON is announcing something untrue.
func (c *Client) call(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	endpoint := c.cfg.BaseURL + path

	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return nil, fmt.Errorf("%s: building request: %w", c.cfg.Name, err)
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: calling %s: %w", c.cfg.Name, endpoint, err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: reading response: %w", c.cfg.Name, err)
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s returned %s: %s",
			c.cfg.Name, endpoint, httpResp.Status, refusal(respBody))
	}
	return respBody, nil
}

// refusal renders why a request was turned down: the endpoint's own sentence
// when the body is the protocol's error object, and an excerpt of whatever
// arrived when it is not. An endpoint in trouble answers in either — a gateway
// that cannot reach a provider often answers in neither — and a message
// quoting only the status would leave the reason on the floor.
func refusal(body []byte) string {
	var decoded struct {
		Error *wireError `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err == nil && decoded.Error != nil {
		return decoded.Error.Error()
	}
	return snippet(body)
}

// Models implements transport.Backend.
//
// There is one implementation of this for both backends because there is one
// endpoint: a gateway lists what it fronts at the same path a provider lists
// its own catalogue at, in the same envelope. Which of them is asked is
// settled before this is reached, by which backend the run configured — this
// client holds that backend's address and that backend's key and knows nothing
// about the other. Where the two really do differ is inside the envelope, and
// decodeModel is where that is reconciled.
func (c *Client) Models(ctx context.Context) ([]transport.Model, error) {
	respBody, err := c.call(ctx, http.MethodGet, modelsPath, nil)
	if err != nil {
		return nil, err
	}

	var decoded wireModels
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("%s: decoding response: %w", c.cfg.Name, err)
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("%s: %s", c.cfg.Name, decoded.Error.Error())
	}

	models := make([]transport.Model, 0, len(decoded.Data))
	for _, m := range decoded.Data {
		// A model with no name cannot be asked for, so there is nothing a
		// listing could say about it and nothing the reader could do with it.
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		models = append(models, decodeModel(m))
	}
	return models, nil
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

// Compile-time check that Client satisfies the transport contract.
var _ transport.Backend = (*Client)(nil)
