package device

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"quack/internal/adb"
)

// The package goes into shell commands, so it must be a valid package name.
type Target struct {
	Device  adb.Device
	Package string
}

// Model and Packages are empty unless the device is ready.
type Info struct {
	adb.Listing
	Model    string
	Packages []string
}

type AppError struct {
	Message string
}

func (e *AppError) Error() string { return e.Message }

// serial and pkg may be empty to match any.
func List(serial adb.Device, pkg string) ([]Info, error) {
	listings, err := adb.Devices()
	if err != nil {
		return nil, err
	}
	var devices []Info
	for _, l := range listings {
		if serial != "" && l.Device != serial {
			continue
		}
		d, err := Inspect(l)
		if err != nil {
			return nil, err
		}
		if pkg != "" && !slices.Contains(d.Packages, pkg) {
			continue
		}
		devices = append(devices, d)
	}
	return devices, nil
}

// device and pkg may be empty to match any. When several targets match, the error lists the flags that pick each.
func Find(device adb.Device, pkg string) (Target, error) {
	devices, err := readyDevices(device)
	if err != nil {
		return Target{}, err
	}
	if pkg != "" && len(devices) == 1 {
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
	out, err := device.Shell("cmd package query-receivers --components -a " + WakeAction)
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
func Inspect(listing adb.Listing) (Info, error) {
	device := Info{Listing: listing}
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

// The quack runtime in the app registers a receiver for this broadcast, which also lists the quack-enabled apps.
const WakeAction = "io.mehow.quack.WAKE"
