package tui

import (
	"context"
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
	// now tells the time, which the end of a run measures from its start.
	now func() time.Time
}

// run is a run of steps, one after another until one does not succeed: the one call of an action, or the steps of a
// recipe.
type run struct {
	// id names the run in the footer: the action's id, or the recipe's name after '$'.
	id string
	// recipe is whether the steps are a recipe's, which the log heads with the recipe and the footer counts.
	recipe bool
	steps  []runStep
	// step indexes the step that runs, and started is when it started.
	step     int
	started  time.Time
	stopping bool
	cancel   context.CancelFunc
}

// runStep is a step of a run, and command the step as the user types it, which heads its lines in the log.
type runStep struct {
	step    recipe.Step
	command string
}

// runEnd is how a step ended: succeeded, failed or canceled.
type runEnd interface{ runEnd() }

type (
	succeeded struct{ result wire.MessageResult }
	failed    struct{ err error }
	canceled  struct{}
)

// runNotification is a notification of a step, and stepEnded its end. Only Update touches the run.
type (
	runNotification struct {
		message string
	}
	stepEnded struct {
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

// start runs the steps of started in a goroutine of its own, which sends the notifications and the end of each step to
// activity, and stops after a step that does not succeed. It returns the run's first lines: a blank one, the recipe's
// name for a recipe, and the first step as the user types it.
func (r *runs) start(app App, activity activity, started *run) (string, tea.Cmd) {
	ctx, cancel := context.WithCancel(activity.ctx)
	started.started, started.cancel = r.now(), cancel
	r.active = started
	steps := started.steps
	work := func() tea.Msg {
		defer cancel()
		for _, s := range steps {
			result, err := recipe.Do(ctx, app, s.step, func(message string) { activity.send(runNotification{message}) })
			var end runEnd
			switch {
			case err == nil:
				end = succeeded{result}
			case ctx.Err() != nil:
				end = canceled{}
			default:
				end = failed{err}
			}
			activity.send(stepEnded{started, end})
			if _, ok := end.(succeeded); !ok {
				break
			}
		}
		return nil
	}
	header := "\n"
	if started.recipe {
		header += commandStyle.Render("› "+started.id) + "\n"
	}
	header += commandStyle.Render("› " + steps[0].command)
	return header, tea.Batch(work, r.spinner.Tick)
}

// notification is the lines of a notification, indented under its step.
func (r *runs) notification(e runNotification) string {
	return prefixed("  ", "  ", e.message)
}

// end is the lines of a step's end, with the time the step took after its message and above its data. A step that
// succeeded and has another after it goes on to that one, whose command follows. Otherwise the run ends and no longer
// shows.
func (r *runs) end(e stepEnded) string {
	var text, data string
	switch end := e.end.(type) {
	case succeeded:
		text = end.result.Message
		if text == "" {
			text = statusStyle.Render("done")
		}
		if end.result.Data != nil {
			data = "\n" + prefixed("  ", "  ", end.result.IndentedData())
		}
	case failed:
		text = problemStyle.Render(end.err.Error())
	case canceled:
		text = statusStyle.Render("canceled")
	}
	lines := text + statusStyle.Render(" · "+duration(r.now().Sub(e.run.started))) + data
	if _, ok := e.end.(succeeded); !ok || e.run.step == len(e.run.steps)-1 {
		r.active = nil
		return lines
	}
	e.run.step++
	e.run.started = r.now()
	return lines + "\n" + commandStyle.Render("› "+e.run.steps[e.run.step].command)
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
	if a.recipe {
		line += fmt.Sprintf(" · step %d/%d %s", a.step+1, len(a.steps), a.steps[a.step].command)
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

func (succeeded) runEnd() {}
func (failed) runEnd()    {}
func (canceled) runEnd()  {}

var commandStyle = lipgloss.NewStyle().Bold(true)
