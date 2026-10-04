package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// atTTY and notTTY are the two situations a run can be in, as a hooks.json
// names them: agi's own standard output is a terminal, or it is a pipe or a
// file.
//
// They are the file's words and so they are the format, the way the event
// names are. "non-tty" is spelled out rather than left to be inferred from
// the absence of the other, because a list is how a hook says which
// situations it wants and a list cannot name an absence.
const (
	atTTY  = "tty"
	notTTY = "non-tty"
)

// enablement is the situations a hook runs in.
//
// There are four of them and two booleans say all four: both, one, the other,
// or neither. The last is what false says and what an empty list says, which
// is a hook that is declared and never runs — a decision rather than a
// mistake, the same way an empty hooks.json is.
//
// It is a type rather than a bool because the question a hook needs answered
// is not whether it is on. A markdown renderer is wanted at a terminal and
// ruinous in a pipe; a guard that posts to a chat room is wanted in the
// unattended run and merely noisy in the attended one. Both are "enabled",
// and they are enabled in opposite halves of the world.
type enablement struct {
	tty    bool
	nonTTY bool
}

// always is what a hook that said nothing about it gets: it runs wherever the
// run's output is going. That is the behavior every hook had before the field
// existed, which is what an absent field has to keep meaning.
var always = enablement{tty: true, nonTTY: true}

// allows reports whether a hook with this enablement runs in a run whose
// output is a terminal, or is not.
func (e enablement) allows(tty bool) bool {
	if tty {
		return e.tty
	}
	return e.nonTTY
}

// compileEnabled reads an entry's enabled field.
//
// It is read here, where the entry is compiled, rather than by giving the
// field a type with an UnmarshalJSON of its own. The decoder would raise its
// error inside decode, where the only thing known is which file is being
// read; here the loop that compiles the entries wraps everything this returns
// with the event and the entry's number, so a manifest holding a dozen hooks
// says which one of them is wrong.
//
// An absent field is every situation, which is what every hook meant before
// the field existed. Everything else is checked strictly: the value is true,
// false, or a list, and a list holds nothing but the two names. A spelling
// agi does not know would otherwise be a hook that runs in neither kind of
// run, which is the silent half of the same failure a dead matcher is.
func compileEnabled(raw json.RawMessage) (enablement, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return always, nil
	}
	if raw[0] == '[' {
		return listed(raw)
	}
	// A JSON null is refused by hand, because unmarshalling one into a bool
	// is not an error — it leaves the bool alone — and a field written null
	// would otherwise read as false and turn the hook off without a word.
	if bytes.Equal(raw, []byte("null")) {
		return enablement{}, unreadable(raw)
	}

	var whether bool
	if err := json.Unmarshal(raw, &whether); err != nil {
		return enablement{}, unreadable(raw)
	}
	if whether {
		return always, nil
	}
	return enablement{}, nil
}

// listed reads the list form, which is the one that says a situation rather
// than a yes or a no.
func listed(raw json.RawMessage) (enablement, error) {
	var named []json.RawMessage
	if err := json.Unmarshal(raw, &named); err != nil {
		return enablement{}, unreadable(raw)
	}

	var e enablement
	for _, item := range named {
		var name string
		// The item is quoted back as the file wrote it rather than as a
		// decoded string, so that a number or an object in the list is shown
		// as what it is instead of as an empty name.
		if err := json.Unmarshal(item, &name); err != nil {
			return enablement{}, unknown(string(item))
		}
		switch name {
		case atTTY:
			e.tty = true
		case notTTY:
			e.nonTTY = true
		default:
			return enablement{}, unknown(string(item))
		}
	}
	return e, nil
}

// unreadable refuses a value that is not one of the three shapes the field
// takes, and says what the three are.
func unreadable(raw json.RawMessage) error {
	return fmt.Errorf("has an enabled of %s, which is not something it can be: write true to "+
		"run the hook in every run, false to run it in none, or a list naming the runs it is "+
		"for — [%q] runs it only when agi's output is a terminal, [%q] only when it is not, "+
		"[%q, %q] in both, and [] in neither",
		raw, atTTY, notTTY, atTTY, notTTY)
}

// unknown refuses a name in the list that is not a situation agi knows,
// which is the mistake the list form invites: it is the half of the field
// where a typo has somewhere to hide.
func unknown(written string) error {
	return fmt.Errorf("has an enabled naming %s, which is not a kind of run agi tells apart: "+
		"it knows %q, for a run whose output is a terminal, and %q, for one whose output is a "+
		"pipe or a file", written, atTTY, notTTY)
}
