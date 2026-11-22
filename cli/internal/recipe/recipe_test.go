package recipe

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/wire"
)

func TestDoRunsEachKindOfStep(t *testing.T) {
	app := &fakeApp{}
	var notified []string

	result, err := Do(context.Background(), app, call("app.greet", "name", "Bob"), func(n wire.Notification) { notified = append(notified, n.Message) })
	if err != nil || result.Message != "Hello, Bob!" || !reflect.DeepEqual(notified, []string{"greeting"}) {
		t.Errorf("call: got %v, %v and notifications %v", result, err, notified)
	}
	if _, err := Do(context.Background(), app, OpenApp{}, nil); err != nil || !reflect.DeepEqual(app.starts, []client.StartMode{client.OpenApp}) {
		t.Errorf("open-app: got %v and starts %v, want one open", err, app.starts)
	}
	started := time.Now()
	if _, err := Do(context.Background(), app, Wait{50 * time.Millisecond}, nil); err != nil || time.Since(started) < 50*time.Millisecond {
		t.Errorf("wait: got %v after %v, want nothing after 50ms", err, time.Since(started))
	}
}

func TestDoRunsAShellCommandOnTheDevice(t *testing.T) {
	app := &fakeApp{output: adb.Output{Stdout: "Success\n"}}

	result, err := Do(context.Background(), app, Shell{"pm clear com.example"}, nil)

	if err != nil || result.Message != "Success" || !reflect.DeepEqual(app.commands, []string{"pm clear com.example"}) {
		t.Errorf("got %v, %v and commands %v", result, err, app.commands)
	}
}

func TestDoRunsACommandOnThisComputer(t *testing.T) {
	result, err := Do(context.Background(), &fakeApp{}, Run{"echo duck; echo noise >&2"}, nil)

	if err != nil || result.Message != "duck" {
		t.Errorf("got %v, %v, want duck", result, err)
	}
}

func TestACommandThatExitsWithAnotherStatusFailsWithItsOutput(t *testing.T) {
	for _, c := range []struct {
		step Step
		app  *fakeApp
		want CommandError
	}{
		{Run{"echo out; echo err >&2; exit 3"}, &fakeApp{}, CommandError{3, "err"}},
		{Run{"echo out; exit 4"}, &fakeApp{}, CommandError{4, "out"}},
		{Shell{"pm clear nope"}, &fakeApp{output: adb.Output{Stderr: "Failed\n", Exit: 1}}, CommandError{1, "Failed"}},
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
	_, err := Do(ctx, &fakeApp{}, Run{"sleep 60"}, nil)

	if err == nil || err.Error() != "canceled" || time.Since(started) > 5*time.Second {
		t.Errorf("got %v after %v, want canceled at once", err, time.Since(started))
	}
}

func TestARunHasTheAppAndTheDeviceInItsEnvironment(t *testing.T) {
	t.Setenv("QUACK_PACKAGE", "com.other")
	t.Setenv("ANDROID_SERIAL", "other-device")

	result, err := Do(context.Background(), &fakeApp{}, Run{`echo "$QUACK_PACKAGE $ANDROID_SERIAL"`}, nil)

	if want := "io.mehow.quack.sample emulator-5554"; err != nil || result.Message != want {
		t.Errorf("got %v, %v, want %s", result, err, want)
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
