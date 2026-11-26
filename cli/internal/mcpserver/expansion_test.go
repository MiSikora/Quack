package mcpserver

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"quack/internal/wire"
)

func TestMCPExpandsActionAndRecipeArgumentsIncludingForms(t *testing.T) {
	for _, tool := range []string{"run_action", "start_action", "run_recipe"} {
		for _, form := range []bool{false, true} {
			for _, tc := range []struct{ name, input, want string }{{"expanded", "${QUACK_INPUT}", "private-token"}, {"escaped", "$${QUACK_INPUT}", "${QUACK_INPUT}"}} {
				name := tool + "/" + tc.name
				if form {
					name += "/form"
				}
				t.Run(name, func(t *testing.T) {
					t.Setenv("QUACK_INPUT", "private-token")
					if tool == "run_recipe" {
						writeRecipes(t, `import quack from "quack"
quack.recipe({name:"input",params:{username:{type:"string",required:true}},run({username}){quack.runAction("account.create",{username});quack.run("printf "+username)}})`)
					}
					source := newFakeSource()
					var options *mcp.ClientOptions
					if form {
						options = &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
							return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"username": tc.input}}, nil
						}}
					}
					rpc := connectWith(t, source, options)
					input := map[string]any{"action": "account.create"}
					if tool == "run_recipe" {
						input = map[string]any{"recipe": "input"}
					}
					if !form {
						input["args"] = map[string]any{"username": tc.input}
					}
					result := call(t, rpc, tool, input)
					if tool == "start_action" {
						result = readToEnd(t, rpc, result.StructuredContent.(map[string]any)["run"].(string))
					}
					if got := source.App.Calls()[0].Args["username"][0]; got != tc.want {
						t.Errorf("sent %q, want %q", got, tc.want)
					}
					if tool == "run_recipe" {
						for _, s := range result.StructuredContent.(map[string]any)["steps"].([]any) {
							line := s.(map[string]any)["step"].(string)
							if strings.Contains(line, "private-token") || !strings.Contains(line, "${QUACK_INPUT}") {
								t.Errorf("unmasked step %q", line)
							}
						}
					}
				})
			}
		}
	}
}

func TestMCPMissingVariablePreventsDispatch(t *testing.T) {
	t.Setenv("QUACK_PARITY_UNSET", "")
	if err := os.Unsetenv("QUACK_PARITY_UNSET"); err != nil {
		t.Fatal(err)
	}
	writeRecipes(t, `import quack from "quack"
quack.recipe({name:"input",params:{username:{type:"string",required:true}},run({username}){quack.runAction("account.create",{username})}})`)
	source := newFakeSource()
	rpc := connect(t, source)
	for _, tool := range []string{"run_action", "start_action", "run_recipe"} {
		input := map[string]any{"args": map[string]any{"username": "${QUACK_PARITY_UNSET}"}}
		if tool == "run_recipe" {
			input["recipe"] = "input"
		} else {
			input["action"] = "account.create"
		}
		result := callTool(t, rpc, tool, input)
		if !result.IsError || !strings.Contains(text(result), "is not set") {
			t.Errorf("missing variable accepted: %s", text(result))
		}
	}
	if len(source.App.Calls()) != 0 {
		t.Fatal("missing variable dispatched")
	}
}

func TestMCPListFormsPreserveExactJSONAndExpandAfterParsing(t *testing.T) {
	t.Setenv("QUACK_INPUT", "private-token")
	for _, tc := range []struct {
		text  string
		valid bool
	}{
		{`["", " spaced ", "a,b", "${QUACK_INPUT}", "$${QUACK_INPUT}"]`, true},
		{`[null]`, false}, {`[1]`, false}, {`["a"] trailing`, false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			source := newFakeSource()
			actions := source.App.Actions()
			actions[1].Params[2].Presence = wire.Required{}
			source.App.SetActions(actions)
			rpc := connectWith(t, source, &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"tags": tc.text}}, nil
			}})
			result := callTool(t, rpc, "run_action", map[string]any{"action": "account.create", "args": map[string]any{"username": "alice"}})
			if result.IsError == tc.valid {
				t.Fatalf("unexpected result: %s", text(result))
			}
			calls := source.App.Calls()
			if !tc.valid {
				if len(calls) != 0 {
					t.Fatal("invalid list ran")
				}
				return
			}
			if got, want := calls[0].Args["tags"], (wire.RawArg{"", " spaced ", "a,b", "private-token", "${QUACK_INPUT}"}); !reflect.DeepEqual(got, want) {
				t.Errorf("sent %#v, want %#v", got, want)
			}
		})
	}
}
