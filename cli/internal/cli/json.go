package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/wire"
)

// eventJSON is an event as --json prints it, on a line before the outcome.
type eventJSON struct {
	Event string `json:"event"`
}

// outcomeJSON is how a command ended, as --json prints it on its last line: codeOK with the action's message and data,
// or the code of a failure and what went wrong.
type outcomeJSON struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// actionJSON is an action as quack --list-actions --json prints it.
type actionJSON struct {
	ID          string      `json:"id"`
	Description string      `json:"description"`
	Params      []paramJSON `json:"params"`
}

type paramJSON struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Choices     []string `json:"choices,omitempty"`
	Required    bool     `json:"required"`
	// Default is a string, or an array of strings for a list param, or absent without a default.
	Default any `json:"default,omitempty"`
}

// failure says how err ends quack: its code for --json, its message without the code, and the exit code. The app's
// error codes keep their names. Failures the CLI finds itself are usage for what the user typed, device and app for
// what cannot be reached, and no_reply for a call that ended without the app's answer.
func failure(err error) (code, message string, exit int) {
	var refused wire.Error
	var usage *usageError
	var device *adb.Error
	var app *client.AppError
	var call *client.CallError
	switch {
	case errors.As(err, &refused):
		exit, ok := errorExits[refused.Code]
		if !ok {
			exit = exitAction
		}
		return string(refused.Code), refused.Message, exit
	case errors.As(err, &usage):
		return "usage", err.Error(), exitUsage
	case errors.As(err, &device):
		return "device", err.Error(), exitDevice
	case errors.As(err, &app):
		return "app", err.Error(), exitDevice
	case errors.As(err, &call):
		return "no_reply", err.Error(), exitAction
	default:
		return "error", err.Error(), exitAction
	}
}

func printActionsJSON(w io.Writer, actions []wire.Action) {
	out := make([]actionJSON, len(actions))
	for i, a := range actions {
		params := make([]paramJSON, len(a.Params))
		for j, p := range a.Params {
			params[j] = paramJSON{Name: p.Name, Type: string(p.Type), Description: p.Description, Choices: p.Choices}
			switch presence := p.Presence.(type) {
			case wire.Required:
				params[j].Required = true
			case wire.Default:
				if p.Type.IsList() {
					params[j].Default = presence.Values
				} else if len(presence.Values) > 0 {
					params[j].Default = presence.Values[0]
				}
			}
		}
		out[i] = actionJSON{ID: a.ID, Description: a.Description, Params: params}
	}
	printJSON(w, out)
}

func printJSON(w io.Writer, value any) {
	// Every value printed here holds only strings, bools, slices and valid JSON, so it always encodes.
	line, _ := json.Marshal(value)
	fmt.Fprintln(w, string(line))
}

// codeOK is the --json code of a command that succeeded.
const codeOK = "ok"
