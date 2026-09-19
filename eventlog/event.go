package eventlog

import (
	"encoding/json"
	"time"
)

// Kind says what an event is. The strings are the format: a reader filters and
// groups by them, so renaming one makes every log written before the rename
// read differently.
type Kind string

// The events a run can produce. Between them they account for everything agi
// does: what it was asked, what it was configured with, every exchange with
// the gateway, every tool that ran, every agent that was spawned, and
// everything that was later taken back out of the conversation.
const (
	// RunStart is agi starting work on a prompt: the model, the gateway, the
	// tools registered, where the run is stored, and what was asked.
	RunStart Kind = "run.start"
	// RunFinish is the run ending, with the reason if it failed.
	RunFinish Kind = "run.finish"
	// RunInterrupt is the run being cut short by the user, at the turn it had
	// reached. What follows it is an ordinary ending: the tool calls that were
	// never made are answered as failures, the conversation is stored, and
	// RunFinish closes the run as it closes any other.
	RunInterrupt Kind = "run.interrupt"
	// SystemPrompt is the prompt the run assembled for itself. It is recorded
	// because nothing else keeps it: a snapshot deliberately does not, since
	// the prompt is built from how agi was invoked. Without it in the log a
	// run could not be reproduced from the record.
	SystemPrompt Kind = "system.prompt"
	// SessionResume is a stored conversation being picked up again.
	SessionResume Kind = "session.resume"
	// SessionSave is the snapshot being written at the end of a run.
	SessionSave Kind = "session.save"
	// Request is a conversation being sent to the gateway.
	Request Kind = "request"
	// Response is what came back: the prose, and any tools it asked for.
	Response Kind = "response"
	// ToolCall is one tool about to run, with the arguments the model gave.
	ToolCall Kind = "tool.call"
	// ToolResult is what a tool produced.
	ToolResult Kind = "tool.result"
	// ToolFailure is a call that could not be made at all.
	ToolFailure Kind = "tool.failure"
	// SubagentStart is a spawned agent beginning its task. It is recorded by
	// the subagent itself, under its own name, so everything that follows it
	// under that name is its work.
	SubagentStart Kind = "subagent.start"
	// SubagentFinish is a spawned agent's answer, or why it has none.
	SubagentFinish Kind = "subagent.finish"
	// SchemaRestate is the model being told to say its answer again as JSON.
	SchemaRestate Kind = "schema.restate"
	// SchemaAttempt is one document the model offered.
	SchemaAttempt Kind = "schema.attempt"
	// SchemaRejected is an attempt that did not match the schema, and why.
	SchemaRejected Kind = "schema.rejected"
	// SchemaAccepted is the document the run returns.
	SchemaAccepted Kind = "schema.accepted"
	// Answer is the prose the agent loop ended on.
	Answer Kind = "answer"
	// Drop is turns being removed from the conversation. The turns are gone
	// from the conversation and will not be in the snapshot; this line is
	// where they are accounted for, which is the log's whole advantage over
	// the snapshot as a record of what happened.
	Drop Kind = "drop"
)

// Event is one line of the log.
//
// Every kind of event is one struct rather than a type each, because a file
// has no closed set of types to switch on: Kind says which fields mean
// anything, and the rest are omitted from the line. It is the same bargain the
// session snapshot makes, and for the same reason. What keeps it honest is
// that events are recorded through Log.Record and read back as JSON, so a
// field that means nothing for a kind simply is not there.
//
// Time and Agent are stamped by the log and must not be set by a caller.
//
// There is no sequence number, and that is deliberate. One goroutine writes
// the file and appends each line as it arrives, so the lines of a run are
// always in the order they were recorded and a number saying so would only
// repeat what counting the lines already tells you. It would also lie in the
// one case where a file really can interleave — two runs sharing a session
// directory — since each of them would number from one.
type Event struct {
	// Time is when the event was recorded, which is when the thing happened.
	// The order of the file is the order events were written down, which is
	// not quite the same thing when several agents are recording at once.
	Time time.Time `json:"time"`
	// Agent names which agent recorded it: the main run, or a subagent.
	Agent string `json:"agent"`
	// Kind says what happened, and which of the fields below mean anything.
	Kind Kind `json:"kind"`

	// Model is the model a request runs on.
	Model string `json:"model,omitempty"`
	// Gateway is the gateway's base URL.
	Gateway string `json:"gateway,omitempty"`
	// Tools names the registered tools, in the order they are offered.
	Tools []string `json:"tools,omitempty"`
	// Session is the directory the run is stored in, empty when it is not
	// stored at all.
	Session string `json:"session,omitempty"`
	// Schema is the answer shape the run was asked for.
	Schema string `json:"schema,omitempty"`

	// Turn numbers the agent loop's passes, from one.
	Turn int `json:"turn,omitempty"`
	// Attempt numbers the tries at restating an answer as JSON, from one.
	Attempt int `json:"attempt,omitempty"`
	// Messages counts the turns in a conversation being sent, stored, or
	// resumed.
	Messages int `json:"messages,omitempty"`

	// Tool names the tool a call, result, or failure belongs to.
	Tool string `json:"tool,omitempty"`
	// RequestID correlates a call with the result or failure answering it,
	// and both with the assistant turn that asked.
	RequestID string `json:"request_id,omitempty"`
	// Arguments is the JSON the model produced for a tool, stored as the
	// object it is so the line stays machine-readable.
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// Requests are the tools an assistant turn asked for.
	Requests []LoggedRequest `json:"requests,omitempty"`

	// Title is a subagent's brief in a few words, as the model titled it.
	Title string `json:"title,omitempty"`
	// Content is whatever the event is mostly made of: a prompt, a system
	// prompt, the model's prose, a tool's output, an answer, a JSON document.
	Content string `json:"content,omitempty"`
	// Reason is why something failed, in agi's words.
	Reason string `json:"reason,omitempty"`

	// From is the turn a drop started at, and Dropped how many turns went.
	From    int `json:"from,omitempty"`
	Dropped int `json:"dropped,omitempty"`

	// Elapsed is how long the thing took, in milliseconds, for the events
	// worth timing: a request, a tool, a subagent, a whole run.
	Elapsed int64 `json:"elapsed_ms,omitempty"`
}

// LoggedRequest is one tool call an assistant turn asked for, as it is
// recorded. It mirrors model.ToolRequest rather than reusing it, so the shape
// of the log is decided here and changing the domain type does not silently
// change the format.
type LoggedRequest struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Since returns how long has passed since start, in the milliseconds Elapsed
// is measured in. It is here so that every event that times something times it
// the same way.
func Since(start time.Time) int64 { return time.Since(start).Milliseconds() }
