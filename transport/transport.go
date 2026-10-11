// Package transport defines how agi exchanges conversations with a backend.
// Implementations live alongside this file; the CLI depends only on the
// interfaces so a provider can be swapped without touching command code.
package transport

import (
	"context"
	"encoding/json"
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

// Model is one model an endpoint offers, as that endpoint describes it.
//
// It is the intersection of what the endpoints agi talks to say about a model
// and never the union. A gateway and a provider each report fields the other
// has never heard of — a gateway says which mode a model is in, a provider
// says which modalities it accepts — and a listing carrying all of them would
// be a different listing per backend, which is the one thing a single command
// must not print.
//
// Only ID is always filled. The rest is whatever the endpoint happened to say,
// and a zero field means it said nothing rather than that it said zero: no
// model has a context window of nothing, so whatever prints a listing shows
// the absence as an absence.
type Model struct {
	// ID is the name a request names the model by, and the one thing a
	// listing is useless without.
	ID string
	// OwnedBy is who provides the model, in the endpoint's own words.
	OwnedBy string
	// ContextWindow is how many tokens one request may carry.
	ContextWindow int
	// MaxOutputTokens is how many tokens one reply may run to.
	MaxOutputTokens int
}

// Backend is a Client that has been configured, and can say how. A run reports
// which backend it is on, which model, and where its requests go — in the log,
// and on screen under --verbose — so those questions belong to whatever
// answers them, and that is the configured backend rather than the caller.
type Backend interface {
	Client
	// Name is what this backend is called, which is the name AGI_BACKEND was
	// set to.
	//
	// It is here beside Model and BaseURL because it is the same kind of
	// question and the same thing knows the answer. The address does not
	// settle it: two backends can be pointed at one endpoint — a LiteLLM
	// gateway reached by its URL, and that same URL given to the openai
	// backend — so a record that held only the address could not say which of
	// them a run went through.
	Name() string
	// Model is the provider-side model name every request runs on.
	Model() string
	// BaseURL is the address requests go to.
	BaseURL() string
	// Payload renders what Send would put on the wire for req, without
	// sending it.
	//
	// It exists because the one thing worth reading when a request is in
	// question is built where nothing can see it. A backend encodes a
	// conversation into whatever its protocol asks for deep inside Send, and
	// the agent loop that records the run holds only this interface — so a
	// log that wanted the body of a request had no way to ask for one.
	//
	// Send is implemented in terms of this rather than beside it, and that is
	// the whole reason what this returns is worth logging. Two renderers
	// would be two opinions about what was sent, and the one in the log would
	// be the quieter of them; with one renderer there is nothing to disagree
	// with, and a change to how a request is encoded cannot reach the wire
	// without reaching the log in the same edit.
	//
	// It is on Backend rather than on Client because it answers for a
	// configuration: the body carries the model name, which belongs to the
	// backend and not to the caller.
	Payload(req Request) (json.RawMessage, error)
	// Models lists what the endpoint offers.
	//
	// It is here rather than on Client because the answer belongs to an
	// address and a key rather than to a conversation: only a backend that
	// has been configured has either, and the agent loop — which is the whole
	// of what Client is for — never asks.
	Models(ctx context.Context) ([]Model, error)
}

// Noop is a Backend that always fails with ErrNotImplemented. It keeps the
// wiring honest until a real backend exists.
type Noop struct{}

// Send implements Client.
func (Noop) Send(context.Context, Request) (Response, error) {
	return Response{}, ErrNotImplemented
}

// Name implements Backend.
func (Noop) Name() string { return "" }

// Model implements Backend.
func (Noop) Model() string { return "" }

// Payload implements Backend.
func (Noop) Payload(Request) (json.RawMessage, error) { return nil, ErrNotImplemented }

// Models implements Backend.
func (Noop) Models(context.Context) ([]Model, error) { return nil, ErrNotImplemented }

// BaseURL implements Backend.
func (Noop) BaseURL() string { return "" }

// Compile-time checks that Noop satisfies both contracts.
var (
	_ Client  = Noop{}
	_ Backend = Noop{}
)
