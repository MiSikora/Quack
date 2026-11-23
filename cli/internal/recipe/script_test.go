package recipe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/config"
	"quack/internal/fakes"
	"quack/internal/step"
	"quack/internal/wire"
)

func TestLoadReadsTheExportedRecipesSortedByNameWithTheirParams(t *testing.T) {
	writeFile(t, "common.ts", `export function helper(): string { return "x" }`)
	path := write(t, `
import quack from "quack"
import { helper } from "./common"

type Unused = { a: string }

export const login = quack.recipe({
  description: "Log in",
  params: {
    user: { type: "string", default: "foo", hints: ["alice", "bob"], description: "Who" },
    count: { type: "int", required: true },
    theme: { type: "enum", choices: ["light", "dark"] },
    tags: { type: "string_list", default: ["a", "b"] },
  },
  run() {},
})
export const clean = quack.recipe({ run() { helper() } })
export const notARecipe = 3
const unexported = quack.recipe({ run() {} })
`)

	s, err := loadApp(t)

	if err != nil {
		t.Fatal(err)
	}
	want := []Recipe{
		{Name: "clean", File: path, script: path},
		{Name: "login", Description: "Log in", File: path, script: path, Params: []wire.Param{
			{Name: "user", Description: "Who", Type: wire.ParamTypeString, Hints: []string{"alice", "bob"}, Presence: wire.Default{Values: []string{"foo"}}},
			{Name: "count", Type: wire.ParamTypeInt, Presence: wire.Required{}},
			{Name: "theme", Type: wire.ParamTypeEnum, Choices: []string{"light", "dark"}, Presence: wire.Optional{}},
			{Name: "tags", Type: wire.ParamTypeStringList, Presence: wire.Default{Values: []string{"a", "b"}}},
		}},
	}
	if !reflect.DeepEqual(s.Recipes, want) {
		t.Errorf("got %+v, want %+v", s.Recipes, want)
	}
}

func TestARecipeTakesTheNameItDeclaresOverItsExportsName(t *testing.T) {
	s := load(t, `
import quack from "quack"
quack.recipe({ name: "app.reset", run() { console.log("reset") } })
const skip = quack.recipe({ name: "onboarding.skip", run() {} })
export { skip as skipOnboarding }
export const open = quack.recipe({ run() {} })
const helper = quack.recipe({ run() {} })`)
	var logged []string

	_, err := s.Run(context.Background(), newApp(), nil, "app.reset", nil, logTo(&logged))

	var names []string
	for _, r := range s.Recipes {
		names = append(names, r.Name)
	}
	if want := []string{"app.reset", "onboarding.skip", "open"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names: got %v, want %v", names, want)
	}
	if want := []string{"reset"}; err != nil || !reflect.DeepEqual(logged, want) {
		t.Errorf("run: got %v, %v, want %v", logged, err, want)
	}
}

func TestARecipeNameHoldsDigits(t *testing.T) {
	s := load(t, `
import quack from "quack"
quack.recipe({ name: "seed100", run() {} })
export const login2fa = quack.recipe({ run() {} })`)

	var names []string
	for _, r := range s.Recipes {
		names = append(names, r.Name)
	}
	if want := []string{"login2fa", "seed100"}; !reflect.DeepEqual(names, want) {
		t.Errorf("got %v, want %v", names, want)
	}
}

func TestLoadReadsAJavaScriptFile(t *testing.T) {
	writeFile(t, "recipes.js", `import quack from "quack"
export const clean = quack.recipe({ description: "Clean", run() {} })`)
	writeConfig(t, "[recipes]\n'.*' = \"recipes.js\"\n")

	s, err := loadApp(t)

	if err != nil || len(s.Recipes) != 1 || s.Recipes[0].Name != "clean" {
		t.Errorf("got %+v, %v", s, err)
	}
}

func TestLoadPassesOverExportsThatAreNoRecipes(t *testing.T) {
	write(t, `import quack from "quack"
export const label = "zażółć"
export const count = 3
export const clean = quack.recipe({ run() {} })`)

	s, err := loadApp(t)

	if err != nil || len(s.Recipes) != 1 || s.Recipes[0].Name != "clean" {
		t.Errorf("got %+v, %v", s, err)
	}
}

func TestLoadFailsSayingWhere(t *testing.T) {
	for _, c := range []struct{ file, want string }{
		{"export const a = (", "io.mehow.quack.sample.ts:1:19: Unexpected end of file"},
		{`import { b } from "lodash"; console.log(b)`, `io.mehow.quack.sample.ts:1:19: Could not resolve "lodash"`},
		{`throw new Error("nope")`, "Error: nope\n    at io.mehow.quack.sample.ts:1:6"},
		{`import quack from "quack"
export const a = quack.recipe({ params: { x: { type: "nope" } }, run() {} })`, "TypeError: param 'x' has type 'nope', not one of string, int, long, double, boolean, string_list, enum"},
		{`import quack from "quack"
export const a = quack.recipe({ params: { x: { type: "enum" } }, run() {} })`, "TypeError: param 'x' is an enum without choices"},
		{`import quack from "quack"
export const a = quack.recipe({ params: { x: { type: "int", default: "many" } }, run() {} })`, "TypeError: param 'x': its default is not a int"},
		{`import quack from "quack"
export const a = quack.recipe({ description: "no run" })`, "TypeError: recipe() takes a definition with a run function"},
		{`import quack from "quack"
export const $a = quack.recipe({ run() {} })`, "recipe '$a': its name may hold only ASCII letters, digits, '_', '.' and '-', and must not start with '-'"},
		{`import quack from "quack"
quack.openApp()`, `TypeError: the "quack" module runs steps only inside a recipe's run`},
		{`import quack from "quack"
quack.recipe({ name: "$a", run() {} })`, "recipe '$a': its name may hold only ASCII letters, digits, '_', '.' and '-', and must not start with '-'"},
		{`import quack from "quack"
quack.recipe({ name: "a", run() {} })
export const a = quack.recipe({ run() {} })`, "two recipes are named 'a'"},
		{`import quack from "quack"
quack.recipe({ name: "-reset", run() {} })`, "recipe '-reset': its name may hold only"},
		{`import quack from "quack"
quack.recipe({ name: "app:reset", run() {} })`, "recipe 'app:reset': its name may hold only"},
	} {
		write(t, c.file)

		_, err := loadApp(t)

		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %s", c.file, err, c.want)
		}
	}
}

func TestRunRunsTheStepsInOrderAndEmitsTheirEventsNumbered(t *testing.T) {
	app := newApp()
	app.CommandReply = func(string) adb.Output { return adb.Output{Stdout: "Success\n"} }
	s := load(t, `
import quack from "quack"
export const r = quack.recipe({
  run() {
    quack.openApp()
    const greeting = quack.runAction("app.greet", { name: "Bob", times: 2, loud: true, tag: ["a", "b"] })
    console.log("got", greeting.message, greeting.notifications[0].message, greeting.notifications[0].data.name)
    quack.shell("pm clear " + quack.shell("echo"))
    console.log(quack.run("echo duck"))
    quack.wait("10ms")
    quack.wait(10)
  },
})`)
	var events []step.Event

	_, err := s.Run(context.Background(), app, nil, "r", nil, collect(&events))

	greet := step.Call{Action: "app.greet", Args: map[string]wire.RawArg{"name": {"Bob"}, "times": {"2"}, "loud": {"true"}, "tag": {"a", "b"}}}
	want := []step.Event{
		step.Started{Number: 1, Line: "/open-app"},
		step.Ended{Number: 1, Step: step.OpenApp{}},
		step.Started{Number: 2, Line: "app.greet loud=true name=Bob tag=a tag=b times=2"},
		step.Notified{Number: 2, Notification: wire.Notification{Message: "greeting", Data: json.RawMessage(`{"name":"Bob"}`)}},
		step.Ended{Number: 2, Step: greet, Result: wire.MessageResult{Message: "Hello, Bob!"}},
		step.Logged{Number: 2, Text: "got Hello, Bob! greeting Bob"},
		step.Started{Number: 3, Line: "shell echo"},
		step.Ended{Number: 3, Step: step.Shell{Command: "echo"}, Result: wire.MessageResult{Message: "Success"}},
		step.Started{Number: 4, Line: "shell pm clear Success"},
		step.Ended{Number: 4, Step: step.Shell{Command: "pm clear Success"}, Result: wire.MessageResult{Message: "Success"}},
		step.Started{Number: 5, Line: "run echo duck"},
		step.Ended{Number: 5, Step: step.Run{Command: "echo duck"}, Result: wire.MessageResult{Message: "duck"}},
		step.Logged{Number: 5, Text: "duck"},
		step.Started{Number: 6, Line: "wait 10ms"},
		step.Ended{Number: 6, Step: step.Wait{Duration: 10 * time.Millisecond}},
		step.Started{Number: 7, Line: "wait 10ms"},
		step.Ended{Number: 7, Step: step.Wait{Duration: 10 * time.Millisecond}},
	}
	if err != nil || !reflect.DeepEqual(events, want) {
		t.Errorf("got %v\n%#v\nwant\n%#v", err, events, want)
	}
	if !reflect.DeepEqual(app.Starts(), []client.StartMode{client.OpenApp}) {
		t.Errorf("starts: got %v", app.Starts())
	}
}

func TestARecipeBranchesOnTheDataOfAnAction(t *testing.T) {
	app := newApp()
	app.Replies["account.status"] = func(context.Context, wire.Call, client.NotificationFunc) (wire.MessageResult, error) {
		return wire.MessageResult{Message: "account.status", Data: json.RawMessage(`{"tries":[1,2],"loggedIn":false}`)}, nil
	}
	s := load(t, `
import quack from "quack"
export const r = quack.recipe({
  run() {
    const status = quack.runAction<{ loggedIn: boolean; tries: number[] }>("account.status")
    if (!status.data.loggedIn) quack.runAction("app.greet", { name: "tries " + status.data.tries.length })
    if (quack.runAction("app.greet", { name: "x" }).data !== null) throw new Error("data without data")
    console.log(JSON.stringify(status.data))
  },
})`)

	var logged []string

	if _, err := s.Run(context.Background(), app, nil, "r", nil, logTo(&logged)); err != nil {
		t.Fatal(err)
	}

	if want := []string{"account.status", "app.greet name='tries 2'", "app.greet name=x"}; !reflect.DeepEqual(lines(app.Calls()), want) {
		t.Errorf("got %v, want %v", lines(app.Calls()), want)
	}
	// The data keeps the app's key order.
	if want := []string{`{"tries":[1,2],"loggedIn":false}`}; !reflect.DeepEqual(logged, want) {
		t.Errorf("logged %v, want %v", logged, want)
	}
}

func TestARunGetsItsArgsConvertedWithDefaults(t *testing.T) {
	s := load(t, `
import quack from "quack"
export const r = quack.recipe({
  params: {
    text: { type: "string", default: "foo" },
    count: { type: "int" },
    size: { type: "long" },
    ratio: { type: "double" },
    flag: { type: "boolean" },
    theme: { type: "enum", choices: ["Light", "Dark"] },
    tag: { type: "string_list", default: ["a"] },
    unset: { type: "string" },
  },
  run(args) { console.log(JSON.stringify(args)) },
})`)
	var logged []string

	_, err := s.Run(context.Background(), newApp(), nil, "r", map[string]wire.RawArg{
		"count": {"3"}, "size": {"9007199254740993"}, "ratio": {"0.5"}, "flag": {"true"}, "theme": {"dark"}, "tag": {"x", "y"},
	}, logTo(&logged))

	want := `{"count":3,"flag":true,"ratio":0.5,"size":9007199254740993,"tag":["x","y"],"text":"foo","theme":"Dark"}`
	if err != nil || len(logged) != 1 || !sameJSON(logged[0], want) {
		t.Errorf("got %v, %v, want %s", logged, err, want)
	}
}

func TestARunFailsOnArgsItsParamsRefuse(t *testing.T) {
	s := load(t, `
import quack from "quack"
export const r = quack.recipe({
  params: { user: { type: "string", required: true }, count: { type: "int" } },
  run() { throw new Error("ran") },
})`)
	for _, c := range []struct {
		args map[string]wire.RawArg
		want string
	}{
		{map[string]wire.RawArg{}, "recipe 'r': missing argument 'user'"},
		{map[string]wire.RawArg{"user": {"a"}, "nope": {"1"}}, "recipe 'r': unknown argument 'nope'"},
		{map[string]wire.RawArg{"user": {"a", "b"}}, "recipe 'r': 'user' given more than once"},
		{map[string]wire.RawArg{"user": {"a"}, "count": {"many"}}, "recipe 'r': 'count' expects int, got 'many'"},
	} {
		_, err := s.Run(context.Background(), newApp(), nil, "r", c.args, ignore)

		var refused wire.Error
		if !errors.As(err, &refused) || refused.Code != wire.ErrorCodeInvalidArgs || refused.Message != c.want {
			t.Errorf("%v: got %v, want %s", c.args, err, c.want)
		}
	}
	if _, err := s.Run(context.Background(), newApp(), nil, "nope", nil, ignore); err == nil || err.Error() != "no recipe named nope for io.mehow.quack.sample" {
		t.Errorf("unknown recipe: got %v", err)
	}
}

func TestAFailedStepEndsTheRunWithItsErrorAndWhereTheRecipeWas(t *testing.T) {
	writeFile(t, "common.ts", `import quack from "quack"

export function fail() {
  quack.runAction("fail")
}`)
	path := write(t, `
import quack from "quack"
import { fail } from "./common"
export const r = quack.recipe({
  run() {
    fail()
  },
})`)
	s, err := loadApp(t)
	if err != nil {
		t.Fatal(err)
	}
	var events []step.Event

	_, err = s.Run(context.Background(), newApp(), nil, "r", nil, collect(&events))

	var refused wire.Error
	var thrown *ScriptError
	if !errors.As(err, &refused) || refused.Code != wire.ErrorCodeRejected || !errors.As(err, &thrown) {
		t.Fatalf("got %v, want the app's error", err)
	}
	if want := []string{"fail (common.ts:4:18)", "run (" + filepath.Base(path) + ":5:9)"}; !reflect.DeepEqual(thrown.Stack, want) {
		t.Errorf("stack: got %q, want %q", thrown.Stack, want)
	}
	if want := "[rejected] no way\n    at fail (common.ts:4:18)\n    at run (io.mehow.quack.sample.ts:5:9)"; err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
	if want := []step.Event{step.Started{Number: 1, Line: "fail"}, step.Failed{Number: 1, Err: refused}}; !errors.Is(err, refused) || !reflect.DeepEqual(events, want) {
		t.Errorf("events: got %v, want a failed step, the one that ended the run", events)
	}
}

func TestARecipeCatchesAFailedStep(t *testing.T) {
	s := load(t, `
import quack from "quack"
export const r = quack.recipe({
  run() {
    try {
      quack.runAction("fail")
    } catch (e) {
      console.log("caught " + (e as Error).message)
    }
  },
})`)
	var events []step.Event

	_, err := s.Run(context.Background(), newApp(), nil, "r", nil, collect(&events))

	want := []step.Event{
		step.Started{Number: 1, Line: "fail"},
		step.Failed{Number: 1, Err: wire.Error{Code: wire.ErrorCodeRejected, Message: "no way"}},
		step.Logged{Number: 1, Text: "caught [rejected] no way"},
	}
	if err != nil || !reflect.DeepEqual(events, want) {
		t.Errorf("got %v, %v, want %v", events, err, want)
	}
}

func TestCancelingInterruptsARecipeWhereverItIs(t *testing.T) {
	s := load(t, `
import quack from "quack"
export const spin = quack.recipe({ run() { while (true) {} } })`)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	started := time.Now()
	_, err := s.Run(ctx, newApp(), nil, "spin", nil, ignore)

	var call *client.CallError
	if !errors.As(err, &call) || err.Error() != "canceled" || time.Since(started) > 5*time.Second {
		t.Errorf("got %v after %v, want canceled at once", err, time.Since(started))
	}
}

func TestCancelingBeforeTheRecipeStartsEndsItAsCanceled(t *testing.T) {
	s := load(t, `
import quack from "quack"
export const spin = quack.recipe({ run() { while (true) {} } })`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.Run(ctx, newApp(), nil, "spin", nil, ignore)

	var call *client.CallError
	if !errors.As(err, &call) || err.Error() != "canceled" {
		t.Errorf("got %v, want canceled", err)
	}
}

func TestWaitUntilChecksUntilTheCheckPassesOrTheTimeoutEnds(t *testing.T) {
	s := load(t, `
import quack from "quack"
export const passes = quack.recipe({
  run() {
    let checks = 0
    quack.waitUntil(() => ++checks === 3, { every: "5ms" })
    console.log("checks " + checks)
  },
})
export const times = quack.recipe({ run() { quack.waitUntil(() => false, { timeout: 30, every: 10 }) } })`)
	var logged []string
	var started []string
	observer := func(e step.Event) {
		logTo(&logged)(e)
		startedLines(&started)(e)
	}

	if _, err := s.Run(context.Background(), newApp(), nil, "passes", nil, observer); err != nil || !reflect.DeepEqual(logged, []string{"checks 3"}) {
		t.Errorf("passes: got %v, %v", logged, err)
	}
	_, err := s.Run(context.Background(), newApp(), nil, "times", nil, observer)
	if err == nil || !strings.HasPrefix(err.Error(), "waitUntil: the check did not pass within 30ms") {
		t.Errorf("times: got %v", err)
	}
	if started != nil {
		t.Errorf("started: got %v, want no steps for the waits between checks", started)
	}
}

func TestARecipeReadsTheAppTheDeviceAndTheEnvironment(t *testing.T) {
	t.Setenv("QUACK_NAME", "Bob")
	os.Unsetenv("QUACK_MISSING")
	s := load(t, `
import quack from "quack"
export const r = quack.recipe({ run() { console.log(quack.app.package, quack.device.serial, quack.env.QUACK_NAME, quack.env.QUACK_MISSING) } })`)
	var logged []string

	_, err := s.Run(context.Background(), newApp(), nil, "r", nil, logTo(&logged))

	if want := []string{"io.mehow.quack.sample emulator-5554 Bob undefined"}; err != nil || !reflect.DeepEqual(logged, want) {
		t.Errorf("got %v, %v, want %v", logged, err, want)
	}
}

func TestStepsShowTheEnvironmentVariablesTheRecipeReadByName(t *testing.T) {
	t.Setenv("QUACK_TOKEN", "s3cret")
	t.Setenv("QUACK_UNREAD", "Bob")
	s := load(t, `
import quack from "quack"
export const r = quack.recipe({
  run() {
    quack.shell("curl -H 'Authorization: " + quack.env.QUACK_TOKEN + "' -d Bob")
    quack.runAction("app.greet", { name: quack.env.QUACK_TOKEN + "-Bob" })
  },
})`)
	app := newApp()
	var started []string

	_, err := s.Run(context.Background(), app, nil, "r", nil, startedLines(&started))

	if want := []string{"shell curl -H 'Authorization: ${QUACK_TOKEN}' -d Bob", "app.greet name='${QUACK_TOKEN}-Bob'"}; err != nil || !reflect.DeepEqual(started, want) {
		t.Errorf("started: got %v, %v, want %v", started, err, want)
	}
	if want := []string{"curl -H 'Authorization: s3cret' -d Bob"}; !reflect.DeepEqual(app.Commands(), want) {
		t.Errorf("commands: got %v, want %v", app.Commands(), want)
	}
	if want := []string{"app.greet name=s3cret-Bob"}; !reflect.DeepEqual(lines(app.Calls()), want) {
		t.Errorf("calls: got %v, want %v", lines(app.Calls()), want)
	}
}

func TestAParamTakesItsEnvironmentVariableWhenTheRunLeavesItOut(t *testing.T) {
	t.Setenv("QUACK_CODE", "s3cret")
	os.Unsetenv("QUACK_MISSING")
	s := load(t, `
import quack from "quack"
export const set = quack.recipe({
  params: { code: { type: "string", required: true, env: "QUACK_CODE" } },
  run({ code }) { quack.runAction("app.greet", { name: code }) },
})
export const unset = quack.recipe({
  params: { code: { type: "string", required: true, env: "QUACK_MISSING" } },
  run() {},
})`)
	app := newApp()
	var started []string

	_, err := s.Run(context.Background(), app, nil, "set", nil, startedLines(&started))

	if want := (wire.Default{Values: []string{"${QUACK_CODE}"}}); !reflect.DeepEqual(s.Recipes[0].Params[0].Presence, want) {
		t.Errorf("set presence: got %v, want %v", s.Recipes[0].Params[0].Presence, want)
	}
	if want := []string{"app.greet name=s3cret"}; err != nil || !reflect.DeepEqual(lines(app.Calls()), want) {
		t.Errorf("calls: got %v, %v, want %v", lines(app.Calls()), err, want)
	}
	if want := []string{"app.greet name='${QUACK_CODE}'"}; !reflect.DeepEqual(started, want) {
		t.Errorf("started: got %v, want %v", started, want)
	}
	if want := (wire.Required{}); !reflect.DeepEqual(s.Recipes[1].Params[0].Presence, want) {
		t.Errorf("unset presence: got %v, want %v", s.Recipes[1].Params[0].Presence, want)
	}
	if _, err := s.Run(context.Background(), app, nil, "unset", nil, ignore); err == nil || !strings.HasSuffix(err.Error(), "missing argument 'code'") {
		t.Errorf("unset: got %v", err)
	}
}

func TestARecipeRunsAnotherAsAStepFollowedByItsStepsAndGetsWhatItReturned(t *testing.T) {
	t.Setenv("QUACK_PIN", "1234")
	s := load(t, `
import quack from "quack"
export const login = quack.recipe({
  params: {
    user: { type: "string", required: true },
    pin: { type: "string", env: "QUACK_PIN" },
    tries: { type: "int", default: 2 },
    tags: { type: "string_list" },
  },
  run({ user, tries, tags }) {
    quack.runAction("app.greet", { name: user })
    return { user, tries, tags, at: new Date(0), skip: () => 1 }
  },
})
export const outer = quack.recipe({
  run() {
    const session = quack.runRecipe(login, { user: "bob", tags: ["a", "b c"], tries: undefined })
    console.log(JSON.stringify(session))
    return [session.user, quack.runRecipe(quack.recipe({ run() {} }))]
  },
})`)
	app := newApp()
	var events []step.Event

	data, err := s.Run(context.Background(), app, nil, "outer", nil, collect(&events))

	var lines []string
	for _, e := range events {
		switch e := e.(type) {
		case step.Started:
			lines = append(lines, fmt.Sprintf("%d › %s", e.Number, e.Line))
		case step.Notified:
			lines = append(lines, fmt.Sprintf("%d notified %s", e.Number, e.Notification.Message))
		case step.Ended:
			lines = append(lines, fmt.Sprintf("%d ended %s", e.Number, e.Result.Message))
		case step.Logged:
			lines = append(lines, fmt.Sprintf("%d %s", e.Number, e.Text))
		}
	}
	want := []string{
		"1 › recipe login user=bob pin='${QUACK_PIN}' tries=2 tags=a tags='b c'",
		"1 ended ",
		"2 › app.greet name=bob",
		"2 notified greeting",
		"2 ended Hello, bob!",
		`2 {"user":"bob","tries":2,"tags":["a","b c"],"at":"1970-01-01T00:00:00.000Z"}`,
		"3 › recipe",
		"3 ended ",
	}
	if err != nil || !reflect.DeepEqual(lines, want) {
		t.Errorf("got %q, %v, want %q", lines, err, want)
	}
	if string(data) != `["bob",null]` {
		t.Errorf("data: got %s", data)
	}
}

func TestRunRecipeTakesArgsAsARunDoesAndThrowsWhatTheOtherRecipeThrows(t *testing.T) {
	s := load(t, `
import quack from "quack"
const login = quack.recipe({
  name: "login",
  params: { user: { type: "string", required: true }, tries: { type: "int" } },
  run() { quack.runAction("fail") },
})
export const outer = quack.recipe({
  run() {
    for (const args of [undefined, { user: "bob", nope: 1 }, { user: "bob", tries: "many" }, { user: "bob" }]) {
      try {
        quack.runRecipe(login, args as any)
      } catch (e) {
        console.log((e as Error).message)
      }
    }
  },
})`)
	var logged []string

	_, err := s.Run(context.Background(), newApp(), nil, "outer", nil, logTo(&logged))

	want := []string{
		"[invalid_args] recipe 'login': missing argument 'user'",
		"[invalid_args] recipe 'login': unknown argument 'nope'",
		"[invalid_args] recipe 'login': 'tries' expects int, got 'many'",
		"[rejected] no way",
	}
	if err != nil || !reflect.DeepEqual(logged, want) {
		t.Errorf("got %q, %v, want %q", logged, err, want)
	}
}

func TestRunRecipeTakesOnlyARecipeAndOnlyInsideARun(t *testing.T) {
	s := load(t, `
import quack from "quack"
export const named = quack.recipe({ run() { quack.runRecipe("named" as any) } })`)
	if _, err := s.Run(context.Background(), newApp(), nil, "named", nil, ignore); err == nil || !strings.HasPrefix(err.Error(), "TypeError: runRecipe() takes a recipe that quack.recipe() returned") {
		t.Errorf("by name: got %v", err)
	}

	write(t, `
import quack from "quack"
export const r = quack.recipe({ run() {} })
quack.runRecipe(r)`)
	if _, err := loadApp(t); err == nil || !strings.HasPrefix(err.Error(), "TypeError: the \"quack\" module runs steps only inside a recipe's run") {
		t.Errorf("outside a run: got %v", err)
	}
}

func TestRecipesThatRunEachOtherWithoutEndFailTheRun(t *testing.T) {
	s := load(t, `
import quack from "quack"
const ping: any = quack.recipe({ name: "ping", run() { quack.runRecipe(pong) } })
const pong: any = quack.recipe({ name: "pong", run() { quack.runRecipe(ping) } })
export const self = quack.recipe({ run() { quack.runRecipe(self) } })
export const deep = quack.recipe({
  run() {
    function down(n: number): number { return n === 0 ? 0 : down(n - 1) + 1 }
    return down(1e6)
  },
})
export const nested = quack.recipe({ run() { quack.runRecipe(deep) } })`)

	for name, want := range map[string]string{
		"ping":   "recipes run each other more than 100 deep",
		"self":   "recipes run each other more than 100 deep",
		"deep":   "the recipe calls functions more than 10000 deep",
		"nested": "the recipe calls functions more than 10000 deep",
	} {
		if _, err := s.Run(context.Background(), newApp(), nil, name, nil, ignore); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %s", name, err, want)
		}
	}
}

func TestARecipeThatReturnsNothingJSONCanHoldReturnsNoData(t *testing.T) {
	s := load(t, `
import quack from "quack"
export const nothing = quack.recipe({ run() {} })
export const fn = quack.recipe({ run() { return (() => 1) as any } })
export const cycle = quack.recipe({ run() { const a: any = {}; a.a = a; return a } })`)

	for _, name := range []string{"nothing", "fn"} {
		if data, err := s.Run(context.Background(), newApp(), nil, name, nil, ignore); data != nil || err != nil {
			t.Errorf("%s: got %s, %v, want no data", name, data, err)
		}
	}
	_, err := s.Run(context.Background(), newApp(), nil, "cycle", nil, ignore)
	if err == nil || !strings.HasPrefix(err.Error(), "recipe 'cycle' returned a value that JSON cannot hold: TypeError") {
		t.Errorf("cycle: got %v", err)
	}
}

func TestFailEndsTheRunWithItsMessageAloneUnlessTheRecipeCatchesIt(t *testing.T) {
	s := load(t, `
import quack from "quack"
const inner = quack.recipe({ run() { quack.fail("no account") } })
export const uncaught = quack.recipe({ run() { quack.runRecipe(inner) } })
export const caught = quack.recipe({
  run() {
    try {
      quack.fail("no account")
    } catch (e) {
      console.log("caught " + (e as Error).message + " " + (e instanceof Error))
    }
  },
})`)
	var logged []string
	observer := logTo(&logged)

	_, err := s.Run(context.Background(), newApp(), nil, "uncaught", nil, observer)
	var thrown *ScriptError
	if err == nil || err.Error() != "no account" || errors.As(err, &thrown) {
		t.Errorf("uncaught: got %#v, want the message without a stack", err)
	}
	if _, err := s.Run(context.Background(), newApp(), nil, "caught", nil, observer); err != nil || !reflect.DeepEqual(logged, []string{"caught no account true"}) {
		t.Errorf("caught: got %q, %v", logged, err)
	}
}

func TestWaitUntilReturnsTheFirstTruthyValueOfTheCheck(t *testing.T) {
	s := load(t, `
import quack from "quack"
export const r = quack.recipe({
  run() {
    const values = [0, "", null, undefined, NaN, false, { id: 7 }]
    const found = quack.waitUntil(() => values.shift(), { every: 1 })
    console.log(JSON.stringify(found), values.length)
  },
})`)
	var logged []string

	_, err := s.Run(context.Background(), newApp(), nil, "r", nil, logTo(&logged))

	if want := []string{`{"id":7} 0`}; err != nil || !reflect.DeepEqual(logged, want) {
		t.Errorf("got %q, %v, want %q", logged, err, want)
	}
}

const pkg = "io.mehow.quack.sample"

var actions = []wire.Action{
	{ID: "app.params", Params: []wire.Param{
		{Name: "text", Type: wire.ParamTypeString, Presence: wire.Optional{}},
		{Name: "tag", Type: wire.ParamTypeStringList, Presence: wire.Default{Values: []string{"a", "b"}}},
	}},
}

// write keeps file as the recipes of pkg in the user's config.
func write(t *testing.T, file string) string {
	t.Helper()
	path := writeFile(t, pkg+".ts", strings.TrimPrefix(file, "\n"))
	writeConfig(t, "[recipes]\n'"+regexp.QuoteMeta(pkg)+"' = \""+pkg+".ts\"\n")
	return path
}

// writeFile writes a file to the user's recipes dir, where the types are up to date, in a config dir of the test's own.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	if !strings.HasPrefix(os.Getenv("XDG_CONFIG_HOME"), os.TempDir()) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	}
	if _, err := writeTypes(Dir()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(Dir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeConfig(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(config.Path(), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// loadApp loads the recipes of pkg from a dir outside any project, and fails on warnings.
func loadApp(t *testing.T) (*Cookbook, error) {
	t.Helper()
	c, warnings, err := Load(t.TempDir(), pkg)
	if warnings != nil {
		t.Fatalf("warnings: %q", warnings)
	}
	return c, err
}

func load(t *testing.T, file string) *Cookbook {
	t.Helper()
	write(t, file)
	c, err := loadApp(t)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sameJSON(a, b string) bool {
	var x, y any
	return json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil && reflect.DeepEqual(x, y)
}

// A key without a value gives the key no values.
func call(action string, keysAndValues ...string) step.Call {
	args := map[string]wire.RawArg{}
	for i := 0; i < len(keysAndValues); i += 2 {
		key := keysAndValues[i]
		args[key] = append(args[key], keysAndValues[i+1:min(i+2, len(keysAndValues))]...)
		if args[key] == nil {
			args[key] = wire.RawArg{}
		}
	}
	return step.Call{Action: action, Args: args}
}

// The app greets whoever a call names, fails a call of fail, and answers each command with no output.
func newApp() *fakes.App {
	greet := func(_ context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error) {
		notify(wire.Notification{Message: "greeting", Data: json.RawMessage(`{"name":"` + call.Args["name"][0] + `"}`)})
		return wire.MessageResult{Message: "Hello, " + call.Args["name"][0] + "!"}, nil
	}
	app := fakes.NewApp()
	app.Replies["app.greet"], app.Replies["app.login"] = greet, greet
	app.Replies["fail"] = func(context.Context, wire.Call, client.NotificationFunc) (wire.MessageResult, error) {
		return wire.MessageResult{}, wire.Error{Code: wire.ErrorCodeRejected, Message: "no way"}
	}
	app.CommandReply = func(string) adb.Output { return adb.Output{} }
	return app
}

// Each call as its line.
func lines(calls []wire.Call) []string {
	var lines []string
	for _, c := range calls {
		lines = append(lines, step.Line(step.Call(c), nil))
	}
	return lines
}

func ignore(step.Event) {}

func collect(events *[]step.Event) func(step.Event) {
	return func(e step.Event) { *events = append(*events, e) }
}

func logTo(logged *[]string) func(step.Event) {
	return func(e step.Event) {
		if e, ok := e.(step.Logged); ok {
			*logged = append(*logged, e.Text)
		}
	}
}

func startedLines(started *[]string) func(step.Event) {
	return func(e step.Event) {
		if e, ok := e.(step.Started); ok {
			*started = append(*started, e.Line)
		}
	}
}
