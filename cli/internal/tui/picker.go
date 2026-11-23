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
	"quack/internal/device"
)

type picker struct {
	step        pickerStep
	device      adb.Device
	want        device.Target
	auto        bool
	skipPackage bool
	back        bool
	quiet       bool
	startup     bool
	connecting  *device.Target
	input       textinput.Model
	selected    string
	width       int
}

type pickerStep int

const (
	deviceStep pickerStep = iota
	packageStep
)

type seenDevice struct {
	device.Info
	inspected bool
}

type pickRow struct {
	key     string
	label   string
	note    string
	ok      bool
	device  adb.Device
	pkg     string
	matched []int
}

func newPicker(want device.Target, width int) *picker {
	p := &picker{want: want, input: newInput("")}
	p.toStep(deviceStep)
	p.resize(width)
	return p
}

func (p *picker) toStep(s pickerStep) {
	p.step = s
	p.input.SetValue("")
	p.selected = ""
	p.input.Placeholder = "pick a device"
	if s == packageStep {
		p.input.Placeholder = "pick an app"
	}
}

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

func (p *picker) allRows(devices []seenDevice) []pickRow {
	var rows []pickRow
	if p.step == packageStep {
		for _, pkg := range p.packages(devices, p.device) {
			rows = append(rows, pickRow{key: pkg, label: pkg, ok: true, device: p.device, pkg: pkg})
		}
		return rows
	}
	for _, d := range devices {
		if p.want.Device != "" && d.Device != p.want.Device {
			continue
		}
		label := string(d.Device)
		if d.Model != "" {
			label = d.Model + " · " + label
		}
		row := pickRow{key: string(d.Device), label: label, device: d.Device}
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
			row.note = "looking for apps…"
		case count == 0:
			row.note = "no quack-enabled app"
		case count == 1:
			row.note, row.ok = "1 app", true
		default:
			row.note, row.ok = strconv.Itoa(count)+" apps", true
		}
		rows = append(rows, row)
	}
	return rows
}

func (p *picker) onlyDevice(devices []seenDevice) (pickRow, bool) {
	var only []pickRow
	for _, d := range devices {
		wanted := p.want.Device == "" || d.Device == p.want.Device
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

func (p *picker) showing(devices []seenDevice) bool {
	return !p.quiet || p.connecting != nil || p.startup && slices.ContainsFunc(p.allRows(devices), func(r pickRow) bool { return r.ok })
}

func (p *picker) alarm(devices []seenDevice, listed bool) string {
	rows := p.allRows(devices)
	switch {
	case !listed:
		return ""
	case len(devices) == 0 && p.want == device.Target{}:
		return "no device"
	case len(devices) == 0:
		return ""
	case p.step == packageStep && len(rows) == 0 && p.want.Package == "":
		return noApp
	case p.step == packageStep:
		return ""
	case slices.ContainsFunc(rows, func(r pickRow) bool { return r.ok || r.note == "looking for apps…" }):
		return ""
	case slices.ContainsFunc(rows, func(r pickRow) bool { return r.note == noApp }):
		return noApp
	}
	return ""
}

const noApp = "no quack-enabled app"

func (p *picker) status(devices []seenDevice, listed bool) string {
	rows := p.allRows(devices)
	pickable := slices.ContainsFunc(rows, func(r pickRow) bool { return r.ok })
	switch {
	case len(rows) == 0 && !p.startup && listed:
		return "no device"
	case len(rows) == 0 || p.step == packageStep:
		return p.waiting(listed, 0)
	case len(rows) == 1 && pickable:
		return rows[0].label + " · " + rows[0].note + " · /pick-device to connect"
	case len(rows) == 1:
		return rows[0].label + " · " + rows[0].note
	case pickable:
		return strconv.Itoa(len(rows)) + " devices · /pick-device to pick one"
	case slices.ContainsFunc(rows, func(r pickRow) bool { return r.note == "looking for apps…" }):
		return strconv.Itoa(len(rows)) + " devices · looking for apps…"
	}
	return strconv.Itoa(len(rows)) + " devices · no quack-enabled app · /pick-device to see them"
}

func (p *picker) packages(devices []seenDevice, serial adb.Device) []string {
	i := slices.IndexFunc(devices, func(d seenDevice) bool { return d.Device == serial })
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

func (p *picker) selection(devices []seenDevice) (pickRow, bool) {
	rows, _ := p.rows(devices)
	i := p.selectedIndex(rows)
	if i < 0 || !rows[i].ok {
		return pickRow{}, false
	}
	return rows[i], true
}

func (p *picker) selectedIndex(rows []pickRow) int {
	if i := slices.IndexFunc(rows, func(r pickRow) bool { return r.key == p.selected && r.ok }); i >= 0 {
		return i
	}
	if i := slices.IndexFunc(rows, func(r pickRow) bool { return r.ok }); i >= 0 {
		return i
	}
	return min(0, len(rows)-1)
}

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

func (p *picker) view(devices []seenDevice) string {
	rows, total := p.rows(devices)
	counter := statusStyle.Render(strconv.Itoa(len(rows)) + "/" + strconv.Itoa(total))
	line := p.input.View()
	return line + strings.Repeat(" ", max(1, p.width-ansi.StringWidth(line)-ansi.StringWidth(counter))) + counter
}

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

func (p *picker) waiting(listed bool, total int) string {
	switch {
	case total > 0:
		return "no match"
	case !listed:
		return "looking for devices…"
	case p.step == packageStep && p.want.Package != "":
		return "waiting for " + p.want.Package + " on " + string(p.device)
	case p.step == packageStep:
		return "no quack-enabled app on " + string(p.device) + " · waiting for one"
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
