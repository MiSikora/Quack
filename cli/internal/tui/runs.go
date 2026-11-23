package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"quack/internal/session"
	"quack/internal/step"
	"quack/internal/wire"
)

type runs struct {
	active  *run
	spinner spinner.Model
	now     func() time.Time
}

type run struct {
	runnable
	step     string
	started  time.Time
	stopping bool
	cancel   context.CancelFunc
}

// Only Update touches the run.
type (
	runEvent struct {
		run   *run
		event step.Event
	}
	runEnded struct {
		run      *run
		data     json.RawMessage
		err      error
		canceled bool
	}
)

type logged struct {
	text string
}

type activity struct {
	ctx context.Context
	ch  chan tea.Msg
}

func newRuns() runs {
	return runs{spinner: spinner.New(spinner.WithSpinner(spinner.Dot)), now: time.Now}
}

func (r *runs) start(activity activity, started *run, work func(context.Context, func(step.Event)) (json.RawMessage, error)) (string, tea.Cmd) {
	ctx, cancel := context.WithCancel(activity.ctx)
	started.started, started.cancel = r.now(), cancel
	r.active = started
	emit := func(e step.Event) {
		if ended, ok := e.(step.Ended); ok {
			if _, nested := ended.Step.(step.RunRecipe); nested {
				return
			}
		}
		activity.send(runEvent{started, e})
	}
	do := func() tea.Msg {
		defer cancel()
		data, err := work(ctx, emit)
		activity.send(runEnded{started, data, err, ctx.Err() != nil})
		return nil
	}
	header := ""
	if started.recipe {
		header = "\n" + commandStyle.Render("› "+started.commandLine(nil))
	}
	return header, tea.Batch(do, r.spinner.Tick)
}

func (r *runs) event(e runEvent, hideData bool) string {
	switch event := e.event.(type) {
	case step.Started:
		e.run.step, e.run.started = event.Line, r.now()
		return commandStyle.Render("› " + event.Line)
	case step.Notified:
		if hideData {
			event.Notification.Data = nil
		}
		return notificationLines(event.Notification)
	case step.Ended:
		if hideData {
			event.Result.Data = nil
		}
		text := event.Result.Message
		if text == "" {
			text = statusStyle.Render("done")
		}
		now := r.now()
		lines := text + statusStyle.Render(" · "+duration(now.Sub(e.run.started)))
		if event.Result.Data != nil {
			lines += "\n" + indent(event.Result.IndentedData())
		}
		e.run.step, e.run.started = "", now
		return lines
	case step.Failed:
		now := r.now()
		line := problemStyle.Render(event.Err.Error()) + statusStyle.Render(" · "+duration(now.Sub(e.run.started)))
		e.run.step, e.run.started = "", now
		return line
	case step.Logged:
		return event.Text
	}
	return ""
}

func (r *runs) ended(e runEnded) string {
	r.active = nil
	var first, rest string
	switch {
	case e.err == nil && e.data != nil:
		return indent(wire.MessageResult{Data: e.data}.IndentedData())
	case e.err == nil:
		return ""
	case e.canceled:
		first = statusStyle.Render("canceled")
	default:
		head, tail, more := strings.Cut(e.err.Error(), "\n")
		first = problemStyle.Render(head)
		if more {
			// Styling each line on its own keeps lipgloss from padding the lines to the widest.
			for line := range strings.SplitSeq(tail, "\n") {
				rest += "\n" + problemStyle.Render(line)
			}
		}
		var failed *session.StepError
		if errors.As(e.err, &failed) {
			return strings.TrimPrefix(rest, "\n")
		}
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
	line := strings.TrimSpace(r.spinner.View()) + " " + a.ID + a.suffix()
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

func (a activity) send(msg tea.Msg) {
	select {
	case a.ch <- msg:
	case <-a.ctx.Done():
	}
}

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
		a.send(logged{statusStyle.Render(line)})
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

func notificationLines(n wire.Notification) string {
	if n.Data == nil {
		return indent(n.Message)
	}
	return indent(n.Message + "\n" + indent(n.IndentedData()))
}

func indent(text string) string { return "  " + strings.ReplaceAll(text, "\n", "\n  ") }

var commandStyle = lipgloss.NewStyle().Bold(true)
