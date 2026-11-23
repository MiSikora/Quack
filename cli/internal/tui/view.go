package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) View() tea.View {
	view := tea.NewView(strings.Join(m.screen(), "\n"))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (m *model) screen() []string {
	rows := m.log.view(m.logHeight())
	if m.width < minWidth || m.height < minHeight {
		return fit(append(rows, "Make the window larger."), m.width, m.height)
	}
	rule := ruleStyle.Render(strings.Repeat("─", m.width))
	rows = append(rows, "", m.title())
	if overlay := m.overlay(); overlay != nil {
		copy(rows[len(rows)-len(overlay)-1:], append([]string{rule}, overlay...))
	}
	rows = append(rows, rule, m.inputRow(), rule, footerStyle.Render(" "+m.footer()))
	return fit(rows, m.width, m.height)
}

func (m *model) logHeight() int { return max(0, m.height-panelRows) }

func (m *model) logShown() int { return min(m.logHeight(), m.logHeight()+1-len(m.overlay())) }

func (m *model) title() string {
	app, devices, listed := m.connection.app, m.tracker.devices, m.tracker.listed
	if app == nil && m.picker != nil {
		status := " · " + m.picker.status(devices, listed)
		alarm := m.picker.alarm(devices, listed)
		before, after, found := strings.Cut(status, alarm)
		if alarm == "" || !found {
			return selectedStyle.Render("quack") + statusStyle.Render(status)
		}
		return selectedStyle.Render("quack") + statusStyle.Render(before) + problemStyle.Render(alarm) + statusStyle.Render(after)
	}
	if app == nil {
		return selectedStyle.Render("quack")
	}
	return selectedStyle.Render("quack") + statusStyle.Render(" · "+m.connection.hello.Package+" "+m.connection.hello.Version+" · "+string(m.connection.target.Device))
}

func (m *model) overlay() []string {
	rows := min(maxListRows, m.logHeight()+1)
	switch {
	case m.picking():
		return m.picker.list(m.tracker.devices, m.tracker.listed, rows)
	case m.box != nil:
		return m.box.view(rows)
	}
	return m.prompt.list(rows)
}

func (m *model) inputRow() string {
	switch {
	case m.picking():
		return m.picker.view(m.tracker.devices)
	case m.box != nil:
		return m.box.preview()
	}
	return m.prompt.view()
}

func (m *model) footer() string {
	switch {
	case m.quit.showing:
		return m.quit.text
	case m.copied.showing:
		return m.copied.text
	case m.refused.showing:
		return m.refused.text
	case m.picking():
		return m.picker.keys(m.connection.app != nil || !m.picker.quiet || m.picker.startup)
	case m.runs.active != nil:
		return m.runs.footer()
	case m.box != nil:
		return m.box.keys()
	case m.log.scrolled > 0:
		return "scrolled back · end newest · " + m.prompt.keys()
	case m.connection.app == nil && !m.prompt.open():
		return "/ for commands · ctrl+c quit"
	}
	return m.prompt.keys()
}

func fit(lines []string, width, height int) []string {
	lines = append(lines, make([]string, max(0, height-len(lines)))...)[:height]
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return lines
}

var (
	footerStyle = lipgloss.NewStyle().Faint(true)
	ruleStyle   = lipgloss.NewStyle().Faint(true)
)

const (
	maxListRows = 10
	panelRows   = 6
	minWidth    = 24
	minHeight   = panelRows + 2
)
