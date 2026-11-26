package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Without the trailing newline.
func EncodeDescribe() []byte {
	line, _ := json.Marshal(requestBody{Method: "describe"})
	return line
}

// Without the trailing newline.
func EncodeCall(call Call) []byte {
	line, _ := json.Marshal(requestBody{Method: "call", Action: call.Action, Args: call.Args})
	return line
}

// line comes without its newline.
func DecodeFrame(line []byte) Frame {
	var body frameBody
	if err := json.Unmarshal(line, &body); err != nil {
		return Malformed{err.Error()}
	}
	switch {
	case body.Hello != nil:
		return Hello{Protocol: body.Hello.Protocol, Package: body.Hello.App.Package, Version: body.Hello.App.Version}
	case body.Notification != nil:
		return Notification{body.Notification.Message, compact(body.Notification.Data)}
	case body.Result != nil && body.Result.Actions != nil:
		return actionsResult(*body.Result.Actions)
	case body.Result != nil && body.Result.Message != nil:
		return MessageResult{*body.Result.Message, compact(body.Result.Data)}
	case body.Result != nil:
		return Malformed{"a result without a message or actions"}
	case body.Error != nil:
		return Error{Code: ErrorCode(body.Error.Code), Message: body.Error.Message}
	default:
		return Malformed{"not a hello, notification, result or error"}
	}
}

func compact(data json.RawMessage) json.RawMessage {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	var out bytes.Buffer
	// The whole line parsed, so data is valid JSON.
	json.Compact(&out, data)
	return out.Bytes()
}

func actionsResult(bodies []actionBody) Frame {
	actions := make([]Action, len(bodies))
	for i, a := range bodies {
		params := make([]Param, len(a.Params))
		for j, p := range a.Params {
			if p.Env != nil && !IsVariable(*p.Env) {
				return Malformed{fmt.Sprintf("param '%s' of '%s': env must name an environment variable", p.Name, a.ID)}
			}
			presence, err := paramPresence(p)
			if err != nil {
				return Malformed{fmt.Sprintf("param '%s' of '%s': %v", p.Name, a.ID, err)}
			}
			params[j] = Param{Name: p.Name, Description: p.Description, Type: ParamType(p.Type), Choices: p.Choices, Hints: p.Hints, Presence: presence}
			if p.Env != nil {
				params[j].Env = *p.Env
			}
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
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
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
	Env         *string         `json:"env"`
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Choices     []string        `json:"choices"`
	Hints       []string        `json:"hints"`
	Required    bool            `json:"required"`
	Default     json.RawMessage `json:"default"`
	Description string          `json:"description"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
