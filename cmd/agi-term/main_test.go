package main

import "testing"

// Telling a read from a line meant for the program is the only decision this
// command makes, and getting it wrong types something into whatever is on the
// other end, which is the one mistake with consequences outside the tool.
func TestReadWaitTellsAReadFromSomethingTyped(t *testing.T) {
	for _, c := range []struct {
		line string
		wait int
		read bool
	}{
		{"-", 0, true},
		{"- 500", 500, true},
		{"-   500   ", 500, true},
		{"- 0", 0, true},
		// Plainly a read, so it reads; typing it would send "- abc" to a
		// program that was never asked for it.
		{"- abc", 0, true},
		{"- -5", 0, true},
		// A line that starts with the character but is not it.
		{"-l", 0, false},
		{"-rf /tmp/x", 0, false},
		{"--help", 0, false},
		{"", 0, false},
		{"ls", 0, false},
		{" -", 0, false},
	} {
		wait, read := readWait(c.line)
		if read != c.read || wait != c.wait {
			t.Errorf("readWait(%q) = %d, %v, want %d, %v", c.line, wait, read, c.wait, c.read)
		}
	}
}
