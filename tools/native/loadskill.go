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

// LoadSkillParams is the input schema for the tool. The id is the whole of it:
// where a skill is on disk is the harness's business, and a model that could
// name a path could name any path.
//
// The field is called id rather than name because a skill now has two names
// and only one of them can be loaded by. A field called name would invite the
// title, which is the mistake the catalogue is laid out to prevent, and the
// schema is the one place a model reads about a call without any prose around
// it.
type LoadSkillParams struct {
	ID string `json:"id"`
}

// LoadSkill turns the id of a skill into the instructions it holds.
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
	// by is every skill the run was given, by the id it is loaded under.
	by map[string]skills.Skill
	// listed is those ids in the order they were found, which is the order the
	// model was shown them in.
	listed []string

	// mu guards loaded. Nothing in an ordinary run calls a tool from two
	// goroutines at once, but a tool that would break if something did is a
	// tool with a rule nobody can see.
	mu sync.Mutex
	// loaded is which skills have already been handed over.
	loaded map[string]bool
}

// NewLoadSkill returns the tool for the skills a run was given, with the ids
// in loaded already marked as handed over.
//
// An agent that starts from nothing passes none: nothing has been loaded into
// a context that does not exist yet. A fork passes what its parent had loaded,
// because a fork starts from the parent's conversation and those instructions
// came across in it — a tool that did not know would let the model spend a
// call fetching a document already sitting above it.
//
// An id that is not one of found is marked all the same and means nothing: a
// skill can only be asked for by an id in found, so such an entry can never be
// reached. Refusing it here would be refusing a caller's bookkeeping over a
// case with no consequence.
func NewLoadSkill(found []skills.Skill, loaded []string) *LoadSkill {
	t := &LoadSkill{
		by:     make(map[string]skills.Skill, len(found)),
		listed: make([]string, 0, len(found)),
		loaded: make(map[string]bool, len(found)),
	}
	for _, s := range found {
		t.by[s.ID] = s
		t.listed = append(t.listed, s.ID)
	}
	for _, id := range loaded {
		t.loaded[id] = true
	}
	return t
}

// Loaded returns the ids of the skills handed over so far, in the order the
// run found them.
//
// The order is the run's and not the order they were loaded in, so that a tool
// built from this is built the same way every time: a map's own order varies
// between runs, and a fork assembled from one would differ from itself.
func (t *LoadSkill) Loaded() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	ids := make([]string, 0, len(t.loaded))
	for _, id := range t.listed {
		if t.loaded[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

// Definition describes the tool to the model. What a skill is and when to load
// one is in the system prompt, beside the list of them; what is here is how to
// call it.
func (t *LoadSkill) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: LoadSkillTool,
		Description: strings.TrimSpace(`
Load one of the available skills and read its instructions.

  {"id": "data-analyzer"}

The id is the one the skill is listed under in the system prompt, in bold, and
it is the only argument: the file is found for you, and no path is accepted
here. A skill may also be listed with a title in quotation marks, which is
there to be read and is not accepted here.

What comes back is the skill's whole text, and above it the directory the skill
is kept in, which is what any relative path in that text is relative to. The
text stays in this conversation and applies to everything you do afterwards, so
load a skill before doing the work it is about rather than after.

Each skill loads once. A second call for the same id is refused, because its
instructions are already above and rereading them there costs nothing. An id
that is not in the list is refused too, with the ids that are.`),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type": "string",
					"description": "The id of the skill to load, exactly as it " +
						"appears in bold in the list of available skills. Not the " +
						"title beside it, not a path, and not a skill you know of " +
						"from elsewhere: only the listed ids can be loaded.",
				},
			},
			"required":             []any{"id"},
			"additionalProperties": false,
		},
	}
}

// Announce implements tools.Announcer: the skill's title, which is what the
// user needs to see the run reach for.
//
// The call was made with the id, and the title is what is shown instead,
// because the two names exist for two different readers. The id is short and
// unambiguous, which is what a model calling a tool needs; the title is the
// sentence somebody wrote to say what the skill is about, which is what a
// person watching a run go by needs. Nothing is lost by showing only the
// title: the directory reported under it ends in the id.
//
// An id the run does not have is shown as the model wrote it. Announcing must
// not fail, and a call that is about to be refused is best shown as the call
// it was.
func (t *LoadSkill) Announce(args json.RawMessage) string {
	var p LoadSkillParams
	if err := json.Unmarshal(args, &p); err != nil {
		return ""
	}

	id := strings.TrimSpace(p.ID)
	if skill, ok := t.by[id]; ok {
		return skill.Name
	}
	return id
}

// Report implements tools.Reporter: it shows the directory the skill was
// loaded from, dimmed under the title the announcement gave. It is worth a
// line because what the model reads is the skill's own text, and a person
// watching has no other way to see which folder on the machine those
// instructions came out of.
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

	id := strings.TrimSpace(p.ID)
	if id == "" {
		return "", fmt.Errorf("%s: no skill was named; %s", LoadSkillTool, t.available())
	}

	skill, ok := t.by[id]
	if !ok {
		return "", fmt.Errorf("%s: there is no skill with the id %q in this run%s; %s",
			LoadSkillTool, id, t.titled(id), t.available())
	}

	// The file is read before the skill is marked as loaded, so a skill that
	// could not be read can be asked for again once whatever was wrong with
	// it is fixed.
	if err := t.claim(id); err != nil {
		return "", err
	}
	text, err := skill.Instructions()
	if err != nil {
		t.release(id)
		return "", fmt.Errorf("%s: %w", LoadSkillTool, err)
	}

	// The instructions are wrapped so that the model can tell where a document
	// somebody else wrote begins and ends. A skill is Markdown, which is what
	// everything around it in the conversation is too, and the id in the tag
	// says which of the available skills this text is. It is the id rather than
	// the title, because the tag is what the model would name the skill back
	// by, and that is the id everywhere else too.
	//
	// Where the skill is kept is said before the document rather than inside
	// it, because it is agi speaking and not the skill's author. A skill
	// mentions the files beside it by a relative path, and nothing else in the
	// conversation says what those paths are relative to; saying it first means
	// the model has read it by the time it reaches the first one, and the
	// result still ends at the closing tag.
	//
	// The title is not repeated here. The model read it in the catalogue when
	// it chose the skill, and what it needs in the result is what the tag gives
	// it: which of the available skills this text is, under the name it would
	// ask for one by.
	return fmt.Sprintf("%s%s\n\n<skill id=%q>\n%s\n</skill>",
		skillDirPrefix, skill.Path, id, text), nil
}

// claim marks a skill as loaded, refusing one that already is.
func (t *LoadSkill) claim(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.loaded[id] {
		return fmt.Errorf("%s: the skill %q is already loaded; its instructions are earlier "+
			"in this conversation, so reread them there rather than loading it again",
			LoadSkillTool, id)
	}
	t.loaded[id] = true
	return nil
}

// release takes a claim back, for a skill that turned out not to be loadable.
func (t *LoadSkill) release(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.loaded, id)
}

// available names the skills a call could have asked for, since a model that
// got the id wrong is one word away from getting it right and the list is
// short by construction.
func (t *LoadSkill) available() string {
	if len(t.listed) == 0 {
		return "this run was given no skills"
	}
	return "the skill ids available are: " + strings.Join(t.listed, ", ")
}

// titled reports, as a clause to hang on a refusal, that what was asked for is
// the title of a skill rather than its id.
//
// It is worth saying because it is the one wrong call the catalogue invites: a
// skill is listed under both, and a model that reaches for the words rather
// than the folder's name has picked the wrong half of an entry it read
// correctly. Naming the id it meant turns the next call into the right one,
// where the list of every id leaves it to search. Anything else gets the empty
// string and the plain refusal.
func (t *LoadSkill) titled(asked string) string {
	for _, id := range t.listed {
		if skill := t.by[id]; skill.Name == asked {
			return fmt.Sprintf(", though %q is the title of the skill whose id is %q, "+
				"which is what to load it by", asked, id)
		}
	}
	return ""
}
