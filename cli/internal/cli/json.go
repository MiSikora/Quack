package cli

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/recipe"
	"quack/internal/wire"
)

type notificationJSON struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
	Step    int             `json:"step,omitempty"`
}

type outcomeJSON struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
	Step    int             `json:"step,omitempty"`
}

type recipeJSON struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Params      []paramJSON `json:"params"`
}

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
	Default     any      `json:"default,omitempty"`
}

func failure(err error) (code, message string, exit int) {
	var refused wire.Error
	var usage *usageError
	var device *adb.Error
	var app *client.AppError
	var call *client.CallError
	var command *recipe.CommandError
	switch {
	case errors.As(err, &refused):
		exit := cmp.Or(errorExits[refused.Code], exitAction)
		message := refused.Message
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

const (
	codeOK           = "ok"
	codeNotification = "notification"
	codeLog          = "log"
)
