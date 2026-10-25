package tools

import (
	"os"
	"strings"

	"github.com/fatih/color"
)

// Styles for the lines a tool call prints. They live here rather than in the
// harness because a tool decides which parts of its announcement are the
// command, the reason, and so on; the harness only decides where to print it.
//
// The color package disables itself when the output is not a terminal and when
// NO_COLOR is set, so these produce plain text when piped or captured.
var (
	// nameStyle renders the bracketed tool name that heads a call. Bold
	// yellow, and the point of it is to be the opposite of the detail lines
	// below: the label is the one word on the screen that says a call is
	// happening at all, so it is the thing to be drawn to, where everything
	// under it is there to be found once you have been. It was cyan, which
	// sat a neighbor away from the blue the detail lines were written in at
	// the time, and a label beside its own detail in a neighboring color is a
	// block of one wash rather than a heading and what it heads.
	nameStyle = color.New(color.FgYellow, color.Bold)
	// detailStyle renders subordinate detail, such as the command a tool is
	// about to run. It is the faint attribute, SGR 2, and nothing else: no
	// color is named, so the text keeps whatever foreground the terminal
	// already uses and is drawn a shade weaker than the prose beside it. That
	// is the convention for this kind of line in the programs sitting next to
	// agi — Claude Code prints "Resume this session with:" and the command
	// under it in exactly this and nothing more — and a second voice the user
	// already reads as subordinate elsewhere needs no argument here.
	//
	// Three earlier attempts say what it is not. Bright black is a color a
	// great many themes draw almost on top of their own background, so the
	// lines it marked could not be read at all; faint is not that, because it
	// weakens the foreground the theme already chose rather than naming a
	// color of its own, and a theme that is readable stays readable. White
	// was that same default foreground at full strength, so the lines looked
	// like ordinary text and were marked as nothing. Blue did sort them from
	// the text around them, but it spent a color on them, and a color is a
	// louder mark than these lines are asking for: the tool label above is
	// the thing meant to be loud.
	//
	// Where a color is used, which is nameStyle above, only the sixteen named
	// ones are, so that what agi looks like is decided by the palette of the
	// terminal it is running in rather than by a number written down in this
	// file. The one exception is the diff band below, and the rule that
	// allows it is narrow: a color written as a number may be used only as a
	// pair, the text and the background both set, on every cell it covers,
	// and switched off before the line ends. What a named color protects
	// against is a color agi chose landing on a color the theme chose —
	// bright black on a dark background, a dark foreground on a dark band —
	// and a cell whose two colors are both agi's has nothing of the theme's
	// left in it to collide with. A lone foreground or background written as
	// a number is still refused.
	detailStyle = color.New(color.Faint)
	// thinkingStyle renders the [thinking] label that heads a model's
	// reasoning. It is nameStyle's shape in another color, because the line
	// it heads is a bracketed label like a tool call's and a different thing:
	// a tool call is agi about to act on the machine, and this is the model
	// deliberating, which touches nothing.
	//
	// Cyan rather than yellow. The label has to be told apart at a glance
	// from the tool calls it sits among, which a neighboring color would not
	// manage, and cyan is far enough from yellow to read as another thing
	// while being neither the red agi uses for nothing else nor a color a
	// dark theme draws near its own background. It is one of the sixteen,
	// for the reason given above.
	thinkingStyle = color.New(color.FgCyan, color.Bold)
)

// The two sides of a change are red and green because that is what a diff is
// drawn in everywhere the user has read one, git first among them, and inside
// a diff the two colors mean "removed" and "added" rather than failure and
// success — which is why green, never a candidate for anything else here, is
// right for this and for nothing else.
//
// A changed line is drawn as a band: a row painted from edge to edge in a dark
// red or a dark green, a near-white text on it, the line number in a muted
// tint of the band, and the words that actually changed on a stronger shade
// of the same color. That is how GitHub, delta and Claude Code draw one, and
// it reads at a glance as one block of change with the change inside it
// picked out.
//
// The band's colors are numbers and not named colors, which the rule above
// allows because every cell the band covers sets both its colors: the theme's
// own background and foreground are not used anywhere inside it. It was red
// and green text with the changed words in reverse video, on the grounds that
// a red or green background under the theme's own foreground is unreadable
// on a good many themes. That is true, and it is why the band sets the
// foreground as well, and the reason there is one palette rather than one per
// theme: the band is dark with light text on it, which reads the same on a
// black terminal and a white one. On a light theme it is heavier than a
// palette chosen for one would be, and that is the whole of the cost.
// Detecting the theme would mean asking the terminal for its background and
// reading the reply off standard input, which is where the prompt comes from.
//
// Every pair has a contrast ratio of at least 4 to 1, the text on the stronger
// shade being the closest. The 256-color pairs are the nearest the xterm
// palette has to the 24-bit ones, and coarser for it. A terminal that has
// not said it draws more than sixteen colors gets the diff as it was before
// the band, which is still drawn in named colors only.

// diffDepth is how many colors the terminal is taken to draw, read once as the
// process starts. It decides only how a diff is drawn; every other style here
// is one of the sixteen and works on any terminal.
var diffDepth = depthFrom(os.Getenv("COLORTERM"), os.Getenv("TERM"))

type colorDepth int

const (
	depth16 colorDepth = iota
	depth256
	depthTrue
)

// depthFrom reads what a terminal says it can draw. COLORTERM is what
// terminals that draw 24-bit color set to say so; a TERM ending in -direct is
// the terminfo spelling of the same claim, and one containing 256color names
// the 256-color palette. Anything else is taken to be sixteen colors, which is
// the safe way round: a terminal wrongly given sixteen draws the old diff,
// where one wrongly given 24-bit color may draw it illegibly.
//
// Known limitation: COLORTERM is not passed over ssh by default, nor through
// sudo, so a terminal that draws 24-bit color can be given the 256-color band
// or the sixteen-color diff for want of being told; exporting
// COLORTERM=truecolor is the fix, and the README says so. The reverse is the
// case that could hurt — a terminal claiming 24-bit color it does not draw. One
// that ignores the sequences draws the diff in the theme's own colors, which
// is plain and readable; one that kept the light text and dropped the dark
// background would be unreadable on a light theme. No terminal in common use
// does that, and agi cannot ask the terminal without reading its reply from
// standard input.
func depthFrom(colorterm, term string) colorDepth {
	switch strings.ToLower(colorterm) {
	case "truecolor", "24bit":
		return depthTrue
	}
	switch {
	case strings.HasSuffix(term, "-direct"):
		return depthTrue
	case strings.Contains(term, "256color"):
		return depth256
	}
	return depth16
}

// diffSide is how one side of a change is drawn: the margin holding the line
// number, the text of the line, and the parts of it that actually changed.
// band is the background the row is painted in as an SGR parameter, and is
// empty for the sixteen-color diff, which paints no background.
type diffSide struct {
	band                   string
	margin, text, emphasis *color.Color
}

// rgb and xterm are a foreground and a background together, as 24-bit and as
// 256-color attributes. There is no constructor for one without the other,
// which is the rule above kept by the type of thing that can be written here.
func rgb(fg, bg [3]int) *color.Color {
	return color.New(38, 2, color.Attribute(fg[0]), color.Attribute(fg[1]), color.Attribute(fg[2]),
		48, 2, color.Attribute(bg[0]), color.Attribute(bg[1]), color.Attribute(bg[2]))
}

func xterm(fg, bg int) *color.Color {
	return color.New(38, 5, color.Attribute(fg), 48, 5, color.Attribute(bg))
}

var (
	bandText    = [3]int{230, 237, 243}
	removedBand = [3]int{84, 36, 38}
	addedBand   = [3]int{28, 68, 40}

	diffSides = map[colorDepth][2]diffSide{
		depth16: {
			{margin: detailStyle, text: color.New(color.FgRed), emphasis: color.New(color.FgRed, color.ReverseVideo)},
			{margin: detailStyle, text: color.New(color.FgGreen), emphasis: color.New(color.FgGreen, color.ReverseVideo)},
		},
		depth256: {
			{band: "48;5;52", margin: xterm(181, 52), text: xterm(255, 52), emphasis: xterm(255, 88)},
			{band: "48;5;22", margin: xterm(151, 22), text: xterm(255, 22), emphasis: xterm(255, 28)},
		},
		depthTrue: {
			{
				band:     "48;2;84;36;38",
				margin:   rgb([3]int{216, 160, 160}, removedBand),
				text:     rgb(bandText, removedBand),
				emphasis: rgb(bandText, [3]int{143, 58, 60}),
			},
			{
				band:     "48;2;28;68;40",
				margin:   rgb([3]int{143, 191, 154}, addedBand),
				text:     rgb(bandText, addedBand),
				emphasis: rgb(bandText, [3]int{46, 125, 67}),
			},
		},
	}
)

func removedSide() diffSide { return diffSides[diffDepth][0] }
func addedSide() diffSide   { return diffSides[diffDepth][1] }

// start draws the start of a changed row — the line number and whatever leads
// up to the text — and is what paints the row's band.
//
// The band reaches the right edge by erasing the row in the band's color
// before anything is written on it, since erasing fills what it erases with
// the background in force. It erases first, not last, on purpose: erasing
// after the text is the usual way and has a known bug, where a line exactly
// as wide as the terminal leaves the cursor waiting on its last column and
// erasing from there takes that last character with it — GNU grep has an
// option to stop doing it for that reason. Erasing an empty row takes nothing.
// The background is switched off again by the reset at the end of whatever
// is written last, so nothing past the row is painted when the screen
// scrolls.
//
// Known limitations of painting it this way. The band assumes the row starts
// where the margin does: anything printed before it on the same row is left
// as it was, and the erase paints from the margin on. A line longer than the
// terminal wraps, and only its first row was erased, so the rows it wraps
// onto are painted as far as the text goes and no further. The band is drawn
// at the width the terminal had when it was printed and does not follow a
// resize. And a terminal that erases in its default background rather than
// the one in force — tmux or GNU screen without background-color erase, a
// pager such as less -R — paints the text and leaves the rest of the row
// plain, which is a ragged band and nothing worse.
func (d diffSide) start(s string) string {
	if d.band == "" || color.NoColor {
		return d.margin.Sprint(s)
	}
	return "\x1b[" + d.band + "m\x1b[K" + d.margin.Sprint(s)
}

// Name renders a tool name as a bracketed, colored label, for example [bash].
func Name(name string) string { return nameStyle.Sprintf("[%s]", name) }

// Thinking renders the label that heads a model's reasoning.
//
// It takes no name because there is only ever one thing to say: a tool label
// names which of many tools is being called, where this marks a block as the
// model's deliberation, and there is one kind of that. The word is written
// down here rather than at the print site so that the label and the color it
// is drawn in cannot drift apart.
func Thinking() string { return thinkingStyle.Sprint("[thinking]") }

// Detail renders subordinate text in the secondary color, one line at a time.
//
// Each line is styled and closed on its own rather than the whole text being
// wrapped in a single span. A span that opens on one line and closes on
// another asks the terminal to carry the attribute across the line break, and
// a great many of them do not: the first line comes out colored and every line
// after it plain, which is what a multi-line bash command looked like.
//
// A blank line is left as it is, so nothing is written where there is nothing
// to color — the same reason block leaves the blank lines inside a command
// blank rather than padding them.
func Detail(s string) string { return perLine(detailStyle, s) }

// RemovedMargin starts a row of text a change takes out: the line number, or
// whatever leads up to the text, and the row's band where there is one. It
// is for a single row, and a row of text drawn by Removed must start with it,
// or the band covers the text and not the row.
func RemovedMargin(s string) string { return removedSide().start(s) }

// AddedMargin starts a row of text a change puts in.
func AddedMargin(s string) string { return addedSide().start(s) }

// Removed renders text a change takes out, the - side of a diff, one line at a
// time for the reason Detail gives: the band's light text on its color, or
// red on a terminal of sixteen colors.
func Removed(s string) string { return perLine(removedSide().text, s) }

// Added renders text a change puts in, the + side of a diff.
func Added(s string) string { return perLine(addedSide().text, s) }

// RemovedEmphasis renders the part of a removed line that changed, inside the
// rest of the line drawn by Removed: a stronger shade of the band, or on
// sixteen colors the line's red in reverse video, which is how git's
// diff-highlight marks the same thing.
func RemovedEmphasis(s string) string { return perLine(removedSide().emphasis, s) }

// AddedEmphasis renders the part of an added line that changed, inside the
// rest of the line drawn by Added.
func AddedEmphasis(s string) string { return perLine(addedSide().emphasis, s) }

// perLine styles every line of s on its own and leaves blank lines alone.
func perLine(style *color.Color, s string) string {
	if !strings.Contains(s, "\n") {
		return style.Sprint(s)
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		lines[i] = style.Sprint(line)
	}
	return strings.Join(lines, "\n")
}

// SingleLine collapses text to one line for display, marking the collapse.
//
// It is for arguments whose point is what they name rather than what they say:
// the path a file is being written to matters more than the contents going
// into it, and a listing of the contents would push the next call off screen.
//
// It is here rather than beside one tool because two packages announce a call
// this way and the question they are answering is the same one, which is the
// argument that put Truncate in this package as well.
func SingleLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		return strings.TrimSpace(s[:i]) + " ..."
	}
	return s
}
