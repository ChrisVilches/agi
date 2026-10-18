package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/session"
)

// The phases after the loop are scaffolding, and what a session stores is
// what was said: whichever phases ran, however many attempts each took, and
// whichever way the run ended, the snapshot written to disk holds the prompt
// and the prose answer and not one harness instruction. Each case is read back
// from the file, since that is what the next run resumes from.
func TestTheSnapshotHoldsNoScaffoldingAfterThePhases(t *testing.T) {
	const (
		answer   = "User 1 is Ada."
		document = `{"id": 1, "name": "Ada"}`
		// A document of the wrong shape, which costs the schema phase a
		// correction.
		misshapen = `{"id": "one", "name": "Ada"}`
		// A verdict that cannot be read, which costs the --exit-code phase a
		// correction.
		unreadable = `{"success": "no"}`
		success    = `{"success": true}`
		failure    = `{"success": false, "reason": "The file was not there."}`
	)
	isNil := func(err error) bool { return err == nil }
	isIncomplete := func(err error) bool {
		var short *incomplete
		return errors.As(err, &short)
	}
	isInterrupted := func(err error) bool { return errors.Is(err, ErrInterrupted) }

	tests := []struct {
		name    string
		schema  bool
		check   bool
		replies []string
		// stopAt is the request the run is stopped at, counting from 1; zero
		// lets every request through.
		stopAt int
		want   func(error) bool
	}{
		{name: "neither", replies: []string{answer}, want: isNil},
		{name: "schema", schema: true, replies: []string{answer, document}, want: isNil},
		{name: "exit-code", check: true, replies: []string{answer, success}, want: isNil},
		{name: "both", schema: true, check: true, replies: []string{answer, document, success}, want: isNil},
		{
			name: "both, with a correction in each phase", schema: true, check: true,
			replies: []string{answer, misshapen, document, unreadable, success},
			want:    isNil,
		},
		{
			name: "both, task reported incomplete", schema: true, check: true,
			replies: []string{answer, document, failure},
			want:    isIncomplete,
		},
		{
			name: "both, task reported incomplete after corrections", schema: true, check: true,
			replies: []string{answer, misshapen, document, unreadable, failure},
			want:    isIncomplete,
		},
		{
			name: "both, interrupted during the schema phase", schema: true, check: true,
			replies: []string{answer, misshapen}, stopAt: 3,
			want: isInterrupted,
		},
		{
			name: "both, interrupted during the question", schema: true, check: true,
			replies: []string{answer, misshapen, document, unreadable}, stopAt: 5,
			want: isInterrupted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sess, err := session.Open(sessionDir(t, "chat"))
			if err != nil {
				t.Fatalf("Open() = %v", err)
			}
			msgs := make([]model.AssistantMessage, 0, len(tt.replies))
			for _, r := range tt.replies {
				msgs = append(msgs, model.NewAssistantMessage(r))
			}
			client := &stoppingClient{replies: msgs, at: tt.stopAt, stop: cancel}

			var schema *model.Schema
			if tt.schema {
				schema = testSchema(t)
			}
			var out bytes.Buffer
			err = runSession(ctx, pipedOutput(&out), nolog(), client, registryWith(t), sess, prompt.Base(),
				"who is user 1?", schema, nil, outcomeCheck{asked: tt.check}, defaultMaxTurns, hooks.None())
			if !tt.want(err) {
				t.Fatalf("runSession() = %v", err)
			}
			if client.sent != len(tt.replies)+min(tt.stopAt, 1) {
				t.Fatalf("requests sent = %d, want every scripted reply used", client.sent)
			}

			stored, err := sess.Load()
			if err != nil {
				t.Fatalf("Load() = %v", err)
			}
			assertUntouched(t, stored, answer)
		})
	}
}

// A session that ran both phases is resumed from the record alone: the next
// run's first request carries the earlier prompt and answer and the new
// prompt, and nothing of how the earlier run was reformatted or graded — the
// conversation is the one it would have been had neither flag been given.
func TestAResumedSessionCarriesNoScaffoldingFromTheRunBefore(t *testing.T) {
	sess, err := session.Open(sessionDir(t, "chat"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	first := &scriptedClient{replies: replies(
		"User 1 is Ada.",
		`{"id": "one", "name": "Ada"}`,
		`{"id": 1, "name": "Ada"}`,
		`{"success": "no"}`,
		`{"success": true}`,
	)}
	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), nolog(), first, registryWith(t), sess, prompt.Base(),
		"who is user 1?", testSchema(t), nil, outcomeCheck{asked: true}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("first runSession() = %v", err)
	}

	second := &scriptedClient{replies: replies("User 2 is Grace.")}
	out.Reset()
	if err := runSession(context.Background(), pipedOutput(&out), nolog(), second, registryWith(t), sess, prompt.Base(),
		"and user 2?", nil, nil, outcomeCheck{}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("second runSession() = %v", err)
	}

	sent := second.sent[0].Conversation.Messages
	if len(sent) != 3 {
		t.Fatalf("resumed request carries %d turns, want the earlier prompt and answer and the new prompt: %+v", len(sent), sent)
	}
	for i, m := range sent {
		if m.Truncatable() {
			t.Errorf("turn %d of the resumed request is a %T, want nothing left of the earlier run's phases", i, m)
		}
	}
	if got, ok := sent[1].(model.AssistantMessage); !ok || got.Content != "User 1 is Ada." {
		t.Errorf("turn 1 = %+v, want the earlier prose answer", sent[1])
	}
}
