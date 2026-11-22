package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// EncodeDescribe returns the request that lists the app's actions, as one line without its newline.
func EncodeDescribe() []byte {
	line, _ := json.Marshal(requestBody{Method: "describe"})
	return line
}

// EncodeCall returns call as one line, without its newline.
func EncodeCall(call Call) []byte {
	line, _ := json.Marshal(requestBody{Method: "call", Action: call.Action, Args: call.Args})
	return line
}

// DecodeFrame reads one line from the app, without its newline.
func DecodeFrame(line []byte) Frame {
	var body frameBody
	if err := json.Unmarshal(line, &body); err != nil {
		return Malformed{err.Error()}
	}
	switch {
	case body.Hello != nil:
		return Hello{Protocol: body.Hello.Protocol, Package: body.Hello.App.Package, Version: body.Hello.App.Version}
	case body.Notification != nil:
		return Notification{body.Notification.Message}
	case body.Result != nil && body.Result.Actions != nil:
		return actionsResult(*body.Result.Actions)
	case body.Result != nil && body.Result.Message != nil:
		return messageResult(*body.Result.Message, body.Result.Data)
	case body.Result != nil:
		return Malformed{"a result without a message or actions"}
	case body.Error != nil:
		return Error{Code: ErrorCode(body.Error.Code), Message: body.Error.Message}
	default:
		return Malformed{"not a hello, notification, result or error"}
	}
}

func messageResult(message string, data json.RawMessage) Frame {
	if len(data) == 0 || string(data) == "null" {
		return MessageResult{Message: message}
	}
	var compact bytes.Buffer
	// The whole line parsed, so data is valid JSON.
	json.Compact(&compact, data)
	return MessageResult{Message: message, Data: compact.Bytes()}
}

func actionsResult(bodies []actionBody) Frame {
	actions := make([]Action, len(bodies))
	for i, a := range bodies {
		params := make([]Param, len(a.Params))
		for j, p := range a.Params {
			presence, err := paramPresence(p)
			if err != nil {
				return Malformed{fmt.Sprintf("param '%s' of '%s': %v", p.Name, a.ID, err)}
			}
			params[j] = Param{Name: p.Name, Description: p.Description, Type: ParamType(p.Type), Choices: p.Choices, Presence: presence}
		}
		actions[i] = Action{ID: a.ID, Description: a.Description, Params: params}
	}
	return ActionsResult{actions}
}

func paramPresence(p paramBody) (Presence, error) {
	switch {
	case p.Required:
		return Required{}, nil
	case p.Default == nil || string(p.Default) == "null":
		return Optional{}, nil
	}
	var value string
	if err := json.Unmarshal(p.Default, &value); err == nil {
		return Default{[]string{value}}, nil
	}
	var values []string
	if err := json.Unmarshal(p.Default, &values); err != nil {
		return nil, fmt.Errorf("default is not a string or an array of strings")
	}
	return Default{values}, nil
}

type requestBody struct {
	Method string            `json:"method"`
	Action string            `json:"action,omitempty"`
	Args   map[string]RawArg `json:"args,omitempty"`
}

type frameBody struct {
	Hello        *helloBody   `json:"hello"`
	Notification *messageBody `json:"notification"`
	Result       *resultBody  `json:"result"`
	Error        *errorBody   `json:"error"`
}

type helloBody struct {
	Protocol int `json:"protocol"`
	App      struct {
		Package string `json:"package"`
		Version string `json:"version"`
	} `json:"app"`
}

type messageBody struct {
	Message string `json:"message"`
}

type resultBody struct {
	Message *string         `json:"message"`
	Data    json.RawMessage `json:"data"`
	Actions *[]actionBody   `json:"actions"`
}

type actionBody struct {
	ID          string      `json:"id"`
	Description string      `json:"description"`
	Params      []paramBody `json:"params"`
}

type paramBody struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Choices     []string        `json:"choices"`
	Required    bool            `json:"required"`
	Default     json.RawMessage `json:"default"`
	Description string          `json:"description"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
