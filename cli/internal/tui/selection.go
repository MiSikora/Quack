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

// point is a cell of the log: a row of its rows and a column of that row. It stays on the same text while the log
// scrolls.
type point struct {
	row, col int
}

// span is the cells of the log from from to to, both included, in reading order.
type span struct {
	from, to point
}

// mouse is what the left button does over the log.
type mouse struct {
	// dragging is whether the button went down over the log and is not up yet, and anchor the cell it went down on.
	dragging bool
	anchor   point
	// pressed is the cell of the last press and pressedAt its time, which tell a double click from two clicks.
	pressed   point
	pressedAt time.Time
}

// click handles a press of a mouse button. The left button over the log starts a selection there, or selects the word
// under it and copies it on a double click. Any press clears the selection there was.
func (m *model) click(msg tea.MouseClickMsg) tea.Cmd {
	m.log.marked = nil
	m.mouse.dragging = false
	if msg.Button != tea.MouseLeft || msg.Y >= m.logShown() {
		return nil
	}
	at := m.log.at(msg.X, msg.Y, m.logHeight())
	double := at == m.mouse.pressed && time.Since(m.mouse.pressedAt) < doubleClickTime
	m.mouse.pressed, m.mouse.pressedAt = at, time.Now()
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

// drag extends the selection to the cell under the mouse. Dragging onto the log's first row or below the rows of it
// that show scrolls the log a row, so that the selection can reach past what the view shows.
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

// release ends a drag and copies what it selected, which stays highlighted. A click without a drag selected nothing.
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

// copy puts text on the clipboard through the terminal, with OSC 52, and through the system's clipboard, for terminals
// that keep OSC 52 from programs, and says so in the footer. A clipboard that fails leaves the other.
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
		m.copied.show(fmt.Sprintf("copied %d characters", utf8.RuneCountInString(text))),
	)
}

// at is the cell of the log under the mouse at x and y in a view of height rows. Above the log's first row it is the
// first cell, and below its last row the end of the last row.
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

// word is the span of the word under at: the cells around it up to a space or the end of the row. There is none on a
// space.
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

// markedText is the text of the selection without its styles. Rows that start lines end with a line break and lose
// the spaces at their ends, and a row cut from the end of the row before it joins that row as it is.
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

// markedCols is the columns of row in the selection, from included to excluded.
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

// highlight is the text of row with its selected cells in reverse video. A row with any selected cells loses its own
// styles.
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

// doubleClickTime is how soon a second press on the same cell makes a double click.
const doubleClickTime = 500 * time.Millisecond
