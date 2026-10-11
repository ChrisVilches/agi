package openai

import "testing"

// The name a client reports is the name it was configured with, which is the
// name AGI_BACKEND was set to. It is the same string that prefixes this
// client's errors, so there is one spelling of a backend's name rather than
// two that could come apart.
func TestNameReportsTheConfiguredBackend(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "a provider reached directly",
			cfg:  okConfig("http://localhost:4000"),
			want: Name,
		},
		{
			name: "a gateway configured on top of this client",
			cfg:  Config{BaseURL: "http://localhost:4000", Model: "test-model", Name: "litellm", URLVar: "LITELLM_URL"},
			want: "litellm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(tt.cfg)
			if err != nil {
				t.Fatalf("New() = %v", err)
			}
			if got := c.Name(); got != tt.want {
				t.Errorf("Name() = %q, want %q", got, tt.want)
			}
			// The name and the address are different answers. A run that
			// recorded only the address could not tell this backend from
			// another pointed at the same endpoint, which is the whole reason
			// Name exists.
			if c.Name() == c.BaseURL() {
				t.Errorf("Name() and BaseURL() both report %q", c.Name())
			}
		})
	}
}
