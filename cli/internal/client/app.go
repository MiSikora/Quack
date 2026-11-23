package client

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"quack/internal/adb"
	"quack/internal/device"
	"quack/internal/wire"
)

type StartMode string

const (
	// Wake also thaws an app that Android froze in the background.
	Wake    StartMode = "wake"
	OpenApp StartMode = "open-app"
)

type CallError struct {
	Message string
}

func (e *CallError) Error() string { return e.Message }

// What a call, a step or a recipe returns when its ctx ends first.
var ErrCanceled = &CallError{"canceled"}

type NotificationFunc func(notification wire.Notification)

type Options struct {
	Target       device.Target
	Mode         StartMode
	StartTimeout time.Duration
	// Status receives "starting" and keep-alive warnings.
	Status io.Writer
}

// Each request runs on a connection of its own.
type App struct {
	device       adb.Device
	pkg          string
	socketName   string
	mode         StartMode
	startTimeout time.Duration
	status       io.Writer
}

func Resolve(options Options) (App, error) {
	serial, pkg := options.Target.Device, options.Target.Package
	// The app puts the user ID in its socket name so that copies in different Android users do not collide.
	user, err := serial.Shell("cmd activity get-current-user")
	if err != nil {
		return App{}, err
	}
	return App{
		device:       serial,
		pkg:          pkg,
		socketName:   pkg + ".quack." + user,
		mode:         options.Mode,
		startTimeout: options.StartTimeout,
		status:       options.Status,
	}, nil
}

// Sorted by id, whatever order the app declares them in.
func (a App) Describe(ctx context.Context) (wire.Hello, []wire.Action, error) {
	hello, result, err := a.send(ctx, wire.EncodeDescribe(), func(wire.Notification) {})
	if err != nil {
		return wire.Hello{}, nil, err
	}
	actions, ok := result.(wire.ActionsResult)
	if !ok {
		return wire.Hello{}, nil, &CallError{"malformed reply: describe answered without actions"}
	}
	slices.SortFunc(actions.Actions, func(a, b wire.Action) int { return cmp.Compare(a.ID, b.ID) })
	return hello, actions.Actions, nil
}

// Canceling ctx closes the connection, which cancels the action in the app.
func (a App) Call(ctx context.Context, call wire.Call, notify NotificationFunc) (wire.MessageResult, error) {
	_, result, err := a.send(ctx, wire.EncodeCall(call), notify)
	if err != nil {
		return wire.MessageResult{}, err
	}
	message, ok := result.(wire.MessageResult)
	if !ok {
		return wire.MessageResult{}, &CallError{"malformed reply: the call answered without a message"}
	}
	return message, nil
}

func (a App) Target() device.Target { return device.Target{Device: a.device, Package: a.pkg} }

func (a App) Command(ctx context.Context, cmd string) (adb.Output, error) {
	return a.device.Command(ctx, cmd)
}

// One round trip. "starting" prints as soon as pidof answers, before the slow part.
func (a App) Start(mode StartMode) error {
	// A stopped app may be missing, which would otherwise surface only as a timeout. Check first, so that "starting"
	// is not said for an app that is not there.
	notInstalled := fmt.Sprintf(`pm path %[1]s >/dev/null 2>&1 || echo "Error: %[1]s is not installed"`, a.pkg)
	out, err := a.device.ShellStream("pidof " + a.pkg + " >/dev/null || { " + notInstalled + "; echo stopped; }; " + fmt.Sprintf(startCommands[mode], a.pkg))
	if err != nil {
		return err
	}
	defer out.Close()
	reader := bufio.NewReader(out)
	first, _ := reader.ReadString('\n')
	if strings.TrimSpace(first) == "stopped" {
		fmt.Fprintf(a.status, "starting %s…\n", a.pkg)
		first = ""
	}
	rest, _ := io.ReadAll(reader)
	for _, line := range strings.Split(first+string(rest), "\n") {
		if strings.HasPrefix(line, "Error") {
			return &device.AppError{Message: strings.TrimSpace(strings.TrimPrefix(line, "Error:"))}
		}
	}
	return nil
}

func (a App) send(ctx context.Context, request []byte, notify NotificationFunc) (wire.Hello, wire.Frame, error) {
	if err := a.Start(a.mode); err != nil {
		return wire.Hello{}, nil, err
	}
	c, err := a.connectRetrying(ctx)
	if err != nil {
		return wire.Hello{}, nil, err
	}
	defer c.close()
	stop := context.AfterFunc(ctx, c.close)
	defer stop()
	watchdog := time.AfterFunc(keepAliveAfter, a.keepAlive)
	defer watchdog.Stop()
	last, err := c.exchange(request, notify)
	if ctx.Err() != nil {
		return wire.Hello{}, nil, ErrCanceled
	}
	if err != nil {
		return wire.Hello{}, nil, err
	}
	switch frame := last.(type) {
	case wire.MessageResult, wire.ActionsResult:
		return c.hello, frame, nil
	case wire.Error:
		return wire.Hello{}, nil, frame
	case wire.Malformed:
		return wire.Hello{}, nil, &CallError{"malformed reply: " + frame.Reason}
	default:
		return wire.Hello{}, nil, &CallError{"the app closed the connection before the action finished"}
	}
}

func (a App) keepAlive() {
	out, err := a.device.Shell("am start-foreground-service -n " + a.pkg + "/" + keepAliveService)
	if err == nil && strings.Contains(out, "Error") {
		err = fmt.Errorf("%s", out)
	}
	if err != nil {
		fmt.Fprintf(a.status, "quack: could not keep %s running in the background: %v\n", a.pkg, err)
	}
}

var startCommands = map[StartMode]string{
	// The broadcast starts a stopped app and thaws one that Android froze in the background.
	Wake: "am broadcast -a " + device.WakeAction + " -p %[1]s --include-stopped-packages",
	// Launcher activities usually lack CATEGORY_DEFAULT, so am start cannot resolve them from the package alone.
	// An app can have several launcher activities. resolve-activity answers with the app chooser then, so take the
	// first one query-activities lists, as monkey does.
	OpenApp: `c=$(cmd package query-activities --brief -a android.intent.action.MAIN -c android.intent.category.LAUNCHER %[1]s | grep -m 1 / | tr -d ' '); ` +
		`case $c in */*) am start -W -n "$c" ;; *) echo "Error: %[1]s has no launcher activity" ;; esac`,
}

// Android freezes a background app about 10 s after quack wakes it, and the app may not start a foreground
// service itself from there.
const keepAliveService = "io.mehow.quack.android.QuackService"

const keepAliveAfter = 3 * time.Second
