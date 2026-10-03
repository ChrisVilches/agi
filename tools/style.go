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
	// sat a neighbor away from the blue the detail lines are written in, and
	// a label beside its own detail in a neighboring color is a block of one
	// wash rather than a heading and what it heads.
	nameStyle = color.New(color.FgYellow, color.Bold)
	// detailStyle renders subordinate detail, such as the command a tool is
	// about to run. It is blue, which says "this is the second voice" rather
	// than "this is barely here": a different color from the text around it,
	// so the eye sorts the two apart at a glance, and a color no theme hides.
	// Two earlier attempts say why it is not something quieter. Bright black
	// is drawn so close to the background by a great many themes that the
	// dimmed lines could not be read at all, and white is the terminal's
	// default foreground, so it looked like ordinary text and marked nothing.
	// Green was not a candidate: it means success in every program that uses
	// it. Nor was yellow, which is the tool label above.
	//
	// Only the sixteen named colors are used, here and everywhere else, so
	// that what agi looks like is decided by the palette of the terminal it is
	// running in rather than by a number written down in this file.
	detailStyle = color.New(color.FgBlue)
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
