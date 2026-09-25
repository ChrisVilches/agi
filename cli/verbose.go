package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/transport"
)

// agentSetup is what one agent in a run was given: the system prompt it reads
// and the tools it may call. The run's own agent has one and so does every
// subagent it spawns, and they are described the same way because a subagent
// is an ordinary run and differs in nothing else.
type agentSetup struct {
	system   string
	registry *tools.Registry
}

// preamble prints what a --verbose run says about itself before the first
// turn, and nothing at all otherwise.
//
// What it prints is everything that decides how the run will go and that the
// run did not get from the user: which model is being asked, through which
// address, the shape the answer has to take, the system prompt the model will
// read, and what each tool was described to it as. None of that appears again
// once the conversation starts, and all of it is assembled from how agi was
// invoked rather than stored anywhere, so a run watched without it can only be
// guessed at.
//
// It says nothing about subagents. A run that describes an agent it has not
// spawned can only describe a hypothetical one — which skills it would be
// given is decided task by task, when the model asks — so what it showed was
// a subagent with no skills, which is a subagent this run may never have. The
// real ones say what they were given as they start, in subagentIntro, where
// there is an agent to say it about.
//
// It is the same account the log opens with, said once to whoever is watching.
// That is not one of the three mechanisms borrowing another's job: the log is
// the record and is read afterwards, in full and at leisure, while this exists
// to be read as the run happens and is discarded when nobody is there.
//
// It is printed from runSession rather than beside the run's other opening
// lines because the system prompt does not exist until then. The prompt is
// assembled from the base and the registered tools, and this is the first
// point at which there is one to show.
func preamble(out output, client transport.Backend, schema *model.Schema, agent agentSetup) {
	if !out.verbose {
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "model %s via %s, %d tool(s)\n\n", client.Model(), client.BaseURL(), agent.registry.Len())
	if schema != nil {
		fmt.Fprintf(&b, "schema %s\n\n", schema)
	}
	describeAgent(&b, agent, map[string]string{})
	out.step("%s", b.String())
}

// subagentIntro is what a --verbose run says about one subagent as it starts:
// which agent this is, what it was asked to do, the prompt it will read, and
// the tools it may call. The skills it was given are part of the prompt, which
// lists them, so there is nothing separate to print for them.
//
// The name leads because several subagents work at once and this is how the
// user tells their blocks apart. Which block comes first is whichever agent
// got going first, which is scheduling, so nothing here may depend on the
// order: every block names its own agent and is complete on its own.
//
// explained is what the run's own listing already said about each of its
// tools, so a tool the user has read about is named rather than explained
// again. It is the caller's copy and is written to as this list is printed,
// which is why each subagent gets one of its own: they run at the same time
// and would otherwise be writing into one map.
//
// It returns the text rather than printing it, because a block is several
// screens long and two of them printed a line at a time would interleave into
// a page belonging to neither. The caller writes it out in one piece.
func subagentIntro(name, title string, agent agentSetup, explained map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n\n", tools.Name(name), tools.Detail(title))
	describeAgent(&b, agent, explained)
	return b.String()
}

// describeAgent writes what one agent was given: the prompt it reads and then
// the tools it may call, in that order, which is the order they decide what
// the agent does in.
func describeAgent(w io.Writer, agent agentSetup, explained map[string]string) {
	fmt.Fprintf(w, "%s\n%s\n\n", tools.Detail("system prompt"), indent(agent.system))
	fmt.Fprintf(w, "%s\n", tools.Detail("tools"))
	toolList(w, agent.registry, explained)
}

// explainedBy returns what a listing of an agent's tools would have said about
// each of them, by name, so that a later listing can tell a description the
// user has read from one they have not.
func explainedBy(r *tools.Registry) map[string]string {
	explained := make(map[string]string, r.Len())
	for _, def := range r.Definitions() {
		explained[def.Name] = describe(def)
	}
	return explained
}

// toolList prints one agent's tools in the order they are offered to the
// model, and adds each to explained as it goes.
//
// A tool explained in an earlier list is named and nothing more. Which tools
// an agent has is the whole of what this list is for, and repeating three
// paragraphs the user has already read would bury it — the more so because a
// subagent's tools are mostly the run's own, so nearly every line of a second
// full listing would be one the reader has to compare against the first to
// discover it says the same thing.
//
// What counts as already explained is the description and not the name. The
// same tool can be described differently to two agents, and pointing the user
// upward at a description that is not the one this agent reads would be worse
// than saying nothing.
//
// A block of prose is given air around it and a bare name is not, so a list of
// names stays a list rather than becoming a column of paragraphs' worth of
// spacing.
func toolList(w io.Writer, r *tools.Registry, explained map[string]string) {
	block := false
	for i, def := range r.Definitions() {
		text := describe(def)
		if explained[def.Name] == text {
			if i > 0 && block {
				fmt.Fprint(w, "\n")
			}
			fmt.Fprintf(w, "%s %s\n", tools.Name(def.Name), tools.Detail("explained above"))
			block = false
			continue
		}
		if i > 0 {
			fmt.Fprint(w, "\n")
		}
		fmt.Fprintf(w, "%s\n%s\n", tools.Name(def.Name), indent(text))
		explained[def.Name] = text
		block = true
	}
	fmt.Fprint(w, "\n")
}

// describe renders what the model was told about one tool: its description and
// nothing else.
//
// The parameters are left out on purpose. They are a JSON Schema object, which
// is several times the length of the prose around it and says only what shape
// a call takes — a question nobody watching a run is asking, and one the log
// answers for anyone who is, since it records every call's arguments as they
// were made. What is worth reading here is the part written to be read: the
// sentences that decide whether the model reaches for the tool at all.
//
// A tool with no description is shown as such rather than as a blank, since an
// empty line under a name looks like a display fault rather than the missing
// description it is.
func describe(def model.ToolDefinition) string {
	if text := strings.TrimSpace(def.Description); text != "" {
		return text
	}
	return "(no description)"
}
