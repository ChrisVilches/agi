package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/transport"
)

// The types below mirror the OpenAI Responses schema, which is what every
// endpoint agi talks to accepts — OpenAI itself, DeepSeek, and a LiteLLM
// gateway fronting either. They are deliberately kept separate from the domain
// types in package model: the wire format is the endpoint's concern, and it
// should be possible to change one without disturbing the other.
//
// They also mark the ceiling AGENTS.md sets. Nothing here asks for structured
// output or prefills an assistant turn, and nothing should be added that does:
// the schema is the intersection of what providers agree on, written out.
//
// The shape of a conversation is the one real difference from the chat
// completions schema this file used to hold. There, a request was a list of
// messages and an assistant turn was one message carrying its tool calls and
// its reasoning as fields. Here a request is a flat list of *items*, and what
// was one assistant turn becomes several of them in the order the model
// produced them, which the model interleaves as it pleases: a thought, a word
// to the user, another thought, the tool it settled on. Nothing above this
// file knows that — `model.AssistantMessage` is still one turn — and
// encodeMessage is where the one becomes the many, by handing back the list
// the reply arrived as rather than by writing a new one in a fixed order.

// How hard a thinking model is asked to think, and whether it is asked to say
// what it thought.
//
// KNOWN LIMITATION: the effort is hardcoded. It is not configurable, there is
// no environment variable for it, and no run can change it. A level has to be
// named — the Responses API has no "whatever the model does by default" — and
// medium is the middle of the three every provider offers, so it is the choice
// that is least wrong for a run nobody tuned. Making it configurable is a
// change to agi's configuration surface rather than to this file, and it is
// deliberately not made here.
//
// The summary is asked for because it is the only way reasoning comes back as
// words at all: OpenAI returns a reasoning item whose thinking is encrypted
// and unreadable unless a summary was requested, and the words are what agi
// prints above the turn and records on its `model.response` line. An endpoint
// that returns its deliberation in the clear ignores this and is unaffected.
const (
	reasoningEffort  = "medium"
	reasoningSummary = "auto"
)

// includeEncryptedReasoning asks for the one extra field a stateless run
// needs: the reasoning the model did, in a form it will take back.
//
// A request here carries the whole conversation every time and keeps nothing on
// the provider's side — `store` is false below — which is what agi already
// does with its own session files and the only arrangement that leaves no copy
// of a run on a server nobody asked to store it. The cost of that is that a
// reasoning item has no server-side state to be referred back to, so the
// endpoint has to hand the thinking back in full, encrypted to itself, and be
// shown it again on the next request. That is what this asks for and what
// model.TurnReplay.OutputItems then holds.
const includeEncryptedReasoning = "reasoning.encrypted_content"

// The kinds of item a conversation is made of. A message is the only one that
// also carries a role, and the only one whose type may be left off the wire —
// an item with a role and no type is a message, which is the spelling every
// endpoint accepts.
const (
	itemMessage            = "message"
	itemReasoning          = "reasoning"
	itemFunctionCall       = "function_call"
	itemFunctionCallOutput = "function_call_output"
)

type wireRequest struct {
	Model string `json:"model"`
	// Instructions is the system prompt, which this protocol takes as a field
	// of the request rather than as the first message of the conversation.
	Instructions string     `json:"instructions,omitempty"`
	Input        []wireItem `json:"input"`
	Tools        []wireTool `json:"tools,omitempty"`
	// Reasoning is sent on every request to every model, for the reason
	// AGENTS.md gives for the reasoning fields generally: nothing agi knows
	// says whether a model reasons, and a list of the ones that do would be
	// wrong the week after it was written. A model with no reasoning to do is
	// expected to disregard it.
	Reasoning wireReasoning `json:"reasoning"`
	Include   []string      `json:"include,omitempty"`
	// Store is always false: see includeEncryptedReasoning. It is sent
	// explicitly rather than left out because the endpoint's own default is to
	// store, and a field omitted here would mean every run of agi retained on
	// a provider's servers for a month.
	Store bool `json:"store"`
}

type wireReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary"`
}

// wireItem is one entry of a conversation: a message, a model's reasoning, a
// tool the model asked for, or what that tool answered. The protocol writes
// all four as objects distinguished by `type`, so one struct with the union of
// their fields is what goes onto the wire, and the fields an item does not use
// are left off it.
//
// Passthrough is the exception and the reason this type marshals itself. A
// reasoning item is an endpoint's own payload — agi has no business knowing
// what is inside one, and with encrypted content there is nothing to know — so
// it is written back out exactly as it arrived rather than rebuilt from parts
// this file would have to understand.
type wireItem struct {
	// Passthrough is an item the endpoint produced, to be handed back
	// unchanged. When it is set every other field is ignored.
	Passthrough json.RawMessage `json:"-"`

	Type string `json:"type,omitempty"`
	// Role and Content are a message's, and travel without a Type: an item
	// with a role is a message.
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
	// CallID correlates a function_call with the function_call_output
	// answering it, and is the id agi knows a tool request by.
	CallID string `json:"call_id,omitempty"`
	Name   string `json:"name,omitempty"`
	// Arguments is a JSON object encoded as a string, per the protocol.
	Arguments string `json:"arguments,omitempty"`
	// Output is what a tool answered, as text.
	Output string `json:"output,omitempty"`
}

// MarshalJSON writes an item, or hands back the endpoint's own bytes when the
// item is one of its own.
func (i wireItem) MarshalJSON() ([]byte, error) {
	if len(i.Passthrough) > 0 {
		return i.Passthrough, nil
	}
	// A defined type without this method, so marshalling it does not recurse.
	type plain wireItem
	return json.Marshal(plain(i))
}

// wireTool is one tool offered to the model. The definition is flat here,
// where the chat completions schema nested it under a `function` key.
type wireTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	// Strict is always false and is always sent. This protocol defaults it to
	// true, where the one agi used to speak defaulted it to false, and strict
	// mode is a stricter contract than a tool's schema here is written to:
	// it requires every property to be required and additional ones to be
	// forbidden. Leaving the field out would therefore change what the
	// endpoint accepts, rather than leaving it as it was.
	Strict bool `json:"strict"`
}

// wireResponse is a reply. Its output is kept as raw items rather than decoded
// in place, because one kind of item has to survive back onto the next request
// byte for byte and the rest have to be read; decodeOutput does both.
type wireResponse struct {
	Output []json.RawMessage `json:"output"`
	Status string            `json:"status"`
	Error  *wireError        `json:"error"`
}

// wireOutputItem is one item of a reply, read for what agi needs from it.
type wireOutputItem struct {
	Type string `json:"type"`
	// Content is a message's text, and on an endpoint that returns its
	// deliberation in the clear, a reasoning item's as well.
	Content []wireTextPart `json:"content"`
	// Summary is what a reasoning item says about itself when the thinking
	// behind it is encrypted.
	Summary []wireTextPart `json:"summary"`
	// CallID, Name and Arguments belong to a function_call.
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// wireTextPart is one piece of text inside an item. The protocol gives each
// piece a type saying what it is — `output_text`, `summary_text`,
// `reasoning_text` — and they all carry the text under the same key, so what
// the type says is already known from the item the part was found in.
type wireTextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// wireModels is the catalogue an endpoint returns, which is a list under an
// envelope both a gateway and a provider spell the same way.
type wireModels struct {
	Data  []wireModel `json:"data"`
	Error *wireError  `json:"error"`
}

// wireModel is one entry of that catalogue.
//
// It reads two names for one number, and that is the whole of where the
// endpoints disagree: a LiteLLM gateway reports a model's context as
// `max_input_tokens`, and DeepSeek reports the same thing as `context_window`.
// Reading both here is what lets everything above this file see one shape —
// the alternative is a listing whose columns depend on which backend was
// configured, which is the thing transport.Model exists to prevent. A field
// the endpoint did not send decodes to zero, which is what that type reads as
// "it did not say".
type wireModel struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by"`
	// MaxInputTokens is a gateway's name for the context window.
	MaxInputTokens int `json:"max_input_tokens"`
	// ContextWindow is the provider's name for the same number.
	ContextWindow   int `json:"context_window"`
	MaxOutputTokens int `json:"max_output_tokens"`
}

type wireError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

func (e *wireError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("%s: %s", e.Type, e.Message)
	}
	return e.Message
}

// encodeRequest maps a transport request onto the wire schema.
//
// The origin is the endpoint this request is going to, which is also the model
// name it runs on. It travels down to encodeAssistant, where a turn is handed
// back only to the endpoint that produced it.
func encodeRequest(origin model.Origin, conv *model.Conversation, defs []model.ToolDefinition) wireRequest {
	input := make([]wireItem, 0, conv.Len())
	for _, m := range conv.Messages {
		input = append(input, encodeMessage(origin, m)...)
	}

	tools := make([]wireTool, 0, len(defs))
	for _, d := range defs {
		tools = append(tools, wireTool{
			Type:        "function",
			Name:        d.Name,
			Description: d.Description,
			Parameters:  d.Parameters,
		})
	}

	return wireRequest{
		Model:        origin.Model,
		Instructions: conv.System,
		Input:        input,
		Tools:        tools,
		Reasoning:    wireReasoning{Effort: reasoningEffort, Summary: reasoningSummary},
		Include:      []string{includeEncryptedReasoning},
		Store:        false,
	}
}

// encodeMessage maps one domain message onto the items it is made of. The
// switch is exhaustive over the closed set of message types.
//
// Every turn but an assistant's is a single item. An assistant's is as many as
// it did things, which is why this returns a list.
func encodeMessage(origin model.Origin, m model.Message) []wireItem {
	switch msg := m.(type) {
	case model.UserMessage:
		return []wireItem{{Role: string(msg.Role()), Content: msg.Content}}
	case model.HarnessInstruction:
		// The harness has no role of its own on the wire, so the instruction
		// travels as a user turn with delimiters marking where it came from.
		return []wireItem{{Role: string(msg.Role()), Content: msg.Render()}}
	case model.AssistantMessage:
		return encodeAssistant(origin, msg)
	case model.ToolAnswer:
		// Both kinds of answer render to the text the model reads, and a
		// message that kept its parts separate assembles them here, at the
		// last moment before they are sent.
		return []wireItem{{
			Type:   itemFunctionCallOutput,
			CallID: msg.AnsweredRequest(),
			Output: msg.Render(),
		}}
	}
	return nil
}

// encodeAssistant writes one assistant turn back out as the items it was made
// of, in the order the model produced them.
//
// A turn the endpoint itself handed over as items goes back as exactly those
// items and nothing is rebuilt: the reply is already the right answer to this
// question, and the one thing that can go wrong here is getting the order
// wrong. A reasoning item is read as belonging to whatever item follows it,
// and a reply interleaves them — a thought, a word to the user, another
// thought, the tool it settled on — so a turn reassembled from its parts, with
// the thinking gathered at the front, hands back a reasoning item whose
// neighbour is another reasoning item, and the endpoint refuses the request.
// Handing back the list as it arrived cannot get that wrong.
//
// Everything below that branch is the turn agi has to write itself: one built
// in the repository rather than returned by a model, one that reasoned about
// nothing and left no items to hand back, and one whose replay belongs to an
// endpoint this request is not going to. Those are written out from the parts
// — what it said, then what it asked for — and carry no deliberation at all.
//
// The words are not sent and there is nowhere to send them: in this protocol a
// turn's deliberation is its items and nothing else, so
// AssistantMessage.ReasoningContent — which is the same thinking in a form a
// person can read — is agi's to print and log and never travels. A turn that
// kept only the words therefore replays nothing; it costs that run that turn's
// thinking and nothing more.
func encodeAssistant(origin model.Origin, m model.AssistantMessage) []wireItem {
	if replayed := replayItems(m.Replay.OutputFor(origin)); len(replayed) > 0 {
		return replayed
	}

	items := make([]wireItem, 0, 1+len(m.ToolRequests))
	if m.Content != "" {
		items = append(items, wireItem{Role: string(m.Role()), Content: m.Content})
	}
	for _, r := range m.ToolRequests {
		args := string(r.Arguments)
		if args == "" {
			args = "{}"
		}
		items = append(items, wireItem{
			Type:      itemFunctionCall,
			CallID:    r.ID,
			Name:      r.Name,
			Arguments: args,
		})
	}
	return items
}

// replayItems splits a turn's stored replay back into the items it was made
// of, so each goes onto the wire where the endpoint expects to find it.
//
// What is stored is the list as the reply carried it, which is one value; what
// a request takes is that list spliced into the conversation, which is several.
// Nothing is looked at on the way past — an item is bytes the endpoint
// produced, for the endpoint to read — and a payload that is not a list of
// items at all is dropped rather than guessed at, which is what a replay saved
// by a client speaking some other protocol looks like from here.
func replayItems(raw json.RawMessage) []wireItem {
	if len(raw) == 0 {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	out := make([]wireItem, 0, len(items))
	for _, item := range items {
		out = append(out, wireItem{Passthrough: item})
	}
	return out
}

// decodeModel maps one listed model onto the domain type, settling the two
// names for the context window in favor of whichever the endpoint sent. An
// endpoint sends one or the other and never both, so there is no case here of
// two numbers disagreeing: the first one found is the answer.
func decodeModel(w wireModel) transport.Model {
	window := w.ContextWindow
	if window == 0 {
		window = w.MaxInputTokens
	}
	return transport.Model{
		ID:              strings.TrimSpace(w.ID),
		OwnedBy:         strings.TrimSpace(w.OwnedBy),
		ContextWindow:   window,
		MaxOutputTokens: w.MaxOutputTokens,
	}
}

// decodeOutput maps a reply's items back onto the one domain type they are
// between them: an assistant turn. A reply is always an assistant turn,
// whatever roles the items in it are labelled with.
//
// A reply that reasoned is both read and kept. Read item by item, for the
// words and the calls, which is what gets printed, logged and acted on; and
// kept whole, as the list it arrived as, which is what goes back on the next
// request. Neither does the other's job — the words are a summary the endpoint
// will not take back, what it will take back is encrypted, and the order the
// two came in is a third thing that only the list records — so a turn carries
// both. It is kept against the origin it came from, since that is the only
// endpoint that can read it back.
//
// An item of a kind this file has never heard of is ignored on the way in and
// kept all the same, since what is kept is the list rather than the items this
// switch recognized. A protocol gains item types, and a reply mentioning one
// is a reply agi can still read the rest of and still hand back whole.
func decodeOutput(origin model.Origin, output []json.RawMessage) model.AssistantMessage {
	var (
		said    []string
		thought []string
		thinker bool
		out     model.AssistantMessage
	)
	for _, raw := range output {
		var item wireOutputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		switch item.Type {
		case itemReasoning:
			thinker = true
			if text := partsText(item.Summary, item.Content); text != "" {
				thought = append(thought, text)
			}
		case itemMessage:
			if text := partsText(item.Content); text != "" {
				said = append(said, text)
			}
		case itemFunctionCall:
			args := json.RawMessage(item.Arguments)
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			out.ToolRequests = append(out.ToolRequests, model.ToolRequest{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: args,
			})
		}
	}

	out.Content = strings.Join(said, "\n\n")
	out.ReasoningContent = strings.Join(thought, "\n\n")
	// A reply with no reasoning in it leaves the turn with no replay, which is
	// what keeps an endpoint that has never heard of one from ever being shown
	// one: there is nothing to hand back, so the turn goes back as the message
	// and the calls agi assembled, exactly as it did before any of this. A
	// reply that did reason keeps all of it — every item, not the reasoning
	// alone — because what has to be preserved is the order they came in.
	if thinker {
		if items, err := json.Marshal(output); err == nil {
			out.Replay = model.NewTurnReplay(origin, items)
		}
	}
	return out
}

// partsText joins the text of every part that carried any. An item's text
// arrives in pieces — a summary in paragraphs, a message in one piece or
// several — and they are the one text the item was saying.
func partsText(groups ...[]wireTextPart) string {
	texts := make([]string, 0, len(groups))
	for _, parts := range groups {
		for _, p := range parts {
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
	}
	return strings.Join(texts, "\n\n")
}
