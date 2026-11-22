package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/recipe"
	"quack/internal/wire"
)

// notificationJSON is a notification as --json prints it, on a line before the outcome: codeNotification and the
// notification's message. Step numbers the recipe's step from 1, and is absent outside a recipe.
type notificationJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Step    int    `json:"step,omitempty"`
}

// outcomeJSON is how a command ended, as --json prints it on its last line: codeOK with the action's message and data,
// or the code of a failure and what went wrong. A recipe prints one for each step, which Step numbers from 1. The one
// of a failed step is its last, unless the recipe caught the failure and went on.
type outcomeJSON struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
	Step    int             `json:"step,omitempty"`
}

// recipeJSON is a recipe as quack --list-recipes --json prints it.
type recipeJSON struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Params      []paramJSON `json:"params"`
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
// what cannot be reached, no_reply for a call that ended without the app's answer, and command for a command of a
// recipe that failed.
func failure(err error) (code, message string, exit int) {
	var refused wire.Error
	var usage *usageError
	var device *adb.Error
	var app *client.AppError
	var call *client.CallError
	var command *recipe.CommandError
	switch {
	case errors.As(err, &refused):
		exit, ok := errorExits[refused.Code]
		if !ok {
			exit = exitAction
		}
		message := refused.Message
		// A recipe that ended on the app's error says where it was.
		var thrown *recipe.ScriptError
		if errors.As(err, &thrown) {
			message = (&recipe.ScriptError{Err: errors.New(message), Stack: thrown.Stack}).Error()
		}
		return string(refused.Code), message, exit
	case errors.As(err, &usage):
		return "usage", err.Error(), exitUsage
	case errors.As(err, &device):
		return "device", err.Error(), exitDevice
	case errors.As(err, &app):
		return "app", err.Error(), exitDevice
	case errors.As(err, &call):
		return "no_reply", err.Error(), exitAction
	case errors.As(err, &command):
		return "command", err.Error(), exitAction
	default:
		return "error", err.Error(), exitAction
	}
}

func printActionsJSON(w io.Writer, actions []wire.Action) {
	out := make([]actionJSON, len(actions))
	for i, a := range actions {
		out[i] = actionJSON{ID: a.ID, Description: a.Description, Params: paramsJSON(a.Params)}
	}
	printJSON(w, out)
}

func paramsJSON(params []wire.Param) []paramJSON {
	out := make([]paramJSON, len(params))
	for i, p := range params {
		out[i] = paramJSON{Name: p.Name, Type: string(p.Type), Description: p.Description, Choices: p.Choices}
		switch presence := p.Presence.(type) {
		case wire.Required:
			out[i].Required = true
		case wire.Default:
			if p.Type.IsList() {
				out[i].Default = presence.Values
			} else if len(presence.Values) > 0 {
				out[i].Default = presence.Values[0]
			}
		}
	}
	return out
}

func printJSON(w io.Writer, value any) {
	// Every value printed here holds only strings, bools, slices and valid JSON, so it always encodes.
	line, _ := json.Marshal(value)
	fmt.Fprintln(w, string(line))
}

// codeOK is the --json code of a command that succeeded, codeNotification of a notification before its outcome, and
// codeLog of a line that a recipe logged.
const (
	codeOK           = "ok"
	codeNotification = "notification"
	codeLog          = "log"
)
