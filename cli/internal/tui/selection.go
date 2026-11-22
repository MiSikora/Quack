package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// A point stays on the same text while the log scrolls.
type point struct {
	row, col int
}

// Both ends are included.
type span struct {
	from, to point
}

type mouse struct {
	dragging  bool
	anchor    point
	pressed   point
	pressedAt time.Time
}

func (m *model) click(msg tea.MouseClickMsg) tea.Cmd {
	m.log.marked = nil
	m.mouse.dragging = false
	if msg.Button != tea.MouseLeft || msg.Y >= m.logShown() {
		return nil
	}
	at := m.log.at(msg.X, msg.Y, m.logHeight())
	now := m.clock.Now()
	double := at == m.mouse.pressed && now.Sub(m.mouse.pressedAt) < doubleClickTime
	m.mouse.pressed, m.mouse.pressedAt = at, now
	if double {
		// A third click starts over rather than counting as another double click.
		m.mouse.pressedAt = time.Time{}
		if word, ok := m.log.word(at); ok {
			m.log.marked = &word
			return m.copy(m.log.markedText())
		}
		return nil
	}
	m.mouse.dragging, m.mouse.anchor = true, at
	return nil
}

// Dragging onto the log's first row or below its shown rows scrolls the log a row, so that the selection can reach
// past the view.
func (m *model) drag(msg tea.MouseMotionMsg) {
	if !m.mouse.dragging {
		return
	}
	height, shown := m.logHeight(), m.logShown()
	switch {
	case msg.Y <= 0:
		m.log.scroll(1, height)
	case msg.Y >= shown:
		m.log.scroll(-1, height)
	}
	at := m.log.at(msg.X, min(msg.Y, shown-1), height)
	if at == m.mouse.anchor {
		m.log.marked = nil
		return
	}
	m.log.marked = &span{earlier(m.mouse.anchor, at), later(m.mouse.anchor, at)}
}

func (m *model) release() tea.Cmd {
	if !m.mouse.dragging {
		return nil
	}
	m.mouse.dragging = false
	if m.log.marked == nil {
		return nil
	}
	return m.copy(m.log.markedText())
}

// OSC 52 reaches the terminal's clipboard, and the system's clipboard covers terminals that keep OSC 52 from
// programs. A clipboard that fails leaves the other.
func (m *model) copy(text string) tea.Cmd {
	if text == "" {
		return nil
	}
	write := m.clipboard
	return tea.Batch(
		tea.SetClipboard(text),
		func() tea.Msg {
			_ = write(text)
			return nil
		},
		m.copied.show(m.clock, fmt.Sprintf("copied %d characters", utf8.RuneCountInString(text))),
	)
}

func (l *sessionLog) at(x, y, height int) point {
	row := l.top(height) + y
	switch {
	case len(l.rows) == 0 || row < 0:
		return point{}
	case row >= len(l.rows):
		return point{len(l.rows) - 1, l.width}
	}
	return point{row, max(0, x)}
}

func (l *sessionLog) word(at point) (span, bool) {
	plain := ansi.Strip(l.rows[at.row].text)
	letter := func(col int) bool {
		cell := ansi.Cut(plain, col, col+1)
		return cell != "" && strings.TrimSpace(cell) != ""
	}
	if !letter(at.col) {
		return span{}, false
	}
	from, to := at.col, at.col
	for from > 0 && letter(from-1) {
		from--
	}
	for letter(to + 1) {
		to++
	}
	return span{point{at.row, from}, point{at.row, to}}, true
}

func (l *sessionLog) markedText() string {
	var b strings.Builder
	for row := l.marked.from.row; row <= l.marked.to.row; row++ {
		from, to := l.markedCols(row)
		text := ansi.Cut(ansi.Strip(l.rows[row].text), from, to)
		if row < l.marked.to.row && l.rows[row+1].continues {
			b.WriteString(text)
			continue
		}
		b.WriteString(strings.TrimRight(text, " "))
		if row < l.marked.to.row {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (l *sessionLog) markedCols(row int) (from, to int) {
	from, to = 0, l.width
	if row == l.marked.from.row {
		from = l.marked.from.col
	}
	if row == l.marked.to.row {
		to = l.marked.to.col + 1
	}
	return from, to
}

// A row with any selected cells loses its own styles.
func (l *sessionLog) highlight(row int) string {
	text := l.rows[row].text
	if l.marked == nil || row < l.marked.from.row || row > l.marked.to.row {
		return text
	}
	plain := ansi.Strip(text)
	from, to := l.markedCols(row)
	to = min(to, ansi.StringWidth(plain))
	if from >= to {
		return plain
	}
	return ansi.Cut(plain, 0, from) + markedStyle.Render(ansi.Cut(plain, from, to)) + ansi.Cut(plain, to, l.width)
}

func earlier(a, b point) point {
	if b.row < a.row || b.row == a.row && b.col < a.col {
		return b
	}
	return a
}

func later(a, b point) point {
	if earlier(a, b) == a {
		return b
	}
	return a
}

var markedStyle = lipgloss.NewStyle().Reverse(true)

const doubleClickTime = 500 * time.Millisecond
