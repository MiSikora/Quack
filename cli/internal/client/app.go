// Package client reaches a quack-enabled app on a device: it finds the app, starts it, and sends it requests.
package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"quack/internal/adb"
	"quack/internal/wire"
)

// StartMode is how the CLI gets the app running before each request.
type StartMode string

const (
	// Wake starts the app in the background, or thaws it when Android froze it there.
	Wake StartMode = "wake"
	// Open shows the app's main screen.
	Open StartMode = "open"
)

// AppError is an app that is missing, does not start, or speaks a protocol this CLI cannot use.
type AppError struct {
	Message string
}

func (e *AppError) Error() string { return e.Message }

// CallError is a request that ended without the app's answer: canceled, or cut off.
type CallError struct {
	Message string
}

func (e *CallError) Error() string { return e.Message }

// EventFunc receives the message of each event of a call as it arrives.
type EventFunc func(message string)

// Target is a quack-enabled app on a device: the device and the app's package. The package goes into shell commands,
// so it must be a valid package name.
type Target struct {
	Device  adb.Device
	Package string
}

// Device is a device adb knows and the quack-enabled apps on it. Model and Packages are empty unless the device is
// ready.
type Device struct {
	adb.Listing
	Model    string
	Packages []string
}

type Options struct {
	Target       Target
	Mode         StartMode
	StartTimeout time.Duration
	// Status receives what the CLI says about the app while it works: that it starts the app, or cannot keep it running.
	Status io.Writer
}

// App is a quack-enabled app on a device. Each request runs on a connection of its own.
type App struct {
	device       adb.Device
	pkg          string
	socketName   string
	mode         StartMode
	startTimeout time.Duration
	status       io.Writer
}

// Resolve finds the app that options name.
func Resolve(options Options) (App, error) {
	device, pkg := options.Target.Device, options.Target.Package
	// The app puts the user ID in its socket name so that copies in different Android users do not collide.
	user, err := device.Shell("cmd activity get-current-user")
	if err != nil {
		return App{}, err
	}
	return App{
		device:       device,
		pkg:          pkg,
		socketName:   pkg + ".quack." + user,
		mode:         options.Mode,
		startTimeout: options.StartTimeout,
		status:       options.Status,
	}, nil
}

// Find returns the one target that device and pkg leave, either of which may be "" for any. It fails when none is
// left, saying what is missing, and when several are, listing the flags that pick each.
func Find(device adb.Device, pkg string) (Target, error) {
	devices, err := readyDevices(device)
	if err != nil {
		return Target{}, err
	}
	if pkg != "" && len(devices) == 1 {
		// Starting the app says it when the package is missing, more clearly than not finding it here.
		return Target{devices[0], pkg}, nil
	}
	var targets []Target
	for _, d := range devices {
		pkgs, err := Packages(d)
		if err != nil {
			return Target{}, err
		}
		for _, p := range pkgs {
			if pkg == "" || p == pkg {
				targets = append(targets, Target{d, p})
			}
		}
	}
	switch {
	case len(targets) == 1:
		return targets[0], nil
	case len(targets) > 1:
		return Target{}, &AppError{ambiguity(targets)}
	case pkg != "":
		return Target{}, &AppError{fmt.Sprintf("no device has %s", pkg)}
	case len(devices) == 1:
		return Target{}, &AppError{fmt.Sprintf("no quack-enabled app on %s", devices[0])}
	default:
		return Target{}, &AppError{fmt.Sprintf("no quack-enabled app on any of the %d devices", len(devices))}
	}
}

// Packages lists the quack-enabled apps on device: those that receive wakeAction.
func Packages(device adb.Device) ([]string, error) {
	out, err := device.Shell("cmd package query-receivers --components -a " + wakeAction)
	if err != nil {
		return nil, err
	}
	var pkgs []string
	for _, line := range strings.Split(out, "\n") {
		// Each match is a component such as io.mehow.quack.sample/io.mehow.quack.android.QuackWakeReceiver.
		if p, _, ok := strings.Cut(strings.TrimSpace(line), "/"); ok {
			pkgs = append(pkgs, p)
		}
	}
	return pkgs, nil
}

// Inspect asks a ready device for its model and its quack-enabled apps. Any other device is only its listing.
func Inspect(listing adb.Listing) (Device, error) {
	device := Device{Listing: listing}
	if listing.State != adb.Ready {
		return device, nil
	}
	model, err := listing.Device.Shell("getprop ro.product.model")
	if err != nil {
		return device, err
	}
	pkgs, err := Packages(listing.Device)
	if err != nil {
		return device, err
	}
	device.Model, device.Packages = model, pkgs
	return device, nil
}

// Describe lists the app's actions.
func (a App) Describe(ctx context.Context) (wire.Hello, []wire.Action, error) {
	hello, result, err := a.send(ctx, wire.EncodeDescribe(), func(string) {})
	if err != nil {
		return wire.Hello{}, nil, err
	}
	actions, ok := result.(wire.ActionsResult)
	if !ok {
		return wire.Hello{}, nil, &CallError{"malformed reply: describe answered without actions"}
	}
	return hello, actions.Actions, nil
}

// Call runs an action and returns its message and data. Canceling ctx closes the connection, which cancels the action
// in the app.
func (a App) Call(ctx context.Context, call wire.Call, onEvent EventFunc) (wire.MessageResult, error) {
	_, result, err := a.send(ctx, wire.EncodeCall(call), onEvent)
	if err != nil {
		return wire.MessageResult{}, err
	}
	message, ok := result.(wire.MessageResult)
	if !ok {
		return wire.MessageResult{}, &CallError{"malformed reply: the call answered without a message"}
	}
	return message, nil
}

// Start gets the app running the way mode asks, in one round trip. It says "starting" when the app was not running,
// as soon as pidof answers and before the slow part.
func (a App) Start(mode StartMode) error {
	// A stopped app may be missing, which would otherwise surface only as a timeout. The check runs first, so that
	// "starting" is not said for an app that is not there.
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
			return &AppError{strings.TrimSpace(strings.TrimPrefix(line, "Error:"))}
		}
	}
	return nil
}

// send gets the app running, sends request on a new connection and returns the app's hello and the result frame,
// a MessageResult or an ActionsResult. The keep alive watchdog starts once the request outlasts keepAliveAfter.
func (a App) send(ctx context.Context, request []byte, onEvent EventFunc) (wire.Hello, wire.Frame, error) {
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
	last, err := c.exchange(request, onEvent)
	if ctx.Err() != nil {
		return wire.Hello{}, nil, &CallError{"canceled"}
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

// keepAlive starts the app's foreground service, which keeps the app from being frozen or killed until no call runs.
// Failing to start it only warns, since the call goes on and may finish before Android freezes the app.
func (a App) keepAlive() {
	out, err := a.device.Shell("am start-foreground-service -n " + a.pkg + "/" + keepAliveService)
	if err == nil && strings.Contains(out, "Error") {
		err = fmt.Errorf("%s", out)
	}
	if err != nil {
		fmt.Fprintf(a.status, "quack: could not keep %s running in the background: %v\n", a.pkg, err)
	}
}

// readyDevices is device when it is ready, or else every device adb reports ready. It fails when none is.
func readyDevices(device adb.Device) ([]adb.Device, error) {
	if device != "" {
		if state, err := device.State(); err != nil || state != adb.Ready {
			return nil, device.Unreachable(fmt.Errorf("it is %s", state))
		}
		return []adb.Device{device}, nil
	}
	listings, err := adb.Devices()
	if err != nil {
		return nil, err
	}
	var devices []adb.Device
	var waiting []string
	for _, l := range listings {
		if l.State == adb.Ready {
			devices = append(devices, l.Device)
		} else {
			waiting = append(waiting, fmt.Sprintf("%s is %s", l.Device, l.State))
		}
	}
	switch {
	case len(devices) > 0:
		return devices, nil
	case len(listings) == 1:
		return nil, listings[0].Device.Unreachable(errors.New(listings[0].State))
	case len(listings) > 1:
		return nil, &adb.Error{Message: "no device is ready: " + strings.Join(waiting, ", ")}
	default:
		return nil, &adb.Error{Message: "no device is connected"}
	}
}

// ambiguity says that several targets are left, with the flags that pick each, ready to paste.
func ambiguity(targets []Target) string {
	devices := map[adb.Device]bool{}
	var flags []string
	for _, t := range targets {
		devices[t.Device] = true
		flags = append(flags, fmt.Sprintf("--device %s --app %s", t.Device, t.Package))
	}
	where := fmt.Sprintf("%d devices", len(devices))
	if len(devices) == 1 {
		where = string(targets[0].Device)
	}
	return fmt.Sprintf("several quack packages on %s, pick one:\n  %s", where, strings.Join(flags, "\n  "))
}

// wakeAction is the broadcast that starts a quack-enabled app. Its receiver also identifies those apps.
const wakeAction = "io.mehow.quack.WAKE"

// startCommands maps each StartMode to its shell command. %[1]s is the package.
var startCommands = map[StartMode]string{
	// The broadcast starts a stopped app and thaws one that Android froze in the background.
	Wake: "am broadcast -a " + wakeAction + " -p %[1]s --include-stopped-packages",
	// Launcher activities usually lack CATEGORY_DEFAULT, so am start cannot resolve them from the package alone.
	// An app can have several launcher activities. resolve-activity answers with the app chooser then, so take the
	// first one query-activities lists, as monkey does.
	Open: `c=$(cmd package query-activities --brief -a android.intent.action.MAIN -c android.intent.category.LAUNCHER %[1]s | grep -m 1 / | tr -d ' '); ` +
		`case $c in */*) am start -W -n "$c" ;; *) echo "Error: %[1]s has no launcher activity" ;; esac`,
}

// keepAliveService keeps a quack-enabled app running while a long call runs. Android freezes an app in the background
// about 10 s after quack wakes it, and the app may not start a foreground service itself from there, so quack starts
// it once a call outlasts keepAliveAfter.
const keepAliveService = "io.mehow.quack.android.QuackService"

const keepAliveAfter = 3 * time.Second
