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
	"github.com/sahilm/fuzzy"

	"quack/internal/wire"
)

type paramBox struct {
	runnable runnable
	fields   []boxField
	focused  int
	width    int
}

type boxField struct {
	param   wire.Param
	field   field
	missing bool
}

type field interface {
	value() wire.RawArg
	press(msg tea.KeyPressMsg) tea.Cmd
	forward(msg tea.Msg) tea.Cmd
	view() string
	focus() tea.Cmd
	blur()
}

type textField struct {
	input    textinput.Model
	list     bool
	hints    []string
	selected int
}

type choiceField struct {
	options []string
	chosen  int
}

func newParamBox(r runnable, width int) *paramBox {
	b := &paramBox{runnable: r}
	for _, p := range r.Params {
		b.fields = append(b.fields, boxField{param: p, field: newField(p)})
	}
	b.resize(width)
	return b
}

func newField(p wire.Param) field {
	var choices []string
	switch p.Type {
	case wire.ParamTypeEnum:
		choices = p.Choices
	case wire.ParamTypeBoolean:
		choices = []string{"true", "false"}
	}
	if choices != nil {
		var options []string
		if _, required := p.DisplayPresence(os.Getenv).(wire.Required); !required {
			options = append(options, "")
		}
		return &choiceField{options: append(options, choices...)}
	}
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = string(p.Type)
	if p.Type.IsList() {
		input.Placeholder = "comma-separated"
	}
	f := &textField{input: input, list: p.Type.IsList(), selected: -1}
	if p.Type == wire.ParamTypeString {
		f.hints = p.Hints
	}
	return f
}

func (b *paramBox) args() map[string]wire.RawArg {
	args := map[string]wire.RawArg{}
	for _, f := range b.fields {
		if value := f.field.value(); len(value) > 0 {
			args[f.param.Name] = value
		}
	}
	return args
}

func (b *paramBox) init() tea.Cmd { return b.fields[0].field.focus() }

func (b *paramBox) press(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if text, ok := b.fields[b.focused].field.(*textField); ok {
			text.pick()
		}
		if b.focused < len(b.fields)-1 {
			return false, b.focus(b.focused + 1)
		}
		for i := range b.fields {
			f := &b.fields[i]
			if _, required := f.param.DisplayPresence(os.Getenv).(wire.Required); required && len(f.field.value()) == 0 {
				f.missing = true
				return false, b.focus(i)
			}
		}
		return true, nil
	case "tab":
		return false, b.focus(min(b.focused+1, len(b.fields)-1))
	case "shift+tab":
		return false, b.focus(max(b.focused-1, 0))
	}
	f := &b.fields[b.focused]
	cmd := f.field.press(msg)
	if len(f.field.value()) > 0 {
		f.missing = false
	}
	return false, cmd
}

func (b *paramBox) forward(msg tea.Msg) tea.Cmd { return b.fields[b.focused].field.forward(msg) }

func (b *paramBox) deselect() bool {
	text, ok := b.fields[b.focused].field.(*textField)
	if !ok || text.selected < 0 {
		return false
	}
	text.selected = -1
	return true
}

func (b *paramBox) hinted() (*textField, bool) {
	text, ok := b.fields[b.focused].field.(*textField)
	return text, ok && len(text.hints) > 0
}

func (b *paramBox) focus(i int) tea.Cmd {
	b.fields[b.focused].field.blur()
	b.focused = i
	return b.fields[i].field.focus()
}

func (b *paramBox) resize(width int) {
	b.width = width
	for _, f := range b.fields {
		if text, ok := f.field.(*textField); ok {
			text.input.SetWidth(b.fieldWidth())
		}
	}
}

func (b *paramBox) keys() string {
	if text, ok := b.hinted(); ok {
		if text.selected >= 0 {
			return "enter pick · ↑↓ select · esc deselect"
		}
		return "↑ hints · enter next · shift+tab back · esc close"
	}
	if _, ok := b.fields[b.focused].field.(*choiceField); ok {
		return "←→ choose · enter next · shift+tab back · esc close"
	}
	return "enter next · shift+tab back · esc close"
}

func (b *paramBox) view(rows int) []string {
	if text, ok := b.hinted(); ok {
		return b.hintsView(text, rows)
	}
	lines := []string{b.header()}
	paramRows := min(len(b.fields), rows-1)
	names := 0
	for _, f := range b.fields {
		names = max(names, ansi.StringWidth(f.param.Name))
	}
	column := b.fieldWidth() + 1
	for _, f := range b.fields {
		column = max(column, ansi.StringWidth(f.field.view()))
	}
	first := max(0, b.focused-paramRows+1)
	for i := first; i < first+paramRows; i++ {
		f := b.fields[i]
		marker := "  "
		if i == b.focused {
			marker = selectedStyle.Render("▸ ")
		}
		lines = append(lines, marker+f.row(names, column))
	}
	return lines
}

func (b *paramBox) hintsView(text *textField, rows int) []string {
	f := b.fields[b.focused]
	matches := text.matches()
	counter := statusStyle.Render(strconv.Itoa(len(matches)) + "/" + strconv.Itoa(len(text.hints)))
	row := selectedStyle.Render("▸ ") + f.row(ansi.StringWidth(f.param.Name), b.fieldWidth()+1)
	row = ansi.Truncate(row, b.width-ansi.StringWidth(counter)-1, "…")
	row += strings.Repeat(" ", max(1, b.width-ansi.StringWidth(row)-ansi.StringWidth(counter))) + counter
	indent := strings.Repeat(" ", ansi.StringWidth(f.param.Name)+2)
	if len(matches) == 0 {
		return []string{b.header(), statusStyle.Render(indent + "  no match"), row}
	}
	count := min(len(matches), rows-2)
	first := max(0, text.selected-count+1)
	var hints []string
	for i := first; i < first+count; i++ {
		marker, style := "  ", lipgloss.NewStyle()
		if i == text.selected {
			marker, style = selectedStyle.Render("▸ "), selectedStyle
		}
		hints = append(hints, indent+marker+highlight(matches[i].Str, matches[i].MatchedIndexes, style))
	}
	slices.Reverse(hints)
	return append(append([]string{b.header()}, hints...), row)
}

func (b *paramBox) preview() string { return "› " + b.runnable.commandLine(b.args()) }

func (b *paramBox) header() string {
	header := commandStyle.Render(b.runnable.ID) + statusStyle.Render(b.runnable.suffix())
	if description := firstLine(b.runnable.Description); description != "" {
		header += statusStyle.Render(" · " + description)
	}
	return header
}

func (f boxField) row(names, column int) string {
	name := f.param.Name + strings.Repeat(" ", names-ansi.StringWidth(f.param.Name))
	value := f.field.view()
	value += strings.Repeat(" ", column-ansi.StringWidth(value))
	note := statusStyle.Render(describe(f.param))
	if f.missing {
		note = problemStyle.Render(describe(f.param))
	}
	return name + "  " + value + "  " + note
}

func (b *paramBox) fieldWidth() int { return min(max(b.width/3, 10), 40) }

func (f *textField) value() wire.RawArg {
	text := f.input.Value()
	if f.selected >= 0 {
		text = f.matches()[f.selected].Str
	}
	switch {
	case f.list:
		return wire.SplitList(text)
	case text == "":
		return nil
	}
	return wire.RawArg{text}
}

func (f *textField) press(msg tea.KeyPressMsg) tea.Cmd {
	if len(f.hints) > 0 {
		switch msg.String() {
		case "up", "ctrl+p":
			f.selected = min(len(f.matches())-1, f.selected+1)
			return nil
		case "down", "ctrl+n":
			f.selected = max(-1, f.selected-1)
			return nil
		}
	}
	text := f.input.Value()
	cmd := f.forward(msg)
	if f.input.Value() != text {
		f.selected = -1
		if strings.TrimSpace(f.input.Value()) != "" && len(f.matches()) > 0 {
			f.selected = 0
		}
	}
	return cmd
}

func (f *textField) matches() fuzzy.Matches {
	text := strings.TrimSpace(f.input.Value())
	if text == "" {
		matches := make(fuzzy.Matches, len(f.hints))
		for i, hint := range f.hints {
			matches[i] = fuzzy.Match{Str: hint, Index: i}
		}
		return matches
	}
	return find(text, f.hints)
}

func (f *textField) pick() {
	if f.selected < 0 {
		return
	}
	f.input.SetValue(f.matches()[f.selected].Str)
	f.input.CursorEnd()
	f.selected = -1
}

func (f *textField) forward(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return cmd
}

func (f *textField) view() string { return f.input.View() }

func (f *textField) focus() tea.Cmd { return f.input.Focus() }

func (f *textField) blur() {
	f.input.Blur()
	f.selected = -1
}

func (f *choiceField) value() wire.RawArg {
	if f.options[f.chosen] == "" {
		return nil
	}
	return wire.RawArg{f.options[f.chosen]}
}

func (f *choiceField) press(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "left":
		f.chosen = (f.chosen + len(f.options) - 1) % len(f.options)
	case "right":
		f.chosen = (f.chosen + 1) % len(f.options)
	}
	return nil
}

func (f *choiceField) forward(tea.Msg) tea.Cmd { return nil }

func (f *choiceField) view() string {
	var options []string
	for i, option := range f.options {
		if option == "" {
			option = "not set"
		}
		if i == f.chosen {
			option = selectedStyle.Render("‹" + option + "›")
		}
		options = append(options, option)
	}
	return strings.Join(options, " ")
}

func (f *choiceField) focus() tea.Cmd { return nil }

func (f *choiceField) blur() {}

func describe(p wire.Param) string {
	var text []string
	switch presence := p.DisplayPresence(os.Getenv).(type) {
	case wire.Required:
		text = append(text, "Required.")
	case wire.Default:
		text = append(text, "Default: "+presence.String()+".")
	}
	if p.Description != "" {
		text = append(text, p.Description)
	}
	return strings.Join(text, " ")
}
