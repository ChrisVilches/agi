package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/transport"
	"github.com/urfave/cli/v3"
)

// modelsCommand lists what the configured backend offers.
//
// It is the first true subcommand, and it is one because it is not a run. Every
// flag agi has varies a conversation with a model; this asks the endpoint a
// question of its own, sends no conversation, starts no session, writes no log,
// and registers no tools. A flag that made agi do something else entirely
// instead of running the agent would be a flag in name only.
//
// The cost of it is that `models` is now a reserved first word: urfave/cli
// consults the command list for the first non-flag argument, so the prompt that
// is exactly that word reaches this instead of the agent. Nothing else changes
// — a prompt that merely begins with it is one argument and matches nothing,
// and a prompt on standard input is not an argument at all — and the word is
// short enough to be worth the one collision.
func modelsCommand() *cli.Command {
	return &cli.Command{
		Name:  "models",
		Usage: "list the models the configured backend offers",
		Description: "Asks the backend named by " + EnvBackend + " which models it serves, and prints them, " +
			"with the model " + EnvDefaultModel + " names under the listing.",
		Action:       modelsAction,
		OnUsageError: usageError,
	}
}

// modelsAction asks the configured backend for its catalogue and prints it,
// then says which of those names a run would use.
//
// It opens the endpoint without a model, which is the whole reason
// openEndpoint exists: this is the command every missing-model error sends the
// user to, and it would be of no use to them if it refused for want of the
// thing they are running it to find out. Reading the variable afterwards to
// report it is not that: nothing is required of it, and a machine that has
// chosen nothing still gets its listing, with a line saying so.
func modelsAction(ctx context.Context, cmd *cli.Command) error {
	// The command takes nothing, and the likeliest way to land here with an
	// argument is a prompt whose first word is `models` — which did not reach
	// the agent and must not look as though it had.
	if n := cmd.Args().Len(); n > 0 {
		return fmt.Errorf("models takes no arguments, but got %d; to run the agent on a prompt starting with that word, pipe it in", n)
	}

	backend, err := openEndpoint()
	if err != nil {
		return err
	}
	models, err := backend.Models(ctx)
	if err != nil {
		return err
	}
	// An endpoint that lists nothing has answered, so this is not a failure of
	// the request — but printing nothing at all is indistinguishable from a
	// listing that failed to reach the screen, and there is nothing agi could
	// be run against here anyway. Saying so, with the address it asked, is the
	// one useful thing left.
	if len(models) == 0 {
		return fmt.Errorf("%s lists no models", backend.BaseURL())
	}
	if err := writeModels(cmd.Root().Writer, models); err != nil {
		return err
	}
	sayDefaultModel(newOutput(cmd.Root().Writer, cmd.Root().ErrWriter, cmd.Bool("verbose")))
	return nil
}

// sayDefaultModel prints which of the names above a run will use, under the
// listing it belongs to.
//
// The listing answers "what can I run on" and leaves "what am I running on"
// hanging, which is the question somebody reading it usually has next: a name
// is chosen once and then lives in a shell profile, where it is out of sight
// exactly when the catalogue is in front of you. Printing it here costs a
// line and saves looking the variable up somewhere else.
//
// Nothing is checked. The name is not looked for among the models printed
// above, and a name absent from them is not remarked on, because a listing is
// a good guide and not a guarantee — `deepseek-chat` is absent from DeepSeek's
// catalogue and resolves to the model that replaced it, and a gateway's alias
// is a route it set up rather than a row it prints. A warning here would be
// agi second-guessing the endpoint, which is the one thing it does not do
// about model names.
//
// It is progress and goes where all of agi's progress goes — the screen at a
// terminal, standard error in a piped --verbose run, nowhere at all in a piped
// run that did not ask. The listing is this command's answer, and a pipe
// carrying it to `grep` or `awk` must carry the models and nothing else; a
// footer on that stream would be one more row that is not a model.
func sayDefaultModel(out output) {
	if model := defaultModel(); model != "" {
		out.step("\n%s\n", tools.Detail(fmt.Sprintf("%s is %s, which every run uses unless --model names another", EnvDefaultModel, model)))
		return
	}
	// Not an error: listing a catalogue is the one thing that runs without a
	// model, and this is the command a missing-model refusal sends people to.
	// So it says what is missing, in the words the refusal would use, before
	// they spend a run finding out.
	out.step("\n%s\n", tools.Detail(fmt.Sprintf("%s is not set: a run has to be given a model, either by setting it or with --model", EnvDefaultModel)))
}

// gutter is how many spaces separate one column from the next.
const gutter = 3

// writeModels prints one model per line, in columns, sorted by name.
//
// The order is agi's and not the endpoint's. A gateway returns its catalogue
// in whatever order it assembled it from the providers behind it, which is
// neither stable between calls nor meaningful to read, and a listing somebody
// is scanning for one name is a listing that should be in the order they would
// look in.
func writeModels(w io.Writer, models []transport.Model) error {
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

	// The name leads because it is the field a request needs and the one a
	// reader came for; everything after it is there to choose between names
	// that are already in front of you.
	head := []string{"MODEL", "OWNED BY", "CONTEXT", "MAX OUTPUT"}
	rows := make([][]string, 0, len(models))
	for _, m := range models {
		rows = append(rows, []string{
			m.ID,
			said(m.OwnedBy),
			counted(m.ContextWindow),
			counted(m.MaxOutputTokens),
		})
	}

	widths := make([]int, len(head))
	for i, h := range head {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	// The header is written through tools.Detail and the widths are worked out
	// here rather than by text/tabwriter, for that reason: tabwriter measures a
	// cell in bytes, and the escape sequence that draws the header faint is
	// bytes the terminal never puts on screen, so every column under it would
	// be padded to the wrong place. The styling disables itself when nobody is
	// at a terminal, which is where the columns matter least anyway.
	if _, err := fmt.Fprintln(w, tools.Detail(columns(head, widths))); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintln(w, columns(row, widths)); err != nil {
			return err
		}
	}
	return nil
}

// columns lays one line's cells out at the given widths. The last cell is left
// unpadded, so no line ends in trailing space for whatever reads it next.
func columns(cells []string, widths []int) string {
	var b strings.Builder
	for i, cell := range cells {
		b.WriteString(cell)
		if i == len(cells)-1 {
			break
		}
		b.WriteString(strings.Repeat(" ", widths[i]-len(cell)+gutter))
	}
	return b.String()
}

// counted renders a token count, or a dash where the backend said nothing.
// Zero is never a real one — no model carries a context of nothing — so it
// reads as the absence it is rather than as a number.
func counted(n int) string {
	if n <= 0 {
		return "-"
	}
	return strconv.Itoa(n)
}

// said renders a field the endpoint may have left out, for the same reason.
func said(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
