package transport

import (
	"context"
	"errors"
	"testing"

	"github.com/ChrisVilches/agi/model"
)

func TestNoopSendReturnsNotImplemented(t *testing.T) {
	req := Request{Conversation: model.NewConversation("be helpful", "hi")}

	resp, err := Noop{}.Send(context.Background(), req)
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Send() error = %v, want ErrNotImplemented", err)
	}
	if resp.Message.Content != "" || resp.Message.WantsTools() {
		t.Errorf("Send() response = %+v, want the zero Response", resp)
	}
}

// The point of the package is that callers can depend on the interfaces alone.
func TestClientInterfaceIsSatisfiable(t *testing.T) {
	var c Client = Noop{}
	if _, err := c.Send(context.Background(), Request{}); err == nil {
		t.Error("Send() through the interface = nil error, want ErrNotImplemented")
	}
}

// A backend answers the two questions a run reports about itself, so nothing
// else has to keep a second copy of either.
func TestBackendInterfaceIsSatisfiable(t *testing.T) {
	var b Backend = Noop{}
	if b.Model() != "" || b.BaseURL() != "" {
		t.Errorf("Noop describes itself as %q via %q, want neither", b.Model(), b.BaseURL())
	}
}
