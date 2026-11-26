package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"quack/internal/client"
	"quack/internal/wire"
)

func TestActionNumbersSurviveRequestsAndResultsExactly(t *testing.T) {
	source := newFakeSource()
	source.App.Replies["account.create"] = func(_ context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error) {
		if got := call.Args["age"][0]; got != "9007199254740993" {
			t.Errorf("sent age %s", got)
		}
		data := json.RawMessage(`{"id":9007199254740993,"nested":[9223372036854775807,-9223372036854775808],"ratio":0.1234567890123456789}`)
		notify(wire.Notification{Message: "number", Data: data})
		return wire.MessageResult{Data: data}, nil
	}

	rpc := connect(t, source)
	for _, mode := range []string{"run_action", "start_action"} {
		result := call(t, rpc, mode, map[string]any{"action": "account.create", "args": map[string]any{"username": "alice", "age": json.Number("9007199254740993")}})
		if mode == "start_action" {
			result = readToEnd(t, rpc, result.StructuredContent.(map[string]any)["run"].(string))
		}
		// The Go client decodes StructuredContent through float64; text retains the server's JSON bytes.
		for _, want := range []string{`"id":9007199254740993`, `9223372036854775807`, `-9223372036854775808`, `0.1234567890123456789`} {
			if !strings.Contains(text(result), want) {
				t.Errorf("%s result lost %s: %s", mode, want, text(result))
			}
		}
	}

}

func TestLongAndEmptyListFormsSendExactValues(t *testing.T) {
	source := newFakeSource()
	actions := source.App.Actions()
	actions[1].Params = []wire.Param{{Name: "username", Type: wire.ParamTypeString, Presence: wire.Required{}}, {Name: "age", Type: wire.ParamTypeLong, Presence: wire.Required{}}, {Name: "tags", Type: wire.ParamTypeStringList, Presence: wire.Required{}}}
	source.App.SetActions(actions)
	options := &mcp.ClientOptions{ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		encoded, _ := json.Marshal(req.Params.RequestedSchema)
		var schema jsonschema.Schema
		json.Unmarshal(encoded, &schema)
		if got := schema.Properties["age"].Type; got != "string" {
			t.Errorf("long form type %s", got)
		}
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"username": "alice", "age": "9223372036854775807", "tags": ""}}, nil
	}}
	call(t, connectWith(t, source, options), "run_action", map[string]any{"action": "account.create"})
	got := source.App.Calls()[0]
	if got.Args["age"][0] != "9223372036854775807" {
		t.Errorf("age %v", got.Args["age"])
	}
	if got.Args["tags"] == nil || len(got.Args["tags"]) != 0 {
		t.Errorf("empty list %v", got.Args["tags"])
	}
	encoded, _ := json.Marshal(got.Args)
	if !strings.Contains(string(encoded), `"tags":[]`) {
		t.Errorf("wire args %s", encoded)
	}
}

func TestToolBoundaryStillRejectsInvalidInputs(t *testing.T) {
	source := newFakeSource()
	rpc := connect(t, source)
	for _, args := range []map[string]any{{}, {"action": 7}, {"action": "account.create", "args": "oops"}} {
		if result := callTool(t, rpc, "run_action", args); !result.IsError {
			t.Errorf("accepted invalid args %v", args)
		}
	}
	if len(source.App.Calls()) != 0 {
		t.Fatal("ran invalid call")
	}
}

func TestLongFormRejectsNumericResponsesRatherThanRounding(t *testing.T) {
	source := newFakeSource()
	actions := source.App.Actions()
	actions[1].Params[1].Type = wire.ParamTypeLong
	actions[1].Params[1].Presence = wire.Required{}
	source.App.SetActions(actions)
	rpc := connectWith(t, source, &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"age": float64(9007199254740992)}}, nil
	}})
	result, err := rpc.CallTool(t.Context(), &mcp.CallToolParams{Name: "run_action", Arguments: map[string]any{"action": "account.create", "args": map[string]any{"username": "alice"}}})
	if err == nil && !result.IsError {
		t.Fatal("numeric long form was accepted")
	}
	if len(source.App.Calls()) != 0 {
		t.Fatal("unsafe long form ran")
	}
}
