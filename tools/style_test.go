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
	// 37 is white, which the usual dark themes render as a light gray.
	if !strings.Contains(got, "\x1b[37m") {
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

// A span that opens on one line and closes on another asks the terminal to
// carry the attribute across the line break, and many do not: the first line
// comes out gray and the rest plain. So every line is styled and closed on its
// own, and a blank line is left alone rather than being given a span with
// nothing in it.
func TestDetailStylesEveryLine(t *testing.T) {
	withColor(t)

	got := Detail("one\ntwo\n\nthree")

	lines := strings.Split(got, "\n")
	if len(lines) != 4 {
		t.Fatalf("Detail() produced %d lines, want the 4 it was given: %q", len(lines), got)
	}
	for i, line := range lines {
		if i == 2 {
			if line != "" {
				t.Errorf("the blank line came back as %q, want it left alone", line)
			}
			continue
		}
		if !strings.HasPrefix(line, "\x1b[") {
			t.Errorf("line %d = %q, want it to open its own style", i, line)
		}
		if !strings.HasSuffix(line, "\x1b[0m") {
			t.Errorf("line %d = %q, want it to close its own style", i, line)
		}
	}

	// A single line is unchanged by any of this.
	if got, want := Detail("alone"), detailStyle.Sprint("alone"); got != want {
		t.Errorf("Detail(one line) = %q, want %q", got, want)
	}
}

// Color is off when nothing is watching, and that must not change what the
// text says.
func TestDetailIsPlainWithoutColor(t *testing.T) {
	previous := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = previous })

	if got := Detail("one\ntwo"); got != "one\ntwo" {
		t.Errorf("Detail() = %q, want the text unchanged when color is off", got)
	}
}
