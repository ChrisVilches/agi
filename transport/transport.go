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
	Model        string
	Conversation model.Conversation
}

// Response is what a backend returns for a Request.
type Response struct {
	Message model.Message
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
