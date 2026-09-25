package cli

import (
	"strings"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/tools/controlflow"
	"github.com/ChrisVilches/agi/transport"
)

// agentSetup is what one kind of agent in a run was given: the system prompt
// it reads and the tools it may call. A run has two of them — its own, and the
// one a subagent it spawned would get — and they are described the same way
// because a subagent is an ordinary run and differs in nothing else.
type agentSetup struct {
	system   string
	registry *tools.Registry
}

// previewAgent is the name the tools shown for a subagent are built under.
//
// A real subagent's name decides where its tools file the copies they keep and
// nothing a tool says about itself, so nothing on the screen depends on which
// name this is. It is not a number, because the numbers are handed out when
// the model asks for subagents and a preview that claimed to be the first one
// would be naming an agent the run may never spawn.
const previewAgent = "subagent"

// preamble prints what a --verbose run says about itself before the first
// turn, and nothing at all otherwise.
//
// What it prints is everything that decides how the run will go and that the
// run did not get from the user: which model is being asked, through which
// address, the shape the answer has to take, the system prompt the model will
// read, what each tool was described to it as, and the same two things again
// for a subagent, which reads a prompt of its own and is given fewer tools.
// None of that appears again once the conversation starts, and all of it is
// assembled from how agi was invoked rather than stored anywhere, so a run
// watched without it can only be guessed at.
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
func preamble(out output, client transport.Backend, schema *model.Schema, agent agentSetup, sub *agentSetup) {
	if !out.verbose {
		return
	}

	out.step("model %s via %s, %d tool(s)\n\n", client.Model(), client.BaseURL(), agent.registry.Len())
	if schema != nil {
		out.step("schema %s\n\n", schema)
	}

	// Every tool explained so far, by name, so that the subagent's list can
	// tell an explanation the user has just read from one they have not.
	explained := map[string]string{}

	out.step("%s\n%s\n\n", tools.Detail("system prompt"), indent(agent.system))
	out.step("%s\n", tools.Detail("tools"))
	toolList(out, agent.registry, explained)

	if sub == nil {
		return
	}
	out.step("%s\n%s\n\n", tools.Detail("subagent system prompt"), indent(sub.system))
	out.step("%s\n", tools.Detail("subagent tools"))
	toolList(out, sub.registry, explained)
}

// toolList prints one agent's tools in the order they are offered to the
// model, and adds each to explained as it goes.
//
// A tool explained in an earlier list is named and nothing more. Which tools
// an agent has is the whole of what this list is for, and repeating three
// paragraphs the user read a moment ago would bury it — the more so because a
// subagent's tools are mostly the run's own, so nearly every line of a second
// full listing would be one the reader has to compare against the first to
// discover it says the same thing.
//
// What counts as already explained is the description and not the name. The
// same tool can be described differently to two agents — a tool that keeps
// copies of what it changes says so only when there is somewhere to keep them
// — and pointing the user upward at a description that is not the one this
// agent reads would be worse than saying nothing.
//
// A block of prose is given air around it and a bare name is not, so a list of
// names stays a list rather than becoming a column of paragraphs' worth of
// spacing.
func toolList(out output, r *tools.Registry, explained map[string]string) {
	block := false
	for i, def := range r.Definitions() {
		text := describe(def)
		if explained[def.Name] == text {
			if i > 0 && block {
				out.step("\n")
			}
			out.step("%s %s\n", tools.Name(def.Name), tools.Detail("explained above"))
			block = false
			continue
		}
		if i > 0 {
			out.step("\n")
		}
		out.step("%s\n%s\n", tools.Name(def.Name), indent(text))
		explained[def.Name] = text
		block = true
	}
	out.step("\n")
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

// subagentPreview returns what a subagent this run spawned would be given, for
// the run to say so before one is asked for.
//
// It goes through workspaceRegistry and subagentSystemPrompt, which are the
// two calls subagentRunner makes and the reason a subagent's tools are the
// run's own minus the control-flow ones. A list written out here instead would
// be right until the next tool was added, and would then be a run describing
// subagents that do not exist.
//
// A run that was given no way to spawn one has nothing to say about them, and
// says nothing: that is the rule keeping an unregistered tool out of the
// system prompt, applied to what a run says about itself. Whether a run has
// the tool is asked of the registry by the name the model calls it by, because
// that is the only thing that decides it.
func subagentPreview(r *tools.Registry, backups string, user []tools.Tool) (*agentSetup, error) {
	if _, ok := r.Lookup(controlflow.SubagentsTool); !ok {
		return nil, nil
	}
	sub, err := workspaceRegistry(backups, previewAgent, user)
	if err != nil {
		return nil, err
	}
	return &agentSetup{system: subagentSystemPrompt(sub), registry: sub}, nil
}
