// Package transport defines how agi exchanges conversations with a backend.
// Implementations live alongside this file; the CLI depends only on the
// interface so a provider can be swapped without touching command code.
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
type Request struct {
	// Model is the provider-side model name.
	Model string
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

// Client is the contract every backend implements.
type Client interface {
	Send(ctx context.Context, req Request) (Response, error)
}

// Noop is a Client that always fails with ErrNotImplemented. It keeps the wiring
// honest until a real backend exists.
type Noop struct{}

// Send implements Client.
func (Noop) Send(context.Context, Request) (Response, error) {
	return Response{}, ErrNotImplemented
}

// Compile-time check that Noop satisfies Client.
var _ Client = Noop{}
