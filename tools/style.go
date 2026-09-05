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
	// nameStyle renders the bracketed tool name that heads a call.
	nameStyle = color.New(color.FgCyan, color.Bold)
	// detailStyle renders subordinate detail, such as the command a tool is
	// about to run. Gray keeps it legible without competing with the reason.
	detailStyle = color.New(color.FgHiBlack)
)

// Name renders a tool name as a bracketed, colored label, for example [bash].
func Name(name string) string { return nameStyle.Sprintf("[%s]", name) }

// Detail renders subordinate text in gray, one line at a time.
//
// Each line is styled and closed on its own rather than the whole text being
// wrapped in a single span. A span that opens on one line and closes on
// another asks the terminal to carry the attribute across the line break, and
// a great many of them do not: the first line comes out gray and every line
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
