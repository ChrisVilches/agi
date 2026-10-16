package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// compileEnabled reads an entry's enabled field, which says whether the hook
// runs at all.
//
// It is read here, where the entry is compiled, rather than by giving the
// field a type with an UnmarshalJSON of its own. The decoder would raise its
// error inside decode, where the only thing known is which file is being
// read; here the loop that compiles the entries wraps everything this returns
// with the event and the entry's number, so a manifest holding a dozen hooks
// says which one of them is wrong.
//
// An absent field is true, which is what every hook meant before the field
// existed. false is a hook that is declared and never runs — a decision
// rather than a mistake, the same way an empty hooks.json is.
//
// It is a yes or a no and nothing else. Which kind of run a hook is for is
// not the file's to say: a MessageDisplay hook fires only where agi's output
// is a terminal, since a piped answer can be put through a renderer by the
// pipe itself, and every other hook fires wherever the run is.
func compileEnabled(raw json.RawMessage) (bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return true, nil
	}
	// A JSON null is refused by hand, because unmarshalling one into a bool
	// is not an error — it leaves the bool alone — and a field written null
	// would otherwise read as false and turn the hook off without a word.
	if bytes.Equal(raw, []byte("null")) {
		return false, unreadable(raw)
	}

	var whether bool
	if err := json.Unmarshal(raw, &whether); err != nil {
		return false, unreadable(raw)
	}
	return whether, nil
}

// unreadable refuses a value that is not true or false.
func unreadable(raw json.RawMessage) error {
	return fmt.Errorf("has an enabled of %s, which is not something it can be: write true to "+
		"run the hook, or false to keep it from running", raw)
}
