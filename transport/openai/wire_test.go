package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
)

// here is the endpoint these tests encode for and decode against. A replay is
// recorded against the backend and model that produced it and handed back to
// nothing else, so a test that encodes a turn has to name where it is going.
var here = model.Origin{Backend: "openai", Model: "m"}

// The system prompt is a field of the request in this protocol rather than the
// first turn of the conversation, so nothing is prepended to the input.
func TestEncodeRequestPutsTheSystemPromptInInstructions(t *testing.T) {
	got := encodeRequest(model.Origin{Backend: "openai", Model: "deepseek-chat"}, model.NewConversation("be helpful", "hi"), nil)

	if got.Instructions != "be helpful" {
		t.Errorf("instructions = %q, want the system prompt", got.Instructions)
	}
	if len(got.Input) != 1 || got.Input[0].Role != "user" {
		t.Errorf("input = %+v, want only the user turn", got.Input)
	}
	if got.Model != "deepseek-chat" {
		t.Errorf("model = %q, want %q", got.Model, "deepseek-chat")
	}
}

func TestEncodeRequestOmitsEmptySystem(t *testing.T) {
	got := encodeRequest(here, model.NewConversation("", "hi"), nil)
	if got.Instructions != "" {
		t.Errorf("instructions = %q, want none", got.Instructions)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if strings.Contains(string(encoded), "instructions") {
		t.Errorf("encoded = %s, want no instructions key at all", encoded)
	}
}

// Every request asks for the same effort and keeps nothing on the endpoint's
// side, and nothing about a run can change either. The effort being hardcoded
// is a known limitation, stated in wire.go where it is set.
func TestEveryRequestAsksForTheSameEffortAndStoresNothing(t *testing.T) {
	got := encodeRequest(here, model.NewConversation("", "hi"), nil)

	if got.Reasoning.Effort != "medium" {
		t.Errorf("effort = %q, want medium", got.Reasoning.Effort)
	}
	if got.Reasoning.Summary != "auto" {
		t.Errorf("summary = %q, want the deliberation asked for in words", got.Reasoning.Summary)
	}
	if got.Store {
		t.Error("store = true, want a run that leaves nothing on the endpoint")
	}
	// Storing nothing is what makes the encrypted reasoning necessary: there
	// is no server-side state for a later request to refer back to.
	if len(got.Include) != 1 || got.Include[0] != includeEncryptedReasoning {
		t.Errorf("include = %v, want the encrypted reasoning asked for", got.Include)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if !strings.Contains(string(encoded), `"store":false`) {
		t.Errorf("encoded = %s, want store sent explicitly, since the default is to store", encoded)
	}
}

// A tool definition is flat in this protocol, where the one before nested it
// under a `function` key.
func TestEncodeToolDefinitions(t *testing.T) {
	defs := []model.ToolDefinition{{
		Name:        "bash",
		Description: "Run a command",
		Parameters:  map[string]any{"type": "object"},
	}}
	got := encodeRequest(here, &model.Conversation{}, defs)

	if len(got.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(got.Tools))
	}
	if got.Tools[0].Type != "function" {
		t.Errorf("tool type = %q, want %q", got.Tools[0].Type, "function")
	}
	if got.Tools[0].Name != "bash" {
		t.Errorf("tool name = %q, want %q", got.Tools[0].Name, "bash")
	}
}

// Strict mode is a stricter contract than agi's tool schemas are written to,
// and this protocol turns it on unless told otherwise, so it is always told.
func TestEncodeTurnsStrictModeOffExplicitly(t *testing.T) {
	defs := []model.ToolDefinition{{Name: "bash", Parameters: map[string]any{"type": "object"}}}
	encoded, err := json.Marshal(encodeRequest(here, &model.Conversation{}, defs))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if !strings.Contains(string(encoded), `"strict":false`) {
		t.Errorf("encoded = %s, want strict sent as false rather than left out", encoded)
	}
}

// Arguments travel as a JSON-encoded string, not as an object.
func TestEncodeToolCallArgumentsAreAString(t *testing.T) {
	m := model.AssistantMessage{ToolRequests: []model.ToolRequest{
		{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
	}}
	encoded, err := json.Marshal(encodeMessage(here, m))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if !strings.Contains(string(encoded), `"arguments":"{\"command\":\"ls\"}"`) {
		t.Errorf("encoded = %s, want arguments as an escaped string", encoded)
	}
}

func TestEncodeToolCallWithoutArgumentsSendsEmptyObject(t *testing.T) {
	m := model.AssistantMessage{ToolRequests: []model.ToolRequest{{ID: "c1", Name: "x"}}}
	items := encodeMessage(here, m)
	if len(items) != 1 {
		t.Fatalf("items = %+v, want only the call", items)
	}
	if items[0].Arguments != "{}" {
		t.Errorf("arguments = %q, want %q", items[0].Arguments, "{}")
	}
}

func TestDecodeOutputRoundTripsToolCalls(t *testing.T) {
	got := decodeOutput(here, rawItems(t,
		`{"type":"function_call","id":"fc_1","call_id":"call-7","name":"bash",`+
			`"arguments":`+quote(`{"command":"df -h"}`)+`}`))

	if got.Role() != model.RoleAssistant {
		t.Errorf("role = %q, want assistant", got.Role())
	}
	if len(got.ToolRequests) != 1 {
		t.Fatalf("tool requests = %d, want 1", len(got.ToolRequests))
	}
	// The id agi knows a call by is the one a result answers, which is
	// `call_id` and not the item's own `id`.
	if got.ToolRequests[0].ID != "call-7" || got.ToolRequests[0].Name != "bash" {
		t.Errorf("tool request = %+v, want id call-7 and name bash", got.ToolRequests[0])
	}
	if string(got.ToolRequests[0].Arguments) != `{"command":"df -h"}` {
		t.Errorf("arguments = %s, want the raw JSON object", got.ToolRequests[0].Arguments)
	}
}

// A reply is an assistant turn whatever the items in it are labelled.
func TestDecodeOutputIsAlwaysAssistant(t *testing.T) {
	got := decodeOutput(here, rawItems(t, messageItem("hello")))
	if got.Role() != model.RoleAssistant {
		t.Errorf("role = %q, want assistant", got.Role())
	}
	if got.Content != "hello" {
		t.Errorf("content = %q, want the text of the message item", got.Content)
	}
}

// A protocol gains item types, and a reply mentioning one agi has never heard
// of is a reply agi can still read the rest of.
func TestDecodeOutputIgnoresItemsItDoesNotKnow(t *testing.T) {
	got := decodeOutput(here, rawItems(t,
		`{"type":"web_search_call","id":"ws_1","status":"completed"}`,
		messageItem("done")))

	if got.Content != "done" {
		t.Errorf("content = %q, want the message read past the unknown item", got.Content)
	}
}

// Every message type must survive the trip to the wire schema, and an
// assistant turn becomes as many items as it did things.
func TestEncodeMessageCoversEveryType(t *testing.T) {
	conv := model.NewConversation("sys", "do it")
	conv.AddAssistant(model.AssistantMessage{
		Content:      "checking",
		ToolRequests: []model.ToolRequest{{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{}`)}},
	})
	conv.AddToolResult("c1", "output")

	got := encodeRequest(here, conv, nil)
	if len(got.Input) != 4 {
		t.Fatalf("input = %d items, want the user turn, the reply, its call and the answer", len(got.Input))
	}
	if got.Input[0].Role != "user" || got.Input[0].Content != "do it" {
		t.Errorf("first item = %+v, want the user turn", got.Input[0])
	}
	if got.Input[1].Role != "assistant" || got.Input[1].Content != "checking" {
		t.Errorf("second item = %+v, want what the model said", got.Input[1])
	}
	if got.Input[2].Type != itemFunctionCall || got.Input[2].CallID != "c1" {
		t.Errorf("third item = %+v, want the call it asked for", got.Input[2])
	}
	if got.Input[3].Type != itemFunctionCallOutput || got.Input[3].CallID != "c1" {
		t.Errorf("fourth item = %+v, want the answer to that call", got.Input[3])
	}
	if got.Input[3].Output != "output" {
		t.Errorf("answer = %q, want what the tool produced", got.Input[3].Output)
	}
}

// A turn that only asked for tools says nothing, and an empty message item
// would be the model saying nothing out loud rather than saying nothing.
func TestEncodeOmitsTheMessageWhenTheTurnOnlyAskedForTools(t *testing.T) {
	items := encodeMessage(here, model.AssistantMessage{
		ToolRequests: []model.ToolRequest{{ID: "c1", Name: "bash"}},
	})
	if len(items) != 1 || items[0].Type != itemFunctionCall {
		t.Errorf("items = %+v, want only the call", items)
	}
}

// A harness instruction has no role of its own on the wire: it travels as a
// user turn, wrapped so the model can see it came from the program.
func TestEncodeHarnessInstruction(t *testing.T) {
	items := encodeMessage(here, model.NewHarnessInstruction("answer in JSON"))
	if len(items) != 1 {
		t.Fatalf("items = %+v, want one", items)
	}
	w := items[0]

	if w.Role != string(model.RoleUser) {
		t.Errorf("role = %q, want %q", w.Role, model.RoleUser)
	}
	const want = "[Instructions] Follow these instructions: answer in JSON"
	if w.Content != want {
		t.Errorf("content = %q, want %q", w.Content, want)
	}
	if w.Type != "" || w.CallID != "" {
		t.Errorf("item = %+v, want a plain message with no call fields", w)
	}
}

// Subagent results reach the endpoint as one answer: the parts are assembled
// at the last moment, not stored assembled.
func TestEncodeSubagentResults(t *testing.T) {
	items := encodeMessage(here, model.NewSubagentResults("call-1", []model.SubagentResult{
		{Title: "First", Answer: "one"},
		{Title: "Second", Answer: "two"},
	}))
	if len(items) != 1 {
		t.Fatalf("items = %+v, want one", items)
	}
	w := items[0]

	if w.Type != itemFunctionCallOutput {
		t.Errorf("type = %q, want %q", w.Type, itemFunctionCallOutput)
	}
	if w.CallID != "call-1" {
		t.Errorf("call_id = %q, want it to answer call-1", w.CallID)
	}
	const want = "=== subagent result #1 of 2: First ===\none\n\n" +
		"=== subagent result #2 of 2: Second ===\ntwo"
	if w.Output != want {
		t.Errorf("output = %q, want %q", w.Output, want)
	}
}

// A reasoning model's deliberation reaches agi as the summary of a reasoning
// item, which is the only part of it a person can read when the thinking
// itself is encrypted.
func TestDecodeOutputReadsReasoningFromTheSummary(t *testing.T) {
	got := decodeOutput(here, rawItems(t,
		`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"comparing the decimal parts"}],`+
			`"encrypted_content":"gAAAAAB..."}`,
		messageItem("9.11 is greater")))

	if got.ReasoningContent != "comparing the decimal parts" {
		t.Errorf("reasoning = %q, want the deliberation the endpoint returned", got.ReasoningContent)
	}
	if got.Content != "9.11 is greater" {
		t.Errorf("content = %q, want the answer alongside it", got.Content)
	}
}

// An endpoint that returns its deliberation in the clear puts it in the item's
// content instead, and it is the same thing read the same way.
func TestDecodeOutputReadsReasoningReturnedInTheClear(t *testing.T) {
	got := decodeOutput(here, rawItems(t,
		`{"type":"reasoning","id":"rs_1","summary":[],`+
			`"content":[{"type":"reasoning_text","text":"the tool answered, so I can tell them"}]}`,
		messageItem("it is cloudy")))

	if got.ReasoningContent != "the tool answered, so I can tell them" {
		t.Errorf("reasoning = %q, want what the endpoint returned in the clear", got.ReasoningContent)
	}
}

// Several reasoning items are one turn's thinking, and so are several
// paragraphs of summary within one item.
func TestDecodeOutputJoinsEveryPieceOfThinking(t *testing.T) {
	got := decodeOutput(here, rawItems(t,
		`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"first"},`+
			`{"type":"summary_text","text":"second"}]}`,
		`{"type":"reasoning","id":"rs_2","summary":[{"type":"summary_text","text":"third"}]}`,
		messageItem("done")))

	if got.ReasoningContent != "first\n\nsecond\n\nthird" {
		t.Errorf("reasoning = %q, want every piece in the order it arrived", got.ReasoningContent)
	}
}

// What is read for the words and what is handed back are not the same thing:
// the words are a summary the endpoint will not take back, and what it will
// take back is encrypted. So the reply is kept exactly as it arrived — all of
// it, because the order the items came in is the third thing neither half of
// the turn records.
func TestDecodeOutputKeepsTheWholeReplyItWasSent(t *testing.T) {
	got := decodeOutput(here, rawItems(t,
		`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"a long think"}],"encrypted_content":"gAAAAAB+/x=="}`,
		messageItem("done")))

	var items []map[string]any
	if err := json.Unmarshal(got.Replay.OutputItems, &items); err != nil {
		t.Fatalf("output items = %s, want a list: %v", got.Replay.OutputItems, err)
	}
	if len(items) != 2 {
		t.Fatalf("output items = %d, want every item the reply carried", len(items))
	}
	if items[0]["encrypted_content"] != "gAAAAAB+/x==" {
		t.Errorf("item = %v, want the endpoint's own payload kept whole", items[0])
	}
	if items[1]["type"] != itemMessage {
		t.Errorf("item = %v, want the message kept beside the thinking", items[1])
	}
	if got.Replay.Origin != here {
		t.Errorf("origin = %+v, want the endpoint that produced it", got.Replay.Origin)
	}
	// The shapes agi no longer writes stay unwritten.
	if len(got.Replay.ThinkingBlocks) != 0 || len(got.Replay.ReasoningItems) != 0 {
		t.Errorf("replay = %+v, want only the reply's own output", got.Replay)
	}
}

// A reply that reasoned about nothing hands nothing back, which is what keeps
// an endpoint that has never heard of a reasoning item from ever being shown
// one: its turns go back as the message and the calls agi assembled.
func TestDecodeOutputHasNothingToReplayWhenNothingWasReasoned(t *testing.T) {
	got := decodeOutput(here, rawItems(t, messageItem("done")))

	if !got.Replay.Empty() {
		t.Errorf("replay = %+v, want nothing to hand back", got.Replay)
	}
	if got.ReasoningContent != "" {
		t.Errorf("reasoning = %q, want none", got.ReasoningContent)
	}
}

// The round trip is the whole feature. A turn goes back out as the items it
// came in as, in the order the model produced them: a reasoning item belongs
// to whatever follows it, so thinking that trailed its turn would be thinking
// attached to the wrong one.
func TestEncodeHandsTheReplyBackAsItArrived(t *testing.T) {
	const items = `[{"type":"reasoning","id":"rs_1","encrypted_content":"gAAAAAB+/x=="},` +
		`{"type":"message","id":"msg_1","role":"assistant",` +
		`"content":[{"type":"output_text","text":"checking"}]},` +
		`{"type":"function_call","id":"fc_1","call_id":"c1","name":"get_date","arguments":"{}"}]`
	encodedItems := encodeMessage(here, model.AssistantMessage{
		Content:      "checking",
		Replay:       model.NewTurnReplay(here, json.RawMessage(items)),
		ToolRequests: []model.ToolRequest{{ID: "c1", Name: "get_date"}},
	})

	if len(encodedItems) != 3 {
		t.Fatalf("items = %d, want the thinking, the words and the call", len(encodedItems))
	}
	for i, item := range encodedItems {
		if len(item.Passthrough) == 0 {
			t.Errorf("item %d = %+v, want the endpoint's own bytes", i, item)
		}
	}

	// Nothing is rebuilt, so nothing is rebuilt wrongly: the message keeps the
	// id and the output_text parts it came with, and the payload is intact.
	encoded, err := json.Marshal(encodedItems)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	for _, want := range []string{
		`"encrypted_content":"gAAAAAB+/x=="`,
		`"id":"msg_1"`,
		`"type":"output_text"`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("encoded = %s, want %s kept", encoded, want)
		}
	}
}

// The order is the point, and it is the one thing a turn rebuilt from its
// parts cannot get right. A model thinks, says something, thinks again and
// then calls a tool; gathering the thinking at the front would leave rs_1
// followed by rs_2, which is a reasoning item without the item it belongs to
// and a request the endpoint refuses.
func TestEncodeKeepsThinkingNextToWhatItBelongsTo(t *testing.T) {
	got := decodeOutput(here, rawItems(t,
		`{"type":"reasoning","id":"rs_1"}`,
		messageItem("let me look that up"),
		`{"type":"reasoning","id":"rs_2"}`,
		`{"type":"function_call","id":"fc_1","call_id":"c1","name":"get_date","arguments":"{}"}`))

	encoded, err := json.Marshal(encodeMessage(here, got))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(encoded, &items); err != nil {
		t.Fatalf("encoded = %s, want a list: %v", encoded, err)
	}

	want := []string{itemReasoning, itemMessage, itemReasoning, itemFunctionCall}
	if len(items) != len(want) {
		t.Fatalf("items = %s, want %d in the order the model produced them", encoded, len(want))
	}
	for i, kind := range want {
		if items[i]["type"] != kind {
			t.Fatalf("items = %s, want %v", encoded, want)
		}
	}
	if items[0]["id"] != "rs_1" || items[2]["id"] != "rs_2" {
		t.Errorf("items = %s, want each thought beside the item it belongs to", encoded)
	}
}

// Every call gets its own thinking, and every one of them has to stay in front
// of its own call rather than in front of the first.
func TestEncodeKeepsOneThoughtPerCallWhereItWas(t *testing.T) {
	got := decodeOutput(here, rawItems(t,
		`{"type":"reasoning","id":"rs_1"}`,
		`{"type":"function_call","id":"fc_1","call_id":"c1","name":"get_date","arguments":"{}"}`,
		`{"type":"reasoning","id":"rs_2"}`,
		`{"type":"function_call","id":"fc_2","call_id":"c2","name":"bash","arguments":"{}"}`))

	encoded, err := json.Marshal(encodeMessage(here, got))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(encoded, &items); err != nil {
		t.Fatalf("encoded = %s, want a list: %v", encoded, err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %s, want four", encoded)
	}
	if items[0]["id"] != "rs_1" || items[1]["call_id"] != "c1" ||
		items[2]["id"] != "rs_2" || items[3]["call_id"] != "c2" {
		t.Errorf("items = %s, want each thought in front of its own call", encoded)
	}
}

// A replay belongs to the endpoint that wrote it and to no other: an item
// encrypted to one model is unreadable to the next, and a request carrying one
// is refused rather than ignored. So a session resumed somewhere else sends
// the turn without it.
func TestEncodeSendsNoReplayToAnotherEndpoint(t *testing.T) {
	const items = `[{"type":"reasoning","id":"rs_1"},` +
		`{"type":"message","id":"msg_1","role":"assistant",` +
		`"content":[{"type":"output_text","text":"done"}]}]`
	for _, elsewhere := range []model.Origin{
		{Backend: "openai", Model: "another-model"},
		{Backend: "litellm", Model: "m"},
		{},
	} {
		got := encodeMessage(here, model.AssistantMessage{
			Content: "done",
			Replay:  model.NewTurnReplay(elsewhere, json.RawMessage(items)),
		})
		if len(got) != 1 || got[0].Role != "assistant" || got[0].Content != "done" {
			t.Errorf("items for %+v = %+v, want the turn itself and nothing else", elsewhere, got)
		}
	}
}

// A replay saved by a client speaking some other protocol is not a list of
// items and cannot be handed back as one. It is dropped rather than guessed
// at: the turn still travels, having lost only thinking this endpoint could
// not have read anyway.
func TestEncodeDropsAReplayItCannotSplit(t *testing.T) {
	got := encodeMessage(here, model.AssistantMessage{
		Content: "done",
		Replay:  model.NewTurnReplay(here, json.RawMessage(`{"not":"a list"}`)),
	})
	if len(got) != 1 || got[0].Role != "assistant" {
		t.Errorf("items = %+v, want the turn itself and nothing else", got)
	}
}

// The shapes agi wrote before it kept whole replies were written without an
// origin beside them, so there is no endpoint they can be said to belong to
// and no request that may carry them. A session that holds one still resumes;
// it resumes without that turn's thinking.
func TestEncodeSendsNoReplayItCannotNameAnEndpointFor(t *testing.T) {
	stored := model.NewStoredReplay(model.Origin{},
		nil,
		json.RawMessage(`[{"type":"reasoning","id":"rs_1"}]`),
		json.RawMessage(`[{"type":"thinking","signature":"sig/abc+123=="}]`))

	encoded, err := json.Marshal(encodeMessage(here, model.AssistantMessage{
		Content: "done",
		Replay:  stored,
	}))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if strings.Contains(string(encoded), "rs_1") || strings.Contains(string(encoded), "sig/abc") {
		t.Errorf("encoded = %s, want a replay of unknown parentage kept off the wire", encoded)
	}
}

// Deliberation belongs to an assistant turn and to no other kind, so nothing
// else carries any.
func TestEncodeNeverPutsAReplayOnAnotherKindOfTurn(t *testing.T) {
	for _, m := range []model.Message{
		model.UserMessage{Content: "hi"},
		model.NewHarnessInstruction("answer in JSON"),
		model.NewToolResult("c1", "output"),
	} {
		encoded, err := json.Marshal(encodeMessage(here, m))
		if err != nil {
			t.Fatalf("marshalling: %v", err)
		}
		if strings.Contains(string(encoded), itemReasoning) {
			t.Errorf("encoded %T = %s, want no reasoning", m, encoded)
		}
	}
}

// The words are agi's to print and log, and in this protocol there is nowhere
// to send them: a turn's deliberation is the reasoning items and nothing else.
// A turn that kept only the words — one restored from a session written before
// this package spoke this protocol — therefore replays nothing.
func TestEncodeSendsNoReasoningWhenAllThatSurvivedWereTheWords(t *testing.T) {
	encoded, err := json.Marshal(encodeMessage(here, model.AssistantMessage{
		Content:          "done",
		ReasoningContent: "a long think",
	}))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if strings.Contains(string(encoded), "a long think") {
		t.Errorf("encoded = %s, want the words kept off the wire", encoded)
	}
}

// rawItems renders a reply's output list the way the endpoint sends it.
func rawItems(t *testing.T, items ...string) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		if !json.Valid([]byte(item)) {
			t.Fatalf("test item is not valid JSON: %s", item)
		}
		out = append(out, json.RawMessage(item))
	}
	return out
}

// messageItem is what the model said, as the endpoint writes it.
func messageItem(text string) string {
	return `{"type":"message","id":"msg_1","role":"assistant","status":"completed",` +
		`"content":[{"type":"output_text","text":` + quote(text) + `,"annotations":[]}]}`
}

func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
