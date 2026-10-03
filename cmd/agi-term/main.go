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
//	"-"            read the screen now
//	"-" and a number   read it after that many milliseconds
//	anything else  type that line, then press Enter
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
// Reading is a separate command, and the typing commands never read, which is
// not a shortcut but the point. terminal_send and terminal_read are two tools
// and a test that always ran them together could not tell which of them was
// wrong. "-" is the command because it is one character that no shell command
// begins with, so it never collides with a line meant for the program.
//
// The number after "-" is the one thing here that is not simply a line typed
// at a terminal, and it is there because without it the tool only works at a
// human's typing speed. A person takes a second to type the next line and the
// program has long since answered; a script piping lines in sends them all
// within a millisecond and reads a screen the program has not drawn yet, which
// is not a terminal being tested but a race. It is also the only way to
// exercise terminal_read's wait_ms, which is a parameter worth testing.
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
// it.
const readCommand = "-"

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

// readWait reports whether a line is the read command, and how long it asks to
// wait first. "-" is zero, "- 500" is five hundred milliseconds, and anything
// else is not a read at all — a line that starts with the character but is not
// it, such as a program's own "-l", is typed like any other.
//
// A number that will not parse makes the line a read of zero rather than
// something typed, since "- abc" was plainly meant as a read and typing it
// into whatever is on the other end is the one outcome nobody wanted.
func readWait(line string) (int, bool) {
	rest, ok := strings.CutPrefix(line, readCommand)
	if !ok || (rest != "" && strings.TrimSpace(rest) == rest) {
		return 0, false
	}
	wait, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil || wait < 0 {
		return 0, true
	}
	return wait, true
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
		if wait, ok := readWait(line); ok {
			call(workspace.TerminalRead, map[string]any{
				"terminal": terminalID,
				"wait_ms":  wait,
			})
		} else {
			call(workspace.TerminalSend, map[string]any{
				"terminal":    terminalID,
				"text":        line,
				"keys":        []string{"ENTER"},
				"explanation": explanation,
			})
		}
	}
	return in.Err()
}
