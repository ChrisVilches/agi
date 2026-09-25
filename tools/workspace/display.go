package workspace

import "strings"

// Helpers for the lines a tool prints before it runs. They render an argument
// the user is about to have acted on their machine, so the question each one
// answers is how much of it has to be visible to recognize what is happening.

// singleLine collapses text to one line for display, marking the collapse.
//
// It is for arguments whose point is what they name rather than what they say:
// the path a file is being written to matters more than the contents going
// into it, and a listing of the contents would push the next call off screen.
func singleLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		return strings.TrimSpace(s[:i]) + " ..."
	}
	return s
}

// block renders text under a marker, keeping every line of it.
//
// It is for an argument the user has to read in full to know what is about to
// happen — a command being run on their machine, where the line that matters
// is as likely to be the last as the first. The marker leads the first line
// and the rest are padded to line up beneath it, so the text reads as one
// block rather than as several calls, and the prompt marker keeps the shape a
// terminal transcript has. The text's own indentation is left as it was: a
// command is written to be read the way the shell reads it.
func block(marker, s string) string {
	head := "  " + marker + " "
	pad := strings.Repeat(" ", len(head))

	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, line := range lines {
		line = strings.TrimRight(line, " \t\r")
		switch {
		case i == 0:
			lines[i] = head + line
		case line == "":
			// A blank line inside the block stays blank rather than becoming
			// padding nobody can see, so nothing is left to select or copy.
			lines[i] = ""
		default:
			lines[i] = pad + line
		}
	}
	return strings.Join(lines, "\n")
}
