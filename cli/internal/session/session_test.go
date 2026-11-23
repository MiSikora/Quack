package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/device"
	"quack/internal/recipe"
	"quack/internal/step"
	"quack/internal/wire"
)

func TestRunRunsAPlainActionAsOneUnnumberedStep(t *testing.T) {
	var events []step.Event

	data, err := Run(context.Background(), &fakeApp{}, actions, nil, Runnable{Name: "account.create"}, map[string]wire.RawArg{"username": {"ann"}, "age": {"7"}}, collect(&events))

	want := []step.Event{
		step.Started{Line: "account.create username=ann age=7"},
		step.Notified{Notification: wire.Notification{Message: "Creating ann"}},
		step.Ended{
			Step:   step.Call{Action: "account.create", Args: map[string]wire.RawArg{"username": {"ann"}, "age": {"7"}}},
			Result: wire.MessageResult{Message: "Created ann", Data: json.RawMessage(`{"id":7}`)},
		},
	}
	if data != nil || err != nil || !reflect.DeepEqual(events, want) {
		t.Errorf("got %s, %v, %#v, want %#v", data, err, events, want)
	}
}

func TestAPlainActionsFailureIsTheRunsErrorWithoutAFailedStep(t *testing.T) {
	var events []step.Event

	_, err := Run(context.Background(), &fakeApp{}, actions, nil, Runnable{Name: "fail"}, nil, collect(&events))

	var failed *StepError
	if err != refused || errors.As(err, &failed) {
		t.Errorf("got %#v, want the app's error as it is", err)
	}
	if want := []step.Event{step.Started{Line: "fail"}}; !reflect.DeepEqual(events, want) {
		t.Errorf("got %#v, want %#v", events, want)
	}
}

func TestRunRunsARecipeWithNumberedStepsAndReturnsItsData(t *testing.T) {
	cookbook := load(t, `
import quack from "quack"
export const r = quack.recipe({
  run() {
    console.log("first")
    const created = quack.runAction("account.create", { age: 7, username: "ann" })
    console.log("then")
    return created.data
  },
})`)
	var events []step.Event

	data, err := Run(context.Background(), &fakeApp{}, actions, cookbook, Runnable{Name: "r", Recipe: true}, nil, collect(&events))

	want := []step.Event{
		step.Logged{Text: "first"},
		step.Started{Number: 1, Line: "account.create username=ann age=7"},
		step.Notified{Number: 1, Notification: wire.Notification{Message: "Creating ann"}},
		step.Ended{
			Number: 1,
			Step:   step.Call{Action: "account.create", Args: map[string]wire.RawArg{"username": {"ann"}, "age": {"7"}}},
			Result: wire.MessageResult{Message: "Created ann", Data: json.RawMessage(`{"id":7}`)},
		},
		step.Logged{Number: 1, Text: "then"},
	}
	if string(data) != `{"id":7}` || err != nil || !reflect.DeepEqual(events, want) {
		t.Errorf("got %s, %v, %#v, want %#v", data, err, events, want)
	}
}

func TestAFailedStepThatEndsTheRecipeComesBackAsAStepError(t *testing.T) {
	cookbook := load(t, `
import quack from "quack"
export const r = quack.recipe({
  run() {
    quack.openApp()
    quack.runAction("fail")
  },
})`)

	_, err := Run(context.Background(), &fakeApp{}, actions, cookbook, Runnable{Name: "r", Recipe: true}, nil, func(step.Event) {})

	var failed *StepError
	var thrown *recipe.ScriptError
	if !errors.As(err, &failed) || failed.Step != 2 || !errors.Is(err, refused) || !errors.As(err, &thrown) {
		t.Fatalf("got %#v, want step 2's error", err)
	}
	if want := "[rejected] no way\n    at run (io.mehow.quack.sample.ts:5:20)"; err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

func TestAFailedStepThatTheRecipeWentOnFromIsNoStepError(t *testing.T) {
	cookbook := load(t, `
import quack from "quack"
export const rethrows = quack.recipe({
  run() {
    try {
      quack.runAction("fail")
    } catch (e) {
      console.log("caught")
      throw e
    }
  },
})
export const goesOn = quack.recipe({
  run() {
    try {
      quack.runAction("fail")
    } catch (e) {}
    quack.openApp()
  },
})`)

	_, rethrown := Run(context.Background(), &fakeApp{}, actions, cookbook, Runnable{Name: "rethrows", Recipe: true}, nil, func(step.Event) {})
	_, wentOn := Run(context.Background(), &fakeApp{}, actions, cookbook, Runnable{Name: "goesOn", Recipe: true}, nil, func(step.Event) {})

	var failed *StepError
	if !errors.Is(rethrown, refused) || errors.As(rethrown, &failed) {
		t.Errorf("rethrows: got %#v, want the app's error without a step", rethrown)
	}
	if wentOn != nil {
		t.Errorf("goesOn: got %v", wentOn)
	}
}

func TestClassifyGivesTheAppsCodeOrTheKindOfFailure(t *testing.T) {
	thrown := &recipe.ScriptError{Err: refused, Stack: []string{"run (r.ts:1:1)"}}
	cases := []struct {
		err           error
		code, message string
	}{
		{refused, "rejected", "no way"},
		{&StepError{Step: 2, Err: thrown}, "rejected", "no way\n    at run (r.ts:1:1)"},
		{&step.CommandError{Exit: 3, Output: "nope"}, "command", "the command exited with 3: nope"},
		{fmt.Errorf("wrapped: %w", client.ErrCanceled), "no_reply", "wrapped: canceled"},
		{&adb.Error{Message: "adb is not running"}, "error", "adb is not running"},
		{&device.AppError{Message: "not installed"}, "error", "not installed"},
	}
	for _, c := range cases {
		if code, message := Classify(c.err); code != c.code || message != c.message {
			t.Errorf("%v: got %s %q, want %s %q", c.err, code, message, c.code, c.message)
		}
	}
}

var actions = []wire.Action{
	{ID: "account.create", Params: []wire.Param{
		{Name: "username", Type: wire.ParamTypeString, Presence: wire.Required{}},
		{Name: "age", Type: wire.ParamTypeInt, Presence: wire.Optional{}},
	}},
}

var refused = wire.Error{Code: wire.ErrorCodeRejected, Message: "no way"}

func collect(events *[]step.Event) func(step.Event) {
	return func(e step.Event) { *events = append(*events, e) }
}

// load keeps file as the recipes of the sample app in a config dir of the test's own, with up-to-date types.
func load(t *testing.T, file string) *recipe.Cookbook {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := recipe.Init(t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recipe.Dir(), "io.mehow.quack.sample.ts"), []byte(strings.TrimPrefix(file, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	mapping := "[recipes]\n'io\\.mehow\\.quack\\.sample' = \"io.mehow.quack.sample.ts\"\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "quack", "config.toml"), []byte(mapping), 0o644); err != nil {
		t.Fatal(err)
	}
	cookbook, warnings, err := recipe.Load(os.Getenv("XDG_CONFIG_HOME"), "io.mehow.quack.sample")
	if err != nil || warnings != nil {
		t.Fatal(err, warnings)
	}
	return cookbook
}

type fakeApp struct{}

func (*fakeApp) Target() device.Target {
	return device.Target{Device: "emulator-5554", Package: "io.mehow.quack.sample"}
}

func (*fakeApp) Command(context.Context, string) (adb.Output, error) { return adb.Output{}, nil }

func (*fakeApp) Start(client.StartMode) error { return nil }

func (*fakeApp) Call(_ context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error) {
	if call.Action == "fail" {
		return wire.MessageResult{}, refused
	}
	username := call.Args["username"][0]
	notify(wire.Notification{Message: "Creating " + username})
	return wire.MessageResult{Message: "Created " + username, Data: json.RawMessage(`{"id":7}`)}, nil
}
