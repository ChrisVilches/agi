package cli

import (
	"strings"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/transport"
)

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
// It is the same account the log opens with, said once to whoever is watching.
// That is not one of the three mechanisms borrowing another's job: the log is
// the record and is read afterwards, in full and at leisure, while this exists
// to be read as the run happens and is discarded when nobody is there.
//
// It is printed from runSession rather than beside the run's other opening
// lines because the system prompt does not exist until then. The prompt is
// assembled from the base and the registered tools, and this is the first
// point at which there is one to show.
func preamble(out output, client transport.Backend, registry *tools.Registry, schema *model.Schema, system string) {
	if !out.verbose {
		return
	}

	out.step("model %s via %s, %d tool(s)\n\n", client.Model(), client.BaseURL(), registry.Len())
	if schema != nil {
		out.step("schema %s\n\n", schema)
	}

	out.step("%s\n%s\n\n", tools.Detail("system prompt"), indent(system))
	for _, def := range registry.Definitions() {
		out.step("%s\n%s\n\n", tools.Name(def.Name), indent(describe(def)))
	}
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
