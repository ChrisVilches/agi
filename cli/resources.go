package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/skills"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/tools/userdefined"
	"github.com/urfave/cli/v3"
)

// resources is everything the folders a run was given brought to it.
//
// It is a value rather than three returns and an error, for the reason wiring
// is one: three results that always travel together are one thing, and the
// next kind of resource added is the one returned by this function and
// forgotten at the call site.
type resources struct {
	tools  []tools.Tool
	skills []skills.Skill
	hooks  *hooks.Set
}

// resourcesFlag reads --resources: the folders the user brought to the run,
// and the three kinds of thing they may hold.
//
// A folder holds its tools in a tools.json, its hooks in a hooks.json, and its
// skills in the folders under it, each of which announces itself with a
// SKILL.md. It may hold any of the three and need not hold all of them, so a
// folder of skills alone is an ordinary folder to name; what it may not do is
// hold none of them, which is refused below.
//
// Everything is read here, before a gateway is contacted or a session is
// created, the way --sys and --schema are. A folder that is not there, a
// tools.json naming a script that is not executable, a hooks.json filed under
// an event agi does not have, a SKILL.md whose frontmatter does not parse, two
// tools or two skills answering to one name: each is a mistake in how the run
// was invoked, and finding it now costs a message rather than a conversation
// the user is paying for.
//
// What cannot be settled here is which run this is, because the session does
// not exist yet. The hooks are given that afterwards, by the one call to
// Set.WithSession in runPrompt.
//
// The order the folders were given in is kept all the way through, and within
// a folder each kind keeps the order it was written in — the order tools.json
// lists a tool, the order hooks.json lists a hook, the order the filesystem
// sorts a skill's folder. It decides two things. Hooks run in sequence and one
// hook's output is the next one's input, so rearranging them would change what
// a tool is finally called with; and the skills are listed to the model in
// this order, so two runs naming the same folders have to produce the same
// prompt, which is the invariant that lets a snapshot store no prompt at all.
func resourcesFlag(cmd *cli.Command) (resources, error) {
	dirs := trimmedPaths(cmd, "resources")
	if len(dirs) == 0 {
		return resources{hooks: hooks.None()}, nil
	}
	if err := eachIsAFolder(dirs); err != nil {
		return resources{}, err
	}

	user, err := userdefined.LoadAll(dirs)
	if err != nil {
		return resources{}, err
	}
	if err := refuseProvidedNames(user); err != nil {
		return resources{}, err
	}
	found, err := skills.Find(dirs)
	if err != nil {
		return resources{}, err
	}
	armed, err := hooks.Load(dirs, hooks.Config{Timeout: cmd.Duration("hook-timeout")})
	if err != nil {
		return resources{}, err
	}
	if err := refuseEmptyFolders(dirs, found); err != nil {
		return resources{}, err
	}
	return resources{tools: user, skills: found, hooks: armed}, nil
}

// eachIsAFolder refuses a --resources that is not there, or is not a folder.
//
// A name matching nothing is a mistake — a typo, a path from another machine,
// a value the shell mangled — and reading it as a folder that happened to hold
// nothing would answer the user's invocation as though they had not typed it.
//
// The packages below each refuse what they were handed for their own reasons,
// and this says the same thing earlier and in the flag's words. That is the
// arrangement refuseProvidedNames already has with the registry: the check the
// user can act on is made where the user's mistake was, and the package keeps
// the one that guards its own contract.
func eachIsAFolder(dirs []string) error {
	for _, dir := range dirs {
		info, err := os.Stat(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("there is no %s; --resources names a folder that must already exist", dir)
		case err != nil:
			return fmt.Errorf("reading %s: %w", dir, err)
		case !info.IsDir():
			return fmt.Errorf("%s is not a folder; --resources names the folder the tools, hooks and skills are in", dir)
		}
	}
	return nil
}

// refuseEmptyFolders refuses a folder that brought the run nothing.
//
// Naming a folder is asking for what is in it, so a folder with nothing in it
// is a request that cannot be honored: the likeliest cause is the wrong path —
// one a project moved, one belonging to another machine, one whose resources
// sit a level further down — and a run that carried on would behave exactly as
// though the flag had not been typed. That is the same reason a tools.json
// declaring no tools is an error rather than an empty list.
//
// What a folder holds is asked of the folder itself for the two files, and of
// the skills already found for the third. Walking it a second time to count
// skills would be reading the whole tree again to learn what the walk just
// finished has already said.
func refuseEmptyFolders(dirs []string, found []skills.Skill) error {
	for _, dir := range dirs {
		holds, err := holdsResources(dir, found)
		if err != nil {
			return err
		}
		if !holds {
			return fmt.Errorf("%s holds no %s, no %s, and no %s under it; there is nothing in "+
				"it for the run", dir, userdefined.FileName, hooks.FileName, skills.FileName)
		}
	}
	return nil
}

// holdsResources reports whether one folder gave the run anything.
//
// A tools.json or a hooks.json that is there is enough, whatever is in it:
// loading has already run and would have refused one that declares nothing, so
// by the time this is asked the file's existence is the answer.
//
// A skill is counted by where it was found. Every skill under the folder is
// somewhere below it in the filesystem, and both paths are absolute and
// cleaned, so a prefix is the whole of the test. The folder itself may be the
// skill, which is what the first comparison is for: a --resources that holds a
// SKILL.md of its own is one skill and nothing else.
func holdsResources(dir string, found []skills.Skill) (bool, error) {
	for _, name := range []string{userdefined.FileName, hooks.FileName} {
		where := filepath.Join(dir, name)
		switch _, err := os.Stat(where); {
		case err == nil:
			return true, nil
		case !errors.Is(err, fs.ErrNotExist):
			return false, fmt.Errorf("reading %s: %w", where, err)
		}
	}

	root, err := filepath.Abs(dir)
	if err != nil {
		return false, fmt.Errorf("resolving %s: %w", dir, err)
	}
	for _, s := range found {
		if s.Path == root || strings.HasPrefix(s.Path, root+string(filepath.Separator)) {
			return true, nil
		}
	}
	return false, nil
}

// refuseProvidedNames rejects a tool that took the name of one agi provides.
//
// The registry would refuse it too, later and in this repository's words; this
// is the same refusal made where the user can act on it, before anything about
// the run exists, and worded for somebody looking at a tools.json rather than
// at agi's source.
func refuseProvidedNames(user []tools.Tool) error {
	provided, err := providedNames()
	if err != nil {
		return err
	}
	for _, t := range user {
		name := t.Definition().Name
		if provided[name] {
			return fmt.Errorf("the tool %q has the same name as one agi provides; rename it in "+
				"its tools.json, since the model addresses a tool by its name and could not tell "+
				"the two apart", name)
		}
	}
	return nil
}
