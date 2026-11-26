package cli

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"quack/internal/adb"
	"quack/internal/device"
	"quack/internal/session"
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
	Env         string   `json:"env,omitempty"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Choices     []string `json:"choices,omitempty"`
	Required    bool     `json:"required"`
	Default     any      `json:"default,omitempty"`
}

func failure(err error) (code, message string, exit int) {
	var usage *usageError
	var unreachable *adb.Error
	var app *device.AppError
	switch {
	case errors.As(err, &usage):
		return "usage", err.Error(), exitUsage
	case errors.As(err, &unreachable):
		return "device", err.Error(), exitDevice
	case errors.As(err, &app):
		return "app", err.Error(), exitDevice
	}
	code, message = session.Classify(err)
	return code, message, cmp.Or(errorExits[wire.ErrorCode(code)], exitAction)
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
		out[i] = paramJSON{
			Name:        p.Name,
			Env:         p.Env,
			Type:        string(p.Type),
			Description: p.Description,
			Choices:     p.Choices,
			Required:    p.Required(),
			Default:     p.DefaultValue(),
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
