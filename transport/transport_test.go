package transport

import (
	"context"
	"errors"
	"testing"

	"github.com/chris/agi/model"
)

func TestNoopSendReturnsNotImplemented(t *testing.T) {
	req := Request{
		Model: "opus",
		Conversation: model.Conversation{Messages: []model.Message{
			{Role: model.RoleUser, Content: "hi"},
		}},
	}

	resp, err := Noop{}.Send(context.Background(), req)
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Send() error = %v, want ErrNotImplemented", err)
	}
	if resp != (Response{}) {
		t.Errorf("Send() response = %+v, want the zero Response", resp)
	}
}

// The point of the package is that callers can depend on the interface alone.
func TestClientInterfaceIsSatisfiable(t *testing.T) {
	var c Client = Noop{}
	if _, err := c.Send(context.Background(), Request{}); err == nil {
		t.Error("Send() through the interface = nil error, want ErrNotImplemented")
	}
}
