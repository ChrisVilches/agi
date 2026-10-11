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
		_, _ = io.WriteString(w, answerReply(answer))
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
	return outputReply(`{"type":"function_call","id":"fc_1","call_id":"` + id +
		`","name":"` + tool + `","arguments":` + string(encoded) + `}`)
}

func answerReply(answer string) string {
	encoded, _ := json.Marshal(answer)
	return outputReply(`{"type":"message","id":"msg_1","role":"assistant","status":"completed",` +
		`"content":[{"type":"output_text","text":` + string(encoded) + `}]}`)
}

// outputReply wraps the items a reply is made of in the envelope the endpoint
// returns them in.
func outputReply(items ...string) string {
	return `{"id":"resp_1","object":"response","status":"completed","output":[` +
		strings.Join(items, ",") + `]}`
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
	// Every run needs a model named, and the fake endpoint answers whatever it
	// is asked for.
	t.Setenv(EnvDefaultModel, "test-model")
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
//
// The run is given no --session, which is what makes it the run this line is
// for: agi picked the directory, so nothing the user typed says where it is.
func TestSessionIsSaidAtBothEnds(t *testing.T) {
	printed := wholeRun(t, "agi", "--verbose", "hello")

	if n := strings.Count(printed, "--session "); n != 2 {
		t.Errorf("--session appears %d time(s), want 2:\n%s", n, printed)
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

// A run given --session says it at neither end. What would be printed is the
// value of the flag, kept as it was typed, so the line would be a string the
// user wrote read back to them from below where they wrote it.
func TestAGivenSessionIsNotSaid(t *testing.T) {
	dir := t.TempDir()
	printed := wholeRun(t, "agi", "--verbose", "--session", dir, "hello")

	if strings.Contains(printed, "--session ") {
		t.Errorf("a run told where to go said it anyway:\n%s", printed)
	}
}

// The rule is about the flags and not about the directory. A directory agi
// made for an earlier run is one the user holds the moment they type it, and
// nothing about the directory itself could tell the two runs apart.
func TestASessionAgiMadeIsNotSaidOnceItIsGiven(t *testing.T) {
	dir := t.TempDir()
	if first := wholeRun(t, "agi", "--verbose", "hello"); !strings.Contains(first, "--session ") {
		t.Fatalf("a run agi chose the directory for did not say so:\n%s", first)
	}
	printed := wholeRun(t, "agi", "--verbose", "--session", dir, "and again")

	if strings.Contains(printed, "--session ") {
		t.Errorf("the same directory was said once it was the user's to name:\n%s", printed)
	}
}

// The rule governs that one line and nothing else. Everything a run says about
// the work — the calls it made, the reasons it gave for them — is said by a
// run that was told where to go, because that is the whole of what somebody
// watching one is watching for.
func TestOnlyTheSessionLineGoes(t *testing.T) {
	srv := scriptedEndpoint(t,
		callReply("1", "mktemp", `{"purpose": "somewhere to put what the run collects"}`),
		answerReply("done"),
	)
	printed := wholeRunAgainst(t, srv, "agi", "--verbose", "--session", t.TempDir(), "hello")

	if !strings.Contains(printed, "somewhere to put what the run collects") {
		t.Errorf("the call the run made was not announced:\n%s", printed)
	}
	if !strings.Contains(printed, "done") {
		t.Errorf("the answer did not reach the output:\n%s", printed)
	}
}

// The rule itself, which is one question about one flag: did the user name a
// directory, or is the one being used agi's own idea. A value of nothing but
// space is refused by the flag's Validator and never reaches here; a real path
// with room around it is the user's all the same, which is why this asks the
// trimmed value rather than the raw one.
func TestChoseIsAboutWhoNamedTheDirectory(t *testing.T) {
	for _, c := range []struct {
		name  string
		flags sessionFlags
		want  bool
	}{
		{"no --session at all", sessionFlags{}, true},
		{"--rm, which names none either", sessionFlags{discard: true}, true},
		{"--session names one", sessionFlags{dir: "/tmp/agi-1b2"}, false},
		{"--session with space around it", sessionFlags{dir: "  /tmp/agi-1b2  "}, false},
	} {
		if got := c.flags.chose(); got != c.want {
			t.Errorf("%s: chose() = %v, want %v", c.name, got, c.want)
		}
	}
}

// --help is where somebody looks for what a flag does, so --session is where
// this rule has to be written down: there is no flag of its own to list it
// under.
func TestHelpSaysGivingASessionSilencesTheLine(t *testing.T) {
	out, err := run(t, "agi", "--help")
	if err != nil {
		t.Fatalf("agi --help: unexpected error: %v", err)
	}
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "--session DIR") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("agi --help does not mention --session: %q", out)
	}
	if !strings.Contains(line, "does not say where it is being kept") {
		t.Errorf("--session is explained as %q, want it to say the line goes with it", line)
	}
}
