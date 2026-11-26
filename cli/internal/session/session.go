package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"quack/internal/client"
	"quack/internal/recipe"
	"quack/internal/step"
	"quack/internal/wire"
)

type Runnable struct {
	Name   string
	Recipe bool
}

// A recipe's run ended with the error of step Step, which failed with nothing started or logged after it.
type StepError struct {
	Step int
	Err  error
}

func (e *StepError) Error() string { return e.Err.Error() }

func (e *StepError) Unwrap() error { return e.Err }

// Run runs a plain action as one step, or a recipe of cookbook, and returns the data the recipe returned. A plain
// action's failure is Run's error with no step.Failed before it; a recipe's comes back as *StepError when the failed
// step ended the run. The steps' lines show a Call's args in the order of its action's params in actions.
func Run(ctx context.Context, app step.App, actions []wire.Action, cookbook *recipe.Cookbook, r Runnable, args step.Arguments, emit func(step.Event)) (json.RawMessage, error) {
	if !r.Recipe {
		return nil, RunPrepared(ctx, app, actions, step.PrepareCall(actions, wire.Call{Action: r.Name, Args: args.Values}, args.Display, os.Getenv), emit)
	}
	var failed *step.Failed
	data, err := cookbook.Run(ctx, app, actions, r.Name, args, func(e step.Event) {
		switch e := e.(type) {
		case step.Failed:
			failed = &e
		case step.Started, step.Logged:
			failed = nil
		}
		emit(e)
	})
	if failed != nil && errors.Is(err, failed.Err) {
		return nil, &StepError{failed.Number, err}
	}
	return data, err
}

// Classify gives err's code: the app's error code, "command", "no_reply" or "error", and the message that goes with it.
func Classify(err error) (code, message string) {
	var refused wire.Error
	var command *step.CommandError
	var call *client.CallError
	switch {
	case errors.As(err, &refused):
		var thrown *recipe.ScriptError
		if errors.As(err, &thrown) {
			return string(refused.Code), (&recipe.ScriptError{Err: errors.New(refused.Message), Stack: thrown.Stack}).Error()
		}
		return string(refused.Code), refused.Message
	case errors.As(err, &command):
		return "command", err.Error()
	case errors.As(err, &call):
		return "no_reply", err.Error()
	}
	return "error", err.Error()
}

// RunPrepared executes one prepared action and emits only its display arguments in step events.
func RunPrepared(ctx context.Context, app step.App, actions []wire.Action, prepared step.PreparedCall, emit func(step.Event)) error {
	call := prepared.Display
	emit(step.Started{Line: step.Line(call, actions)})
	result, err := prepared.Do(ctx, app, func(notification wire.Notification) {
		emit(step.Notified{Notification: notification})
	})
	if err != nil {
		return err
	}
	emit(step.Ended{Step: call, Result: result})
	return nil
}
