// Package fakes holds the test doubles that the tests of several packages share. Only tests import it, so it stays out
// of the binaries.
package fakes

import (
	"context"
	"io"
	"slices"
	"sync"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/device"
	"quack/internal/wire"
)

// A Reply answers a call of one action, as the app would.
type Reply func(ctx context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error)

// App answers a call with the Reply of its action, and a call of an action without one as the app answers an unknown
// action. A command answers with what CommandReply gives, or else prints "ran" and the command. A start writes
// "starting" to Status. Set the fields before the App is in use, and change what it describes with SetActions and
// SetDescribeErr.
type App struct {
	Device       adb.Device
	Package      string
	Replies      map[string]Reply
	CommandReply func(cmd string) adb.Output
	Status       io.Writer

	mu          sync.Mutex
	actions     []wire.Action
	describeErr error
	calls       []wire.Call
	starts      []client.StartMode
	commands    []string
}

func NewApp(actions ...wire.Action) *App {
	return &App{Device: "emulator-5554", Package: "io.mehow.quack.sample", Replies: map[string]Reply{}, actions: actions}
}

func (a *App) Target() device.Target { return device.Target{Device: a.Device, Package: a.Package} }

func (a *App) Describe(context.Context) (wire.Hello, []wire.Action, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.describeErr != nil {
		return wire.Hello{}, nil, a.describeErr
	}
	return wire.Hello{Protocol: 1, Package: a.Package, Version: "1.0"}, a.actions, nil
}

func (a *App) Call(ctx context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error) {
	a.mu.Lock()
	a.calls = append(a.calls, call)
	reply := a.Replies[call.Action]
	a.mu.Unlock()
	if reply == nil {
		return wire.MessageResult{}, wire.Error{Code: wire.ErrorCodeUnknownAction, Message: "no action '" + call.Action + "'"}
	}
	return reply(ctx, call, notify)
}

func (a *App) Start(ctx context.Context, mode client.StartMode) error {
	if ctx.Err() != nil {
		return client.ErrCanceled
	}
	a.mu.Lock()
	a.starts = append(a.starts, mode)
	a.mu.Unlock()
	if a.Status != nil {
		io.WriteString(a.Status, "starting "+a.Package+"…\n")
	}
	return nil
}

func (a *App) Command(_ context.Context, cmd string) (adb.Output, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.commands = append(a.commands, cmd)
	if a.CommandReply != nil {
		return a.CommandReply(cmd), nil
	}
	return adb.Output{Stdout: "ran " + cmd + "\n"}, nil
}

func (a *App) SetActions(actions []wire.Action) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = actions
}

func (a *App) SetDescribeErr(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.describeErr = err
}

func (a *App) Actions() []wire.Action {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.actions
}

func (a *App) Calls() []wire.Call {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.calls)
}

func (a *App) Starts() []client.StartMode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.starts)
}

func (a *App) Commands() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.commands)
}
