package tui

import (
	"os"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"quack/internal/recipe"
	"quack/internal/step"
	"quack/internal/wire"
)

type prompt struct {
	input    textinput.Model
	actions  []wire.Action
	recipes  []recipe.Recipe
	commands []command
	browsing bool
	grouped  bool
	group    string
	selected int
	width    int
}

type runnable struct {
	wire.Action
	recipe bool
}

type choice struct {
	id          string
	description string
	runnable    runnable
	command     *command
	group       bool
	matched     []int
}

func newInput(placeholder string) textinput.Model {
	input := textinput.New()
	input.Prompt = "> "
	input.Placeholder = placeholder
	styles := input.Styles()
	styles.Focused.Prompt = promptStyle
	styles.Blurred.Prompt = promptStyle
	input.SetStyles(styles)
	return input
}

func (p *prompt) connect(app bool) {
	p.commands = slices.DeleteFunc(commands(), func(c command) bool { return c.needsApp && !app })
	p.input.Placeholder = "find an action or a recipe, $ for recipes only, / for commands"
	if !app {
		p.input.Placeholder = "/ for commands"
	}
}

func (p *prompt) choices() ([]choice, int) {
	text := strings.TrimSpace(p.input.Value())
	var all []choice
	switch query, recipes := strings.CutPrefix(text, "$"); {
	case recipes:
		text = query
		all = p.runnables(false)
	case strings.HasPrefix(text, "/"):
		for _, c := range p.commands {
			all = append(all, choice{id: c.name, description: c.description, command: &c})
		}
	default:
		all = p.runnables(true)
	}
	if text == "" && p.browsing {
		return p.browse(all), len(all)
	}
	if text == "" || text == "/" {
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

func (p *prompt) runnables(actions bool) []choice {
	var all []choice
	for _, r := range p.recipes {
		all = append(all, runnableChoice(runnable{wire.Action{ID: r.Name, Description: r.Description, Params: r.Params}, true}))
	}
	if actions {
		for _, a := range p.actions {
			all = append(all, runnableChoice(runnable{Action: a}))
		}
	}
	return all
}

func (p *prompt) browse(all []choice) []choice {
	all = slices.Clone(all)
	slices.SortStableFunc(all, func(a, b choice) int { return strings.Compare(a.id, b.id) })
	if p.group != "" {
		return slices.DeleteFunc(all, func(c choice) bool { return !strings.HasPrefix(c.id, p.group) })
	}
	if !p.grouped {
		return all
	}
	sizes := map[string]int{}
	for _, c := range all {
		if ns, ok := namespace(c.id); ok {
			sizes[ns]++
		}
	}
	var rows []choice
	for _, c := range all {
		ns, ok := namespace(c.id)
		switch {
		case !ok || sizes[ns] < 2:
			rows = append(rows, c)
		case !slices.ContainsFunc(rows, func(row choice) bool { return row.group && row.id == ns }):
			rows = append(rows, choice{id: ns, description: strconv.Itoa(sizes[ns]), group: true})
		}
	}
	slices.SortStableFunc(rows, func(a, b choice) int { return strings.Compare(a.id, b.id) })
	return rows
}

func namespace(id string) (string, bool) {
	i := strings.IndexByte(id, '.')
	if i < 0 {
		return "", false
	}
	return id[:i+1], true
}

func runnableChoice(r runnable) choice {
	return choice{id: r.ID, description: r.Description, runnable: r}
}

func (p *prompt) open() bool { return p.input.Value() != "" || p.browsing }

func (p *prompt) selection() (choice, bool) {
	choices, _ := p.choices()
	if !p.open() || len(choices) == 0 {
		return choice{}, false
	}
	return choices[p.selected], true
}

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
	case "right":
		if c, ok := p.selection(); ok && c.group {
			p.openGroup(c.id)
			return nil
		}
	case "left", "backspace":
		if p.group != "" && p.input.Value() == "" {
			p.closeGroup()
			return nil
		}
	}
	text := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != text {
		p.selected, p.browsing, p.group = 0, false, ""
	}
	return cmd
}

func (p *prompt) openGroup(group string) {
	p.group, p.selected = group, 0
}

func (p *prompt) closeGroup() {
	group := p.group
	p.group, p.selected = "", 0
	choices, _ := p.choices()
	p.selected = max(0, slices.IndexFunc(choices, func(c choice) bool { return c.group && c.id == group }))
}

func (p *prompt) forward(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd
}

func (p *prompt) clear() bool {
	had := p.open()
	p.input.SetValue("")
	p.selected, p.browsing, p.group = 0, false, ""
	return had
}

func (p *prompt) setActions(actions []wire.Action) {
	p.keepSelection(func() { p.actions = actions })
}

func (p *prompt) setRecipes(recipes []recipe.Recipe) {
	p.keepSelection(func() { p.recipes = recipes })
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
		if c.command == nil && c.id == selected.id && c.group == selected.group && c.runnable.recipe == selected.runnable.recipe {
			p.selected = i
		}
	}
}

func (p *prompt) resize(width int) {
	p.width = width
	widest := len(strconv.Itoa(max(len(p.actions)+len(p.recipes), len(commands()))))
	p.input.SetWidth(max(1, width-ansi.StringWidth(p.input.Prompt)-2*widest-3))
}

func (p *prompt) view() string {
	choices, total := p.choices()
	counter := strconv.Itoa(len(choices)) + "/" + strconv.Itoa(total)
	if p.browsing && p.group != "" {
		counter = p.group + " " + counter
	}
	counter = statusStyle.Render(counter)
	line := ansi.Truncate(p.input.View(), p.width-ansi.StringWidth(counter)-1, "")
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
			empty = "  No recipes. /init-recipes starts .quack/ here, or map the app in config.toml"
		}
		return []string{statusStyle.Render(empty)}
	}
	lines := p.rows(choices, min(len(choices), rows))
	slices.Reverse(lines)
	return lines
}

func (p *prompt) keys() string {
	c, ok := p.selection()
	switch {
	case ok && c.group:
		return "enter or → open · ↑↓ select · esc close · ctrl+c cancel/quit"
	case p.browsing && p.group != "":
		return "enter run · tab params · ↑↓ select · ← back · esc close · ctrl+c cancel/quit"
	case p.open():
		return "enter run · tab params · ↑↓ select · esc close · ctrl+c cancel/quit"
	}
	return "type or ↑ to find an action · ctrl+o open app · ctrl+c cancel/quit"
}

func (p *prompt) rows(choices []choice, count int) []string {
	first := max(0, p.selected-count+1)
	idWidth := 0
	for _, c := range choices {
		idWidth = max(idWidth, ansi.StringWidth(c.id+c.suffix()))
	}
	var rows []string
	for i, c := range choices[first : first+count] {
		marker, style := "  ", lipgloss.NewStyle()
		if first+i == p.selected {
			marker, style = selectedStyle.Render("▸ "), selectedStyle
		}
		padding := strings.Repeat(" ", idWidth-ansi.StringWidth(c.id+c.suffix())+3)
		row := marker + highlight(c.id, c.matched, style) + statusStyle.Render(c.suffix()) + padding +
			statusStyle.Render(firstLine(c.description))
		rows = append(rows, ansi.Truncate(row, p.width, "…"))
	}
	return rows
}

func (c choice) suffix() string {
	if c.command != nil {
		return ""
	}
	return c.runnable.suffix()
}

func (r runnable) suffix() string {
	if r.recipe {
		return " ƒ"
	}
	return ""
}

func (r runnable) commandLine(args map[string]wire.RawArg) string {
	shown := step.PrepareCall([]wire.Action{r.Action}, wire.Call{Action: r.ID, Args: args}, nil, os.Getenv).Display
	line := step.CommandLine(r.Action, shown.Args)
	if r.recipe {
		return "--recipe " + line
	}
	return line
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
