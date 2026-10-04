package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ChrisVilches/agi/transport/openai"
)

// answeringEndpoint is a gateway that answers every request with the same
// prose and no tool calls, which is the shortest run there is: one exchange,
// and the agent loop ends on the answer.
//
// It exists so that a whole run can be driven from the command line down,
// which is the only way to see what agi prints at both ends of one. Everything
// below runPrompt is reachable without a network; runPrompt itself is not,
// since it opens a backend before it does anything else.
func answeringEndpoint(t *testing.T, answer string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"`+answer+`"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// scriptedEndpoint is a gateway that hands back the replies it was given, one
// per request, and the last one over again if the run asks for more. A reply is
// the JSON body of a response, so a test writes tool calls as the wire spells
// them; callReply and answerReply write the two that are needed.
func scriptedEndpoint(t *testing.T, replies ...string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	next := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		reply := replies[min(next, len(replies)-1)]
		next++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// callReply is a reply that calls one tool, and answerReply one that ends the
// run with prose.
func callReply(id, tool, args string) string {
	encoded, _ := json.Marshal(args)
	return `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"` + id +
		`","type":"function","function":{"name":"` + tool + `","arguments":` + string(encoded) +
		`}}]},"finish_reason":"tool_calls"}]}`
}

func answerReply(answer string) string {
	return `{"choices":[{"message":{"role":"assistant","content":"` + answer + `"},"finish_reason":"stop"}]}`
}

// wholeRun runs agi against a gateway that answers at once, and returns
// everything it printed.
//
// --verbose is what makes the progress stream readable here. The buffer it
// writes into is not a terminal, so without it the stream is discarded and
// there would be nothing to look at; with it the stream goes to the error
// writer, which is the same buffer.
func wholeRun(t *testing.T, args ...string) string {
	t.Helper()
	return wholeRunAgainst(t, answeringEndpoint(t, "done"), args...)
}

// wholeRunAgainst is wholeRun against a gateway the test built itself, for a
// run that has to get further than one exchange.
func wholeRunAgainst(t *testing.T, srv *httptest.Server, args ...string) string {
	t.Helper()

	t.Setenv(EnvBackend, openai.Name)
	t.Setenv(openai.EnvURL, srv.URL)
	// A session agi names for itself is made under TMPDIR, so pointing it at
	// the test's own directory keeps a run that keeps everything from leaving
	// anything behind.
	t.Setenv("TMPDIR", t.TempDir())

	var out bytes.Buffer
	app := NewApp("test")
	app.Writer = &out
	app.ErrWriter = &out
	app.Reader = strings.NewReader("")

	if err := app.Run(context.Background(), args); err != nil {
		t.Fatalf("%v = %v, want nil", args, err)
	}
	return out.String()
}

// The directory a run is kept in is said twice: once before the work, for
// somebody who wants to look inside it while the run is going, and once after,
// for somebody who has read the answer and wants to ask the next question.
func TestSessionIsSaidAtBothEnds(t *testing.T) {
	dir := t.TempDir()
	printed := wholeRun(t, "agi", "--verbose", "--session", dir, "hello")

	want := "--session " + dir
	if n := strings.Count(printed, want); n != 2 {
		t.Errorf("%q appears %d time(s), want 2:\n%s", want, n, printed)
	}
}

// It is said as the flag that names it, because the line is there to be pasted
// in front of the next question.
func TestSessionIsSaidAsTheFlag(t *testing.T) {
	if line := sessionLine("/tmp/agi-1b2"); !strings.Contains(line, "--session /tmp/agi-1b2") {
		t.Errorf("sessionLine() = %q, want the flag and the directory", line)
	}
}

// A --rm run says it once, at the top. The directory is removed as the run
// returns, so a line at the bottom would name a path that was gone by the time
// it was read; while the run is going it is there like any other, which is
// what somebody wanting to look inside it needs.
func TestDiscardedSessionIsSaidOnlyAtTheTop(t *testing.T) {
	printed := wholeRun(t, "agi", "--verbose", "--rm", "hello")

	if n := strings.Count(printed, "--session "); n != 1 {
		t.Errorf("--session appears %d time(s) with --rm, want 1:\n%s", n, printed)
	}
}
