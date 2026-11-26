package session_test

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
	"quack/internal/fakes"
	"quack/internal/recipe"
	"quack/internal/session"
	"quack/internal/step"
	"quack/internal/wire"
)

func TestRunRunsAPlainActionAsOneUnnumberedStep(t *testing.T) {
	var events []step.Event

	data, err := session.Run(context.Background(), newApp(), actions, nil, session.Runnable{Name: "account.create"}, map[string]wire.RawArg{"username": {"ann"}, "age": {"7"}}, collect(&events))

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

	_, err := session.Run(context.Background(), newApp(), actions, nil, session.Runnable{Name: "fail"}, nil, collect(&events))

	var failed *session.StepError
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
  name: "r",
  run() {
    console.log("first")
    const created = quack.runAction("account.create", { age: 7, username: "ann" })
    console.log("then")
    return created.data
  },
})`)
	var events []step.Event

	data, err := session.Run(context.Background(), newApp(), actions, cookbook, session.Runnable{Name: "r", Recipe: true}, nil, collect(&events))

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
  name: "r",
  run() {
    quack.openApp()
    quack.runAction("fail")
  },
})`)

	_, err := session.Run(context.Background(), newApp(), actions, cookbook, session.Runnable{Name: "r", Recipe: true}, nil, func(step.Event) {})

	var failed *session.StepError
	var thrown *recipe.ScriptError
	if !errors.As(err, &failed) || failed.Step != 2 || !errors.Is(err, refused) || !errors.As(err, &thrown) {
		t.Fatalf("got %#v, want step 2's error", err)
	}
	if want := "[rejected] no way\n    at run (io.mehow.quack.sample.ts:6:20)"; err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

func TestAFailedStepThatTheRecipeWentOnFromIsNoStepError(t *testing.T) {
	cookbook := load(t, `
import quack from "quack"
export const rethrows = quack.recipe({
  name: "rethrows",
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
  name: "goesOn",
  run() {
    try {
      quack.runAction("fail")
    } catch (e) {}
    quack.openApp()
  },
})`)

	_, rethrown := session.Run(context.Background(), newApp(), actions, cookbook, session.Runnable{Name: "rethrows", Recipe: true}, nil, func(step.Event) {})
	_, wentOn := session.Run(context.Background(), newApp(), actions, cookbook, session.Runnable{Name: "goesOn", Recipe: true}, nil, func(step.Event) {})

	var failed *session.StepError
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
		{&session.StepError{Step: 2, Err: thrown}, "rejected", "no way\n    at run (r.ts:1:1)"},
		{&step.CommandError{Exit: 3, Output: "nope"}, "command", "the command exited with 3: nope"},
		{fmt.Errorf("wrapped: %w", client.ErrCanceled), "no_reply", "wrapped: canceled"},
		{&adb.Error{Message: "adb is not running"}, "error", "adb is not running"},
		{&device.AppError{Message: "not installed"}, "error", "not installed"},
	}
	for _, c := range cases {
		if code, message := session.Classify(c.err); code != c.code || message != c.message {
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
	location := recipe.Location{Dir: t.TempDir()}
	if _, err := location.Init(""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recipe.Dir(), "io.mehow.quack.sample.ts"), []byte(strings.TrimPrefix(file, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	mapping := "[recipes]\n'io\\.mehow\\.quack\\.sample' = \"io.mehow.quack.sample.ts\"\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "quack", "config.toml"), []byte(mapping), 0o644); err != nil {
		t.Fatal(err)
	}
	cookbook, warnings, err := location.Load("io.mehow.quack.sample")
	if err != nil || warnings != nil {
		t.Fatal(err, warnings)
	}
	return cookbook
}

// The app creates the account a call names, and fails a call of fail.
func newApp() *fakes.App {
	app := fakes.NewApp()
	app.Replies["account.create"] = func(_ context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error) {
		username := call.Args["username"][0]
		notify(wire.Notification{Message: "Creating " + username})
		return wire.MessageResult{Message: "Created " + username, Data: json.RawMessage(`{"id":7}`)}, nil
	}
	app.Replies["fail"] = func(context.Context, wire.Call, client.NotificationFunc) (wire.MessageResult, error) {
		return wire.MessageResult{}, refused
	}
	return app
}

func TestPlainActionEnvironmentStaysOutOfEventsButResultsAreUnchanged(t *testing.T) {
	t.Setenv("QUACK_TOKEN", "secret")
	app := newApp()
	catalog := []wire.Action{{ID: "account.create", Params: []wire.Param{{Name: "username", Env: "QUACK_TOKEN", Presence: wire.Required{}}}}}
	var events []step.Event
	_, err := session.Run(t.Context(), app, catalog, nil, session.Runnable{Name: "account.create"}, nil, collect(&events))
	if err != nil {
		t.Fatal(err)
	}
	if got := app.Calls()[0].Args["username"][0]; got != "secret" {
		t.Fatal(got)
	}
	if got := events[0].(step.Started).Line; got != "account.create username='${QUACK_TOKEN}'" {
		t.Fatal(got)
	}
	done := events[len(events)-1].(step.Ended)
	if done.Step.(step.Call).Args["username"][0] != "${QUACK_TOKEN}" || done.Result.Message != "Created secret" {
		t.Fatalf("ended: %#v", done)
	}
}
