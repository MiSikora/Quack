package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"quack/internal/recipe"
	"quack/internal/wire"
)

// prompt is the picker: the input, and under it the list of the actions whose id the text matches, best first, one of
// them selected. Empty text lists every action, text that starts with '$' matches the app's recipes instead, and text
// that starts with '/' the TUI's commands.
type prompt struct {
	input   textinput.Model
	actions []wire.Action
	// recipes are the app's recipes, sorted by name, and recipePath the file they come from.
	recipes    []recipe.Recipe
	recipePath string
	// selected indexes choices().
	selected int
	width    int
}

// choice is an action, a recipe or a command that the text matches, with the byte offsets of the matched characters of
// its id.
type choice struct {
	id          string
	description string
	action      wire.Action
	// recipe is the chosen recipe, and command the chosen command. Both are nil for an action.
	recipe  *recipe.Recipe
	command *command
	matched []int
}

func newPrompt(actions []wire.Action) prompt {
	input := textinput.New()
	input.Prompt = "> "
	input.Placeholder = "find an action, $ for recipes, / for commands"
	styles := input.Styles()
	styles.Focused.Prompt = promptStyle
	styles.Blurred.Prompt = promptStyle
	input.SetStyles(styles)
	return prompt{input: input, actions: actions}
}

// choices are what the words of the text match, fuzzily and ignoring case, best first, and how many there are to
// match. Blank text matches every action in the app's order, a lone '$' every recipe by name, and a lone '/' every
// command in the table's order.
func (p *prompt) choices() ([]choice, int) {
	text := strings.TrimSpace(p.input.Value())
	var all []choice
	switch {
	case strings.HasPrefix(text, "$"):
		for _, r := range p.recipes {
			all = append(all, choice{id: "$" + r.Name, description: r.Description, recipe: &r})
		}
	case strings.HasPrefix(text, "/"):
		for _, c := range commands() {
			all = append(all, choice{id: c.name, description: c.description, command: &c})
		}
	default:
		for _, a := range p.actions {
			all = append(all, choice{id: a.ID, description: a.Description, action: a})
		}
	}
	if text == "" || text == "$" || text == "/" {
		return all, len(all)
	}
	ids := make([]string, len(all))
	for i, c := range all {
		ids[i] = c.id
	}
	var matches []choice
	for _, match := range find(text, ids) {
		c := all[match.Index]
		c.matched = match.MatchedIndexes
		matches = append(matches, c)
	}
	return matches, len(all)
}

func (p *prompt) selection() (choice, bool) {
	choices, _ := p.choices()
	if len(choices) == 0 {
		return choice{}, false
	}
	return choices[p.selected], true
}

func (p *prompt) value() string { return p.input.Value() }

// update moves the selection or edits the text. Down moves to the next match, away from the input, and up back toward
// the best one. A new text selects its best match.
func (p *prompt) update(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "down", "ctrl+n":
		choices, _ := p.choices()
		p.selected = min(max(0, len(choices)-1), p.selected+1)
		return nil
	case "up", "ctrl+p":
		p.selected = max(0, p.selected-1)
		return nil
	}
	text := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != text {
		p.selected = 0
	}
	return cmd
}

// forward hands the input a message it may be waiting for, such as a cursor blink.
func (p *prompt) forward(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd
}

// clear empties the text and selects the first row, and says whether there was text.
func (p *prompt) clear() bool {
	had := p.input.Value() != ""
	p.input.SetValue("")
	p.selected = 0
	return had
}

// setActions replaces the actions, keeping the selected action selected while the text still matches it.
func (p *prompt) setActions(actions []wire.Action) {
	p.keepSelection(func() { p.actions = actions })
}

// setRecipes replaces the recipes and the file they come from, keeping the selected recipe selected while the text
// still matches it.
func (p *prompt) setRecipes(recipes []recipe.Recipe, path string) {
	p.keepSelection(func() { p.recipes, p.recipePath = recipes, path })
}

// keepSelection makes change, and selects again what was selected while the text still matches it.
func (p *prompt) keepSelection(change func()) {
	selected, ok := p.selection()
	change()
	p.selected = 0
	p.resize(p.width)
	if !ok || selected.command != nil {
		return
	}
	choices, _ := p.choices()
	for i, c := range choices {
		if c.id == selected.id {
			p.selected = i
		}
	}
}

func (p *prompt) focus() tea.Cmd { return p.input.Focus() }

func (p *prompt) blur() { p.input.Blur() }

// resize leaves room on the input's line for the counter at its widest.
func (p *prompt) resize(width int) {
	p.width = width
	widest := len(strconv.Itoa(max(len(p.actions), len(p.recipes), len(commands()))))
	p.input.SetWidth(max(1, width-ansi.StringWidth(p.input.Prompt)-2*widest-3))
}

// view is the input's line, with how many choices match out of how many there are at its right end.
func (p *prompt) view() string {
	choices, total := p.choices()
	counter := statusStyle.Render(strconv.Itoa(len(choices)) + "/" + strconv.Itoa(total))
	line := p.input.View()
	return line + strings.Repeat(" ", max(1, p.width-ansi.StringWidth(line)-ansi.StringWidth(counter))) + counter
}

// list is rows lines of what the text matches, best first, over blank ones, so that the list keeps its height while
// the user types and the best match stays next to the input. The choices scroll to keep the selection in sight. Without
// any recipes, '$' says where to write them.
func (p *prompt) list(rows int) []string {
	choices, total := p.choices()
	if len(choices) == 0 {
		empty := "  no match"
		if total == 0 && strings.HasPrefix(p.input.Value(), "$") {
			empty = "  No recipes. Create " + p.recipePath
		}
		return append([]string{statusStyle.Render(empty)}, make([]string, rows-1)...)
	}
	lines := p.rows(choices, min(len(choices), rows))
	return append(lines, make([]string, rows-len(lines))...)
}

// rows are count of choices, one per line, the best first, with the selection among them. The selected row is
// highlighted across the whole width.
func (p *prompt) rows(choices []choice, count int) []string {
	first := max(0, p.selected-count+1)
	idWidth := 0
	for _, c := range choices {
		idWidth = max(idWidth, ansi.StringWidth(c.id))
	}
	var rows []string
	for i, c := range choices[first : first+count] {
		marker, style := "  ", lipgloss.NewStyle()
		if first+i == p.selected {
			marker, style = selectedStyle.Render("▸ "), selectedStyle
		}
		padding := strings.Repeat(" ", idWidth-ansi.StringWidth(c.id)+3)
		row := marker + highlight(c.id, c.matched, style) + padding + statusStyle.Render(firstLine(c.description))
		rows = append(rows, ansi.Truncate(row, p.width, "…"))
	}
	return rows
}

// highlight renders id in style, and the characters at the byte offsets in matched in matchStyle too.
func highlight(id string, matched []int, style lipgloss.Style) string {
	var b strings.Builder
	for i, r := range id {
		s := style
		for _, m := range matched {
			if m == i {
				s = s.Inherit(matchStyle)
			}
		}
		b.WriteString(s.Render(string(r)))
	}
	return b.String()
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

var (
	selectedStyle = lipgloss.NewStyle().Bold(true)
	matchStyle    = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	promptStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan)
)
