package terminal

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// namedKeys are the bytes a real terminal sends for each key that has a name
// rather than a character.
var namedKeys = map[string]string{
	"ENTER":     "\r",
	"RETURN":    "\r",
	"TAB":       "\t",
	"BACKTAB":   "\x1b[Z",
	"ESC":       "\x1b",
	"ESCAPE":    "\x1b",
	"BACKSPACE": "\x7f",
	"SPACE":     " ",
	"INSERT":    "\x1b[2~",
	"DELETE":    "\x1b[3~",
	"HOME":      "\x1b[H",
	"END":       "\x1b[F",
	"PAGE_UP":   "\x1b[5~",
	"PAGE_DOWN": "\x1b[6~",
	"F1":        "\x1bOP",
	"F2":        "\x1bOQ",
	"F3":        "\x1bOR",
	"F4":        "\x1bOS",
	"F5":        "\x1b[15~",
	"F6":        "\x1b[17~",
	"F7":        "\x1b[18~",
	"F8":        "\x1b[19~",
	"F9":        "\x1b[20~",
	"F10":       "\x1b[21~",
	"F11":       "\x1b[23~",
	"F12":       "\x1b[24~",
}

// arrowKeys holds only the final byte of each arrow, because what comes before
// it depends on the mode the program has put the terminal in; see keyBytes.
var arrowKeys = map[string]string{"UP": "A", "DOWN": "B", "RIGHT": "C", "LEFT": "D"}

// keyAliases are the other spellings people use for the same keys.
var keyAliases = map[string]string{
	"PGUP": "PAGE_UP",
	"PGDN": "PAGE_DOWN",
	"DEL":  "DELETE",
	"INS":  "INSERT",
	"BS":   "BACKSPACE",
}

var (
	ctrlPattern = regexp.MustCompile(`^(?:CTRL|C)_(.)$`)
	altPattern  = regexp.MustCompile(`^(?:ALT|M|META)_(.+)$`)
)

// UnknownKeyError reports a key name that has no translation. It is a type so
// that a caller can say which name was wrong, which is what the person or the
// model that wrote it needs to correct it.
type UnknownKeyError struct{ Name string }

func (e *UnknownKeyError) Error() string { return fmt.Sprintf("terminal: unknown key %q", e.Name) }

// keyBytes translates a key name such as "CTRL_C", "UP" or "F5" to the bytes a
// real terminal would send for it.
//
// A name is read case-insensitively, with - and + accepted for _, because
// ctrl-c, Ctrl+C and CTRL_C are all how somebody writes the same key. The one
// place case survives is the character after ALT_, since ALT_x and ALT_X are
// different keys.
//
// application says whether the program has put the terminal in application
// cursor-key mode, as vim and less do. An arrow is ESC [ x in the normal mode
// and ESC O x in that one, and a program in that mode reads the normal
// encoding as something else entirely.
func keyBytes(name string, application bool) ([]byte, error) {
	trimmed := strings.TrimSpace(name)
	key := strings.NewReplacer("-", "_", "+", "_").Replace(strings.ToUpper(trimmed))
	if alias, ok := keyAliases[key]; ok {
		key = alias
	}
	if final, ok := arrowKeys[key]; ok {
		prefix := "\x1b["
		if application {
			prefix = "\x1bO"
		}
		return []byte(prefix + final), nil
	}
	if seq, ok := namedKeys[key]; ok {
		return []byte(seq), nil
	}
	if m := ctrlPattern.FindStringSubmatch(key); m != nil {
		// Control with A-Z, or with one of @ [ \ ] ^ _, clears the top three
		// bits of the character; Control-? is DEL, which is the one exception.
		ch := m[1][0]
		if len(m[1]) == 1 && ch >= '@' && ch <= '_' {
			return []byte{ch & 0x1f}, nil
		}
		if m[1] == "?" {
			return []byte{0x7f}, nil
		}
	}
	if m := altPattern.FindStringSubmatch(key); m != nil {
		inner := m[1]
		if len([]rune(inner)) == 1 {
			r := []rune(trimmed)
			return append([]byte{0x1b}, string(r[len(r)-1])...), nil
		}
		rest, err := keyBytes(inner, application)
		if err != nil {
			return nil, &UnknownKeyError{Name: name}
		}
		return append([]byte{0x1b}, rest...), nil
	}
	return nil, &UnknownKeyError{Name: name}
}

// KeyNames lists the key names Terminal.Key accepts, sorted, with the two
// families that are patterns rather than names at the end.
func KeyNames() []string {
	set := map[string]bool{}
	for _, m := range []map[string]string{namedKeys, arrowKeys, keyAliases} {
		for k := range m {
			set[k] = true
		}
	}
	names := make([]string, 0, len(set)+2)
	for k := range set {
		names = append(names, k)
	}
	sort.Strings(names)
	return append(names, "CTRL_A..CTRL_Z", "ALT_<key>")
}
