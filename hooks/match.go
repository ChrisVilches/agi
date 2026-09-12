package hooks

import (
	"bytes"
	"encoding/json"
	"regexp"
)

// matchAll is what a manifest writes to say a hook runs for every tool. It is
// not a regular expression — a lone star does not compile as one — so it is
// recognized before anything is compiled, the way Claude Code recognizes it.
const matchAll = "*"

// compileMatcher turns the matcher a manifest wrote into the pattern a tool's
// name is tested against. A nil pattern accepts every tool.
//
// An absent matcher, an empty one, and "*" all mean every tool, which is the
// three spellings Claude Code accepts for it.
//
// Everything else is a regular expression, matched anywhere in the name — so
// bash also matches a tool called bashlite, and ^bash$ is how a manifest asks
// for exactness. Claude Code has a second rule, where a matcher made only of
// letters, digits, spaces, commas and pipes is compared literally instead,
// and agi deliberately does not: the specification says every matcher here is
// a regular expression, and one rule that always holds is worth more than two
// that are told apart by the characters somebody happened to type.
func compileMatcher(raw string) (*regexp.Regexp, error) {
	if raw == "" || raw == matchAll {
		return nil, nil
	}
	return regexp.Compile(raw)
}

// satisfied reports whether the hook's conditions accept the call's
// arguments.
//
// The conditions are combined with OR: at least one has to match. A hook with
// none imposes no condition and accepts every call its matcher accepted,
// which is what makes a guard over a whole tool writable at all.
//
// A parameter the call did not include fails its own condition and is not an
// error, since tools have optional parameters and another condition may still
// fire the hook. Arguments that are not a JSON object satisfy nothing, so a
// hook with conditions does not run: the tool is about to refuse the call and
// say why, and two complaints about one mistake is one too many.
func (h *Hook) satisfied(args json.RawMessage) bool {
	if len(h.conditions) == 0 {
		return true
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil {
		return false
	}

	for name, pattern := range h.conditions {
		raw, given := fields[name]
		if !given {
			continue
		}
		if pattern.MatchString(text(raw)) {
			return true
		}
	}
	return false
}

// text renders one argument for matching against.
//
// A string is matched as itself, which is the case every condition in a
// manifest is really written for. Anything else is matched against its
// compact JSON, so a number, a boolean, an object and a list all have
// something to test — "^12" matches the number 120, and an object matches the
// text {"a":1}.
//
// The alternative was for a non-string to match nothing at all, and it is
// worse: it would quietly ignore a condition the user wrote, which is the
// worst thing a guard can do. Rendering never silently fails to fire, and it
// lets a condition reach into an argument whose shape agi does not know,
// which for a tool the user wrote themselves is most of them.
func text(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}
