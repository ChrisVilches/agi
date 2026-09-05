package native

import "sync/atomic"

// Counter hands out the numbers that name the agents a run spawns.
//
// It is created once per run and shared by every Subagents tool in it — the
// run's own, and the one each fork builds for itself — rather than being a
// field on the tool. A tool-scoped counter was right while a run had exactly
// one of these tools, and stopped being right the moment a fork could build a
// second: two counters starting from one put two different agents under one
// name.
//
// The name is not only a label. An agent's copies of the files it changes are
// filed under it too, so a reader sorting that directory would find two
// subagent-1 entries belonging to different agents with no way to tell which
// change was whose. The log survives it — every line there says which side of
// a fork it was on — but a directory of file copies has no such field, and it
// is read by a person rather than by a program.
//
// It also keeps a rule the repository already states: the numbering counts
// across calls, so a run that fans out twice does not reuse the first group's
// names. A fork is one more place those calls come from.
//
// The zero value is ready to use and numbers from one.
type Counter struct{ n atomic.Int64 }

// Take claims a block of n numbers and returns the first of them.
//
// A whole block is claimed at once so that a call numbers its tasks in the
// order they were written rather than the order their goroutines happened to
// start, and so that two calls racing cannot interleave their numbers. A
// number taken from scheduling would put a different agent under the same name
// on the next identical run, which is the rule that already makes a call's
// answers come back in the order they were asked for.
func (c *Counter) Take(n int) int {
	if n < 1 {
		n = 1
	}
	return int(c.n.Add(int64(n))) - n + 1
}
