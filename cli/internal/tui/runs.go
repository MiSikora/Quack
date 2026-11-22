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

// runs is the action or the recipe running in the app, one at a time. The run writes its lines into the log as they
// come, and shows in the footer until it ends.
type runs struct {
	// active is the run, or nil while none runs. A run that is stopping stays active until it ends.
	active  *run
	spinner spinner.Model
	// now tells the time, which the end of a step measures from its start.
	now func() time.Time
}

// run is the one call of an action, or a run of a recipe, which takes its steps one after another until it returns or
// throws.
type run struct {
	// id names the run in the footer: the action's id, or the recipe's name after '$'.
	id string
	// recipe is whether the run is a recipe's, which the log heads with the recipe and the footer follows step by step.
	recipe bool
	// step is the step that runs as the user types it, or empty between steps.
	step string
	// started is when the step that runs started, or else when the last step ended, or the run started.
	started time.Time
	// failure is the error of the last step that failed, which the end of the run does not repeat when it is the one
	// that ended the run.
	failure  error
	stopping bool
	cancel   context.CancelFunc
}

// work is what a run does: it takes its steps against the app, tells observer about each, and returns the error that
// ended it.
type work func(ctx context.Context, observer recipe.Observer) error

// runEnd is how a run ended: finished, failed or canceled.
type runEnd interface{ runEnd() }

type (
	finished struct{}
	failed   struct{ err error }
	canceled struct{}
)

// stepStarted is the start of a step, as the user types it, runNotification a notification of the step, stepEnded or
// stepFailed its end, runLogged a line the recipe logged, and runEnded the end of the run. Only Update touches the run.
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

// statusLine is a line the app's client says about what it does, such as starting the app.
type statusLine struct {
	text string
}

// activity carries messages from the goroutines that wait on the app to Update, one at a time through listen. Its
// context ends with the program, so that nothing waits on a program that is gone.
type activity struct {
	ctx context.Context
	ch  chan tea.Msg
}

func newRuns() runs {
	return runs{spinner: spinner.New(spinner.WithSpinner(spinner.Dot)), now: time.Now}
}

// start does the work of started in a goroutine of its own, which sends what the work tells it to activity, each step
// as actions have the user type it, and the end of the run last. It returns the run's first lines: a blank one, and the
// recipe's name for a recipe.
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

// stepStarted is the line of a step's start, the step as the user types it, which heads the step's lines.
func (r *runs) stepStarted(e stepStarted) string {
	e.run.step, e.run.started = e.command, r.now()
	return commandStyle.Render("› " + e.command)
}

// notification is the lines of a notification, indented under its step.
func (r *runs) notification(e runNotification) string {
	return prefixed("  ", "  ", e.message)
}

// stepEnded is the lines of a step's end: its message, or done without one, the time the step took, and its data
// under them.
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

// stepFailed is the line of a step's failure: its error, and the time the step took. The recipe may catch the error
// and go on.
func (r *runs) stepFailed(e stepFailed) string {
	now := r.now()
	line := problemStyle.Render(e.err.Error()) + statusStyle.Render(" · "+duration(now.Sub(e.run.started)))
	e.run.step, e.run.started, e.run.failure = "", now, e.err
	return line
}

// ended is the lines of the run's end, after which it no longer shows: nothing for a run that finished, and else the
// error or canceled, with the time since the last step started or ended after its first line. The rest of an error,
// such as where a recipe threw, goes under it.
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
			// Each line takes the style on its own, which pads no line to the width of the others.
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

// stop cancels the run unless it is stopping already, and says whether it did.
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

// tick turns the spinner while a run runs. It lets the spinner stop otherwise, and start starts it again.
func (r *runs) tick(msg tea.Msg) tea.Cmd {
	if _, ok := msg.(spinner.TickMsg); !ok || r.active == nil {
		return nil
	}
	var cmd tea.Cmd
	r.spinner, cmd = r.spinner.Update(msg)
	return cmd
}

// footer names the run, for the footer while it is active, with the step that runs for a recipe.
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

// send hands msg to the program, or drops it once the program is gone.
func (a activity) send(msg tea.Msg) {
	select {
	case a.ch <- msg:
	case <-a.ctx.Done():
	}
}

// listen waits for the next message. Update listens again after each one.
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

// Write turns the client's status lines into messages.
func (a activity) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimSuffix(string(p), "\n"), "\n") {
		a.send(statusLine{line})
	}
	return len(p), nil
}

// duration is how long a run took: to a hundredth of a second under a minute, as in 1.23s, and in whole seconds from a
// minute on, as in 1m05s.
func duration(d time.Duration) string {
	if d = d.Round(10 * time.Millisecond); d < time.Minute {
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// prefixed starts the first line of text with first and the others with rest.
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
