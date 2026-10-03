// Command agi-term drives agi's terminal tools by hand.
//
// It exists to answer one question that a real run answers slowly and
// expensively: what does a model actually see when it types something into a
// terminal? Here a person types the line and reads the result, and the result
// is the same string the model would have been handed — not a rendering of it
// and not a second implementation of the same idea. The tools are built the
// way the agent builds them, called the way the agent calls them, and whatever
// they return is printed unchanged.
//
// It is a testing tool and nothing more. There are no flags, no configuration,
// no session, no log, and no model anywhere in it. Nothing here is on the path
// a real run takes, so it may be rewritten freely; what it must not do is
// reimplement any part of what it is testing, since a copy that drifts tests
// the copy.
//
// The whole of its language is a line at a time:
//
//	"-"            read the visible screen now
//	"- 60"         read the last 60 lines, reaching into scrollback
//	"- 500ms"      wait half a second, then read
//	"- 500ms 60"   both, in either order
//	anything else  type that line, press Enter, and read what it did
//	end of input   close the terminal and stop
//
// Typing is mapped to a line because that is what a person does at a terminal,
// and the Enter is added rather than typed so that the line read from standard
// input is the line sent, with nothing to escape. An empty line is a bare
// Enter, which is what answers a prompt that takes a default. Multi-line input
// is deliberately not possible: a terminal takes one line at a time anyway,
// and a heredoc typed into this tool would be a feature of this tool rather
// than of anything it is testing.
//
// A typed line reads the screen back after a short wait, because that is what
// terminal_send does: given a wait or a line count it answers with the screen,
// and given neither it answers "Sent to terminal 1." The combined form is the
// one a model mostly uses — typing a command and seeing what it did is the
// commonest thing done with a terminal, and two calls for it is a turn spent
// every time — so a tool for watching what a model sees has to use it too.
//
// Reading is still a command of its own, for the case the wait was too short
// and for looking further back than the screen. "-" is that command because
// it is one character that no shell command begins with, so it never collides
// with a line meant for the program.
//
// The numbers after "-" are terminal_read's own two parameters and are the
// only thing here that is not simply a line typed at a terminal. The wait is
// there because without it the tool works only at a person's typing speed: a
// script piping lines in sends them all within a millisecond and reads a
// screen the program has not drawn yet, which is a race rather than a terminal
// being tested.
//
// The wait is the one that carries a unit, and that is worth saying because
// the first version had it the other way round. A bare number after "-" was a
// wait in milliseconds, and the first person to use it read "- 10000" as ten
// thousand lines and wondered why the tool had hung. Both readings are
// reasonable, which is the problem: the number has to say which it is, and
// since one of the two is guessed right by default it is the other that
// carries the "ms".
//
// What it still cannot reach is a key by name — CTRL_C, UP, TAB; terminal_send
// takes them and nothing here sends them — and terminal_send's own two waits.
// Both are a few lines to add if the testing wants them.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/tools/workspace"
)

// readCommand is the line that reads the screen instead of being typed into
// it, and waitSuffix is what marks the read's number as a wait rather than a
// count of lines.
const (
	readCommand = "-"
	waitSuffix  = "ms"
)

// explanation is what every send reports as its reason. The tool requires one
// and is right to: a model typing keys has to say why first. Here the answer
// is the same every time and is true — a person typed it — so it is a constant
// rather than something asked for at the prompt, which would be a question
// with one answer.
const explanation = "typed by hand in agi-term"

// terminalID is the terminal every command acts on. One terminal is all a tool
// for watching what a screen comes back as needs; the cap, the refusals for a
// terminal that was closed and for one that never existed, and everything else
// about keeping several are the tools' own behavior and are tested with the
// tools' own tests.
const terminalID = 1

// sendWaitMS is how long every typed line waits before reading back, which is
// what makes a send answer with the screen rather than with "Sent to terminal
// 1." terminal_send reads when it is given a wait or a line count and not
// otherwise, and the combined form is the one a model mostly uses, since
// typing a command and seeing what it did is the commonest thing done with a
// terminal. A tool for watching what the model sees should therefore see the
// same thing.
//
// It is a constant rather than something the line can set, because the text
// of the line is the thing being typed and anything else on it would have to
// be carved back out of a program's own arguments. Half a second is enough
// for most commands to have printed something and short enough not to be felt
// between one typed line and the next; a command still working is read again
// with "-", which is what that command is for.
const sendWaitMS = 500

// readArgs reports whether a line is the read command, and the two numbers
// terminal_read takes: how many lines to read, and how long to wait first.
//
// A bare number is lines, and a wait has to say "ms". That way round because
// the two numbers are not equally guessable: "- 60" reads as sixty lines to
// anybody who has read terminal_read's parameters, and a unit on the other
// one is what stops "- 10000" from being a ten-second pause somebody meant as
// ten thousand lines. Both are optional and either order will do, since a
// number carrying its own unit does not need a position as well.
//
// A line that starts with the character but is not the command — a program's
// own "-l", or "--help" — is typed like any other. A word that is neither
// number is ignored and the line still reads, since "- abc" was plainly meant
// as a read and typing it into whatever is on the other end is the one
// outcome nobody wanted.
func readArgs(line string) (lines, wait int, ok bool) {
	rest, found := strings.CutPrefix(line, readCommand)
	if !found || (rest != "" && strings.TrimSpace(rest) == rest) {
		return 0, 0, false
	}
	for _, word := range strings.Fields(rest) {
		if ms, err := strconv.Atoi(strings.TrimSuffix(word, waitSuffix)); err == nil &&
			strings.HasSuffix(word, waitSuffix) && ms >= 0 {
			wait = ms
			continue
		}
		if n, err := strconv.Atoi(word); err == nil && n >= 0 {
			lines = n
		}
	}
	return lines, wait, true
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run opens a terminal, drives it from standard input, and closes it. The
// error it returns is only ever one that stopped it: a tool call that failed
// is printed and the loop goes on, because a refusal is a thing worth looking
// at and the terminal is still there to try the next line on.
func run() error {
	ts := workspace.NewTerminals(terminalID)
	// Closing ends the program in the terminal and everything it started. It
	// is deferred rather than left to the process exiting, because a shell
	// with jobs in the background outlives a parent that does not ask.
	defer ts.Close()

	byName := map[string]tools.Tool{}
	for _, tool := range ts.Tools() {
		byName[tool.Definition().Name] = tool
	}

	ctx := context.Background()
	// call runs a tool the way the agent loop does and prints what came back.
	// A failure prints as a failure: the model is handed the error text as the
	// call's answer, so seeing it here is seeing what it would read.
	call := func(name string, args any) {
		raw, err := json.Marshal(args)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		out, err := byName[name].Call(ctx, raw)
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(out)
	}

	call(workspace.TerminalOpen, map[string]any{})

	// Scanner is enough because a line is all this reads and a line longer
	// than its buffer is not a line anybody typed.
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		line := in.Text()
		// A line that is the read command, or the read command and a number,
		// reads. Anything else is typed, including a line that merely starts
		// with the character: "-l" is a flag somebody meant for a program.
		if lines, wait, ok := readArgs(line); ok {
			call(workspace.TerminalRead, map[string]any{
				"terminal": terminalID,
				"lines":    lines,
				"wait_ms":  wait,
			})
		} else {
			call(workspace.TerminalSend, map[string]any{
				"terminal":      terminalID,
				"text":          line,
				"keys":          []string{"ENTER"},
				"explanation":   explanation,
				"wait_after_ms": sendWaitMS,
			})
		}
	}
	return in.Err()
}
