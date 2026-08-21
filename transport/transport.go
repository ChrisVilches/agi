// Package transport defines how agi exchanges conversations with a backend.
// Implementations live alongside this file; the CLI depends only on the
// interfaces so a provider can be swapped without touching command code.
package transport

import (
	"context"
	"errors"

	"github.com/ChrisVilches/agi/model"
)

// ErrNotImplemented is returned by placeholder implementations that exist to
// satisfy the interface but cannot yet talk to a backend.
var ErrNotImplemented = errors.New("transport: not implemented")

// Request is a single completion request.
//
// It carries no model name. Which model a run is on is part of how its backend
// was configured rather than something the caller decides per request: one
// backend, one model, named in that backend's own environment variables. A
// field here as well would be a second place for the answer to live, and the
// two would disagree the first time something forgot to fill it in.
type Request struct {
	// Conversation is the run so far, including the system prompt.
	Conversation *model.Conversation
	// Tools the model may call. An empty slice means the model must answer
	// directly.
	Tools []model.ToolDefinition
}

// Response is what a backend returns for a Request. A reply is always an
// assistant turn, so the type says so.
type Response struct {
	Message model.AssistantMessage
}

// Client is the contract every backend implements: hand it a conversation, get
// the next assistant turn back. Everything that only needs to run a
// conversation — the agent loop, and the tests that exercise it without a
// network — depends on this and nothing more.
type Client interface {
	Send(ctx context.Context, req Request) (Response, error)
}

// Backend is a Client that has been configured, and can say how. A run reports
// which model it is on and where its requests go — in the log, and on screen
// under --verbose — so the two questions belong to whatever answers them, and
// that is the configured backend rather than the caller.
type Backend interface {
	Client
	// Model is the provider-side model name every request runs on.
	Model() string
	// BaseURL is the address requests go to.
	BaseURL() string
}

// Noop is a Backend that always fails with ErrNotImplemented. It keeps the
// wiring honest until a real backend exists.
type Noop struct{}

// Send implements Client.
func (Noop) Send(context.Context, Request) (Response, error) {
	return Response{}, ErrNotImplemented
}

// Model implements Backend.
func (Noop) Model() string { return "" }

// BaseURL implements Backend.
func (Noop) BaseURL() string { return "" }

// Compile-time checks that Noop satisfies both contracts.
var (
	_ Client  = Noop{}
	_ Backend = Noop{}
)
