package tools

import "fmt"

// Truncate shortens oversized tool output, keeping the head and the tail and
// saying in between how much was dropped.
//
// It is here rather than in one tool because every tool that hands a program's
// output back to the model has the same problem and no reason to answer it
// differently: output that would fill the context window is worth less than
// the two ends of it, and where a long result was cut has to be visible, or
// the model reads a truncated file as the whole of one. Each tool still
// chooses its own limit, since what counts as too much depends on what the
// tool produces.
//
// A limit of zero or less means no limit at all.
func Truncate(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	half := limit / 2
	omitted := len(s) - 2*half
	return s[:half] +
		fmt.Sprintf("\n... [%d bytes omitted] ...\n", omitted) +
		s[len(s)-half:]
}
