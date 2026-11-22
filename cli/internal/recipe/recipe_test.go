package recipe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"quack/internal/client"
	"quack/internal/wire"
)

func TestPathIsInXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/config")

	if got, want := Path("io.mehow.quack.sample"), "/config/quack/recipes/io.mehow.quack.sample.toml"; got != want {
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

		if got, want := Path("io.mehow.quack.sample"), "/home/duck/.config/quack/recipes/io.mehow.quack.sample.toml"; got != want {
			t.Errorf("XDG_CONFIG_HOME %s: got %s, want %s", value, got, want)
		}
	}
}

func TestLoadReadsEveryKindOfStepSortedByName(t *testing.T) {
	write(t, `
[login]
description = "Log in as the test user"
steps = [
  { open-app = true },
  { wait = "2s" },
  { action = "account.login", args = { user = "foo", remember = true, count = 3, ratio = 0.5, tag = ["a", 1] } },
]

[delete]
steps = [{ action = "account.delete" }]
`)

	recipes, err := Load(pkg)

	want := []Recipe{
		{Name: "delete", Steps: []Step{Call{Action: "account.delete"}}},
		{Name: "login", Description: "Log in as the test user", Steps: []Step{
			OpenApp{},
			Wait{2 * time.Second},
			Call{Action: "account.login", Args: map[string]wire.RawArg{
				"user": {"foo"}, "remember": {"true"}, "count": {"3"}, "ratio": {"0.5"}, "tag": {"a", "1"},
			}},
		}},
	}
	if err != nil || !reflect.DeepEqual(recipes, want) {
		t.Errorf("got %#v, %v, want %#v", recipes, err, want)
	}
}

func TestLoadOfAMissingFileHasNoRecipes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if recipes, err := Load(pkg); recipes != nil || err != nil {
		t.Errorf("got %v, %v, want no recipes", recipes, err)
	}
}

func TestLoadFailsOnABrokenFileNamingTheRecipeAndTheStep(t *testing.T) {
	for _, c := range []struct{ file, want string }{
		{`[a]
steps = []`, ": recipe 'a' has no steps"},
		{`[a]
description = "no steps"`, ": recipe 'a' has no steps"},
		{`[a]
steps = [{ open-app = true }, {}]`, ": recipe 'a' step 2: it needs one of action, open-app or wait"},
		{`[a]
steps = [{ open-app = true, wait = "1s" }]`, ": recipe 'a' step 1: it takes only one of action, open-app or wait"},
		{`[a]
steps = [{ args = { x = "1" } }]`, ": recipe 'a' step 1: args needs an action"},
		{`[a]
steps = [{ open-app = false }]`, ": recipe 'a' step 1: open-app must be true"},
		{`[a]
steps = [{ wait = "soon" }]`, ": recipe 'a' step 1: wait 'soon' is not a duration above zero, such as 2s or 500ms"},
		{`[a]
steps = [{ wait = "0s" }]`, ": recipe 'a' step 1: wait '0s' is not a duration above zero, such as 2s or 500ms"},
		{`[a]
steps = [{ action = "x", args = { t = { y = 1 } } }]`, ": recipe 'a' step 1: arg 't' must be a string, a number, a boolean, or an array of those"},
		{`[a]
steps = [{ action = "x", args = { t = [[1]] } }]`, ": recipe 'a' step 1: arg 't' must be a string, a number, a boolean, or an array of those"},
		{`[a]
steps = [{ action = "x", args = { t = 1979-05-27 } }]`, ": recipe 'a' step 1: arg 't' must be a string, a number, a boolean, or an array of those"},
		{`["-a"]
steps = [{ open-app = true }]`, ": recipe '-a': its name must not be empty, contain whitespace, or start with '-', '/' or '$'"},
		{`["$a"]
steps = [{ open-app = true }]`, ": recipe '$a': its name must not be empty, contain whitespace, or start with '-', '/' or '$'"},
		{`["a b"]
steps = [{ open-app = true }]`, ": recipe 'a b': its name must not be empty, contain whitespace, or start with '-', '/' or '$'"},
		{`[""]
steps = [{ open-app = true }]`, ": recipe '': its name must not be empty, contain whitespace, or start with '-', '/' or '$'"},
		{`[a]
steps = [{ open-app = true, nope = 1 }]`, ":2:29: unknown key 'nope'"},
		{`[a]
steps = [{ open-app = true }]
nope = 1`, ":3:1: unknown key 'nope'"},
		{`[a
`, ":1:3: expected ']' to close table name"},
	} {
		path := write(t, c.file)

		recipes, err := Load(pkg)

		if err == nil || err.Error() != path+c.want {
			t.Errorf("%s\ngot %v, %v, want %s", c.file, recipes, err, path+c.want)
		}
	}
}

func TestCheckPassesCallsTheAppWouldTake(t *testing.T) {
	r := recipeOf(
		OpenApp{},
		Wait{time.Second},
		call("app.greet", "name", "Bob"),
		call("app.params", "count", "-3", "size", "9000000000", "ratio", "1e3", "flag", "false", "theme", "DARK", "tag", "a", "tag", "b"),
		call("app.params", "tag"),
	)

	if err := Check(r, actions); err != nil {
		t.Errorf("got %v, want no error", err)
	}
}

func TestCheckFailsOnACallTheAppWouldRefuse(t *testing.T) {
	for _, c := range []struct {
		call Call
		want wire.Error
	}{
		{call("nope"), wire.Error{Code: wire.ErrorCodeUnknownAction, Message: "recipe 'r' step 2: no action 'nope'"}},
		{call("app.greet", "name", "Bob", "nope", "1"), invalid("unknown argument 'nope'")},
		{call("app.greet"), invalid("missing argument 'name'")},
		{call("app.greet", "name", "Bob", "name", "Al"), invalid("'name' given more than once")},
		{call("app.greet", "name"), invalid("'name' expects string, got none")},
		{call("app.params", "count", "3000000000"), invalid("'count' expects int, got '3000000000'")},
		{call("app.params", "count", "1.5"), invalid("'count' expects int, got '1.5'")},
		{call("app.params", "size", "x"), invalid("'size' expects long, got 'x'")},
		{call("app.params", "ratio", "NaN"), invalid("'ratio' expects double, got 'NaN'")},
		{call("app.params", "ratio", "1e400"), invalid("'ratio' expects double, got '1e400'")},
		{call("app.params", "flag", "True"), invalid("'flag' expects boolean, got 'True'")},
		{call("app.params", "theme", "blue"), invalid("'theme' expects one of light, dark, got 'blue'")},
	} {
		err := Check(recipeOf(OpenApp{}, c.call), actions)

		if !reflect.DeepEqual(err, c.want) {
			t.Errorf("%v: got %v, want %v", c.call, err, c.want)
		}
	}
}

func TestDoRunsEachKindOfStep(t *testing.T) {
	app := &fakeApp{}
	var notified []string

	result, err := Do(context.Background(), app, call("app.greet", "name", "Bob"), func(m string) { notified = append(notified, m) })
	if err != nil || result.Message != "Hello, Bob!" || !reflect.DeepEqual(notified, []string{"greeting"}) {
		t.Errorf("call: got %v, %v and notifications %v", result, err, notified)
	}
	if _, err := Do(context.Background(), app, OpenApp{}, nil); err != nil || !reflect.DeepEqual(app.starts, []client.StartMode{client.Open}) {
		t.Errorf("open-app: got %v and starts %v, want one open", err, app.starts)
	}
	started := time.Now()
	if _, err := Do(context.Background(), app, Wait{50 * time.Millisecond}, nil); err != nil || time.Since(started) < 50*time.Millisecond {
		t.Errorf("wait: got %v after %v, want nothing after 50ms", err, time.Since(started))
	}
}

func TestCancelingEndsAWaitAndSkipsAStepThatHasNotStarted(t *testing.T) {
	app := &fakeApp{}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	started := time.Now()
	_, err := Do(ctx, app, Wait{time.Minute}, nil)
	if err == nil || err.Error() != "canceled" || time.Since(started) > time.Second {
		t.Errorf("wait: got %v after %v, want canceled at once", err, time.Since(started))
	}
	if _, err := Do(ctx, app, OpenApp{}, nil); err == nil || err.Error() != "canceled" || app.starts != nil {
		t.Errorf("open-app: got %v and starts %v, want canceled without a start", err, app.starts)
	}
}

func TestLineWritesAStepAsTheUserTypesIt(t *testing.T) {
	for _, c := range []struct {
		step Step
		want string
	}{
		{call("app.params", "tag", "b c", "text", "hi", "tag", "a"), "app.params text=hi tag='b c' tag=a"},
		{call("other", "z", "1", "a", "2"), "other a=2 z=1"},
		{OpenApp{}, "/open-app"},
		{Wait{1500 * time.Millisecond}, "wait 1.5s"},
	} {
		if got := Line(c.step, actions); got != c.want {
			t.Errorf("got %s, want %s", got, c.want)
		}
	}
}

func TestShellReadsAQuotedValueBackAsTyped(t *testing.T) {
	for _, value := range []string{"", "plain", "Bob Smith", "$HOME", "`id`", `a"b`, "it's", `back\slash`, "line\nbreak", "zażółć"} {
		out, err := exec.Command("sh", "-c", "printf %s "+quote(value)).Output()
		if err != nil || string(out) != value {
			t.Errorf("sh read %s back as %q (%v), want %q", quote(value), out, err, value)
		}
	}
}

const pkg = "io.mehow.quack.sample"

var actions = []wire.Action{
	{ID: "app.greet", Params: []wire.Param{
		{Name: "name", Type: wire.ParamTypeString, Presence: wire.Required{}},
		{Name: "times", Type: wire.ParamTypeInt, Presence: wire.Default{Values: []string{"1"}}},
	}},
	{ID: "app.params", Params: []wire.Param{
		{Name: "text", Type: wire.ParamTypeString, Presence: wire.Optional{}},
		{Name: "count", Type: wire.ParamTypeInt, Presence: wire.Optional{}},
		{Name: "size", Type: wire.ParamTypeLong, Presence: wire.Optional{}},
		{Name: "ratio", Type: wire.ParamTypeDouble, Presence: wire.Optional{}},
		{Name: "flag", Type: wire.ParamTypeBoolean, Presence: wire.Optional{}},
		{Name: "theme", Type: wire.ParamTypeEnum, Choices: []string{"light", "dark"}, Presence: wire.Optional{}},
		{Name: "tag", Type: wire.ParamTypeStringList, Presence: wire.Default{Values: []string{"a", "b"}}},
	}},
}

// write writes file as the recipes of pkg in a config dir of the test's own, and returns its path.
func write(t *testing.T, file string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := Path(pkg)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimPrefix(file, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func recipeOf(steps ...Step) Recipe { return Recipe{Name: "r", Steps: steps} }

// call calls action with keys and values in turn. A key without a value gives the key no values.
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

func invalid(message string) wire.Error {
	return wire.Error{Code: wire.ErrorCodeInvalidArgs, Message: "recipe 'r' step 2: " + message}
}

type fakeApp struct {
	starts []client.StartMode
}

func (a *fakeApp) Call(_ context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error) {
	notify("greeting")
	return wire.MessageResult{Message: "Hello, " + call.Args["name"][0] + "!"}, nil
}

func (a *fakeApp) Start(mode client.StartMode) error {
	a.starts = append(a.starts, mode)
	return nil
}
