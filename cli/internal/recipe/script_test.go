package recipe

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/wire"
)

func TestPathIsATypeScriptFileInXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/config")

	if got, want := Path(pkg), "/config/quack/recipes/io.mehow.quack.sample.ts"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestPathFallsBackToDotConfigInHome(t *testing.T) {
	for _, value := range []string{"", "unset"} {
		if value == "unset" {
			t.Setenv("XDG_CONFIG_HOME", "")
			os.Unsetenv("XDG_CONFIG_HOME")
		} else {
			t.Setenv("XDG_CONFIG_HOME", value)
		}
		t.Setenv("HOME", "/home/duck")

		if got, want := Path(pkg), "/home/duck/.config/quack/recipes/io.mehow.quack.sample.ts"; got != want {
			t.Errorf("XDG_CONFIG_HOME %s: got %s, want %s", value, got, want)
		}
	}
}

func TestPathIsTheJavaScriptFileWhenOnlyThatExists(t *testing.T) {
	path := writeFile(t, pkg+".js", "")

	if got := Path(pkg); got != path {
		t.Errorf("got %s, want %s", got, path)
	}
}

func TestLoadOfAMissingFileHasNoRecipes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if s, err := Load(pkg); s != nil || err != nil {
		t.Errorf("got %v, %v", s, err)
	}
}

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

	s, err := Load(pkg)

	if err != nil {
		t.Fatal(err)
	}
	want := []Recipe{
		{Name: "clean"},
		{Name: "login", Description: "Log in", Params: []wire.Param{
			{Name: "user", Description: "Who", Type: wire.ParamTypeString, Hints: []string{"alice", "bob"}, Presence: wire.Default{Values: []string{"foo"}}},
			{Name: "count", Type: wire.ParamTypeInt, Presence: wire.Required{}},
			{Name: "theme", Type: wire.ParamTypeEnum, Choices: []string{"light", "dark"}, Presence: wire.Optional{}},
			{Name: "tags", Type: wire.ParamTypeStringList, Presence: wire.Default{Values: []string{"a", "b"}}},
		}},
	}
	if s.Path != path || !reflect.DeepEqual(s.Recipes, want) {
		t.Errorf("got %s %+v, want %s %+v", s.Path, s.Recipes, path, want)
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

	_, err := s.Run(context.Background(), &fakeApp{}, "app.reset", nil, Observer{Logged: func(text string) { logged = append(logged, text) }})

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

func TestLoadReadsAJavaScriptFile(t *testing.T) {
	writeFile(t, pkg+".js", `import quack from "quack"
export const clean = quack.recipe({ description: "Clean", run() {} })`)

	s, err := Load(pkg)

	if err != nil || len(s.Recipes) != 1 || s.Recipes[0].Name != "clean" {
		t.Errorf("got %+v, %v", s, err)
	}
}

func TestLoadPassesOverExportsThatAreNoRecipes(t *testing.T) {
	writeFile(t, pkg+".ts", `import quack from "quack"
export const label = "zażółć"
export const count = 3
export const clean = quack.recipe({ run() {} })`)

	s, err := Load(pkg)

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
export const $a = quack.recipe({ run() {} })`, "recipe '$a': its name may hold only ASCII letters, '_', '.' and '-', and must not start with '-'"},
		{`import quack from "quack"
quack.openApp()`, `TypeError: the "quack" module runs steps only inside a recipe's run`},
		{`import quack from "quack"
quack.recipe({ name: "$a", run() {} })`, "recipe '$a': its name may hold only ASCII letters, '_', '.' and '-', and must not start with '-'"},
		{`import quack from "quack"
quack.recipe({ name: "a", run() {} })
export const a = quack.recipe({ run() {} })`, "two recipes are named 'a'"},
		{`import quack from "quack"
export const reset2 = quack.recipe({ run() {} })`, "recipe 'reset2': its name may hold only"},
		{`import quack from "quack"
quack.recipe({ name: "-reset", run() {} })`, "recipe '-reset': its name may hold only"},
		{`import quack from "quack"
quack.recipe({ name: "app:reset", run() {} })`, "recipe 'app:reset': its name may hold only"},
	} {
		write(t, c.file)

		_, err := Load(pkg)

		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %s", c.file, err, c.want)
		}
	}
}

func TestLoadFailsWhenBothFilesExist(t *testing.T) {
	ts := write(t, "")
	js := writeFile(t, pkg+".js", "")

	_, err := Load(pkg)

	if want := ts + " and " + js + " both hold recipes of " + pkg + ": keep one"; err == nil || err.Error() != want {
		t.Errorf("got %v, want %s", err, want)
	}
}

func TestLoadBringsTheFilesQuackOwnsUpToDate(t *testing.T) {
	write(t, "")
	stale := filepath.Join(Dir(), "quack.d.ts")
	os.WriteFile(stale, []byte("old"), 0o644)

	if _, err := Load(pkg); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string][]byte{"quack.d.ts": quackTypes, "tsconfig.json": tsconfig} {
		if got, _ := os.ReadFile(filepath.Join(Dir(), name)); string(got) != string(want) {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

func TestInitWritesTheFilesQuackOwnsOnce(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	written, err := Init("")

	want := []string{filepath.Join(Dir(), "quack.d.ts"), filepath.Join(Dir(), "tsconfig.json")}
	if err != nil || !reflect.DeepEqual(written, want) {
		t.Fatalf("got %v, %v, want %v", written, err, want)
	}
	for name, want := range map[string][]byte{"quack.d.ts": quackTypes, "tsconfig.json": tsconfig} {
		if got, _ := os.ReadFile(filepath.Join(Dir(), name)); string(got) != string(want) {
			t.Errorf("%s: got %q", name, got)
		}
	}
	if written, err := Init(""); err != nil || len(written) != 0 {
		t.Errorf("again: got %v, %v, want nothing written", written, err)
	}
	if entries, _ := os.ReadDir(Dir()); len(entries) != 2 {
		t.Errorf("the recipes dir has %d files, want only the two quack owns", len(entries))
	}
}

func TestInitWithAPackageStartsItsRecipesFileWithARecipeThatRuns(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	written, err := Init(pkg)

	want := []string{filepath.Join(Dir(), "quack.d.ts"), filepath.Join(Dir(), "tsconfig.json"), Path(pkg)}
	if err != nil || !reflect.DeepEqual(written, want) {
		t.Fatalf("got %v, %v, want %v", written, err, want)
	}
	s, err := Load(pkg)
	if err != nil || len(s.Recipes) != 1 || s.Recipes[0].Name != "open" {
		t.Fatalf("starter: got %+v, %v", s, err)
	}
	app := &fakeApp{}
	if _, err := s.Run(context.Background(), app, "open", nil, Observer{}); err != nil || !reflect.DeepEqual(app.starts, []client.StartMode{client.OpenApp}) {
		t.Errorf("open: got %v and starts %v", err, app.starts)
	}
	if written, err := Init(pkg); err != nil || len(written) != 0 {
		t.Errorf("again: got %v, %v, want nothing written", written, err)
	}
}

func TestInitKeepsARecipesFile(t *testing.T) {
	for _, name := range []string{pkg + ".ts", pkg + ".js"} {
		path := writeFile(t, name, "// mine")
		t.Cleanup(func() { os.Remove(path) })

		if written, err := Init(pkg); err != nil || slices.Contains(written, Path(pkg)) {
			t.Errorf("%s: got %v, %v", name, written, err)
		}
		if got, _ := os.ReadFile(path); string(got) != "// mine" {
			t.Errorf("%s: got %q", name, got)
		}
		os.Remove(path)
	}
}

func TestRunRunsTheStepsInOrderAndTellsTheObserver(t *testing.T) {
	app := &fakeApp{output: adb.Output{Stdout: "Success\n"}}
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
	var events []string
	observer := Observer{
		Started:  func(step Step) { events = append(events, "› "+Line(step, nil)) },
		Notified: func(n wire.Notification) { events = append(events, "notified "+n.Message) },
		Ended:    func(_ Step, result wire.MessageResult) { events = append(events, "ended "+result.Message) },
		Logged:   func(text string) { events = append(events, "logged "+text) },
	}

	_, err := s.Run(context.Background(), app, "r", nil, observer)

	want := []string{
		"› /open-app", "ended ",
		"› app.greet loud=true name=Bob tag=a tag=b times=2", "notified greeting", "ended Hello, Bob!",
		"logged got Hello, Bob! greeting Bob",
		"› shell echo", "ended Success",
		"› shell pm clear Success", "ended Success",
		"› run echo duck", "ended duck", "logged duck",
		"› wait 10ms", "ended ",
		"› wait 10ms", "ended ",
	}
	if err != nil || !reflect.DeepEqual(events, want) {
		t.Errorf("got %v\n%q\nwant\n%q", err, events, want)
	}
	if !reflect.DeepEqual(app.starts, []client.StartMode{client.OpenApp}) {
		t.Errorf("starts: got %v", app.starts)
	}
}

func TestARecipeBranchesOnTheDataOfAnAction(t *testing.T) {
	app := &fakeApp{data: map[string]string{"account.status": `{"tries":[1,2],"loggedIn":false}`}}
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

	if _, err := s.Run(context.Background(), app, "r", nil, Observer{Logged: func(text string) { logged = append(logged, text) }}); err != nil {
		t.Fatal(err)
	}

	if want := []string{"account.status", "app.greet name='tries 2'", "app.greet name=x"}; !reflect.DeepEqual(app.calls, want) {
		t.Errorf("got %v, want %v", app.calls, want)
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

	_, err := s.Run(context.Background(), &fakeApp{}, "r", map[string]wire.RawArg{
		"count": {"3"}, "size": {"9007199254740993"}, "ratio": {"0.5"}, "flag": {"true"}, "theme": {"dark"}, "tag": {"x", "y"},
	}, Observer{Logged: func(text string) { logged = append(logged, text) }})

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
		_, err := s.Run(context.Background(), &fakeApp{}, "r", c.args, Observer{})

		var refused wire.Error
		if !errors.As(err, &refused) || refused.Code != wire.ErrorCodeInvalidArgs || refused.Message != c.want {
			t.Errorf("%v: got %v, want %s", c.args, err, c.want)
		}
	}
	if _, err := s.Run(context.Background(), &fakeApp{}, "nope", nil, Observer{}); err == nil || !strings.HasPrefix(err.Error(), "no recipe named nope in ") {
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
	s, err := Load(pkg)
	if err != nil {
		t.Fatal(err)
	}
	var ended []Step
	var failed []error

	_, err = s.Run(context.Background(), &fakeApp{}, "r", nil, Observer{
		Ended:  func(step Step, _ wire.MessageResult) { ended = append(ended, step) },
		Failed: func(_ Step, err error) { failed = append(failed, err) },
	})

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
	if ended != nil {
		t.Errorf("ended: got %v, want none", ended)
	}
	if !errors.Is(err, refused) || len(failed) != 1 || failed[0] != error(refused) {
		t.Errorf("failed: got %v, want the app's error once, the one that ended the run", failed)
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
	var events []string

	_, err := s.Run(context.Background(), &fakeApp{}, "r", nil, Observer{
		Started: func(step Step) { events = append(events, "› "+Line(step, nil)) },
		Failed:  func(_ Step, err error) { events = append(events, "failed "+err.Error()) },
		Logged:  func(text string) { events = append(events, text) },
	})

	if want := []string{"› fail", "failed [rejected] no way", "caught [rejected] no way"}; err != nil || !reflect.DeepEqual(events, want) {
		t.Errorf("got %q, %v, want %q", events, err, want)
	}
}

func TestCancelingInterruptsARecipeWhereverItIs(t *testing.T) {
	s := load(t, `
import quack from "quack"
export const spin = quack.recipe({ run() { while (true) {} } })`)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	started := time.Now()
	_, err := s.Run(ctx, &fakeApp{}, "spin", nil, Observer{})

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

	_, err := s.Run(ctx, &fakeApp{}, "spin", nil, Observer{})

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
	var started []Step
	observer := Observer{Logged: func(text string) { logged = append(logged, text) }, Started: func(step Step) { started = append(started, step) }}

	if _, err := s.Run(context.Background(), &fakeApp{}, "passes", nil, observer); err != nil || !reflect.DeepEqual(logged, []string{"checks 3"}) {
		t.Errorf("passes: got %v, %v", logged, err)
	}
	_, err := s.Run(context.Background(), &fakeApp{}, "times", nil, observer)
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

	_, err := s.Run(context.Background(), &fakeApp{}, "r", nil, Observer{Logged: func(text string) { logged = append(logged, text) }})

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
	app := &fakeApp{}
	var started []string

	_, err := s.Run(context.Background(), app, "r", nil, Observer{Started: func(step Step) { started = append(started, Line(step, nil)) }})

	if want := []string{"shell curl -H 'Authorization: ${QUACK_TOKEN}' -d Bob", "app.greet name='${QUACK_TOKEN}-Bob'"}; err != nil || !reflect.DeepEqual(started, want) {
		t.Errorf("started: got %v, %v, want %v", started, err, want)
	}
	if want := []string{"curl -H 'Authorization: s3cret' -d Bob"}; !reflect.DeepEqual(app.commands, want) {
		t.Errorf("commands: got %v, want %v", app.commands, want)
	}
	if want := []string{"app.greet name=s3cret-Bob"}; !reflect.DeepEqual(app.calls, want) {
		t.Errorf("calls: got %v, want %v", app.calls, want)
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
	app := &fakeApp{}
	var started []string

	_, err := s.Run(context.Background(), app, "set", nil, Observer{Started: func(step Step) { started = append(started, Line(step, nil)) }})

	if want := (wire.Default{Values: []string{"${QUACK_CODE}"}}); !reflect.DeepEqual(s.Recipes[0].Params[0].Presence, want) {
		t.Errorf("set presence: got %v, want %v", s.Recipes[0].Params[0].Presence, want)
	}
	if want := []string{"app.greet name=s3cret"}; err != nil || !reflect.DeepEqual(app.calls, want) {
		t.Errorf("calls: got %v, %v, want %v", app.calls, err, want)
	}
	if want := []string{"app.greet name='${QUACK_CODE}'"}; !reflect.DeepEqual(started, want) {
		t.Errorf("started: got %v, want %v", started, want)
	}
	if want := (wire.Required{}); !reflect.DeepEqual(s.Recipes[1].Params[0].Presence, want) {
		t.Errorf("unset presence: got %v, want %v", s.Recipes[1].Params[0].Presence, want)
	}
	if _, err := s.Run(context.Background(), app, "unset", nil, Observer{}); err == nil || !strings.HasSuffix(err.Error(), "missing argument 'code'") {
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
	app := &fakeApp{}
	var events []string

	data, err := s.Run(context.Background(), app, "outer", nil, Observer{
		Started: func(step Step) { events = append(events, "› "+Line(step, nil)) },
		Ended:   func(_ Step, result wire.MessageResult) { events = append(events, "ended "+result.Message) },
		Logged:  func(text string) { events = append(events, text) },
	})

	want := []string{
		"› recipe login user=bob pin='${QUACK_PIN}' tries=2 tags=a tags='b c'",
		"ended ",
		"› app.greet name=bob",
		"ended Hello, bob!",
		`{"user":"bob","tries":2,"tags":["a","b c"],"at":"1970-01-01T00:00:00.000Z"}`,
		"› recipe",
		"ended ",
	}
	if err != nil || !reflect.DeepEqual(events, want) {
		t.Errorf("got %q, %v, want %q", events, err, want)
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

	_, err := s.Run(context.Background(), &fakeApp{}, "outer", nil, Observer{Logged: func(text string) { logged = append(logged, text) }})

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
	if _, err := s.Run(context.Background(), &fakeApp{}, "named", nil, Observer{}); err == nil || !strings.HasPrefix(err.Error(), "TypeError: runRecipe() takes a recipe that quack.recipe() returned") {
		t.Errorf("by name: got %v", err)
	}

	write(t, `
import quack from "quack"
export const r = quack.recipe({ run() {} })
quack.runRecipe(r)`)
	if _, err := Load(pkg); err == nil || !strings.HasPrefix(err.Error(), "TypeError: the \"quack\" module runs steps only inside a recipe's run") {
		t.Errorf("outside a run: got %v", err)
	}
}

func TestARecipeThatReturnsNothingJSONCanHoldReturnsNoData(t *testing.T) {
	s := load(t, `
import quack from "quack"
export const nothing = quack.recipe({ run() {} })
export const fn = quack.recipe({ run() { return (() => 1) as any } })
export const cycle = quack.recipe({ run() { const a: any = {}; a.a = a; return a } })`)

	for _, name := range []string{"nothing", "fn"} {
		if data, err := s.Run(context.Background(), &fakeApp{}, name, nil, Observer{}); data != nil || err != nil {
			t.Errorf("%s: got %s, %v, want no data", name, data, err)
		}
	}
	_, err := s.Run(context.Background(), &fakeApp{}, "cycle", nil, Observer{})
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
	observer := Observer{Logged: func(text string) { logged = append(logged, text) }}

	_, err := s.Run(context.Background(), &fakeApp{}, "uncaught", nil, observer)
	var thrown *ScriptError
	if err == nil || err.Error() != "no account" || errors.As(err, &thrown) {
		t.Errorf("uncaught: got %#v, want the message without a stack", err)
	}
	if _, err := s.Run(context.Background(), &fakeApp{}, "caught", nil, observer); err != nil || !reflect.DeepEqual(logged, []string{"caught no account true"}) {
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

	_, err := s.Run(context.Background(), &fakeApp{}, "r", nil, Observer{Logged: func(text string) { logged = append(logged, text) }})

	if want := []string{`{"id":7} 0`}; err != nil || !reflect.DeepEqual(logged, want) {
		t.Errorf("got %q, %v, want %q", logged, err, want)
	}
}

func TestExpandArgsReplacesVariables(t *testing.T) {
	t.Setenv("QUACK_NAME", "Bob")
	os.Unsetenv("QUACK_MISSING")

	args, err := ExpandArgs(map[string]wire.RawArg{"name": {"${QUACK_NAME} $$5 $$${QUACK_NAME} $HOME ${QUACK_PACKAGE}"}}, target)
	if want := "Bob $$5 $${QUACK_NAME} $HOME io.mehow.quack.sample"; err != nil || args["name"][0] != want {
		t.Errorf("got %v, %v, want %s", args, err, want)
	}
	if _, err := ExpandArgs(map[string]wire.RawArg{"name": {"${QUACK_MISSING}"}}, target); err == nil || err.Error() != "arg 'name': environment variable 'QUACK_MISSING' is not set" {
		t.Errorf("missing: got %v", err)
	}
}

const pkg = "io.mehow.quack.sample"

var target = client.Target{Device: "emulator-5554", Package: pkg}

var actions = []wire.Action{
	{ID: "app.params", Params: []wire.Param{
		{Name: "text", Type: wire.ParamTypeString, Presence: wire.Optional{}},
		{Name: "tag", Type: wire.ParamTypeStringList, Presence: wire.Default{Values: []string{"a", "b"}}},
	}},
}

func write(t *testing.T, file string) string {
	t.Helper()
	return writeFile(t, pkg+".ts", strings.TrimPrefix(file, "\n"))
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	if !strings.HasPrefix(os.Getenv("XDG_CONFIG_HOME"), os.TempDir()) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	}
	path := filepath.Join(Dir(), name)
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func load(t *testing.T, file string) *Script {
	t.Helper()
	write(t, file)
	s, err := Load(pkg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func sameJSON(a, b string) bool {
	var x, y any
	return json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil && reflect.DeepEqual(x, y)
}

// A key without a value gives the key no values.
func call(action string, keysAndValues ...string) Call {
	args := map[string]wire.RawArg{}
	for i := 0; i < len(keysAndValues); i += 2 {
		key := keysAndValues[i]
		args[key] = append(args[key], keysAndValues[i+1:min(i+2, len(keysAndValues))]...)
		if args[key] == nil {
			args[key] = wire.RawArg{}
		}
	}
	return Call{Action: action, Args: args}
}

type fakeApp struct {
	starts   []client.StartMode
	commands []string
	output   adb.Output
	data     map[string]string
	calls    []string
}

func (a *fakeApp) Target() client.Target { return target }

func (a *fakeApp) Command(_ context.Context, cmd string) (adb.Output, error) {
	a.commands = append(a.commands, cmd)
	return a.output, nil
}

func (a *fakeApp) Call(_ context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error) {
	a.calls = append(a.calls, Line(Call(call), nil))
	switch {
	case call.Action == "fail":
		return wire.MessageResult{}, wire.Error{Code: wire.ErrorCodeRejected, Message: "no way"}
	case a.data[call.Action] != "":
		return wire.MessageResult{Message: call.Action, Data: json.RawMessage(a.data[call.Action])}, nil
	}
	notify(wire.Notification{Message: "greeting", Data: json.RawMessage(`{"name":"` + call.Args["name"][0] + `"}`)})
	return wire.MessageResult{Message: "Hello, " + call.Args["name"][0] + "!"}, nil
}

func (a *fakeApp) Start(mode client.StartMode) error {
	a.starts = append(a.starts, mode)
	return nil
}
