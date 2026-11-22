package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"quack/internal/wire"
)

// paramBox asks for the params of one action: a row for each param, under the action's id and over a preview of the
// call. One row has the focus.
type paramBox struct {
	action  wire.Action
	fields  []boxField
	focused int
	width   int
}

// boxField is a param and the field that edits it.
type boxField struct {
	param wire.Param
	field field
	// missing is whether the user tried to run the action while this required param was empty. It turns the note red
	// until the param is set.
	missing bool
}

// field edits the value of a param: a textField or a choiceField.
type field interface {
	// value is what the user set, or nothing.
	value() wire.RawArg
	press(msg tea.KeyPressMsg) tea.Cmd
	// forward hands the field a message it may be waiting for, such as a cursor blink.
	forward(msg tea.Msg) tea.Cmd
	view() string
	focus() tea.Cmd
	blur()
}

// textField is one line of text, for a string or a number, or for the comma-separated values of a string_list.
type textField struct {
	input textinput.Model
	list  bool
}

// choiceField picks one of options with ← and →. An empty option is the choice to leave the param unset.
type choiceField struct {
	options []string
	chosen  int
}

func newParamBox(action wire.Action, width int) *paramBox {
	b := &paramBox{action: action}
	for _, p := range action.Params {
		b.fields = append(b.fields, boxField{param: p, field: newField(p)})
	}
	b.resize(width)
	return b
}

// newField is a choice for an enum or a boolean, and text for anything else. A param that is not required can be
// left unset, which is the first choice.
func newField(p wire.Param) field {
	_, required := p.Presence.(wire.Required)
	var choices []string
	switch p.Type {
	case wire.ParamTypeEnum:
		choices = p.Choices
	case wire.ParamTypeBoolean:
		choices = []string{"true", "false"}
	}
	if choices != nil {
		var options []string
		if !required {
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
	return &textField{input: input, list: p.Type.IsList()}
}

// args are the params the user set, as a call sends them.
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

// press handles a key, and says whether the user asked to run the action. Enter moves to the next field, and on the
// last one runs the action, unless a required param is empty. Then the focus moves to that param instead.
func (b *paramBox) press(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if b.focused < len(b.fields)-1 {
			return false, b.focus(b.focused + 1)
		}
		for i := range b.fields {
			f := &b.fields[i]
			if _, required := f.param.Presence.(wire.Required); required && len(f.field.value()) == 0 {
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

func (b *paramBox) focus(i int) tea.Cmd {
	b.fields[b.focused].field.blur()
	b.focused = i
	return b.fields[i].field.focus()
}

// resize gives the text fields half the width the names leave.
func (b *paramBox) resize(width int) {
	b.width = width
	for _, f := range b.fields {
		if text, ok := f.field.(*textField); ok {
			text.input.SetWidth(b.fieldWidth())
		}
	}
}

// keys are the keys that work in the box.
func (b *paramBox) keys() string {
	if _, ok := b.fields[b.focused].field.(*choiceField); ok {
		return "←→ choose · enter next · shift+tab back · esc close"
	}
	return "enter next · shift+tab back · esc close"
}

// view is rows lines: the action's id and description, the params, and the call they make. The params scroll to keep
// the focused one in sight. With room for only one param, the id goes.
func (b *paramBox) view(rows int) []string {
	header := commandStyle.Render(b.action.ID)
	if description := firstLine(b.action.Description); description != "" {
		header += statusStyle.Render(" · " + description)
	}
	lines := []string{header}
	paramRows := rows - 2
	if paramRows < 1 {
		lines, paramRows = nil, rows-1
	}
	names := 0
	for _, f := range b.fields {
		names = max(names, ansi.StringWidth(f.param.Name))
	}
	column := b.fieldWidth() + 1
	for _, f := range b.fields {
		column = max(column, ansi.StringWidth(f.field.view()))
	}
	first := max(0, b.focused-paramRows+1)
	for i := first; i < min(len(b.fields), first+paramRows); i++ {
		f := b.fields[i]
		marker := "  "
		if i == b.focused {
			marker = selectedStyle.Render("▸ ")
		}
		name := f.param.Name + strings.Repeat(" ", names-ansi.StringWidth(f.param.Name))
		value := f.field.view()
		value += strings.Repeat(" ", column-ansi.StringWidth(value))
		note := statusStyle.Render(describe(f.param))
		if f.missing {
			note = problemStyle.Render(describe(f.param))
		}
		lines = append(lines, marker+name+"  "+value+"  "+note)
	}
	for len(lines) < rows-1 {
		lines = append(lines, "")
	}
	return append(lines, "› "+commandLine(b.action, b.args()))
}

func (b *paramBox) fieldWidth() int { return min(max(b.width/3, 10), 40) }

func (f *textField) value() wire.RawArg {
	text := f.input.Value()
	switch {
	case f.list:
		return split(text)
	case text == "":
		return nil
	}
	return wire.RawArg{text}
}

func (f *textField) press(msg tea.KeyPressMsg) tea.Cmd { return f.forward(msg) }

func (f *textField) forward(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return cmd
}

func (f *textField) view() string { return f.input.View() }

func (f *textField) focus() tea.Cmd { return f.input.Focus() }

func (f *textField) blur() { f.input.Blur() }

func (f *choiceField) value() wire.RawArg {
	if f.options[f.chosen] == "" {
		return nil
	}
	return wire.RawArg{f.options[f.chosen]}
}

// press moves the choice with ← and →, round from either end.
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

// view is the options in a row, the chosen one as ‹x›.
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

// describe is the help line of a param, as quack --list-actions writes it.
func describe(p wire.Param) string {
	var text []string
	switch presence := p.Presence.(type) {
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

// split splits the text of a string_list on commas into its values, with spaces around them trimmed and empty ones
// dropped. \, is a comma inside a value.
func split(text string) wire.RawArg {
	var values wire.RawArg
	var value strings.Builder
	add := func() {
		if v := strings.TrimSpace(value.String()); v != "" {
			values = append(values, v)
		}
		value.Reset()
	}
	for i := 0; i < len(text); i++ {
		switch {
		case strings.HasPrefix(text[i:], `\,`):
			value.WriteByte(',')
			i++
		case text[i] == ',':
			add()
		default:
			value.WriteByte(text[i])
		}
	}
	add()
	return values
}
