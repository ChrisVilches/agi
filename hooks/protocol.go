package hooks

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/ChrisVilches/agi/tools"
)

// input is the JSON object a hook reads on standard input.
//
// The field names are Claude Code's, so that a hook script written for that
// program finds what it expects. Only the fields with a true twin in agi are
// sent: transcript_path, scratchpad_dir, permission_mode, effort, prompt_id,
// agent_type, mcp_server, turn_id and message_id are left out, because a
// field carrying an invented value would be worse than a missing one. They
// are listed as unsupported in the specification, since a script that reads
// one is a script that will not work here and its author is owed the reason.
//
// The object has two halves. ToolName, ToolInput, ToolResponse and Delta are
// what the hook decides about; the rest is what it decides in. agi's own
// machinery reads none of it.
//
// It is one struct for every event, with hook_event_name saying which fields
// mean anything, which is the bargain eventlog.Event already makes and for
// the same reason: the reader on the other side switches on that name
// whatever agi does here, and a struct per event would be several types to
// keep in step for one field each.
type input struct {
	HookEventName Event `json:"hook_event_name"`

	// The tool events only. They carry omitempty so that a MessageDisplay
	// payload does not arrive with an empty tool name and a null tool input,
	// which would have a script branching on a call that does not exist.
	ToolName  string          `json:"tool_name,omitempty"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`

	// ToolResponse is PostToolUse's: what the tool answered. It is raw
	// because its shape is the tool's own, exactly as it is in Claude Code —
	// an object for a tool implementing tools.Responder, and the plain
	// string the model reads for every other one.
	ToolResponse json.RawMessage `json:"tool_response,omitempty"`
	// DurationMS is how long the call took, PostToolUse's and optional in
	// Claude Code too. A pointer so that a call agi somehow timed at zero is
	// still reported as timed.
	DurationMS *int64 `json:"duration_ms,omitempty"`

	// MessageDisplay's four. Delta carries the text agi is about to print.
	// agi fires this event once per finished message, which is the shape
	// Claude Code takes in a non-interactive run — there the single call
	// holds the whole message, Index is zero and Final is true — so those
	// two are sent with exactly those values rather than left out. They are
	// pointers because their true values are the zero ones, and a script
	// reading .final has to find false rather than nothing.
	Delta string `json:"delta,omitempty"`
	Index *int   `json:"index,omitempty"`
	Final *bool  `json:"final,omitempty"`

	CWD       string `json:"cwd,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
}

// common is the half of the payload that is the same at every event: which
// run this is, which agent is asking, and where agi is.
//
// An empty agent is the agent the user is talking to, and leaves agent_id
// out. That is Claude Code's rule — the field is there to tell a subagent's
// call from a main-thread one, and a script that tests for its presence
// would read every agi call as a subagent's if the main agent were named.
func (s *Set) common(event Event, agent string) input {
	return input{HookEventName: event, CWD: s.cwd, SessionID: s.session, AgentID: agent}
}

// describeCall renders what a hook is told about one tool call.
//
// Arguments that are not valid JSON are sent as null rather than smuggled
// into the object, which would make the whole payload unreadable. The model
// can produce arguments no tool would accept, and a hook with no conditions
// runs before the tool gets to refuse them, so this is reachable: a hook that
// looks sees null and knows as much as agi does.
//
// The response is nil at PreToolUse, where there is nothing to report yet,
// and so is the duration.
func (s *Set) describeCall(event Event, c Call, response json.RawMessage) input {
	in := s.common(event, c.Agent)
	in.ToolName = c.Tool
	in.ToolInput = json.RawMessage("null")
	in.ToolUseID = c.RequestID
	in.ToolResponse = response
	if json.Valid(c.Arguments) {
		in.ToolInput = c.Arguments
	}
	if response != nil && c.Elapsed > 0 {
		ms := c.Elapsed.Milliseconds()
		in.DurationMS = &ms
	}
	return in
}

// describeMessage renders what a hook is told about text agi is about to
// print. There is no tool, nothing pending, and the request that produced
// these words was answered turns ago as far as the conversation is
// concerned, so the text and its position in the message are the whole of it.
func (s *Set) describeMessage(m Message) input {
	index, final := 0, true
	in := s.common(MessageDisplay, m.Agent)
	in.Delta = m.Content
	in.Index = &index
	in.Final = &final
	return in
}

// response renders a tool's result in the shape the hook is shown it in.
//
// A tool with no shape of its own is shown the string the model reads, which
// is every tool in agi but bash. A shape that will not marshal is a bug in
// that tool rather than anything a hook did, and the string stands in for it
// so the guard still runs on something true.
func response(shape tools.Responder, result string) json.RawMessage {
	if shape != nil {
		if raw, err := json.Marshal(shape.Response(result)); err == nil {
			return raw
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return raw
}

// replacement reads what a PostToolUse hook wrote in updatedToolOutput back
// into the one string the model reads.
//
// It is false for a value the tool's shape does not accept, and for a value
// that is not a string where there is no shape. Claude Code's rule is the
// same: a replacement that does not match the tool's output is ignored and
// the original output stands.
func replacement(shape tools.Responder, raw json.RawMessage) (string, bool) {
	if shape != nil {
		return shape.Replacement(raw)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false
	}
	return text, true
}

// reply is the JSON object a hook may print on standard output.
//
// Unknown fields are ignored rather than refused, which is the opposite of
// what a manifest does with one and is the right way round for both. A
// manifest is written for agi, so a field agi does not know is a typo. This
// is written for Claude Code's protocol, so a field agi does not know is a
// field another program reads — suppressOutput and terminalSequence among
// them — and refusing it would break a script that is doing nothing wrong.
// It is the same rule a SKILL.md's frontmatter follows.
//
// Continue is a pointer because its default is true and its absence has to be
// told from a hook deliberately writing false.
type reply struct {
	Continue      *bool  `json:"continue"`
	StopReason    string `json:"stopReason"`
	SystemMessage string `json:"systemMessage"`

	// Decision and Reason are PostToolUse's way of stopping. PreToolUse says
	// it through the permission fields below instead.
	Decision string `json:"decision"`
	Reason   string `json:"reason"`

	Specific *specific `json:"hookSpecificOutput"`
}

// specific is the half of a reply that differs by event.
//
// DisplayContent is a pointer because an empty string is a hook deliberately
// printing nothing, which is not the same as a hook that left the field out.
// The others need no such care: an absent decision and an empty one both let
// the call through, and an empty updatedInput changes nothing.
type specific struct {
	PermissionDecision       string          `json:"permissionDecision"`
	PermissionDecisionReason string          `json:"permissionDecisionReason"`
	UpdatedInput             json.RawMessage `json:"updatedInput"`
	UpdatedToolOutput        json.RawMessage `json:"updatedToolOutput"`
	AdditionalContext        string          `json:"additionalContext"`
	DisplayContent           *string         `json:"displayContent"`
}

// The permission decisions a PreToolUse hook may return.
//
// "ask" escalates to the user in Claude Code and is treated as "deny" here,
// because agi has no interactive permission prompt and a run is as often
// piped with nobody watching as it is run at a terminal. A guard that wanted
// a human decision gets the safe half of it, and the model is told the reason
// so it can explain itself or try another way. This is one of the places agi
// is a flavor of Claude Code rather than a copy of it.
const (
	decisionAllow = "allow"
	decisionDeny  = "deny"
	decisionAsk   = "ask"
	decisionDefer = "defer"
	decisionBlock = "block"
)

// refuses reports whether a permission decision means the call may not be
// made. An empty decision is no decision, which lets the call through.
func refuses(decision string) bool {
	switch decision {
	case decisionDeny, decisionAsk:
		return true
	default:
		return false
	}
}

// halts reports whether a reply asked for the agent to stop.
func (r *reply) halts() bool { return r.Continue != nil && !*r.Continue }

// overreached names, in a phrase that finishes "the hook X …", whatever a
// reply asked for that a MessageDisplay hook cannot have.
//
// Claude Code accepts those fields for this event and discards them. agi
// discards them too and says so, which is the rule an "ask" decision already
// follows: quietly doing something other than what a hook asked is how
// somebody comes to believe their guard behaves one way when it behaves
// another. The likeliest cause is a hook filed under the wrong event, and a
// script that refuses a message and is never told is a script whose author
// will look everywhere but here.
//
// Only the first is named. A reply doing two of these at once is one
// confused script, and listing every way it was confused would bury the news
// that none of it happened.
func (r *reply) overreached() string {
	only := r.Specific
	switch {
	case r.halts():
		return "asked to stop the agent"
	case r.blocks():
		return "asked to stop the agent after the message"
	case refuses(only.decision()):
		return "refused the message"
	case only.context() != "":
		return "asked to add context to the conversation"
	case only != nil && (len(only.UpdatedInput) > 0 || len(only.UpdatedToolOutput) > 0):
		return "asked to change a tool call"
	}
	return ""
}

// blocks reports whether a PostToolUse reply asked for the agent to stop once
// it has read the result.
func (r *reply) blocks() bool { return r.Decision == decisionBlock }

// looksLikeJSON reports whether a hook's standard output is meant to be read
// as a reply, which Claude Code decides by whether it opens and closes as an
// object. Anything else is a hook printing its own progress.
func looksLikeJSON(out string) bool {
	trimmed := strings.TrimSpace(out)
	return strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")
}

// parseReply reads what a hook printed. A hook that printed nothing worth
// reading returns a nil reply and no error, which is the ordinary case.
func parseReply(out string) (*reply, error) {
	if !looksLikeJSON(out) {
		return nil, nil
	}
	var r reply
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// merge applies a partial updatedInput to the arguments a call was going to
// be made with.
//
// It is partial by Claude Code's definition: the object names only the fields
// it is changing and everything else is kept. So a Bash guard rewriting a
// command does not have to restate the rest of the call, which it may not
// even know the shape of.
//
// Arguments that are not an object have nothing to merge into, and the
// update stands alone. That is the only sensible reading: the hook was shown
// null, wrote an object, and means the call to be made with it.
func merge(args, update json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(update)) == 0 {
		return args, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(update, &fields); err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return args, nil
	}

	var merged map[string]json.RawMessage
	if err := json.Unmarshal(args, &merged); err != nil || merged == nil {
		return update, nil
	}
	for name, value := range fields {
		merged[name] = value
	}
	return json.Marshal(merged)
}
