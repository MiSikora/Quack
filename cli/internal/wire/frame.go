// Package wire is the quack protocol as the CLI speaks it: the requests it sends and the frames the app answers with.
// It mirrors Frame and Codec in the Kotlin library.
package wire

import (
	"bytes"
	"encoding/json"
)

// ProtocolVersion is the protocol this CLI speaks. It cannot talk to an app whose hello names another.
const ProtocolVersion = 1

// Frame is one line from the app. The app says Hello, then sends any number of Event, then one result or Error: a
// MessageResult for a call, an ActionsResult for describe. Malformed stands for a line that is none of them.
type Frame interface{ frame() }

type Hello struct {
	Protocol int
	Package  string
	Version  string
}

type Event struct {
	Message string
}

// MessageResult ends a call. Data is the JSON the action returned with its message, compacted, or nil without any.
type MessageResult struct {
	Message string
	Data    json.RawMessage
}

type ActionsResult struct {
	Actions []Action
}

// Error ends a request that failed. It is the error the CLI reports.
type Error struct {
	Code    ErrorCode
	Message string
}

type Malformed struct {
	Reason string
}

func (Hello) frame()         {}
func (Event) frame()         {}
func (MessageResult) frame() {}
func (ActionsResult) frame() {}
func (Error) frame()         {}
func (Malformed) frame()     {}

// IndentedData is Data with two spaces per level, as quack prints it.
func (r MessageResult) IndentedData() string {
	var out bytes.Buffer
	// Data is valid JSON, since DecodeFrame compacted it.
	json.Indent(&out, r.Data, "", "  ")
	return out.String()
}

func (e Error) Error() string { return "[" + string(e.Code) + "] " + e.Message }

// ErrorCode says why the app refused or failed a request. An app may send a code this list does not have yet.
type ErrorCode string

const (
	ErrorCodeBadFrame      ErrorCode = "bad_frame"
	ErrorCodeUnknownAction ErrorCode = "unknown_action"
	ErrorCodeInvalidArgs   ErrorCode = "invalid_args"
	ErrorCodeRejected      ErrorCode = "rejected"
	ErrorCodeFailed        ErrorCode = "failed"
)
