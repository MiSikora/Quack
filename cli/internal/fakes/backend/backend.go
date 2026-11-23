// Package backend holds the fake session.Backend. It is apart from package fakes, whose App the tests of step and
// recipe use, because session imports those packages and Go allows no import cycle through a package's own tests.
package backend

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/device"
	"quack/internal/fakes"
	"quack/internal/session"
)

// Backend lists the devices that Set set last, and finds the target of App. It connects to the app that Add added for
// a target, or else to App, and hands that app Status. Track sends the devices again each time Set changes them, and
// fails FailTracks times before that.
type Backend struct {
	App        *fakes.App
	Status     io.Writer
	FindErr    error
	FailTracks int

	mu           sync.Mutex
	devices      []device.Info
	apps         map[device.Target]*fakes.App
	send         func([]adb.Listing)
	held         map[device.Target]chan struct{}
	connects     []string
	finds        []string
	inspects     int
	packageLists int
}

func New(app *fakes.App, devices ...device.Info) *Backend {
	return &Backend{App: app, devices: devices, apps: map[device.Target]*fakes.App{}, held: map[device.Target]chan struct{}{}}
}

func (b *Backend) Track(ctx context.Context, send func([]adb.Listing)) error {
	b.mu.Lock()
	if b.FailTracks > 0 {
		b.FailTracks--
		b.mu.Unlock()
		return &adb.Error{Message: "lost track of the devices: EOF"}
	}
	b.send = send
	listings := b.listings()
	b.mu.Unlock()
	send(listings)
	<-ctx.Done()
	return nil
}

func (b *Backend) Devices(adb.Device, string) ([]device.Info, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.devices), nil
}

func (b *Backend) Inspect(listing adb.Listing) (device.Info, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inspects++
	for _, d := range b.devices {
		if d.Device == listing.Device {
			d.Listing = listing
			return d, nil
		}
	}
	return device.Info{}, errors.New(string(listing.Device) + " is not connected")
}

func (b *Backend) Packages(serial adb.Device) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.packageLists++
	for _, d := range b.devices {
		if d.Device == serial {
			return d.Packages, nil
		}
	}
	return nil, errors.New(string(serial) + " is not connected")
}

func (b *Backend) Find(serial adb.Device, pkg string) (device.Target, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.finds = append(b.finds, string(serial)+" "+pkg)
	if b.FindErr != nil {
		return device.Target{}, b.FindErr
	}
	return b.App.Target(), nil
}

// A connect that Hold holds waits for the release.
func (b *Backend) Connect(target device.Target, openApp bool) (session.App, error) {
	mode := client.Wake
	if openApp {
		mode = client.OpenApp
	}
	b.mu.Lock()
	held := b.held[target]
	b.connects = append(b.connects, string(target.Device)+" "+target.Package+" "+string(mode))
	b.mu.Unlock()
	if held != nil {
		<-held
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	app, ok := b.apps[target]
	if !ok {
		app = b.App
	}
	app.Status = b.Status
	return app, nil
}

func (b *Backend) Set(devices ...device.Info) {
	b.mu.Lock()
	b.devices = devices
	send, listings := b.send, b.listings()
	b.mu.Unlock()
	if send != nil {
		send(listings)
	}
}

// Installing an app changes nothing that adb lists.
func (b *Backend) Install(serial adb.Device, pkgs ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, d := range b.devices {
		if d.Device == serial {
			b.devices[i].Packages = pkgs
		}
	}
}

func (b *Backend) Add(target device.Target, app *fakes.App) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.apps[target] = app
}

func (b *Backend) Hold(target device.Target) (release func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	held := make(chan struct{})
	b.held[target] = held
	return sync.OnceFunc(func() { close(held) })
}

// Each connect as its device, package and start mode.
func (b *Backend) Connects() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.connects)
}

// Each find as its serial and package.
func (b *Backend) Finds() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.finds)
}

func (b *Backend) Inspects() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inspects
}

func (b *Backend) PackageLists() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.packageLists
}

func (b *Backend) listings() []adb.Listing {
	var listings []adb.Listing
	for _, d := range b.devices {
		listings = append(listings, d.Listing)
	}
	return listings
}
