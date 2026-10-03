package main

import "testing"

// Telling a read from a line meant for the program is the only decision this
// command makes, and getting it wrong types something into whatever is on the
// other end, which is the one mistake with consequences outside the tool.
//
// Which number is which is checked here too, because the two are not
// interchangeable in a way anyone would notice: a wait read as a count of
// lines is a tool that looks hung, and a count read as a wait is a screen
// that came back shorter than it should have.
func TestReadArgsTellsAReadFromSomethingTyped(t *testing.T) {
	for _, c := range []struct {
		line        string
		lines, wait int
		read        bool
	}{
		{line: "-", read: true},
		// A bare number is lines, which is what it is guessed to be.
		{line: "- 60", lines: 60, read: true},
		{line: "- 10000", lines: 10000, read: true},
		// A wait says so.
		{line: "- 500ms", wait: 500, read: true},
		{line: "- 0ms", read: true},
		// Both, in either order, since each says what it is.
		{line: "- 500ms 60", lines: 60, wait: 500, read: true},
		{line: "- 60 500ms", lines: 60, wait: 500, read: true},
		{line: "-   60   500ms  ", lines: 60, wait: 500, read: true},
		// Plainly a read, so it reads; typing it would send "- abc" to a
		// program that was never asked for it.
		{line: "- abc", read: true},
		{line: "- -5", read: true},
		{line: "- 5ms extra", wait: 5, read: true},
		// A line that starts with the character but is not the command.
		{line: "-l", read: false},
		{line: "-rf /tmp/x", read: false},
		{line: "--help", read: false},
		{line: "", read: false},
		{line: "ls", read: false},
		{line: " -", read: false},
	} {
		lines, wait, read := readArgs(c.line)
		if read != c.read || lines != c.lines || wait != c.wait {
			t.Errorf("readArgs(%q) = %d lines, %dms, %v; want %d lines, %dms, %v",
				c.line, lines, wait, read, c.lines, c.wait, c.read)
		}
	}
}
