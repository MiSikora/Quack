package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"quack/internal/recipe"
	"quack/internal/wire"
)

type runs struct {
	// A run that is stopping stays active until it ends.
	active  *run
	spinner spinner.Model
	now     func() time.Time
}

type run struct {
	id      string
	recipe  bool
	step    string
	started time.Time
	// Kept so that the run's end does not repeat the error of the step that ended it.
	failure  error
	stopping bool
	cancel   context.CancelFunc
}

type work func(ctx context.Context, observer recipe.Observer) error

type runEnd interface{ runEnd() }

type (
	finished struct{}
	failed   struct{ err error }
	canceled struct{}
)

// Only Update touches the run.
type (
	stepStarted struct {
		run     *run
		command string
	}
	runNotification struct {
		message string
	}
	stepEnded struct {
		run    *run
		result wire.MessageResult
	}
	stepFailed struct {
		run *run
		err error
	}
	runLogged struct {
		text string
	}
	runEnded struct {
		run *run
		end runEnd
	}
)

type statusLine struct {
	text string
}

// Messages reach Update one at a time through listen. The context ends with the program, so that nothing waits on a
// program that is gone.
type activity struct {
	ctx context.Context
	ch  chan tea.Msg
}

func newRuns() runs {
	return runs{spinner: spinner.New(spinner.WithSpinner(spinner.Dot)), now: time.Now}
}

func (r *runs) start(activity activity, started *run, actions []wire.Action, work work) (string, tea.Cmd) {
	ctx, cancel := context.WithCancel(activity.ctx)
	started.started, started.cancel = r.now(), cancel
	r.active = started
	observer := recipe.Observer{
		Started:  func(step recipe.Step) { activity.send(stepStarted{started, recipe.Line(step, actions)}) },
		Notified: func(message string) { activity.send(runNotification{message}) },
		Ended:    func(_ recipe.Step, result wire.MessageResult) { activity.send(stepEnded{started, result}) },
		Failed:   func(_ recipe.Step, err error) { activity.send(stepFailed{started, err}) },
		Logged:   func(text string) { activity.send(runLogged{text}) },
	}
	do := func() tea.Msg {
		defer cancel()
		err := work(ctx, observer)
		var end runEnd
		switch {
		case err == nil:
			end = finished{}
		case ctx.Err() != nil:
			end = canceled{}
		default:
			end = failed{err}
		}
		activity.send(runEnded{started, end})
		return nil
	}
	header := ""
	if started.recipe {
		header = "\n" + commandStyle.Render("› "+started.id)
	}
	return header, tea.Batch(do, r.spinner.Tick)
}

func (r *runs) stepStarted(e stepStarted) string {
	e.run.step, e.run.started = e.command, r.now()
	return commandStyle.Render("› " + e.command)
}

func (r *runs) notification(e runNotification) string {
	return prefixed("  ", "  ", e.message)
}

func (r *runs) stepEnded(e stepEnded) string {
	text := e.result.Message
	if text == "" {
		text = statusStyle.Render("done")
	}
	now := r.now()
	lines := text + statusStyle.Render(" · "+duration(now.Sub(e.run.started)))
	if e.result.Data != nil {
		lines += "\n" + prefixed("  ", "  ", e.result.IndentedData())
	}
	e.run.step, e.run.started = "", now
	return lines
}

func (r *runs) stepFailed(e stepFailed) string {
	now := r.now()
	line := problemStyle.Render(e.err.Error()) + statusStyle.Render(" · "+duration(now.Sub(e.run.started)))
	e.run.step, e.run.started, e.run.failure = "", now, e.err
	return line
}

func (r *runs) ended(e runEnded) string {
	r.active = nil
	var first, rest string
	switch end := e.end.(type) {
	case finished:
		return ""
	case failed:
		head, tail, more := strings.Cut(end.err.Error(), "\n")
		first = problemStyle.Render(head)
		if more {
			// Styling each line on its own keeps lipgloss from padding the lines to the widest.
			for line := range strings.SplitSeq(tail, "\n") {
				rest += "\n" + problemStyle.Render(line)
			}
		}
		// The failed step said its error already, so only where the recipe was is left to say.
		if e.run.failure != nil && errors.Is(end.err, e.run.failure) {
			return strings.TrimPrefix(rest, "\n")
		}
	case canceled:
		first = statusStyle.Render("canceled")
	}
	return first + statusStyle.Render(" · "+duration(r.now().Sub(e.run.started))) + rest
}

func (r *runs) stop() bool {
	if r.active == nil || r.active.stopping {
		return false
	}
	r.active.cancel()
	r.active.stopping = true
	return true
}

func (r *runs) cancel() {
	if r.active != nil {
		r.active.cancel()
	}
}

// The spinner stops ticking while no run runs, and start starts it again.
func (r *runs) tick(msg tea.Msg) tea.Cmd {
	if _, ok := msg.(spinner.TickMsg); !ok || r.active == nil {
		return nil
	}
	var cmd tea.Cmd
	r.spinner, cmd = r.spinner.Update(msg)
	return cmd
}

func (r *runs) footer() string {
	// The spinner's frames end with a space of their own.
	a := r.active
	line := strings.TrimSpace(r.spinner.View()) + " " + a.id
	if a.recipe && a.step != "" {
		line += " · " + a.step
	}
	if a.stopping {
		line += " canceling…"
	}
	return line + " · ctrl+c cancel action"
}

func newActivity(ctx context.Context) activity {
	return activity{ctx, make(chan tea.Msg)}
}

// send drops msg once the program is gone.
func (a activity) send(msg tea.Msg) {
	select {
	case a.ch <- msg:
	case <-a.ctx.Done():
	}
}

// Update listens again after each message.
func (a activity) listen() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-a.ch:
			return msg
		case <-a.ctx.Done():
			return nil
		}
	}
}

func (a activity) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimSuffix(string(p), "\n"), "\n") {
		a.send(statusLine{line})
	}
	return len(p), nil
}

func duration(d time.Duration) string {
	if d = d.Round(10 * time.Millisecond); d < time.Minute {
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func prefixed(first, rest, text string) string {
	lines := strings.Split(text, "\n")
	for i := range lines {
		if i == 0 {
			lines[i] = first + lines[i]
		} else {
			lines[i] = rest + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func (finished) runEnd() {}
func (failed) runEnd()   {}
func (canceled) runEnd() {}

var commandStyle = lipgloss.NewStyle().Bold(true)
