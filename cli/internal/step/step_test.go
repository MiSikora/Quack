package step

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/device"
	"quack/internal/fakes"
	"quack/internal/wire"
)

func TestDoRunsEachKindOfStep(t *testing.T) {
	app := newApp(adb.Output{})
	var notified []string

	result, err := Do(context.Background(), app, call("app.greet", "name", "Bob"), func(n wire.Notification) { notified = append(notified, n.Message) })
	if err != nil || result.Message != "Hello, Bob!" || !reflect.DeepEqual(notified, []string{"greeting"}) {
		t.Errorf("call: got %v, %v and notifications %v", result, err, notified)
	}
	if _, err := Do(context.Background(), app, OpenApp{}, nil); err != nil || !reflect.DeepEqual(app.Starts(), []client.StartMode{client.OpenApp}) {
		t.Errorf("open-app: got %v and starts %v, want one open", err, app.Starts())
	}
	started := time.Now()
	if _, err := Do(context.Background(), app, Wait{50 * time.Millisecond}, nil); err != nil || time.Since(started) < 50*time.Millisecond {
		t.Errorf("wait: got %v after %v, want nothing after 50ms", err, time.Since(started))
	}
}

func TestDoRunsAShellCommandOnTheDevice(t *testing.T) {
	app := newApp(adb.Output{Stdout: "Success\n"})

	result, err := Do(context.Background(), app, Shell{"pm clear com.example"}, nil)

	if err != nil || result.Message != "Success" || !reflect.DeepEqual(app.Commands(), []string{"pm clear com.example"}) {
		t.Errorf("got %v, %v and commands %v", result, err, app.Commands())
	}
}

func TestDoRunsACommandOnThisComputer(t *testing.T) {
	result, err := Do(context.Background(), newApp(adb.Output{}), Run{"echo duck; echo noise >&2"}, nil)

	if err != nil || result.Message != "duck" {
		t.Errorf("got %v, %v, want duck", result, err)
	}
}

func TestACommandThatExitsWithAnotherStatusFailsWithItsOutput(t *testing.T) {
	for _, c := range []struct {
		step Step
		app  *fakes.App
		want CommandError
	}{
		{Run{"echo out; echo err >&2; exit 3"}, newApp(adb.Output{}), CommandError{3, "err"}},
		{Run{"echo out; exit 4"}, newApp(adb.Output{}), CommandError{4, "out"}},
		{Shell{"pm clear nope"}, newApp(adb.Output{Stderr: "Failed\n", Exit: 1}), CommandError{1, "Failed"}},
	} {
		_, err := Do(context.Background(), c.app, c.step, nil)

		var failed *CommandError
		if !errors.As(err, &failed) || *failed != c.want {
			t.Errorf("%v: got %v, want %v", c.step, err, &c.want)
		}
	}
	if got := (&CommandError{Exit: 2}).Error(); got != "the command exited with 2" {
		t.Errorf("got %s", got)
	}
}

func TestCancelingEndsACommandOnThisComputer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	started := time.Now()
	_, err := Do(ctx, newApp(adb.Output{}), Run{"sleep 60"}, nil)

	if err == nil || err.Error() != "canceled" || time.Since(started) > 5*time.Second {
		t.Errorf("got %v after %v, want canceled at once", err, time.Since(started))
	}
}

func TestARunHasTheAppAndTheDeviceInItsEnvironment(t *testing.T) {
	t.Setenv("QUACK_PACKAGE", "com.other")
	t.Setenv("ANDROID_SERIAL", "other-device")

	result, err := Do(context.Background(), newApp(adb.Output{}), Run{`echo "$QUACK_PACKAGE $ANDROID_SERIAL"`}, nil)

	if want := "io.mehow.quack.sample emulator-5554"; err != nil || result.Message != want {
		t.Errorf("got %v, %v, want %s", result, err, want)
	}
}

func TestCancelingEndsAWaitAndSkipsAStepThatHasNotStarted(t *testing.T) {
	app := newApp(adb.Output{})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	started := time.Now()
	_, err := Do(ctx, app, Wait{time.Minute}, nil)
	if err == nil || err.Error() != "canceled" || time.Since(started) > time.Second {
		t.Errorf("wait: got %v after %v, want canceled at once", err, time.Since(started))
	}
	if _, err := Do(ctx, app, OpenApp{}, nil); err == nil || err.Error() != "canceled" || app.Starts() != nil {
		t.Errorf("open-app: got %v and starts %v, want canceled without a start", err, app.Starts())
	}
}

func TestLineWritesAStepAsTheUserTypesIt(t *testing.T) {
	for _, c := range []struct {
		step Step
		want string
	}{
		{call("app.params", "tag", "b c", "text", "hi", "tag", "a"), "app.params text=hi tag='b c' tag=a"},
		{call("other", "z", "1", "a", "2"), "other a=2 z=1"},
		{call("app.params", "tag"), "app.params tag=[]"},
		{OpenApp{}, "/open-app"},
		{Wait{1500 * time.Millisecond}, "wait 1.5s"},
		{call("app.greet", "name", "${QUACK_NAME}"), "app.greet name='${QUACK_NAME}'"},
		{Shell{"pm clear com.example"}, "shell pm clear com.example"},
		{Run{"./seed.sh"}, "run ./seed.sh"},
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

func TestExpandArgsReplacesVariables(t *testing.T) {
	t.Setenv("QUACK_NAME", "Bob")
	os.Unsetenv("QUACK_MISSING")

	args, err := ExpandArgs(map[string]wire.RawArg{"name": {"${QUACK_NAME} $$5 $$${QUACK_NAME} $HOME ${QUACK_PACKAGE}"}}, target)
	if want := "Bob $$5 $${QUACK_NAME} $HOME io.mehow.quack.sample"; err != nil || args.Values["name"][0] != want {
		t.Errorf("got %v, %v, want %s", args, err, want)
	}
	if _, err := ExpandArgs(map[string]wire.RawArg{"name": {"${QUACK_MISSING}"}}, target); err == nil || err.Error() != "arg 'name': environment variable 'QUACK_MISSING' is not set" {
		t.Errorf("missing: got %v", err)
	}
}

var target = device.Target{Device: "emulator-5554", Package: "io.mehow.quack.sample"}

var actions = []wire.Action{
	{ID: "app.params", Params: []wire.Param{
		{Name: "text", Type: wire.ParamTypeString, Presence: wire.Optional{}},
		{Name: "tag", Type: wire.ParamTypeStringList, Presence: wire.Default{Values: []string{"a", "b"}}},
	}},
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

// The app greets whoever a call of app.greet names, and answers each command with output.
func newApp(output adb.Output) *fakes.App {
	app := fakes.NewApp()
	app.Replies["app.greet"] = func(_ context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error) {
		notify(wire.Notification{Message: "greeting", Data: json.RawMessage(`{"name":"` + call.Args["name"][0] + `"}`)})
		return wire.MessageResult{Message: "Hello, " + call.Args["name"][0] + "!"}, nil
	}
	app.CommandReply = func(string) adb.Output { return output }
	return app
}

func TestExpandedArgumentsKeepProvenanceAndOnlyExpandOnce(t *testing.T) {
	t.Setenv("QUACK_SECRET", "secret ${QUACK_MISSING} $(echo unsafe)")
	original := map[string]wire.RawArg{"name": {"prefix-${QUACK_SECRET}", "$${QUACK_SECRET}", "$(echo literal)"}, "tags": {}}
	args, err := ExpandArgs(original, target)
	if err != nil {
		t.Fatal(err)
	}
	if args.Values["name"][0] != "prefix-secret ${QUACK_MISSING} $(echo unsafe)" || args.Values["name"][1] != "${QUACK_SECRET}" || args.Values["name"][2] != "$(echo literal)" {
		t.Errorf("expanded %v", args.Values)
	}
	if !reflect.DeepEqual(args.Display, original) || args.Environment["QUACK_SECRET"] != "secret ${QUACK_MISSING} $(echo unsafe)" {
		t.Errorf("provenance %v", args)
	}
	if args.Values["tags"] == nil || len(args.Values["tags"]) != 0 {
		t.Errorf("lost empty list %v", args.Values)
	}
}
