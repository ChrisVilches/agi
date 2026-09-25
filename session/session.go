// Package session persists what a run leaves behind, so that running agi again
// can pick it up where the last one stopped.
//
// A session is a directory holding conversation.json, the snapshot, and —
// unless the run was told not to keep one — log.jsonl, the stream of
// everything that happened while it was produced. Passing the same directory
// to a second run is what turns a one-shot answer into a conversation: the
// stored turns come back, the new prompt is appended to them, and the model
// sees the whole exchange.
//
// A tool that changes a file the user already had keeps a copy of it here too,
// under backups. That is not a third account of the run alongside the other
// two: it is the files themselves, kept so that a change can be undone, and
// this package only says where they go. See BackupDir.
//
// A session usually outlives the run that made it, and Ephemeral is the
// exception: a session that is deleted when its run ends. It is an ordinary
// session until then, which is what keeps every mechanism above free of any
// knowledge that some runs keep nothing.
//
// The two files answer different questions and this package only writes one of
// them. The snapshot is the conversation as it stood when the run finished, so
// that the next run can continue it; it is replaced wholesale every time, and
// what a run dropped along the way is simply not in it. The log is written by
// package eventlog, appended to rather than replaced, and keeps what the
// snapshot cannot: every event in order, including the turns that were later
// taken back out. This package decides only where it goes, which is LogPath.
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
//
// A session is locked while a run is using it, so that two agi processes
// cannot have the same one open at once. The lock is the operating system's
// and it is taken and given back by the run rather than by anything here; see
// Lock for what it is and why it is that kind of lock.
package session

// TODO: forking a session may come later, to take a conversation somewhere
// without disturbing where it came from. It would create a new session rather
// than reusing the old one, so it is no exception to the rule that a session
// belongs to one run at a time, and is locked like any other. The snapshot
// is easy — it is copied and used as it is, which is what it is for. The log
// is the open question, and it is worth deciding rather than defaulting to
// whatever is easiest to write. Copying it duplicates the same history into
// two files that then diverge; starting an empty one gives the fork an
// account that begins in the middle of a conversation, with nothing to say
// how it got there; recording where the fork came from and leaving the parent
// log where it is keeps each file honest but makes reading a forked run a
// matter of following a trail. None of this is decided, and none of it is
// needed before forking exists.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ChrisVilches/agi/model"
)

const (
	// snapshotFile holds the conversation, rewritten after every run.
	snapshotFile = "conversation.json"
	// logFile holds the stream of events, appended to by every run. The
	// extension is the convention for one JSON document per line.
	logFile = "log.jsonl"
	// backupDirName holds the copies a tool took of files before changing
	// them. The session owns the name because the session owns the directory;
	// nothing here writes into it, the same way nothing here writes the log.
	backupDirName = "backups"
	// namePrefix begins the name of a directory agi creates for itself. The
	// rest of the name comes from os.MkdirTemp, which makes it unique by
	// creating it, so two runs starting at once cannot land on one directory.
	//
	// It is also what Remove checks before deleting anything, so it is not
	// only a convenience for reading a listing: it is the mark that says a
	// directory is agi's own and not one the user asked for by name.
	namePrefix = "agi-"
	// dirPerm and filePerm keep a session to its owner: it holds whatever the
	// user asked and whatever the machine answered, on a machine whose
	// temporary directory everyone can read. dirPerm is what New's directory
	// comes out as rather than something set here, since os.MkdirTemp creates
	// it; a directory the user made themselves is theirs to set as they like.
	dirPerm  fs.FileMode = 0o700
	filePerm fs.FileMode = 0o600
)

// Session is a place to store a run, and every session has a directory: New
// makes one, Open takes the one the user named, and Ephemeral makes one that
// is deleted when the run ends. There is no session with nowhere to write, so
// nothing downstream has to ask whether there is somewhere to put what it
// produces.
type Session struct {
	// dir is the directory holding the snapshot. Every constructor sets it,
	// and nothing outside this package can build a Session without one.
	dir string
	// ephemeral says the directory is agi's own and is to be deleted when the
	// run ends. Only Ephemeral sets it, which is what makes it mean "agi
	// created this": Open cannot produce one, so a directory the user named
	// can never be marked for deletion.
	ephemeral bool
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

// Open returns the session stored in dir, which has to be there already. A
// directory with nothing in it yet is a session with nothing said yet, and is
// opened happily; a directory that does not exist is an error.
//
// It used to create what it could not find, and that was wrong. Naming a
// session is how a user asks for one conversation in particular, so a name
// that matches nothing is a mistake — a typo, a path from another machine, a
// value the shell mangled — and answering it by starting an empty session
// gives the user a fresh conversation wearing the name of the one they asked
// for. The question they meant to continue is answered as if they had never
// asked it. Saying so costs one mkdir and takes nothing away, since a run that
// does not name a session still gets a directory of its own from New.
func Open(dir string) (*Session, error) {
	if dir == "" {
		return nil, errors.New("session: no directory given")
	}

	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("session: there is no session at %s; "+
			"create the directory first if this is a new one (mkdir -p %s), "+
			"or check the path if it was meant to be an existing session", dir, dir)
	case err != nil:
		return nil, fmt.Errorf("session: %w", err)
	case !info.IsDir():
		return nil, fmt.Errorf("session: %s is not a directory", dir)
	}
	return &Session{dir: dir}, nil
}

// Ephemeral creates a session that is deleted when the run that made it ends,
// which is what --rm asks for.
//
// It is an ordinary session while it exists, and that is the point. The run
// writes its snapshot, its log, and whatever its tools keep, all in the usual
// place, so nothing downstream has to know that this run keeps nothing: what
// makes it keep nothing is one call to Remove at the end rather than a second
// kind of session that every mechanism would have to be taught about.
func Ephemeral() (*Session, error) {
	s, err := New()
	if err != nil {
		return nil, err
	}
	s.ephemeral = true
	return s, nil
}

// Dir returns the directory the session is stored in.
func (s *Session) Dir() string { return s.dir }

// path returns where the snapshot lives.
func (s *Session) path() string { return filepath.Join(s.dir, snapshotFile) }

// LogPath returns where the run's log belongs. Nothing here writes it: the
// session owns the directory and so decides the name, and package eventlog
// does the writing. A run that keeps no log is what --no-log asks for, which
// is eventlog's business rather than a session with nowhere to put one.
func (s *Session) LogPath() string {
	return filepath.Join(s.dir, logFile)
}

// BackupDir returns where copies of files a run changed belong. Nothing here
// creates it or writes into it: the session owns the directory and so decides
// the name, and whichever tool takes a copy does the copying. It is the same
// division as LogPath.
//
// Every run has one, --rm included: an ephemeral session is an ordinary
// session until it is removed, so a tool has somewhere to keep a copy for as
// long as the run could still want to read it back.
func (s *Session) BackupDir() string {
	return filepath.Join(s.dir, backupDirName)
}

// Remove deletes the session and everything in it. A session that is not
// ephemeral is left exactly where it is, which is every session the user named
// and every one a run was asked to keep.
//
// The two checks are deliberately both here, next to the only line in agi that
// destroys anything the user might want. The first is that the session marked
// itself ephemeral, which only Ephemeral does. The second is that the
// directory carries agi's own name prefix, so that even a session wrongly
// marked cannot take a directory agi did not create with it. Neither check is
// left to the caller: a rule enforced where the call is made is a rule that
// stops holding the first time somebody adds another caller, and by then the
// damage is a deleted directory rather than a failed test.
//
// The prefix is checked rather than the parent directory. Where sessions are
// made is os.MkdirTemp's business and follows TMPDIR, so a machine pointing
// that somewhere other than /tmp would fail a check written against a fixed
// path — and the feature would stop working rather than fail loudly.
//
// A directory that is already gone is not an error: the point is that it is
// not there afterwards.
func (s *Session) Remove() error {
	if !s.ephemeral {
		return nil
	}
	if !strings.HasPrefix(filepath.Base(s.dir), namePrefix) {
		return fmt.Errorf("session: refusing to remove %s, which is not a directory agi created", s.dir)
	}
	if err := os.RemoveAll(s.dir); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	return nil
}

// Load returns the conversation the session holds: the turns, and no system
// prompt, since none is stored. A caller that means to send it anywhere should
// go through Resume, which supplies one.
//
// A session with nothing in it — one just created, or a directory the user
// made for a conversation that has not started — returns a nil conversation
// and no error, because having nothing stored yet is the ordinary case rather
// than a failure.
func (s *Session) Load() (*model.Conversation, error) {
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
//
// The file is written alongside its destination and renamed into place, so a
// run interrupted midway through cannot leave a half-written snapshot where a
// whole one used to be.
func (s *Session) Save(conv *model.Conversation) error {
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
