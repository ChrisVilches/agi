package terminal

import (
	"errors"
	"testing"
)

// Each name has to come out as the bytes a real terminal sends for that key,
// whichever way it was spelled, and an arrow has to follow the mode the
// program asked for.
func TestKeyBytesTranslatesNames(t *testing.T) {
	cases := []struct {
		name        string
		application bool
		want        string
	}{
		{"CTRL_C", false, "\x03"},
		{"ctrl-d", false, "\x04"},
		{"Ctrl+Z", false, "\x1a"},
		{"CTRL_?", false, "\x7f"},
		{"ENTER", false, "\r"},
		{"ESC", false, "\x1b"},
		{"UP", false, "\x1b[A"},
		{"UP", true, "\x1bOA"},
		{"ALT_x", false, "\x1bx"},
		{"ALT_X", false, "\x1bX"},
		{"F5", false, "\x1b[15~"},
		{"pgup", false, "\x1b[5~"},
		{"ALT_UP", false, "\x1b\x1b[A"},
	}
	for _, c := range cases {
		got, err := keyBytes(c.name, c.application)
		if err != nil || string(got) != c.want {
			t.Errorf("keyBytes(%q, %v) = %q, %v, want %q", c.name, c.application, got, err, c.want)
		}
	}
}

// A name with no translation is refused with the name in the error, since that
// is the part whoever wrote it has to correct.
func TestKeyBytesRefusesAnUnknownName(t *testing.T) {
	for _, name := range []string{"NOT_A_KEY", "ALT_NOT_A_KEY", "CTRL_AB"} {
		_, err := keyBytes(name, false)
		var unknown *UnknownKeyError
		if !errors.As(err, &unknown) || unknown.Name != name {
			t.Errorf("keyBytes(%q) = %v, want an UnknownKeyError naming it", name, err)
		}
	}
}

// Every plain name KeyNames lists has to be one keyBytes accepts, or the list
// is advertising keys that cannot be sent.
func TestKeyNamesAreAllAccepted(t *testing.T) {
	for _, name := range KeyNames() {
		if name == "CTRL_A..CTRL_Z" || name == "ALT_<key>" {
			continue
		}
		if _, err := keyBytes(name, false); err != nil {
			t.Errorf("KeyNames lists %q, and keyBytes refuses it: %v", name, err)
		}
	}
}
