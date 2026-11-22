package wire

import (
	"bytes"
	"encoding/json"
)

// It cannot talk to an app whose hello names another.
const ProtocolVersion = 1

// The app says Hello, then any number of Notification, then one result or Error. Malformed is a line that is none
// of them.
type Frame interface{ frame() }

type Hello struct {
	Protocol int
	Package  string
	Version  string
}

// Data is compacted JSON, or nil when the action sent none.
type Notification struct {
	Message string
	Data    json.RawMessage
}

// Data is compacted JSON, or nil when the action returned none.
type MessageResult struct {
	Message string
	Data    json.RawMessage
}

type ActionsResult struct {
	Actions []Action
}

type Error struct {
	Code    ErrorCode
	Message string
}

type Malformed struct {
	Reason string
}

func (Hello) frame()         {}
func (Notification) frame()  {}
func (MessageResult) frame() {}
func (ActionsResult) frame() {}
func (Error) frame()         {}
func (Malformed) frame()     {}

func (n Notification) IndentedData() string { return indent(n.Data) }

func (r MessageResult) IndentedData() string { return indent(r.Data) }

func indent(data json.RawMessage) string {
	var out bytes.Buffer
	// data is valid JSON, since DecodeFrame compacted it.
	json.Indent(&out, data, "", "  ")
	return out.String()
}

func (e Error) Error() string { return "[" + string(e.Code) + "] " + e.Message }

// An app may send a code this list does not have yet.
type ErrorCode string

const (
	ErrorCodeBadFrame      ErrorCode = "bad_frame"
	ErrorCodeUnknownAction ErrorCode = "unknown_action"
	ErrorCodeInvalidArgs   ErrorCode = "invalid_args"
	ErrorCodeRejected      ErrorCode = "rejected"
	ErrorCodeFailed        ErrorCode = "failed"
)
