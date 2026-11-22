package client

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"quack/internal/adb"
	"quack/internal/wire"
)

type StartMode string

const (
	// Wake also thaws an app that Android froze in the background.
	Wake    StartMode = "wake"
	OpenApp StartMode = "open-app"
)

type AppError struct {
	Message string
}

func (e *AppError) Error() string { return e.Message }

type CallError struct {
	Message string
}

func (e *CallError) Error() string { return e.Message }

type NotificationFunc func(message string)

// The package goes into shell commands, so it must be a valid package name.
type Target struct {
	Device  adb.Device
	Package string
}

// Model and Packages are empty unless the device is ready.
type Device struct {
	adb.Listing
	Model    string
	Packages []string
}

type Options struct {
	Target       Target
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

// device and pkg may be empty to match any. When several targets match, the error lists the flags that pick each.
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

func IsPackageName(pkg string) bool { return packagePattern.MatchString(pkg) }

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

// A device that is not ready is only its listing.
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

// Sorted by id, whatever order the app declares them in.
func (a App) Describe(ctx context.Context) (wire.Hello, []wire.Action, error) {
	hello, result, err := a.send(ctx, wire.EncodeDescribe(), func(string) {})
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

func (a App) Target() Target { return Target{a.device, a.pkg} }

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
			return &AppError{strings.TrimSpace(strings.TrimPrefix(line, "Error:"))}
		}
	}
	return nil
}

// The keep-alive watchdog starts once the request outlasts keepAliveAfter.
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

// Failing to start the service only warns, since the call may finish before Android freezes the app.
func (a App) keepAlive() {
	out, err := a.device.Shell("am start-foreground-service -n " + a.pkg + "/" + keepAliveService)
	if err == nil && strings.Contains(out, "Error") {
		err = fmt.Errorf("%s", out)
	}
	if err != nil {
		fmt.Fprintf(a.status, "quack: could not keep %s running in the background: %v\n", a.pkg, err)
	}
}

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
	return fmt.Sprintf("several quack-enabled apps on %s, pick one:\n  %s", where, strings.Join(flags, "\n  "))
}

var packagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

// Its receiver is also how quack recognizes a quack-enabled app.
const wakeAction = "io.mehow.quack.WAKE"

var startCommands = map[StartMode]string{
	// The broadcast starts a stopped app and thaws one that Android froze in the background.
	Wake: "am broadcast -a " + wakeAction + " -p %[1]s --include-stopped-packages",
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
