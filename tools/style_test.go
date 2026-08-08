package tools

import (
	"strings"
	"testing"

	"github.com/fatih/color"
)

// withColor forces color on for one test, whatever the terminal is.
func withColor(t *testing.T) {
	t.Helper()
	previous := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = previous })
}

func TestNameIsBracketed(t *testing.T) {
	color.NoColor = true
	if got := Name("bash"); got != "[bash]" {
		t.Errorf("Name() = %q, want %q", got, "[bash]")
	}
}

func TestNameIsColored(t *testing.T) {
	withColor(t)

	got := Name("bash")
	if !strings.Contains(got, "[bash]") {
		t.Errorf("Name() = %q, want it to contain the label", got)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("Name() = %q, want ANSI color codes", got)
	}
	// Bold plus a color resets as "\x1b[0;22m", so match the reset prefix.
	if !strings.Contains(got, "\x1b[0") || !strings.HasSuffix(got, "m") {
		t.Errorf("Name() = %q, want the styling reset at the end", got)
	}
}

// The command under a call is gray so the reason reads first.
func TestDetailIsGray(t *testing.T) {
	withColor(t)

	got := Detail("$ df -h")
	if !strings.Contains(got, "$ df -h") {
		t.Errorf("Detail() = %q, want it to contain the text", got)
	}
	// 90 is bright black, which terminals render as gray.
	if !strings.Contains(got, "\x1b[90m") {
		t.Errorf("Detail() = %q, want the gray color code", got)
	}
}

// Piped or captured output must stay plain, which is what keeps agi usable in
// a pipeline and makes these tests readable.
func TestStylesArePlainWhenColorIsOff(t *testing.T) {
	previous := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = previous })

	if got := Detail("$ ls"); got != "$ ls" {
		t.Errorf("Detail() = %q, want plain text", got)
	}
	if got := Name("bash"); strings.Contains(got, "\x1b[") {
		t.Errorf("Name() = %q, want no escape codes", got)
	}
}
