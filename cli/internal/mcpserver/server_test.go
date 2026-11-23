package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/recipe"
	"quack/internal/wire"
)

func TestListDevicesGivesEachDeviceWithItsApps(t *testing.T) {
	source := &fakeSource{devices: []client.Device{
		{Listing: adb.Listing{Device: "emulator-5554", State: "device"}, Model: "Pixel 8", Packages: []string{"io.mehow.quack.sample"}},
		{Listing: adb.Listing{Device: "R5CT20ABCDE", State: "unauthorized"}},
	}}
	session := connect(t, source)

	result := call(t, session, "list_devices", nil)

	want := `{"devices":[` +
		`{"serial":"emulator-5554","model":"Pixel 8","state":"device","apps":["io.mehow.quack.sample"]},` +
		`{"serial":"R5CT20ABCDE","model":"","state":"unauthorized","apps":[]}]}`
	assertStructured(t, result, want)
}

func TestListActionsGivesTheParamsOfEachActionThatMatchesTheFilter(t *testing.T) {
	session := connect(t, newFakeSource())

	result := call(t, session, "list_actions", map[string]any{"filter": "^account\\."})

	want := `{"device":"emulator-5554","app":"io.mehow.quack.sample","version":"1.0","actions":[` +
		`{"id":"account.create","description":"Create an account","params":[` +
		`{"name":"username","type":"string","description":"Who signs in","hints":["alice","bob"],"required":true},` +
		`{"name":"age","type":"int","description":"","required":false,"default":"30"},` +
		`{"name":"tags","type":"string_list","description":"","required":false,"default":["a","b"]}]},` +
		`{"id":"account.delete","description":"Delete the account","params":[]}]}`
	assertStructured(t, result, want)
}

func TestListActionsAsksForTheDeviceAndAppItWasGiven(t *testing.T) {
	source := newFakeSource()
	session := connect(t, source)

	call(t, session, "list_actions", map[string]any{"device": "emulator-5554", "app": "io.mehow.quack.sample"})

	if want := []string{"emulator-5554 io.mehow.quack.sample"}; !reflect.DeepEqual(source.finds, want) {
		t.Errorf("finds are %q, want %q", source.finds, want)
	}
}

func TestRunActionPassesArgsAsTheAppParsesThem(t *testing.T) {
	source := newFakeSource()
	session := connect(t, source)

	call(t, session, "run_action", map[string]any{
		"action": "account.create",
		"args": map[string]any{
			"username": "alice",
			"age":      42,
			"id":       "12345678901234567890",
			"rate":     json.Number("0.1"),
			"admin":    true,
			"tags":     []any{"a", "b"},
			"note":     nil,
		},
	})

	want := wire.Call{Action: "account.create", Args: map[string]wire.RawArg{
		"username": {"alice"},
		"age":      {"42"},
		"id":       {"12345678901234567890"},
		"rate":     {"0.1"},
		"admin":    {"true"},
		"tags":     {"a", "b"},
	}}
	if got := source.app.calls; !reflect.DeepEqual(got, []wire.Call{want}) {
		t.Errorf("calls are %+v, want %+v", got, want)
	}
}

func TestRunActionStartsTheAppInTheBackgroundUnlessToldToOpenIt(t *testing.T) {
	source := newFakeSource()
	session := connect(t, source)

	call(t, session, "run_action", map[string]any{"action": "account.create", "args": map[string]any{"username": "alice"}})
	call(t, session, "run_action", map[string]any{"action": "account.create", "args": map[string]any{"username": "bob"}, "open_app": true})

	if want := []client.StartMode{client.Wake, client.OpenApp}; !reflect.DeepEqual(source.modes, want) {
		t.Errorf("modes are %q, want %q", source.modes, want)
	}
}

func TestRunActionGivesTheNotificationsTheMessageAndTheData(t *testing.T) {
	session := connect(t, newFakeSource())

	result := call(t, session, "run_action", map[string]any{"action": "account.create", "args": map[string]any{"username": "alice"}})

	assertStructured(t, result, `{"notifications":[{"message":"Creating alice","data":{"username":"alice"}}],"message":"Created alice","data":{"id":7}}`)
}

func TestRunActionSendsNotificationsAsProgressWhenAskedTo(t *testing.T) {
	progress := &progressLog{}
	session := connectWith(t, newFakeSource(), &mcp.ClientOptions{ProgressNotificationHandler: progress.handle})
	params := &mcp.CallToolParams{Name: "run_action", Arguments: map[string]any{"action": "account.create", "args": map[string]any{"username": "alice"}}}
	params.SetProgressToken("run-1")

	if _, err := session.CallTool(t.Context(), params); err != nil {
		t.Fatal(err)
	}

	progress.waitFor(t, []string{"Creating alice"})
}

func TestRunActionReportsTheAppsErrorAfterTheNotifications(t *testing.T) {
	session := connect(t, newFakeSource())

	result := callTool(t, session, "run_action", map[string]any{"action": "account.delete"})

	assertError(t, result, "Deleting\n[rejected] there is no account")
}

func TestRunActionAsksTheUserForMissingRequiredParams(t *testing.T) {
	source := newFakeSource()
	var asked *mcp.ElicitParams
	session := connectWith(t, source, &mcp.ClientOptions{ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = req.Params
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"username": "carol"}}, nil
	}})

	call(t, session, "run_action", map[string]any{"action": "account.create", "args": map[string]any{"age": 42}})

	if want := "account.create needs username."; asked == nil || asked.Message != want {
		t.Fatalf("asked %+v, want the message %q", asked, want)
	}
	schema, _ := json.Marshal(asked.RequestedSchema)
	wantSchema := `{"type":"object","required":["username"],"properties":{"username":{"type":"string","title":"username","description":"Who signs in"}}}`
	assertJSON(t, string(schema), wantSchema)
	want := wire.Call{Action: "account.create", Args: map[string]wire.RawArg{"username": {"carol"}, "age": {"42"}}}
	if got := source.app.calls; !reflect.DeepEqual(got, []wire.Call{want}) {
		t.Errorf("calls are %+v, want %+v", got, want)
	}
}

func TestRunActionAsksClientsOnOlderProtocolsForMissingRequiredParams(t *testing.T) {
	source := newFakeSource()
	options := &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"username": "carol"}}, nil
	}}
	session := connectAt(t, source, options, "2025-06-18")

	call(t, session, "run_action", map[string]any{"action": "account.create"})

	want := wire.Call{Action: "account.create", Args: map[string]wire.RawArg{"username": {"carol"}}}
	if got := source.app.calls; !reflect.DeepEqual(got, []wire.Call{want}) {
		t.Errorf("calls are %+v, want %+v", got, want)
	}
}

func TestRunActionDoesNotAskForParamsItWasGiven(t *testing.T) {
	asked := false
	session := connectWith(t, newFakeSource(), &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = true
		return &mcp.ElicitResult{Action: "cancel"}, nil
	}})

	call(t, session, "run_action", map[string]any{"action": "account.create", "args": map[string]any{"username": "alice"}})

	if asked {
		t.Error("the user was asked, want no form")
	}
}

func TestRunActionFailsWhenTheUserDeclinesToGiveParams(t *testing.T) {
	source := newFakeSource()
	session := connectWith(t, source, &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}})

	result := callTool(t, session, "run_action", map[string]any{"action": "account.create"})

	assertError(t, result, "the user declined to give username for account.create")
	if len(source.app.calls) > 0 {
		t.Errorf("the app got calls %+v, want none", source.app.calls)
	}
}

func TestToolsReportWhyNoAppWasFound(t *testing.T) {
	source := newFakeSource()
	source.findErr = &client.AppError{Message: "several quack packages on emulator-5554, pick one:\n  --device emulator-5554 --app a.b\n  --device emulator-5554 --app c.d"}
	session := connect(t, source)

	for tool, args := range map[string]map[string]any{"list_actions": nil, "run_action": {"action": "account.create"}} {
		result := callTool(t, session, tool, args)

		assertError(t, result, source.findErr.Error())
	}
}

func TestRunActionRefusesArgsThatAreNotTextNumbersOrBooleans(t *testing.T) {
	source := newFakeSource()
	session := connect(t, source)

	result := callTool(t, session, "run_action", map[string]any{"action": "account.create", "args": map[string]any{"username": map[string]any{"first": "alice"}}})

	assertError(t, result, `argument "username" must be a string, a number, a boolean or an array of them`)
	if len(source.app.calls) > 0 {
		t.Errorf("the app got calls %+v, want none", source.app.calls)
	}
}

func TestListActionsRefusesAnInvalidFilter(t *testing.T) {
	session := connect(t, newFakeSource())

	result := callTool(t, session, "list_actions", map[string]any{"filter": "("})

	if !result.IsError || !strings.HasPrefix(text(result), "invalid filter: ") {
		t.Errorf("the result is %q with isError %v, want an invalid filter error", text(result), result.IsError)
	}
}

func TestReadRunFollowsARunWhileItWaitsAndGivesItsEnd(t *testing.T) {
	source := newFakeSource()
	session := connect(t, source)

	run := startRun(t, session, map[string]any{"action": "sync.present"})
	first := call(t, session, "read_run", map[string]any{"run": run, "wait": "5s"})
	close(source.app.release)
	last := call(t, session, "read_run", map[string]any{"run": run, "after": 1, "wait": "5s"})

	assertStructured(t, first, `{"notifications":[{"message":"Pairing code","data":{"code":"abc123"}}],"next":1,"done":false}`)
	assertStructured(t, last, `{"notifications":[],"next":1,"done":true,"message":"Paired"}`)
}

func TestReadRunGivesNothingNewWhenItsWaitRunsOut(t *testing.T) {
	session := connect(t, newFakeSource())
	run := startRun(t, session, map[string]any{"action": "sync.present"})
	call(t, session, "read_run", map[string]any{"run": run, "wait": "5s"})

	result := call(t, session, "read_run", map[string]any{"run": run, "after": 1, "wait": "1ms"})

	assertStructured(t, result, `{"notifications":[],"next":1,"done":false}`)
}

func TestReadRunGivesTheErrorOfARunTheAppRefused(t *testing.T) {
	session := connect(t, newFakeSource())
	run := startRun(t, session, map[string]any{"action": "account.delete"})

	result := readToEnd(t, session, run)

	assertStructured(t, result, `{"notifications":[{"message":"Deleting"}],"next":1,"done":true,"error":{"code":"rejected","message":"there is no account"}}`)
}

func TestReadRunCountsTheNotificationsARunDroppedPastItsCap(t *testing.T) {
	session := connect(t, newFakeSource())
	run := startRun(t, session, map[string]any{"action": "app.chatty"})
	readToEnd(t, session, run)

	result := call(t, session, "read_run", map[string]any{"run": run})

	var out readRunOutput
	got, _ := json.Marshal(result.StructuredContent)
	json.Unmarshal(got, &out)
	if out.Dropped != 2 || out.Next != 1002 || len(out.Notifications) != 1000 || out.Notifications[0].Message != "3" || !out.Done {
		t.Errorf("got dropped %d, next %d, %d notifications from %q and done %v, want 2, 1002, 1000 from \"3\" and done",
			out.Dropped, out.Next, len(out.Notifications), out.Notifications[0].Message, out.Done)
	}
}

func TestCancelRunEndsTheRunAsCanceled(t *testing.T) {
	session := connect(t, newFakeSource())
	run := startRun(t, session, map[string]any{"action": "sync.present"})
	call(t, session, "read_run", map[string]any{"run": run, "wait": "5s"})

	call(t, session, "cancel_run", map[string]any{"run": run})
	result := call(t, session, "read_run", map[string]any{"run": run, "after": 1, "wait": "5s"})
	call(t, session, "cancel_run", map[string]any{"run": run})

	assertStructured(t, result, `{"notifications":[],"next":1,"done":true,"error":{"code":"canceled","message":"the run was canceled"}}`)
}

func TestRunsEndWithTheServersContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	session := connectUntil(t, ctx, newFakeSource())
	run := startRun(t, session, map[string]any{"action": "sync.present"})
	call(t, session, "read_run", map[string]any{"run": run, "wait": "5s"})

	cancel()
	result := call(t, session, "read_run", map[string]any{"run": run, "after": 1, "wait": "5s"})

	assertStructured(t, result, `{"notifications":[],"next":1,"done":true,"error":{"code":"canceled","message":"the run was canceled"}}`)
}

func TestStartActionRefusesWhatTheAppWouldRefuseWithoutCallingIt(t *testing.T) {
	source := newFakeSource()
	session := connect(t, source)

	unknown := callTool(t, session, "start_action", map[string]any{"action": "nope"})
	extra := callTool(t, session, "start_action", map[string]any{"action": "account.create", "args": map[string]any{"username": "alice", "bogus": "x"}})
	missing := callTool(t, session, "start_action", map[string]any{"action": "account.create"})

	assertError(t, unknown, "[unknown_action] no action 'nope'")
	assertError(t, extra, "[invalid_args] unknown argument 'bogus'")
	assertError(t, missing, "[invalid_args] missing argument 'username'")
	if len(source.app.calls) != 0 {
		t.Errorf("calls are %+v, want none", source.app.calls)
	}
}

func TestStartActionAsksTheUserForMissingRequiredParams(t *testing.T) {
	session := connectWith(t, newFakeSource(), &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"username": "carol"}}, nil
	}})

	run := startRun(t, session, map[string]any{"action": "account.create"})
	result := readToEnd(t, session, run)

	assertStructured(t, result, `{"notifications":[{"message":"Creating carol","data":{"username":"carol"}}],"next":1,"done":true,"message":"Created carol","data":{"id":7}}`)
}

func TestListRunsGivesEachRunOldestFirst(t *testing.T) {
	session := connect(t, newFakeSource())
	waiting := startRun(t, session, map[string]any{"action": "sync.present"})
	call(t, session, "read_run", map[string]any{"run": waiting, "wait": "5s"})
	readToEnd(t, session, startRun(t, session, map[string]any{"action": "account.delete"}))

	result := call(t, session, "list_runs", nil)

	assertStructured(t, result, `{"runs":[`+
		`{"run":"r1","action":"sync.present","device":"emulator-5554","app":"io.mehow.quack.sample","done":false,"next":1},`+
		`{"run":"r2","action":"account.delete","device":"emulator-5554","app":"io.mehow.quack.sample","done":true,"next":1}]}`)
}

func TestFinishedRunsPastTheCapAreGone(t *testing.T) {
	session := connect(t, newFakeSource())
	for range maxFinishedRuns + 1 {
		readToEnd(t, session, startRun(t, session, map[string]any{"action": "account.delete"}))
	}

	result := callTool(t, session, "read_run", map[string]any{"run": "r1"})
	kept := call(t, session, "read_run", map[string]any{"run": "r2"})

	assertError(t, result, "no run r1")
	if kept.IsError {
		t.Errorf("r2 is gone: %s", text(kept))
	}
}

func TestRunToolsFailForARunTheyDoNotHave(t *testing.T) {
	session := connect(t, newFakeSource())

	read := callTool(t, session, "read_run", map[string]any{"run": "r9"})
	canceled := callTool(t, session, "cancel_run", map[string]any{"run": "r9"})

	assertError(t, read, "no run r9")
	assertError(t, canceled, "no run r9")
}

func TestListRecipesGivesTheParamsOfEachRecipeThatMatchesTheFilter(t *testing.T) {
	source := newFakeSource()
	path := writeRecipes(t, `
import quack from "quack"
export const fresh = quack.recipe({
  description: "Start over",
  params: { user: { type: "string", default: "alice", hints: ["alice", "bob"] }, count: { type: "int", required: true } },
  run() {},
})
export const gone = quack.recipe({ run() {} })`)
	session := connect(t, source)

	result := call(t, session, "list_recipes", map[string]any{"filter": "^fr"})

	want := `{"device":"emulator-5554","app":"io.mehow.quack.sample","recipes":[` +
		`{"name":"fresh","description":"Start over","params":[` +
		`{"name":"user","type":"string","description":"","hints":["alice","bob"],"required":false,"default":"alice"},` +
		`{"name":"count","type":"int","description":"","required":true}],"file":` + quoted(path) + `}]}`
	assertStructured(t, result, want)
	if len(source.modes) > 0 {
		t.Errorf("the app started %q, want it left alone", source.modes)
	}
}

func TestListRecipesWithoutRecipesGivesNone(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	session := connect(t, newFakeSource())

	result := call(t, session, "list_recipes", map[string]any{})

	assertStructured(t, result, `{"device":"emulator-5554","app":"io.mehow.quack.sample","recipes":[]}`)
}

func TestListRecipesGivesTheProblemsThatLeftFilesOut(t *testing.T) {
	path := writeRecipes(t, `import quack from "quack"
export const fresh = quack.recipe({ run() {} })`)
	mapping := "[recipes]\n'.*' = [\"io.mehow.quack.sample.ts\", \"gone.ts\"]\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "quack", "config.toml"), []byte(mapping), 0o644); err != nil {
		t.Fatal(err)
	}
	session := connect(t, newFakeSource())

	result := call(t, session, "list_recipes", map[string]any{})

	problem := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "quack", "config.toml") + ": [recipes] maps io.mehow.quack.sample to gone.ts, which does not exist"
	assertStructured(t, result, `{"device":"emulator-5554","app":"io.mehow.quack.sample","recipes":[`+
		`{"name":"fresh","description":"","params":[],"file":`+quoted(path)+`}],"problems":[`+quoted(problem)+`]}`)
}

func TestRunRecipeGivesWhatEachStepGaveAndWhatItLogged(t *testing.T) {
	source := newFakeSource()
	writeRecipes(t, `
import quack from "quack"
export const fresh = quack.recipe({
  params: { user: { type: "string", required: true } },
  run({ user }) {
    const created = quack.runAction<{ id: number }>("account.create", { username: user })
    console.log("id " + created.data.id)
    quack.openApp()
    quack.wait("1ms")
    quack.shell("pm clear com.example")
    quack.run("echo seeded")
  },
})`)
	session := connect(t, source)

	result := call(t, session, "run_recipe", map[string]any{"recipe": "fresh", "args": map[string]any{"user": "alice"}})

	assertStructured(t, result, `{"steps":[`+
		`{"step":"account.create username=alice","notifications":[{"message":"Creating alice","data":{"username":"alice"}}],"message":"Created alice","data":{"id":7}},`+
		`{"step":"/open-app","notifications":[],"message":""},`+
		`{"step":"wait 1ms","notifications":[],"message":""},`+
		`{"step":"shell pm clear com.example","notifications":[],"message":"ran pm clear com.example"},`+
		`{"step":"run echo seeded","notifications":[],"message":"seeded"}],"log":["id 7"]}`)
	if want := []client.StartMode{client.OpenApp}; !reflect.DeepEqual(source.app.starts, want) {
		t.Errorf("starts are %q, want %q", source.app.starts, want)
	}
}

func TestRunRecipeGivesTheOtherRecipesItRanAsStepsAndWhatItReturned(t *testing.T) {
	source := newFakeSource()
	writeRecipes(t, `
import quack from "quack"
export const create = quack.recipe({
  params: { user: { type: "string", required: true } },
  run({ user }) { return quack.runAction<{ id: number }>("account.create", { username: user }).data },
})
export const fresh = quack.recipe({
  run() { return { account: quack.runRecipe(create, { user: "alice" }) } },
})`)
	session := connect(t, source)

	result := call(t, session, "run_recipe", map[string]any{"recipe": "fresh"})

	assertStructured(t, result, `{"steps":[`+
		`{"step":"recipe create user=alice","notifications":[],"message":""},`+
		`{"step":"account.create username=alice","notifications":[{"message":"Creating alice","data":{"username":"alice"}}],"message":"Created alice","data":{"id":7}}],`+
		`"log":[],"data":{"account":{"id":7}}}`)
}

func TestRunRecipeSendsStepsNotificationsAndLogsAsProgressWhenAskedTo(t *testing.T) {
	source := newFakeSource()
	writeRecipes(t, `
import quack from "quack"
export const fresh = quack.recipe({
  run() {
    quack.openApp()
    quack.runAction("account.create", { username: "alice" })
    console.log("done")
  },
})`)
	progress := &progressLog{}
	session := connectWith(t, source, &mcp.ClientOptions{ProgressNotificationHandler: progress.handle})
	params := &mcp.CallToolParams{Name: "run_recipe", Arguments: map[string]any{"recipe": "fresh"}}
	params.SetProgressToken("run-1")

	if _, err := session.CallTool(t.Context(), params); err != nil {
		t.Fatal(err)
	}

	progress.waitFor(t, []string{"› /open-app", "› account.create username=alice", "Creating alice", "done"})
}

func TestRunRecipeEndsAtAStepThatFailsSayingWhere(t *testing.T) {
	source := newFakeSource()
	writeRecipes(t, `
import quack from "quack"
export const redo = quack.recipe({
  run() {
    quack.runAction("account.create", { username: "alice" })
    quack.runAction("account.delete")
    quack.runAction("account.create", { username: "bob" })
  },
})`)
	session := connect(t, source)

	result := callTool(t, session, "run_recipe", map[string]any{"recipe": "redo"})

	assertError(t, result, "› account.create username=alice\nCreating alice\nCreated alice\n› account.delete\nDeleting\n"+
		"[rejected] there is no account\n    at run (io.mehow.quack.sample.ts:5:20)")
	if got := len(source.app.calls); got != 2 {
		t.Errorf("the app got %d calls, want 2", got)
	}
}

func TestRunRecipeGivesTheErrorOfAStepTheRecipeCaught(t *testing.T) {
	source := newFakeSource()
	writeRecipes(t, `
import quack from "quack"
export const careful = quack.recipe({
  run() {
    try {
      quack.runAction("account.delete")
    } catch (e) {}
    quack.runAction("account.create", { username: "alice" })
  },
})`)
	session := connect(t, source)

	result := call(t, session, "run_recipe", map[string]any{"recipe": "careful"})

	assertStructured(t, result, `{"steps":[`+
		`{"step":"account.delete","notifications":[{"message":"Deleting"}],"message":"","error":{"code":"rejected","message":"there is no account"}},`+
		`{"step":"account.create username=alice","notifications":[{"message":"Creating alice","data":{"username":"alice"}}],"message":"Created alice","data":{"id":7}}],"log":[]}`)
}

func TestRunRecipeRefusesArgsItsParamsRefuse(t *testing.T) {
	source := newFakeSource()
	writeRecipes(t, `
import quack from "quack"
export const fresh = quack.recipe({ params: { user: { type: "string", required: true } }, run() { quack.runAction("app.greet") } })`)
	session := connect(t, source)

	result := callTool(t, session, "run_recipe", map[string]any{"recipe": "fresh"})

	assertError(t, result, "[invalid_args] recipe 'fresh': missing argument 'user'")
	if len(source.app.calls) > 0 {
		t.Errorf("the app got calls %+v, want none", source.app.calls)
	}
}

func TestRunRecipeAsksTheUserForMissingRequiredParams(t *testing.T) {
	writeRecipes(t, `
import quack from "quack"
export const fresh = quack.recipe({
  params: {
    count: { type: "int", required: true },
    rate: { type: "double", required: true },
    admin: { type: "boolean", required: true },
    mode: { type: "enum", choices: ["fast", "slow"], required: true },
    tags: { type: "string_list", required: true },
    note: { type: "string" },
  },
  run({ count, rate, admin, mode, tags }) { console.log(JSON.stringify([count, rate, admin, mode, tags])) },
})`)
	var asked *mcp.ElicitParams
	session := connectWith(t, newFakeSource(), &mcp.ClientOptions{ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = req.Params
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{
			"rate": 0.5, "admin": true, "mode": "slow", "tags": "a, b\\,c",
		}}, nil
	}})

	result := call(t, session, "run_recipe", map[string]any{"recipe": "fresh", "args": map[string]any{"count": 3}})

	schema, _ := json.Marshal(asked.RequestedSchema)
	wantSchema := `{"type":"object","required":["rate","admin","mode","tags"],"properties":{` +
		`"rate":{"type":"number","title":"rate"},` +
		`"admin":{"type":"boolean","title":"admin"},` +
		`"mode":{"type":"string","title":"mode","enum":["fast","slow"]},` +
		`"tags":{"type":"string","title":"tags","description":"Separate the values with commas."}}}`
	assertJSON(t, string(schema), wantSchema)
	assertStructured(t, result, `{"steps":[],"log":["[3,0.5,true,\"slow\",[\"a\",\"b,c\"]]"]}`)
}

func TestRunRecipeNamesTheAppWithoutTheRecipe(t *testing.T) {
	writeRecipes(t, `export {}`)
	session := connect(t, newFakeSource())

	result := callTool(t, session, "run_recipe", map[string]any{"recipe": "fresh"})

	assertError(t, result, "no recipe named fresh for io.mehow.quack.sample")
}

func TestRunRecipeSaysWhyRecipesWereLeftOut(t *testing.T) {
	writeRecipes(t, `export {}`)
	mapping := "[recipes]\n'io\\.mehow\\.quack\\.sample' = [\"io.mehow.quack.sample.ts\", \"gone.ts\"]\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "quack", "config.toml"), []byte(mapping), 0o644); err != nil {
		t.Fatal(err)
	}
	session := connect(t, newFakeSource())

	result := callTool(t, session, "run_recipe", map[string]any{"recipe": "fresh"})

	assertError(t, result, "no recipe named fresh for io.mehow.quack.sample\n"+
		filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "quack", "config.toml")+": [recipes] maps io.mehow.quack.sample to gone.ts, which does not exist")
}

// writeRecipes keeps file as the recipes of the sample app in a config dir of the test's own, with up-to-date types.
func writeRecipes(t *testing.T, file string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := recipe.Init(t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(recipe.Dir(), "io.mehow.quack.sample.ts")
	if err := os.WriteFile(path, []byte(strings.TrimPrefix(file, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	mapping := "[recipes]\n'io\\.mehow\\.quack\\.sample' = \"io.mehow.quack.sample.ts\"\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "quack", "config.toml"), []byte(mapping), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func quoted(text string) string {
	out, _ := json.Marshal(text)
	return string(out)
}

// The client handles notifications on a goroutine of their own, so a call can return before the handler has
// seen the notifications sent ahead of its result.
type progressLog struct {
	mu       sync.Mutex
	messages []string
}

func (l *progressLog) handle(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, req.Params.Message)
}

func (l *progressLog) waitFor(t *testing.T, want []string) {
	t.Helper()
	var got []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		l.mu.Lock()
		got = slices.Clone(l.messages)
		l.mu.Unlock()
		if len(got) >= len(want) {
			break
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("progress is %q, want %q", got, want)
	}
}

func connect(t *testing.T, source *fakeSource) *mcp.ClientSession {
	return connectWith(t, source, nil)
}

func connectWith(t *testing.T, source *fakeSource, options *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	return connectAt(t, source, options, "")
}

func connectAt(t *testing.T, source *fakeSource, options *mcp.ClientOptions, version string) *mcp.ClientSession {
	t.Helper()
	return connectFull(t, t.Context(), source, options, version)
}

// The server's runs end with ctx, while the session lasts as long as the test.
func connectUntil(t *testing.T, ctx context.Context, source *fakeSource) *mcp.ClientSession {
	t.Helper()
	return connectFull(t, ctx, source, nil, "")
}

// "" asks for the latest protocol version.
func connectFull(t *testing.T, ctx context.Context, source *fakeSource, options *mcp.ClientOptions, version string) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := New(ctx, source, "test").Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, options).Connect(t.Context(), clientTransport, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		session.Close()
		serverSession.Wait()
	})
	return session
}

func call(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result := callTool(t, session, tool, args)
	if result.IsError {
		t.Fatalf("%s failed: %s", tool, text(result))
	}
	return result
}

func startRun(t *testing.T, session *mcp.ClientSession, args map[string]any) string {
	t.Helper()
	return call(t, session, "start_action", args).StructuredContent.(map[string]any)["run"].(string)
}

// Reads until the run is done, and returns that read: from index 0, so that it holds the run's notifications.
func readToEnd(t *testing.T, session *mcp.ClientSession, run string) *mcp.CallToolResult {
	t.Helper()
	for next := 0.0; ; {
		result := call(t, session, "read_run", map[string]any{"run": run, "after": int(next), "wait": "5s"})
		out := result.StructuredContent.(map[string]any)
		if out["done"] == true {
			return call(t, session, "read_run", map[string]any{"run": run})
		}
		next = out["next"].(float64)
	}
}

func callTool(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("calling %s: %v", tool, err)
	}
	return result
}

func assertStructured(t *testing.T, result *mcp.CallToolResult, want string) {
	t.Helper()
	got, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, string(got), want)
}

func assertJSON(t *testing.T, got, want string) {
	t.Helper()
	var gotValue, wantValue any
	json.Unmarshal([]byte(got), &gotValue)
	json.Unmarshal([]byte(want), &wantValue)
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("the JSON is\n%s\nwant\n%s", got, want)
	}
}

func assertError(t *testing.T, result *mcp.CallToolResult, want string) {
	t.Helper()
	if !result.IsError || text(result) != want {
		t.Errorf("the result is %q with isError %v, want the error %q", text(result), result.IsError, want)
	}
}

func text(result *mcp.CallToolResult) string {
	var texts []string
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	return strings.Join(texts, "\n")
}

type fakeSource struct {
	devices []client.Device
	app     *fakeApp
	findErr error
	finds   []string
	modes   []client.StartMode
}

func newFakeSource() *fakeSource {
	return &fakeSource{app: &fakeApp{release: make(chan struct{})}}
}

func (s *fakeSource) Devices() ([]client.Device, error) { return s.devices, nil }

func (s *fakeSource) Find(device adb.Device, pkg string) (client.Target, error) {
	s.finds = append(s.finds, string(device)+" "+pkg)
	if s.findErr != nil {
		return client.Target{}, s.findErr
	}
	return client.Target{Device: "emulator-5554", Package: "io.mehow.quack.sample"}, nil
}

func (s *fakeSource) Connect(_ client.Target, mode client.StartMode) (App, error) {
	s.modes = append(s.modes, mode)
	return s.app, nil
}

// The config dir is outside any project, and the recipes come only from the user's config.
func (s *fakeSource) Recipes(pkg string) (*recipe.Cookbook, []string, error) {
	return recipe.Load(os.Getenv("XDG_CONFIG_HOME"), pkg)
}

type fakeApp struct {
	mu     sync.Mutex
	calls  []wire.Call
	starts []client.StartMode
	// Closing it lets sync.present end.
	release chan struct{}
}

func (a *fakeApp) Target() client.Target {
	return client.Target{Device: "emulator-5554", Package: "io.mehow.quack.sample"}
}

func (a *fakeApp) Command(_ context.Context, cmd string) (adb.Output, error) {
	return adb.Output{Stdout: "ran " + cmd + "\n"}, nil
}

func (a *fakeApp) Start(mode client.StartMode) error {
	a.starts = append(a.starts, mode)
	return nil
}

func (a *fakeApp) Describe(context.Context) (wire.Hello, []wire.Action, error) {
	return wire.Hello{Package: "io.mehow.quack.sample", Version: "1.0"}, []wire.Action{
		{ID: "app.greet", Description: "Greet someone"},
		{ID: "account.create", Description: "Create an account", Params: []wire.Param{
			{Name: "username", Type: wire.ParamTypeString, Description: "Who signs in", Hints: []string{"alice", "bob"}, Presence: wire.Required{}},
			{Name: "age", Type: wire.ParamTypeInt, Presence: wire.Default{Values: []string{"30"}}},
			{Name: "tags", Type: wire.ParamTypeStringList, Presence: wire.Default{Values: []string{"a", "b"}}},
		}},
		{ID: "account.delete", Description: "Delete the account"},
		{ID: "sync.present", Description: "Show a pairing code and wait for a device to pair"},
		{ID: "app.chatty", Description: "Notify 1002 times"},
	}, nil
}

func (a *fakeApp) Call(ctx context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error) {
	a.mu.Lock()
	a.calls = append(a.calls, call)
	a.mu.Unlock()
	switch call.Action {
	case "sync.present":
		notify(wire.Notification{Message: "Pairing code", Data: json.RawMessage(`{"code":"abc123"}`)})
		select {
		case <-a.release:
			return wire.MessageResult{Message: "Paired"}, nil
		case <-ctx.Done():
			return wire.MessageResult{}, ctx.Err()
		}
	case "app.chatty":
		for i := 1; i <= 1002; i++ {
			notify(wire.Notification{Message: strconv.Itoa(i)})
		}
		return wire.MessageResult{Message: "Done"}, nil
	case "account.create":
		username := call.Args["username"][0]
		notify(wire.Notification{Message: "Creating " + username, Data: json.RawMessage(`{"username":"` + username + `"}`)})
		return wire.MessageResult{Message: "Created " + username, Data: json.RawMessage(`{"id":7}`)}, nil
	case "account.delete":
		notify(wire.Notification{Message: "Deleting"})
		return wire.MessageResult{}, wire.Error{Code: wire.ErrorCodeRejected, Message: "there is no account"}
	default:
		return wire.MessageResult{}, errors.New("unknown action")
	}
}
