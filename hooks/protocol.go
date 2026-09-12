package hooks

import (
	"bytes"
	"encoding/json"
	"strings"
)

// input is the JSON object a hook reads on standard input.
//
// The field names are Claude Code's, so that a hook script written for that
// program finds what it expects. Only the fields with a true twin in agi are
// sent: transcript_path, scratchpad_dir, permission_mode, effort, prompt_id
// and agent_type are left out, because a field carrying an invented value
// would be worse than a missing one.
//
// The object has two halves. ToolName, ToolInput and ToolOutput are what the
// hook decides about; the rest is what it decides in. agi's own machinery
// reads none of it.
type input struct {
	HookEventName Event           `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolUseID     string          `json:"tool_use_id,omitempty"`
	ToolOutput    *payload        `json:"tool_output,omitempty"`
	CWD           string          `json:"cwd,omitempty"`
	SessionID     string          `json:"session_id,omitempty"`
	AgentID       string          `json:"agent_id,omitempty"`
}

// payload is how a tool's output travels, in and out.
//
// agi's tool output is a string, and this wraps it in the object Claude Code
// uses so that a script reading .tool_output.text finds it, and so that a
// script replacing the output writes the shape its documentation describes.
// Two fields is a small price for a ported script working unchanged.
type payload struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// textPayload wraps a tool's output for sending.
func textPayload(text string) *payload { return &payload{Type: "text", Text: text} }

// encode renders what a hook is told about one call.
//
// Arguments that are not valid JSON are sent as null rather than smuggled
// into the object, which would make the whole payload unreadable. The model
// can produce arguments no tool would accept, and a hook with no conditions
// runs before the tool gets to refuse them, so this is reachable: a hook that
// looks sees null and knows as much as agi does.
func (s *Set) encode(event Event, c Call, out *payload) ([]byte, error) {
	in := input{
		HookEventName: event,
		ToolName:      c.Tool,
		ToolInput:     json.RawMessage("null"),
		ToolUseID:     c.RequestID,
		ToolOutput:    out,
		CWD:           s.cwd,
		SessionID:     s.session,
		AgentID:       c.Agent,
	}
	if json.Valid(c.Arguments) {
		in.ToolInput = c.Arguments
	}
	return json.Marshal(in)
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
type specific struct {
	PermissionDecision       string          `json:"permissionDecision"`
	PermissionDecisionReason string          `json:"permissionDecisionReason"`
	UpdatedInput             json.RawMessage `json:"updatedInput"`
	UpdatedToolOutput        *payload        `json:"updatedToolOutput"`
	AdditionalContext        string          `json:"additionalContext"`
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
