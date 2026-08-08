package tools

import "github.com/fatih/color"

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

// Detail renders subordinate text in gray.
func Detail(s string) string { return detailStyle.Sprint(s) }
