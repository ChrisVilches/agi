package tools

import (
	"strings"

	"github.com/fatih/color"
)

// Styles for the lines a tool call prints. They live here rather than in the
// harness because a tool decides which parts of its announcement are the
// command, the reason, and so on; the harness only decides where to print it.
//
// The color package disables itself when the output is not a terminal and when
// NO_COLOR is set, so these produce plain text when piped or captured.
var (
	// nameStyle renders the bracketed tool name that heads a call. Bold
	// yellow, and the point of it is to be the opposite of the detail lines
	// below: the label is the one word on the screen that says a call is
	// happening at all, so it is the thing to be drawn to, where everything
	// under it is there to be found once you have been. It was cyan, which
	// sat a neighbor away from the blue the detail lines were written in at
	// the time, and a label beside its own detail in a neighboring color is a
	// block of one wash rather than a heading and what it heads.
	nameStyle = color.New(color.FgYellow, color.Bold)
	// detailStyle renders subordinate detail, such as the command a tool is
	// about to run. It is the faint attribute, SGR 2, and nothing else: no
	// color is named, so the text keeps whatever foreground the terminal
	// already uses and is drawn a shade weaker than the prose beside it. That
	// is the convention for this kind of line in the programs sitting next to
	// agi — Claude Code prints "Resume this session with:" and the command
	// under it in exactly this and nothing more — and a second voice the user
	// already reads as subordinate elsewhere needs no argument here.
	//
	// Three earlier attempts say what it is not. Bright black is a color a
	// great many themes draw almost on top of their own background, so the
	// lines it marked could not be read at all; faint is not that, because it
	// weakens the foreground the theme already chose rather than naming a
	// color of its own, and a theme that is readable stays readable. White
	// was that same default foreground at full strength, so the lines looked
	// like ordinary text and were marked as nothing. Blue did sort them from
	// the text around them, but it spent a color on them, and a color is a
	// louder mark than these lines are asking for: the tool label above is
	// the thing meant to be loud.
	//
	// Where a color is used, which is nameStyle above, only the sixteen named
	// ones are, here and everywhere else, so that what agi looks like is
	// decided by the palette of the terminal it is running in rather than by
	// a number written down in this file.
	detailStyle = color.New(color.Faint)
)

// Name renders a tool name as a bracketed, colored label, for example [bash].
func Name(name string) string { return nameStyle.Sprintf("[%s]", name) }

// Detail renders subordinate text in the secondary color, one line at a time.
//
// Each line is styled and closed on its own rather than the whole text being
// wrapped in a single span. A span that opens on one line and closes on
// another asks the terminal to carry the attribute across the line break, and
// a great many of them do not: the first line comes out colored and every line
// after it plain, which is what a multi-line bash command looked like.
//
// A blank line is left as it is, so nothing is written where there is nothing
// to color — the same reason block leaves the blank lines inside a command
// blank rather than padding them.
func Detail(s string) string {
	if !strings.Contains(s, "\n") {
		return detailStyle.Sprint(s)
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		lines[i] = detailStyle.Sprint(line)
	}
	return strings.Join(lines, "\n")
}

// SingleLine collapses text to one line for display, marking the collapse.
//
// It is for arguments whose point is what they name rather than what they say:
// the path a file is being written to matters more than the contents going
// into it, and a listing of the contents would push the next call off screen.
//
// It is here rather than beside one tool because two packages announce a call
// this way and the question they are answering is the same one, which is the
// argument that put Truncate in this package as well.
func SingleLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		return strings.TrimSpace(s[:i]) + " ..."
	}
	return s
}
