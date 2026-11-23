package session

import (
	"context"
	"io"
	"time"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/device"
	"quack/internal/step"
	"quack/internal/wire"
)

type App interface {
	step.App
	Describe(ctx context.Context) (wire.Hello, []wire.Action, error)
}

type Backend interface {
	// send gets the devices as they are, then again on each change. Returns when ctx ends, or with an error when the
	// devices can no longer be tracked.
	Track(ctx context.Context, send func([]adb.Listing)) error
	// serial and pkg may be empty to match any.
	Devices(serial adb.Device, pkg string) ([]device.Info, error)
	Inspect(listing adb.Listing) (device.Info, error)
	Packages(serial adb.Device) ([]string, error)
	Find(serial adb.Device, pkg string) (device.Target, error)
	// Each request of the App starts the app first: in the background, or on its main screen with openApp.
	Connect(target device.Target, openApp bool) (App, error)
}

// ADB is the Backend over adb. Status receives "starting" and keep-alive warnings.
type ADB struct {
	StartTimeout time.Duration
	Status       io.Writer
}

func (ADB) Track(ctx context.Context, send func([]adb.Listing)) error { return adb.Track(ctx, send) }

func (ADB) Devices(serial adb.Device, pkg string) ([]device.Info, error) {
	return device.List(serial, pkg)
}

func (ADB) Inspect(listing adb.Listing) (device.Info, error) { return device.Inspect(listing) }

func (ADB) Packages(serial adb.Device) ([]string, error) { return device.Packages(serial) }

func (ADB) Find(serial adb.Device, pkg string) (device.Target, error) {
	return device.Find(serial, pkg)
}

func (b ADB) Connect(target device.Target, openApp bool) (App, error) {
	mode := client.Wake
	if openApp {
		mode = client.OpenApp
	}
	app, err := client.Resolve(client.Options{Target: target, Mode: mode, StartTimeout: b.StartTimeout, Status: b.Status})
	if err != nil {
		return nil, err
	}
	return app, nil
}
