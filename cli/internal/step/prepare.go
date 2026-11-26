package step

import (
	"context"
	"maps"

	"quack/internal/client"
	"quack/internal/wire"
)

// PreparedCall keeps host values private to execution. Display is safe for command events and history.
type PreparedCall struct {
	call    wire.Call
	Display Call
}

func PrepareCall(actions []wire.Action, call wire.Call, shownArgs map[string]wire.RawArg, getenv func(string) string) PreparedCall {
	args := maps.Clone(call.Args)
	shown := maps.Clone(shownArgs)
	if shownArgs == nil {
		shown = maps.Clone(call.Args)
	}
	if action, ok := wire.FindAction(actions, call.Action); ok {
		for _, p := range action.Params {
			if _, given := args[p.Name]; given || p.Env == "" {
				continue
			}
			if value := getenv(p.Env); value != "" {
				if args == nil {
					args = map[string]wire.RawArg{}
				}
				if shown == nil {
					shown = map[string]wire.RawArg{}
				}
				args[p.Name] = wire.RawArg{value}
				shown[p.Name] = wire.RawArg{"${" + p.Env + "}"}
			}
		}
	}
	return PreparedCall{call: wire.Call{Action: call.Action, Args: args}, Display: Call{Action: call.Action, Args: shown}}
}

func (p PreparedCall) Do(ctx context.Context, app App, notify client.NotificationFunc) (wire.MessageResult, error) {
	return app.Call(ctx, p.call, notify)
}
