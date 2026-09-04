package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/skills"
	"github.com/ChrisVilches/agi/tools"
)

// LoadSkillTool is the name the tool is registered and called under. It is a
// constant because three things have to agree on it: the tool, whatever asks
// the registry whether a run has it, and the prompt that tells the model what
// to call.
const LoadSkillTool = "loadskill"

// skillDirPrefix begins the line of a result that says where the skill is
// kept. It is a constant because two things need it: Call writes the line, and
// Report finds it again to show the path to whoever is watching.
//
// The directory is the whole of the rest of the line, and the sentence is
// worded so that it ends there, which is what lets Report cut the prefix off
// and be left holding a path.
const skillDirPrefix = "Any relative path in this skill's text is relative to the " +
	"directory the skill is kept in, which is "

// LoadSkillParams is the input schema for the tool. The name is the whole of
// it: where a skill is on disk is the harness's business, and a model that
// could name a path could name any path.
type LoadSkillParams struct {
	Name string `json:"name"`
}

// LoadSkill turns the name of a skill into the instructions it holds.
//
// It acts on the agent rather than on the machine — what it changes is what
// the agent knows for the rest of the run — which is why it is here and not in
// workspace, and why the skills available to a run have to be explained in the
// system prompt rather than left to this tool's description. A model that has
// not been told what a skill is will not go looking for one.
//
// It is registered only when the run was given skills, and the section of the
// system prompt that lists them is assembled from the same list, so neither
// can exist without the other.
//
// A skill loads once. The instructions stay in the conversation after they
// arrive, so a second copy would say nothing the first did not and would cost
// the context it took up; asking for one already loaded is refused and the
// model is told where to reread it. That bookkeeping is the tool's own state,
// which is why every agent that has one has its own: two agents reading the
// same skill have each read it once.
type LoadSkill struct {
	// by is every skill the run was given, by the name it is loaded under.
	by map[string]skills.Skill
	// listed is those names in the order they were found, which is the order
	// the model was shown them in.
	listed []string

	// mu guards loaded. Nothing in an ordinary run calls a tool from two
	// goroutines at once, but a tool that would break if something did is a
	// tool with a rule nobody can see.
	mu sync.Mutex
	// loaded is which skills have already been handed over.
	loaded map[string]bool
}

// NewLoadSkill returns the tool for the skills a run was given.
func NewLoadSkill(found []skills.Skill) *LoadSkill {
	t := &LoadSkill{
		by:     make(map[string]skills.Skill, len(found)),
		listed: make([]string, 0, len(found)),
		loaded: make(map[string]bool, len(found)),
	}
	for _, s := range found {
		t.by[s.Name] = s
		t.listed = append(t.listed, s.Name)
	}
	return t
}

// Definition describes the tool to the model. What a skill is and when to load
// one is in the system prompt, beside the list of them; what is here is how to
// call it.
func (t *LoadSkill) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: LoadSkillTool,
		Description: strings.TrimSpace(`
Load one of the available skills and read its instructions.

  {"name": "data-analyzer"}

The name is the one the skill is listed under in the system prompt, and it is
the only argument: the file is found for you, and no path is accepted here.

What comes back is the skill's whole text, and above it the directory the skill
is kept in, which is what any relative path in that text is relative to. The
text stays in this conversation and applies to everything you do afterwards, so
load a skill before doing the work it is about rather than after.

Each skill loads once. A second call for the same name is refused, because its
instructions are already above and rereading them there costs nothing. A name
that is not in the list is refused too, with the names that are.`),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type": "string",
					"description": "The name of the skill to load, exactly as it " +
						"appears in the list of available skills. Not a path, and not " +
						"a skill you know of from elsewhere: only the listed names " +
						"can be loaded.",
				},
			},
			"required":             []any{"name"},
			"additionalProperties": false,
		},
	}
}

// Announce implements tools.Announcer: the name is the whole of the call, and
// it is what the user needs to see the run reach for.
func (t *LoadSkill) Announce(args json.RawMessage) string {
	var p LoadSkillParams
	if err := json.Unmarshal(args, &p); err != nil {
		return ""
	}
	return strings.TrimSpace(p.Name)
}

// Report implements tools.Reporter: it shows the directory the skill was
// loaded from, dimmed under the name the call asked for. It is worth a line
// because what the model reads is the skill's own text, and a person watching
// has no other way to see which folder on the machine those instructions came
// out of.
//
// It reads the path back out of the result rather than remembering it, so that
// what the user is shown is what the model was told.
func (t *LoadSkill) Report(result string) string {
	for _, line := range strings.Split(result, "\n") {
		if dir, ok := strings.CutPrefix(line, skillDirPrefix); ok {
			return tools.Detail("  " + dir)
		}
	}
	return ""
}

// Call implements tools.Tool. It returns the skill's instructions, or an error
// for a call that could not be answered with any.
//
// Every refusal here is an error rather than text saying what went wrong,
// because no skill was loaded in any of them: the call did not happen. The
// model reads the reason on the next turn and corrects the call, which is what
// a refused call is for.
func (t *LoadSkill) Call(_ context.Context, args json.RawMessage) (string, error) {
	var p LoadSkillParams
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("%s: arguments are not valid JSON: %w", LoadSkillTool, err)
	}

	name := strings.TrimSpace(p.Name)
	if name == "" {
		return "", fmt.Errorf("%s: no skill was named; %s", LoadSkillTool, t.available())
	}

	skill, ok := t.by[name]
	if !ok {
		return "", fmt.Errorf("%s: there is no skill called %q in this run; %s",
			LoadSkillTool, name, t.available())
	}

	// The file is read before the skill is marked as loaded, so a skill that
	// could not be read can be asked for again once whatever was wrong with
	// it is fixed.
	if err := t.claim(name); err != nil {
		return "", err
	}
	text, err := skill.Instructions()
	if err != nil {
		t.release(name)
		return "", fmt.Errorf("%s: %w", LoadSkillTool, err)
	}

	// The instructions are wrapped so that the model can tell where a document
	// somebody else wrote begins and ends. A skill is Markdown, which is what
	// everything around it in the conversation is too, and the name in the tag
	// says which of the available skills this text is.
	//
	// Where the skill is kept is said before the document rather than inside
	// it, because it is agi speaking and not the skill's author. A skill
	// mentions the files beside it by a relative path, and nothing else in the
	// conversation says what those paths are relative to; saying it first means
	// the model has read it by the time it reaches the first one, and the
	// result still ends at the closing tag.
	return fmt.Sprintf("%s%s\n\n<skill name=%q>\n%s\n</skill>",
		skillDirPrefix, skill.Path, name, text), nil
}

// claim marks a skill as loaded, refusing one that already is.
func (t *LoadSkill) claim(name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.loaded[name] {
		return fmt.Errorf("%s: the skill %q is already loaded; its instructions are earlier "+
			"in this conversation, so reread them there rather than loading it again",
			LoadSkillTool, name)
	}
	t.loaded[name] = true
	return nil
}

// release takes a claim back, for a skill that turned out not to be loadable.
func (t *LoadSkill) release(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.loaded, name)
}

// available names the skills a call could have asked for, since a model that
// got the name wrong is one word away from getting it right and the list is
// short by construction.
func (t *LoadSkill) available() string {
	if len(t.listed) == 0 {
		return "this run was given no skills"
	}
	return "the skills available are: " + strings.Join(t.listed, ", ")
}
