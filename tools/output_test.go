package tools

import (
	"strings"
	"testing"
)

func TestTruncateKeepsHeadAndTail(t *testing.T) {
	const limit = 1000
	s := strings.Repeat("a", limit/2) + strings.Repeat("b", 500) + strings.Repeat("c", limit/2)

	got := Truncate(s, limit)
	if len(got) >= len(s) {
		t.Fatalf("Truncate() did not shorten %d bytes", len(s))
	}
	if !strings.HasPrefix(got, "aaa") || !strings.HasSuffix(got, "ccc") {
		t.Error("Truncate() did not keep both the head and the tail")
	}
	// Where the cut was made has to be visible, or the model reads what is
	// left as the whole of it.
	if !strings.Contains(got, "bytes omitted") {
		t.Error("Truncate() did not say that output was omitted")
	}
}

func TestTruncateLeavesShortOutputAlone(t *testing.T) {
	if got := Truncate("short", 1000); got != "short" {
		t.Errorf("Truncate() = %q, want the output unchanged", got)
	}
	if got := Truncate("", 1000); got != "" {
		t.Errorf("Truncate() = %q, want empty", got)
	}
}

// A tool that does not want a limit passes none.
func TestTruncateWithoutALimit(t *testing.T) {
	s := strings.Repeat("x", 10_000)
	for _, limit := range []int{0, -1} {
		if got := Truncate(s, limit); got != s {
			t.Errorf("Truncate(limit=%d) shortened output that had no limit", limit)
		}
	}
}
