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

// --show-session=false is the run that says it at neither end. It is for
// whoever already holds the directory — agi-repl, which made the session and
// printed it before the first prompt — and for them the two lines are a path
// they were given, repeated for every question they ask.
func TestSessionIsNotSaidWhenNotAsked(t *testing.T) {
	dir := t.TempDir()
	printed := wholeRun(t, "agi", "--verbose", "--show-session=false", "--session", dir, "hello")

	if strings.Contains(printed, "--session ") {
		t.Errorf("the run said where it was kept after being told not to:\n%s", printed)
	}
}

// The answer is not the progress stream and is never what this flag is about.
// A run told to say nothing about its session still answers, on standard
// output, whatever it is attached to.
func TestTheAnswerSurvivesTheSessionLineGoing(t *testing.T) {
	printed := wholeRun(t, "agi", "--show-session=false", "--session", t.TempDir(), "hello")

	if !strings.Contains(printed, "done") {
		t.Errorf("the answer did not reach the output:\n%s", printed)
	}
}

// It is one line's switch and not a volume knob. Everything a run says about
// the work — the calls it made, the reasons it gave for them — is said by a
// run that was told not to say where it is kept, because that is the whole of
// what somebody watching one is watching for.
func TestOnlyTheSessionLineGoes(t *testing.T) {
	srv := scriptedEndpoint(t,
		callReply("1", "mktemp", `{"purpose": "somewhere to put what the run collects"}`),
		answerReply("done"),
	)
	printed := wholeRunAgainst(t, srv, "agi", "--verbose", "--show-session=false",
		"--session", t.TempDir(), "hello")

	if !strings.Contains(printed, "somewhere to put what the run collects") {
		t.Errorf("the call the run made was not announced:\n%s", printed)
	}
}

// The flag is on unless it is turned off: a one-shot run has no other way back
// to the conversation it just had, so the default is the line being there.
func TestTheSessionIsSaidByDefault(t *testing.T) {
	dir := t.TempDir()
	printed := wholeRun(t, "agi", "--verbose", "--session", dir, "hello")

	if !strings.Contains(printed, "--session "+dir) {
		t.Errorf("a run given no flag about it did not say where it was kept:\n%s", printed)
	}
}

// The one flag agi has that is on until it is turned off has to say so where
// it is read. urfave prints a default only for a flag that takes a value, so
// without DefaultText the help lists --show-session like every other boolean
// in it, all of which are off, and a user would type it to get what they
// already had.
func TestHelpSaysTheSessionLineIsOnAlready(t *testing.T) {
	out, err := run(t, "agi", "--help")
	if err != nil {
		t.Fatalf("agi --help: unexpected error: %v", err)
	}
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "--show-session ") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("agi --help does not mention --show-session: %q", out)
	}
	if !strings.Contains(line, "(default: true)") {
		t.Errorf("--show-session is listed as %q, want it to say it is on already", line)
	}
	if !strings.Contains(line, "--show-session=false") {
		t.Errorf("--show-session is listed as %q, want the spelling that turns it off", line)
	}
}

// Given twice, the last one wins, which is what lets agi-repl pass
// --show-session=false of its own and still be overruled by a user who wrote
// the flag themselves.
func TestTheLastShowSessionWins(t *testing.T) {
	dir := t.TempDir()
	printed := wholeRun(t, "agi", "--verbose", "--show-session=false",
		"--show-session=true", "--session", dir, "hello")

	if !strings.Contains(printed, "--session "+dir) {
		t.Errorf("the second --show-session did not overrule the first:\n%s", printed)
	}
}
