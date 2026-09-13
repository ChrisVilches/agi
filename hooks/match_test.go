package hooks

import (
	"encoding/json"
	"testing"
)

// fires reports whether the one hook a manifest declares runs for a call.
func fires(t *testing.T, manifest, tool, args string) bool {
	t.Helper()
	s := one(t, manifest)
	if s.Len() != 1 {
		t.Fatalf("the manifest declares %d hooks, want 1", s.Len())
	}
	return len(matching(s, PreToolUse, tool, args)) == 1
}

// matching is what the two public methods do between them: take the hooks of
// an event and ask each whether it accepts the call.
func matching(s *Set, event Event, tool, args string) []*Hook {
	var found []*Hook
	for _, h := range s.forEvent(event) {
		if h.matches(tool, json.RawMessage(args)) {
			found = append(found, h)
		}
	}
	return found
}

// hook builds a manifest around one entry, so a test reads as the entry it is
// about.
func hook(entry string) string {
	return `{"PreToolUse": [` + entry + `]}`
}

func TestMatcherAcceptsByName(t *testing.T) {
	entry := hook(`{"matcher": "^bash$", "command": "x"}`)

	if !fires(t, entry, "bash", `{}`) {
		t.Error("an anchored matcher did not fire for the tool it names")
	}
	if fires(t, entry, "writefile", `{}`) {
		t.Error("an anchored matcher fired for another tool")
	}
}

// A matcher is a regular expression matched anywhere in the name, which is
// Claude Code's behaviour and is why ^bash$ exists for exactness.
func TestMatcherMatchesAnywhere(t *testing.T) {
	if !fires(t, hook(`{"matcher": "bash", "command": "x"}`), "bashlite", `{}`) {
		t.Error("an unanchored matcher did not fire for a name containing it")
	}
}

func TestMatcherAlternation(t *testing.T) {
	entry := hook(`{"matcher": "mcp__fs__.*|writefile|patchfile", "command": "x"}`)

	for _, tool := range []string{"mcp__fs__read", "writefile", "patchfile"} {
		if !fires(t, entry, tool, `{}`) {
			t.Errorf("the matcher did not fire for %q", tool)
		}
	}
	if fires(t, entry, "bash", `{}`) {
		t.Error("the matcher fired for a tool it does not name")
	}
}

// Absent, empty and "*" all mean every tool. A lone star is not a valid
// regular expression, so it is recognized before anything is compiled.
func TestMatcherEveryTool(t *testing.T) {
	for _, entry := range []string{
		hook(`{"command": "x"}`),
		hook(`{"matcher": "", "command": "x"}`),
		hook(`{"matcher": "*", "command": "x"}`),
	} {
		if !fires(t, entry, "anything", `{}`) {
			t.Errorf("%s did not fire for every tool", entry)
		}
	}
}

// The conditions are combined with OR: one match is enough.
func TestConditionsAreOred(t *testing.T) {
	entry := hook(`{"if": {"path": "\\.ts$", "content": "export default"}, "command": "x"}`)

	if !fires(t, entry, "writefile", `{"path": "a.ts", "content": "nothing"}`) {
		t.Error("the first condition alone did not fire the hook")
	}
	if !fires(t, entry, "writefile", `{"path": "a.go", "content": "export default x"}`) {
		t.Error("the second condition alone did not fire the hook")
	}
	if fires(t, entry, "writefile", `{"path": "a.go", "content": "nothing"}`) {
		t.Error("the hook fired with no condition matching")
	}
}

// A parameter the call did not include fails its own condition and is not an
// error: tools have optional parameters, and another condition may still
// fire the hook.
func TestConditionOnAParameterThatIsNotThere(t *testing.T) {
	entry := hook(`{"if": {"missing": ".", "path": "^/etc/"}, "command": "x"}`)

	if !fires(t, entry, "writefile", `{"path": "/etc/hosts"}`) {
		t.Error("an absent parameter stopped another condition from firing the hook")
	}
	if fires(t, entry, "writefile", `{"path": "/home/chris/notes"}`) {
		t.Error("the hook fired when neither condition matched")
	}
}

// A value that is not a string is matched against its compact JSON, so every
// condition somebody writes can fire. The alternative — a non-string matching
// nothing — would quietly ignore a condition the user wrote.
func TestConditionsOnValuesThatAreNotStrings(t *testing.T) {
	cases := []struct {
		name  string
		entry string
		args  string
		want  bool
	}{
		{"a number", hook(`{"if": {"timeout": "^12"}, "command": "x"}`), `{"timeout": 120}`, true},
		{"a boolean", hook(`{"if": {"force": "true"}, "command": "x"}`), `{"force": true}`, true},
		{"an object", hook(`{"if": {"env": "\"PROD\""}, "command": "x"}`), `{"env": {"mode": "PROD"}}`, true},
		{"a list", hook(`{"if": {"paths": "\\.ts"}, "command": "x"}`), `{"paths": ["a.ts"]}`, true},
		{"no match", hook(`{"if": {"timeout": "^9"}, "command": "x"}`), `{"timeout": 120}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fires(t, c.entry, "bash", c.args); got != c.want {
				t.Errorf("fired = %v, want %v", got, c.want)
			}
		})
	}
}

// A string is matched as itself and not as the JSON it would encode to, so a
// pattern does not have to know about quoting.
func TestConditionOnAStringMatchesTheStringItself(t *testing.T) {
	if !fires(t, hook(`{"if": {"command": "^git push"}, "command": "x"}`), "bash", `{"command": "git push --force"}`) {
		t.Error("a string was not matched as itself")
	}
}

// Arguments that are not an object satisfy nothing, so a hook with conditions
// stays out of it: the tool is about to refuse the call and say why.
func TestConditionsOnArgumentsThatAreNotAnObject(t *testing.T) {
	entry := hook(`{"if": {"path": "."}, "command": "x"}`)

	for _, args := range []string{`[]`, `"text"`, `not json`, ``} {
		if fires(t, entry, "writefile", args) {
			t.Errorf("the hook fired on arguments %q", args)
		}
	}
}

// A hook with no conditions runs whatever the arguments are, including
// arguments nothing could be read out of.
func TestNoConditionsRunsRegardless(t *testing.T) {
	if !fires(t, hook(`{"matcher": "bash", "command": "x"}`), "bash", `not json`) {
		t.Error("a hook with no conditions did not fire")
	}
}

// Matching is per event: a PostToolUse hook is not offered to the call that
// happens before the tool runs.
func TestMatchingIsPerEvent(t *testing.T) {
	s := one(t, `{
	  "PreToolUse":  [{"command": "before"}],
	  "PostToolUse": [{"command": "after"}]
	}`)

	pre := matching(s, PreToolUse, "bash", `{}`)
	if len(pre) != 1 || pre[0].Command() != "before" {
		t.Errorf("PreToolUse matched %d hooks, want only the one declared for it", len(pre))
	}
	post := matching(s, PostToolUse, "bash", `{}`)
	if len(post) != 1 || post[0].Command() != "after" {
		t.Errorf("PostToolUse matched %d hooks, want only the one declared for it", len(post))
	}
}

// Watches is the matcher asked on its own, without a call to judge, so that a
// caller which knows the tools a run has can find a hook that can never fire.
// What an absent or wildcard matcher means stays in this package: a caller
// comparing strings would have to reimplement the three spellings of "every
// tool" and would eventually get one wrong.
func TestWatches(t *testing.T) {
	cases := []struct {
		matcher string
		tool    string
		want    bool
	}{
		{`"matcher": "^bash$",`, "bash", true},
		{`"matcher": "^bash$",`, "Bash", false},
		{`"matcher": "bash",`, "bashlite", true},
		{`"matcher": "bash|writefile",`, "writefile", true},
		{`"matcher": "bash|writefile",`, "patchfile", false},
		// The three spellings of every tool.
		{`"matcher": "*",`, "anything", true},
		{`"matcher": "",`, "anything", true},
		{``, "anything", true},
	}
	for _, c := range cases {
		s := one(t, hook(`{`+c.matcher+` "command": "x"}`))
		if got := s.All()[0].Watches(c.tool); got != c.want {
			t.Errorf("matcher %q watching %q = %v, want %v", c.matcher, c.tool, got, c.want)
		}
	}
}

// A run given no hooks matches nothing, and nothing has to ask whether there
// are any.
func TestNoneMatchesNothing(t *testing.T) {
	if got := matching(None(), PreToolUse, "bash", `{}`); len(got) != 0 {
		t.Errorf("None() matched %d hooks, want none", len(got))
	}
	if None().Len() != 0 {
		t.Errorf("None().Len() = %d, want 0", None().Len())
	}
}
