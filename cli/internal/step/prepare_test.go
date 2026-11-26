package step

import (
	"context"
	"reflect"
	"testing"

	"quack/internal/client"
	"quack/internal/fakes"
	"quack/internal/wire"
)

func TestPreparedCallBindsOnlyOmittedArgsAndKeepsDisplaySeparate(t *testing.T) {
	catalog := []wire.Action{{ID: "a", Params: []wire.Param{
		{Name: "required", Env: "TOKEN", Presence: wire.Required{}},
		{Name: "tags", Env: "TAGS", Type: wire.ParamTypeStringList},
		{Name: "fallback", Env: "EMPTY", Presence: wire.Default{Values: []string{"literal"}}},
	}}}
	for _, explicit := range []map[string]wire.RawArg{nil, {"required": {""}, "tags": {}}, {"required": {"${TOKEN}"}}} {
		app := fakes.NewApp()
		app.Replies["a"] = func(_ context.Context, call wire.Call, _ client.NotificationFunc) (wire.MessageResult, error) {
			return wire.MessageResult{Message: "echo " + call.Args["required"][0]}, nil
		}
		prepared := PrepareCall(catalog, wire.Call{Action: "a", Args: explicit}, nil, func(name string) string { return map[string]string{"TOKEN": "secret ${OTHER}", "TAGS": "a,b c"}[name] })
		result, err := prepared.Do(t.Context(), app, nil)
		if err != nil {
			t.Fatal(err)
		}
		got := app.Calls()[0].Args
		if _, given := got["fallback"]; given {
			t.Fatal("empty env must leave literal fallback to Android")
		}
		if _, given := explicit["required"]; !given {
			if got["required"][0] != "secret ${OTHER}" || prepared.Display.Args["required"][0] != "${TOKEN}" || result.Message != "echo secret ${OTHER}" {
				t.Fatalf("execution/display/returned result: %v, %v, %v", got, prepared.Display, result)
			}
		} else if !reflect.DeepEqual(got["required"], explicit["required"]) {
			t.Fatal("explicit arg changed")
		}
		if value, given := explicit["tags"]; given {
			if !reflect.DeepEqual(got["tags"], value) {
				t.Fatal("explicit empty list changed")
			}
		} else if !reflect.DeepEqual(got["tags"], wire.RawArg{"a,b c"}) {
			t.Fatal("list env must be one literal element")
		}
	}
}
