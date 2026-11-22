package tui

import (
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"quack/internal/recipe"
	"quack/internal/wire"
)

type prompt struct {
	input      textinput.Model
	actions    []wire.Action
	recipes    []recipe.Recipe
	recipePath string
	browsing   bool
	selected   int
	width      int
}

type choice struct {
	id          string
	description string
	// A recipe comes as an action whose id is its name after '$', so that it opens the box as an action does.
	action  wire.Action
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

func (p *prompt) choices() ([]choice, int) {
	text := strings.TrimSpace(p.input.Value())
	var all []choice
	switch {
	case strings.HasPrefix(text, "$"):
		for _, r := range p.recipes {
			action := wire.Action{ID: "$" + r.Name, Description: r.Description, Params: r.Params}
			all = append(all, choice{id: action.ID, description: action.Description, action: action})
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

func (p *prompt) open() bool { return p.input.Value() != "" || p.browsing }

// Only while the list shows, so that nothing the user cannot see runs.
func (p *prompt) selection() (choice, bool) {
	choices, _ := p.choices()
	if !p.open() || len(choices) == 0 {
		return choice{}, false
	}
	return choices[p.selected], true
}

func (p *prompt) value() string { return p.input.Value() }

func (p *prompt) update(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "up", "ctrl+p":
		if !p.open() {
			p.browsing = true
			return nil
		}
		choices, _ := p.choices()
		p.selected = min(max(0, len(choices)-1), p.selected+1)
		return nil
	case "down", "ctrl+n":
		p.selected = max(0, p.selected-1)
		return nil
	}
	text := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != text {
		p.selected, p.browsing = 0, false
	}
	return cmd
}

func (p *prompt) forward(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd
}

func (p *prompt) clear() bool {
	had := p.open()
	p.input.SetValue("")
	p.selected, p.browsing = 0, false
	return had
}

func (p *prompt) setActions(actions []wire.Action) {
	p.keepSelection(func() { p.actions = actions })
}

func (p *prompt) setRecipes(recipes []recipe.Recipe, path string) {
	p.keepSelection(func() { p.recipes, p.recipePath = recipes, path })
}

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

func (p *prompt) resize(width int) {
	p.width = width
	widest := len(strconv.Itoa(max(len(p.actions), len(p.recipes), len(commands()))))
	p.input.SetWidth(max(1, width-ansi.StringWidth(p.input.Prompt)-2*widest-3))
}

func (p *prompt) view() string {
	choices, total := p.choices()
	counter := statusStyle.Render(strconv.Itoa(len(choices)) + "/" + strconv.Itoa(total))
	line := p.input.View()
	return line + strings.Repeat(" ", max(1, p.width-ansi.StringWidth(line)-ansi.StringWidth(counter))) + counter
}

func (p *prompt) list(rows int) []string {
	if !p.open() {
		return nil
	}
	choices, total := p.choices()
	if len(choices) == 0 {
		empty := "  no match"
		if total == 0 && strings.HasPrefix(p.input.Value(), "$") {
			empty = "  No recipes. Create " + p.recipePath
		}
		return []string{statusStyle.Render(empty)}
	}
	lines := p.rows(choices, min(len(choices), rows))
	slices.Reverse(lines)
	return lines
}

func (p *prompt) keys() string {
	if p.open() {
		return "enter run · tab params · ↑↓ select · esc close · ctrl+c cancel/quit"
	}
	return "type or ↑ to find an action · ctrl+o open app · ctrl+c cancel/quit"
}

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
