// Package session persists what a run leaves behind, so that running agi again
// can pick it up where the last one stopped.
//
// A session is a directory holding one file, conversation.json. Passing the
// same directory to a second run is what turns a one-shot answer into a
// conversation: the stored turns come back, the new prompt is appended to
// them, and the model sees the whole exchange.
//
// What is stored is what was said, and only that. The system prompt is not
// part of it: it is assembled from how agi was invoked, so every run builds
// its own and the invocation stays the source of truth. See Resume.
//
// What is stored is a snapshot of agi's own conversation, taken after the run
// has finished. Anything the run had already dropped by then — the schema
// phase's scaffolding, for instance — was never part of the conversation at
// that point and so is not in the file either. That is the point of dropping
// it: a resumed session continues from what was actually said.
package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ChrisVilches/agi/model"
)

const (
	// snapshotFile is the one file a session directory holds.
	snapshotFile = "conversation.json"
	// namePrefix begins the name of a directory agi creates for itself. The
	// rest of the name comes from os.MkdirTemp, which makes it unique by
	// creating it, so two runs starting at once cannot land on one directory.
	namePrefix = "agi-"
	// dirPerm and filePerm keep a session to its owner: it holds whatever the
	// user asked and whatever the machine answered, on a machine whose
	// temporary directory everyone can read.
	dirPerm  fs.FileMode = 0o700
	filePerm fs.FileMode = 0o600
)

// Session is a place to store a run. The zero value stores nothing, which is
// what Discarded returns.
type Session struct {
	// dir is the directory holding the snapshot, empty when the run is to
	// leave nothing behind.
	dir string
}

// New creates a fresh session in the system's temporary directory, which is
// /tmp unless TMPDIR says otherwise. The name is made unique by creating it,
// so nothing is overwritten and two runs cannot collide.
func New() (*Session, error) {
	dir, err := os.MkdirTemp("", namePrefix)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	return &Session{dir: dir}, nil
}

// Open returns the session stored in dir, creating the directory if it is not
// there yet. An existing directory is resumed from; a new one starts empty.
func Open(dir string) (*Session, error) {
	if dir == "" {
		return nil, errors.New("session: no directory given")
	}

	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, dirPerm); err != nil {
			return nil, fmt.Errorf("session: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("session: %w", err)
	case !info.IsDir():
		return nil, fmt.Errorf("session: %s is not a directory", dir)
	}
	return &Session{dir: dir}, nil
}

// Discarded returns a session that stores nothing: the run happens and leaves
// no trace. Every method works on it, so a caller never has to ask whether
// there is somewhere to store things.
func Discarded() *Session { return &Session{} }

// Dir returns the directory the session is stored in, empty when it is
// discarded.
func (s *Session) Dir() string { return s.dir }

// Stored reports whether the session keeps anything at all.
func (s *Session) Stored() bool { return s.dir != "" }

// path returns where the snapshot lives.
func (s *Session) path() string { return filepath.Join(s.dir, snapshotFile) }

// Load returns the conversation the session holds: the turns, and no system
// prompt, since none is stored. A caller that means to send it anywhere should
// go through Resume, which supplies one.
//
// A session with nothing in it — one just created, or a discarded one —
// returns a nil conversation and no error, because having nothing stored yet
// is the ordinary case rather than a failure.
func (s *Session) Load() (*model.Conversation, error) {
	if !s.Stored() {
		return nil, nil
	}

	data, err := os.ReadFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}

	conv, err := decode(data)
	if err != nil {
		return nil, fmt.Errorf("%w (in %s)", err, s.path())
	}
	return conv, nil
}

// Resume returns the conversation the next run should start from: what the
// session holds with prompt appended as the next user turn, or a conversation
// of its own when the session holds nothing yet. Running agi again against the
// same session is the whole of the chat: each run adds one exchange.
//
// The system prompt is the one passed in, because there is no other. Nobody
// said it: it is assembled from how this run was invoked, and a run resumed
// with different tools or a different base prompt is owed the prompt its own
// arguments describe, not the one some earlier invocation happened to build.
// Storing a copy would mean keeping data that goes stale the moment an
// argument changes, to no purpose, since this is where it would be replaced.
func (s *Session) Resume(system, prompt string) (*model.Conversation, error) {
	conv, err := s.Load()
	if err != nil {
		return nil, err
	}
	if conv == nil {
		return model.NewConversation(system, prompt), nil
	}

	conv.System = system
	conv.AddUser(prompt)
	return conv, nil
}

// Save writes a snapshot of conv, replacing whatever the session held before.
// A discarded session writes nothing and reports no error, since storing
// nothing is what it is for.
//
// The file is written alongside its destination and renamed into place, so a
// run interrupted midway through cannot leave a half-written snapshot where a
// whole one used to be.
func (s *Session) Save(conv *model.Conversation) error {
	if !s.Stored() {
		return nil
	}

	data, err := encode(conv)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(s.dir, snapshotFile+".*")
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	defer os.Remove(tmp.Name()) // A no-op once the rename below has happened.

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("session: %w", err)
	}
	if err := tmp.Chmod(filePerm); err != nil {
		tmp.Close()
		return fmt.Errorf("session: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path()); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	return nil
}
