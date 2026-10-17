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

// The command under a call is in the secondary color so the reason reads
// first.
func TestDetailIsTheSecondaryColor(t *testing.T) {
	withColor(t)

	got := Detail("$ df -h")
	if !strings.Contains(got, "$ df -h") {
		t.Errorf("Detail() = %q, want it to contain the text", got)
	}
	// 2 is faint, the second voice the detail lines are written in. It names
	// no color, so the line keeps the terminal's own foreground.
	if !strings.Contains(got, "\x1b[2m") {
		t.Errorf("Detail() = %q, want the secondary color code", got)
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
// comes out colored and the rest plain. So every line is styled and closed on
// its own, and a blank line is left alone rather than being given a span with
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
		// Faint closes as "\x1b[22m", the reset for bold and faint alone,
		// rather than the "\x1b[0m" that clears everything. What matters is
		// that the line closes what it opened, so the suffix is matched
		// loosely and the exact code is left to the color package.
		if !strings.HasSuffix(line, "m") || !strings.Contains(line[1:], "\x1b[") {
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

func TestThinkingIsBracketed(t *testing.T) {
	color.NoColor = true
	if got := Thinking(); got != "[thinking]" {
		t.Errorf("Thinking() = %q, want %q", got, "[thinking]")
	}
}

// The label heading a model's reasoning has to be told apart at a glance from
// the tool calls it sits among, so it is a color and not the same one.
func TestThinkingIsColoredAndNotTheToolColor(t *testing.T) {
	withColor(t)

	got := Thinking()
	if !strings.Contains(got, "[thinking]") {
		t.Errorf("Thinking() = %q, want it to contain the label", got)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("Thinking() = %q, want ANSI color codes", got)
	}
	if styleOf(got) == styleOf(Name("thinking")) {
		t.Errorf("Thinking() = %q, want a color of its own rather than the tool label's", got)
	}
}

// styleOf returns the escape sequence a styled label opens with, which is what
// says which color it was drawn in.
func styleOf(s string) string {
	_, rest, found := strings.Cut(s, "\x1b[")
	if !found {
		return ""
	}
	opening, _, _ := strings.Cut(rest, "m")
	return opening
}

// The two sides of a change are drawn the way every diff draws them, and in
// two colors that are not each other's, so the line going out and the line
// coming in are told apart without reading the marker.
func TestRemovedAndAddedAreDiffColors(t *testing.T) {
	withColor(t)

	removed, added := Removed("- old"), Added("+ new")
	if !strings.Contains(removed, "- old") || !strings.Contains(added, "+ new") {
		t.Fatalf("Removed() = %q, Added() = %q, want the text kept", removed, added)
	}
	// 31 is red and 32 is green, two of the sixteen named colors.
	if styleOf(removed) != "31" {
		t.Errorf("Removed() = %q, want red", removed)
	}
	if styleOf(added) != "32" {
		t.Errorf("Added() = %q, want green", added)
	}
}

// Like Detail, each line opens and closes its own span, and a blank line is
// left blank.
func TestRemovedAndAddedStyleEveryLine(t *testing.T) {
	withColor(t)

	for name, style := range map[string]func(string) string{"Removed": Removed, "Added": Added} {
		lines := strings.Split(style("one\n\ntwo"), "\n")
		if len(lines) != 3 || lines[1] != "" {
			t.Fatalf("%s() = %q, want three lines with the blank one left alone", name, lines)
		}
		for _, i := range []int{0, 2} {
			if !strings.HasPrefix(lines[i], "\x1b[") || !strings.HasSuffix(lines[i], "m") {
				t.Errorf("%s() line %d = %q, want it to open and close its own style", name, i, lines[i])
			}
		}
	}
}

func TestRemovedAndAddedArePlainWithoutColor(t *testing.T) {
	previous := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = previous })

	if got := Removed("- a\n- b"); got != "- a\n- b" {
		t.Errorf("Removed() = %q, want plain text", got)
	}
	if got := Added("+ a"); got != "+ a" {
		t.Errorf("Added() = %q, want plain text", got)
	}
}
