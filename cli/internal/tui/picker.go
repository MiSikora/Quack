package tui

import (
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"quack/internal/adb"
	"quack/internal/client"
)

// picker picks a device and then a quack-enabled app on it, in two steps that take the place of the action picker. Its
// rows come from the devices the model follows, so they change as devices connect and apps install.
type picker struct {
	step step
	// device is the device of the package step.
	device adb.Device
	// want narrows the choices to a device, a package or both, as --device and --app do. "" is any.
	want client.Target
	// auto takes the only device by itself, and skipPackage the only package, so that a step with one choice does not
	// show. back is whether esc on the package step goes back to the device step, which showed.
	auto        bool
	skipPackage bool
	back        bool
	// connecting is the pick being started and described, or nil.
	connecting *client.Target
	input      textinput.Model
	// selected is the key of the row the user moved to, or "" for the first row that can be picked. Rows come and go as
	// devices do, so the selection follows its row rather than its place.
	selected string
	width    int
}

type step int

const (
	deviceStep step = iota
	packageStep
)

// seenDevice is a device adb lists, and what quack found on it once inspected says so.
type seenDevice struct {
	client.Device
	inspected bool
}

// pickRow is a device or a package the picker lists. A row that is not ok shows dimmed and cannot be picked, and note
// says why.
type pickRow struct {
	key     string
	label   string
	note    string
	ok      bool
	device  adb.Device
	pkg     string
	matched []int
}

func newPicker(want client.Target, width int) *picker {
	input := textinput.New()
	input.Prompt = "> "
	styles := input.Styles()
	styles.Focused.Prompt = promptStyle
	styles.Blurred.Prompt = promptStyle
	input.SetStyles(styles)
	p := &picker{want: want, input: input}
	p.toStep(deviceStep)
	p.resize(width)
	return p
}

// toStep shows step with no text, and the package step for device.
func (p *picker) toStep(s step) {
	p.step = s
	p.input.SetValue("")
	p.selected = ""
	p.input.Placeholder = "pick a device"
	if s == packageStep {
		p.input.Placeholder = "pick a package"
	}
}

// rows are the choices of the step that the words of the text match, fuzzily and ignoring case, best first, and how
// many there are to match. Blank text keeps adb's order of devices and the device's order of packages.
func (p *picker) rows(devices []seenDevice) ([]pickRow, int) {
	all := p.allRows(devices)
	text := strings.TrimSpace(p.input.Value())
	if text == "" {
		return all, len(all)
	}
	labels := make([]string, len(all))
	for i, r := range all {
		labels[i] = r.label
	}
	var matches []pickRow
	for _, match := range find(text, labels) {
		r := all[match.Index]
		r.matched = match.MatchedIndexes
		matches = append(matches, r)
	}
	return matches, len(all)
}

// allRows are every choice of the step that want leaves. With a package wanted, a device shows until it is known not
// to have it.
func (p *picker) allRows(devices []seenDevice) []pickRow {
	var rows []pickRow
	if p.step == packageStep {
		for _, pkg := range p.packages(devices, p.device) {
			rows = append(rows, pickRow{key: pkg, label: pkg, ok: true, device: p.device, pkg: pkg})
		}
		return rows
	}
	for _, d := range devices {
		if p.want.Device != "" && d.Device.Device != p.want.Device {
			continue
		}
		label := string(d.Device.Device)
		if d.Model != "" {
			label = d.Model + " · " + label
		}
		row := pickRow{key: string(d.Device.Device), label: label, device: d.Device.Device}
		count := len(p.packages(devices, row.device))
		if p.want.Package != "" && d.inspected && count == 0 {
			continue
		}
		switch {
		case d.State == "unauthorized":
			row.note = "unauthorized · accept the USB debugging prompt"
		case d.State == "offline":
			row.note = "offline · wait for adb to see it again, or reconnect it"
		case d.State != adb.Ready:
			row.note = d.State
		case !d.inspected:
			row.note = "looking for packages…"
		case count == 0:
			row.note = "no quack package"
		case count == 1:
			row.note, row.ok = "1 package", true
		default:
			row.note, row.ok = strconv.Itoa(count)+" packages", true
		}
		rows = append(rows, row)
	}
	return rows
}

// onlyDevice is the only device row that can be picked, while no ready device is still being asked for its apps.
func (p *picker) onlyDevice(devices []seenDevice) (pickRow, bool) {
	var only []pickRow
	for _, d := range devices {
		wanted := p.want.Device == "" || d.Device.Device == p.want.Device
		if wanted && d.State == adb.Ready && !d.inspected {
			return pickRow{}, false
		}
	}
	for _, r := range p.allRows(devices) {
		if r.ok {
			only = append(only, r)
		}
	}
	if len(only) != 1 {
		return pickRow{}, false
	}
	return only[0], true
}

// packages are the quack-enabled apps on device that want leaves.
func (p *picker) packages(devices []seenDevice, device adb.Device) []string {
	i := slices.IndexFunc(devices, func(d seenDevice) bool { return d.Device.Device == device })
	if i < 0 {
		return nil
	}
	var pkgs []string
	for _, pkg := range devices[i].Packages {
		if p.want.Package == "" || pkg == p.want.Package {
			pkgs = append(pkgs, pkg)
		}
	}
	return pkgs
}

// selection is the selected row, when it can be picked.
func (p *picker) selection(devices []seenDevice) (pickRow, bool) {
	rows, _ := p.rows(devices)
	i := p.selectedIndex(rows)
	if i < 0 || !rows[i].ok {
		return pickRow{}, false
	}
	return rows[i], true
}

// selectedIndex is the index of the selected row in rows: the row the user moved to while it can be picked, or else
// the first one that can be, or else the first row, or -1 without rows.
func (p *picker) selectedIndex(rows []pickRow) int {
	if i := slices.IndexFunc(rows, func(r pickRow) bool { return r.key == p.selected && r.ok }); i >= 0 {
		return i
	}
	if i := slices.IndexFunc(rows, func(r pickRow) bool { return r.ok }); i >= 0 {
		return i
	}
	return min(0, len(rows)-1)
}

// update moves the selection over the rows that can be picked, or edits the text. Up moves to the next row, away from
// the input, and down back toward the first. A new text selects its best match.
func (p *picker) update(msg tea.KeyPressMsg, devices []seenDevice) tea.Cmd {
	rows, _ := p.rows(devices)
	selected := p.selectedIndex(rows)
	switch msg.String() {
	case "up", "ctrl+p":
		for i := selected + 1; i < len(rows); i++ {
			if rows[i].ok {
				p.selected = rows[i].key
				break
			}
		}
		return nil
	case "down", "ctrl+n":
		for i := selected - 1; i >= 0; i-- {
			if rows[i].ok {
				p.selected = rows[i].key
				break
			}
		}
		return nil
	}
	text := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != text {
		p.selected = ""
	}
	return cmd
}

func (p *picker) forward(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd
}

func (p *picker) focus() tea.Cmd { return p.input.Focus() }

// clear empties the text, and says whether there was text.
func (p *picker) clear() bool {
	had := p.input.Value() != ""
	p.input.SetValue("")
	p.selected = ""
	return had
}

func (p *picker) resize(width int) {
	p.width = width
	p.input.SetWidth(max(1, width-ansi.StringWidth(p.input.Prompt)-9))
}

// view is the input's line, with how many rows match out of how many there are at its right end.
func (p *picker) view(devices []seenDevice) string {
	rows, total := p.rows(devices)
	counter := statusStyle.Render(strconv.Itoa(len(rows)) + "/" + strconv.Itoa(total))
	line := p.input.View()
	return line + strings.Repeat(" ", max(1, p.width-ansi.StringWidth(line)-ansi.StringWidth(counter))) + counter
}

// list is a line for each row the text matches, up to height of them, the first last, next to the input, or else a
// line that says what the picker waits for. The rows scroll to keep the selection in sight.
func (p *picker) list(devices []seenDevice, listed bool, height int) []string {
	rows, total := p.rows(devices)
	if len(rows) == 0 {
		return []string{statusStyle.Render("  " + p.waiting(listed, total))}
	}
	selected := p.selectedIndex(rows)
	count := min(len(rows), height)
	first := max(0, selected-count+1)
	labelWidth := 0
	for _, r := range rows {
		labelWidth = max(labelWidth, ansi.StringWidth(r.label))
	}
	var lines []string
	for i, r := range rows[first : first+count] {
		padding := strings.Repeat(" ", labelWidth-ansi.StringWidth(r.label)+3)
		var line string
		switch {
		case !r.ok:
			line = statusStyle.Render("  " + r.label + padding + r.note)
		case first+i == selected:
			line = selectedStyle.Render("▸ ") + highlight(r.label, r.matched, selectedStyle) + padding + statusStyle.Render(r.note)
		default:
			line = "  " + highlight(r.label, r.matched, lipgloss.NewStyle()) + padding + statusStyle.Render(r.note)
		}
		lines = append(lines, ansi.Truncate(line, p.width, "…"))
	}
	slices.Reverse(lines)
	return lines
}

// waiting says what is missing when no row shows.
func (p *picker) waiting(listed bool, total int) string {
	switch {
	case total > 0:
		return "no match"
	case !listed:
		return "looking for devices…"
	case p.step == packageStep && p.want.Package != "":
		return "waiting for " + p.want.Package + " on " + string(p.device)
	case p.step == packageStep:
		return "no quack package on " + string(p.device) + " · waiting for one"
	case p.want.Device != "" && p.want.Package != "":
		return "waiting for " + p.want.Package + " on " + string(p.want.Device)
	case p.want.Package != "":
		return "waiting for " + p.want.Package
	case p.want.Device != "":
		return "waiting for " + string(p.want.Device)
	default:
		return "no device · waiting for one to connect"
	}
}

// keys are the keys that work in the step.
func (p *picker) keys(canClose bool) string {
	if p.connecting != nil {
		return "starting " + p.connecting.Package + " on " + string(p.connecting.Device) + "…"
	}
	keys := "enter pick · ↑↓ select"
	switch {
	case p.step == packageStep && p.back:
		keys += " · esc back"
	case canClose:
		keys += " · esc close"
	}
	return keys + " · ctrl+c quit"
}
