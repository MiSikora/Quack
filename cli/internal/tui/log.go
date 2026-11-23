package tui

import (
	"bytes"
	"io"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type sessionLog struct {
	lines    []string
	width    int
	rows     []logRow
	scrolled int
	marked   *span
}

type logRow struct {
	text      string
	continues bool
}

func (l *sessionLog) add(text string) {
	lines := strings.Split(text, "\n")
	l.lines = append(l.lines, lines...)
	rows := cutRows(lines, l.width)
	l.rows = append(l.rows, rows...)
	if l.scrolled > 0 {
		l.scrolled += len(rows)
	}
}

func (l *sessionLog) clear() {
	*l = sessionLog{width: l.width}
}

func (l *sessionLog) resize(width, height int) {
	if width != l.width {
		l.width = width
		l.rows = cutRows(l.lines, width)
		l.marked = nil
	}
	l.scroll(0, height)
}

func (l *sessionLog) scroll(rows, height int) {
	l.scrolled = max(0, min(l.scrolled+rows, len(l.rows)-height))
}

func (l *sessionLog) top(height int) int {
	return max(0, len(l.rows)-l.scrolled-height)
}

func (l *sessionLog) view(height int) []string {
	view := make([]string, max(0, height))
	top := l.top(height)
	for y := range view {
		if i := top + y; i < len(l.rows) {
			view[y] = l.highlight(i)
		}
	}
	return view
}

func cutRows(lines []string, width int) []logRow {
	if width <= 0 {
		return nil
	}
	var rows []logRow
	for _, line := range lines {
		for i, text := range strings.Split(restyle(ansi.Hardwrap(line, width, true)), "\n") {
			rows = append(rows, logRow{text, i > 0})
		}
	}
	return rows
}

// WrapWriter ends the styles of each line at its end and starts them again on the next.
func restyle(text string) string {
	var b bytes.Buffer
	w := lipgloss.NewWrapWriter(&b)
	io.WriteString(w, text)
	w.Close()
	return b.String()
}
