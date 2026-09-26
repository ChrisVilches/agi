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

// skillTitlePrefix and skillDirPrefix begin the two lines a result opens with:
// which skill this is, in the words its author titled it, and the directory it
// is kept in. They are constants because two things need each line: Call
// writes it, and Report finds it again to show the user what the model was
// told.
//
// Each line ends with the part Report wants, and the sentences are worded so
// that they do, which is what lets Report cut a prefix off and be left holding
// the title or the path alone.
const (
	skillTitlePrefix = "The skill loaded here is titled: "

	skillDirPrefix = "Any relative path in this skill's text is relative to the " +
		"directory the skill is kept in, which is "
)

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

What comes back is the skill's whole text, and above it the skill's title and
the directory it is kept in, which is what any relative path in that text is
relative to. The text stays in this conversation and applies to everything you
do afterwards, so load a skill before doing the work it is about rather than
after.

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

// Announce implements tools.Announcer: the id is the whole of the call, and it
// is what the user needs to see the run reach for. It is the id and not the
// title because this is the call as the model made it, and a call is announced
// as it was made.
func (t *LoadSkill) Announce(args json.RawMessage) string {
	var p LoadSkillParams
	if err := json.Unmarshal(args, &p); err != nil {
		return ""
	}
	return strings.TrimSpace(p.ID)
}

// Report implements tools.Reporter: two lines under the call, the skill's
// title and then the directory it was loaded from.
//
// Both are worth a line, and for different reasons. The title says in words
// what the run just took on, which the id beside a folder's name only hints
// at; the directory says which folder on the machine those instructions came
// out of, which nothing else shows, since what the model reads is the skill's
// own text.
//
// They are read back out of the result rather than remembered, so that what
// the user is shown is what the model was told. A line that is not there is
// left out rather than standing empty.
func (t *LoadSkill) Report(result string) string {
	var lines []string
	for _, line := range strings.Split(result, "\n") {
		if title, ok := strings.CutPrefix(line, skillTitlePrefix); ok {
			lines = append(lines, "  "+title)
		}
		if dir, ok := strings.CutPrefix(line, skillDirPrefix); ok {
			lines = append(lines, "  "+dir)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return tools.Detail(strings.Join(lines, "\n"))
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
	// The two lines above the document are agi speaking and not the skill's
	// author, which is why they are outside it. The title says which skill this
	// is in the words somebody wrote for the purpose. The directory is there
	// because a skill mentions the files beside it by a relative path and
	// nothing else in the conversation says what those paths are relative to;
	// saying it first means the model has read it by the time it reaches the
	// first one, and the result still ends at the closing tag.
	return fmt.Sprintf("%s%s\n%s%s\n\n<skill id=%q>\n%s\n</skill>",
		skillTitlePrefix, skill.Name, skillDirPrefix, skill.Path, id, text), nil
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
