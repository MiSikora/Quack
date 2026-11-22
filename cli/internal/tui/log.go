package tui

import (
	"bytes"
	"io"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// sessionLog is everything the session wrote, newest last, cut into the rows of the window's width. It fills the view
// from the top down, as a terminal does, and once the view is full it shows the newest row at the bottom and scrolls
// back through older output.
type sessionLog struct {
	// lines are the lines as they were written, for cutting them again at another width.
	lines []string
	width int
	rows  []logRow
	// scrolled is how many rows the view sits above the newest row. At 0 the view follows new output.
	scrolled int
	// marked is the selection, highlighted in the view, or nil without one.
	marked *span
}

// logRow is one row of the log as it is drawn. Each row carries its own styles, so that it draws alike wherever the
// view starts.
type logRow struct {
	text string
	// continues is whether the row goes on from the row above, which was cut at the width, rather than starting a line.
	continues bool
}

// add writes text under what is there. A view scrolled back stays where it is.
func (l *sessionLog) add(text string) {
	lines := strings.Split(text, "\n")
	l.lines = append(l.lines, lines...)
	rows := cutRows(lines, l.width)
	l.rows = append(l.rows, rows...)
	if l.scrolled > 0 {
		l.scrolled += len(rows)
	}
}

// resize cuts the lines again at width, for a view of height rows. A view that followed new output keeps following
// it, and one scrolled back stays as far back as the new rows allow.
func (l *sessionLog) resize(width, height int) {
	if width != l.width {
		l.width = width
		l.rows = cutRows(l.lines, width)
		l.marked = nil
	}
	l.scroll(0, height)
}

// scroll moves the view of height rows by rows, back through older output when rows is positive, and stops at either
// end.
func (l *sessionLog) scroll(rows, height int) {
	l.scrolled = max(0, min(l.scrolled+rows, len(l.rows)-height))
}

// follow brings the view back to the newest output.
func (l *sessionLog) follow() { l.scrolled = 0 }

// top is the index of the row that shows on the view's first row of height. It is the first row while the log is
// shorter than the view.
func (l *sessionLog) top(height int) int {
	return max(0, len(l.rows)-l.scrolled-height)
}

// view is height rows of the log, the newest at the bottom unless scrolled back, with blank rows under the log while it
// is shorter than the view, and the selection highlighted.
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

// cutRows cuts lines into rows of width. A row cut from the end of the one before it continues it.
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

// restyle ends the styles of each line of text at its end and starts them again on the next.
func restyle(text string) string {
	var b bytes.Buffer
	w := lipgloss.NewWrapWriter(&b)
	io.WriteString(w, text)
	w.Close()
	return b.String()
}
