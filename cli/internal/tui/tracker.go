package tui

import (
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"quack/internal/adb"
	"quack/internal/device"
	"quack/internal/session"
)

type tracker struct {
	backend session.Backend
	devices []seenDevice
	listed  bool
	asked   int
	pending map[adb.Device]int
	polling bool
}

type listed struct {
	listings []adb.Listing
}

type (
	inspected struct {
		serial adb.Device
		device device.Info
		asked  int
		err    error
	}
	polled struct{}
)

func newTracker(backend session.Backend) tracker {
	return tracker{backend: backend, pending: map[adb.Device]int{}}
}

func (t *tracker) track(activity activity, clock clock) tea.Cmd {
	backend := t.backend
	return func() tea.Msg {
		failing := false
		for activity.ctx.Err() == nil {
			err := backend.Track(activity.ctx, func(listings []adb.Listing) {
				failing = false
				activity.send(listed{listings})
			})
			if err != nil && !failing {
				failing = true
				activity.send(logged{problemStyle.Render(err.Error())})
			}
			select {
			case <-activity.ctx.Done():
			case <-clock.After(pollTime):
			}
		}
		return nil
	}
}

func (t *tracker) list(listings []adb.Listing) []tea.Cmd {
	t.listed = true
	var devices []seenDevice
	var cmds []tea.Cmd
	for _, l := range listings {
		if i := t.index(l.Device); i >= 0 && t.devices[i].State == l.State {
			devices = append(devices, t.devices[i])
			continue
		}
		devices = append(devices, seenDevice{Info: device.Info{Listing: l}})
		cmds = append(cmds, t.inspect(l))
	}
	t.devices = devices
	for device := range t.pending {
		if t.index(device) < 0 {
			delete(t.pending, device)
		}
	}
	return cmds
}

func (t *tracker) inspect(listing adb.Listing) tea.Cmd {
	if listing.State != adb.Ready {
		return nil
	}
	backend := t.backend
	return t.ask(listing.Device, func() (device.Info, error) { return backend.Inspect(listing) })
}

func (t *tracker) ask(serial adb.Device, inspect func() (device.Info, error)) tea.Cmd {
	t.asked++
	asked := t.asked
	t.pending[serial] = asked
	return func() tea.Msg {
		device, err := inspect()
		return inspected{serial, device, asked, err}
	}
}

func (t *tracker) inspectAll() tea.Cmd {
	var cmds []tea.Cmd
	for _, d := range t.devices {
		if _, waiting := t.pending[d.Device]; !waiting {
			cmds = append(cmds, t.inspect(d.Listing))
		}
	}
	return tea.Batch(cmds...)
}

func (t *tracker) refresh(serial adb.Device) tea.Cmd {
	i := t.index(serial)
	if i < 0 {
		return nil
	}
	if _, waiting := t.pending[serial]; waiting {
		return nil
	}
	seen := t.devices[i]
	if !seen.inspected || seen.State != adb.Ready {
		return t.inspect(seen.Listing)
	}
	backend := t.backend
	return t.ask(seen.Device, func() (device.Info, error) {
		info := seen.Info
		packages, err := backend.Packages(info.Device)
		info.Packages = packages
		return info, err
	})
}

func (t *tracker) inspected(msg inspected) {
	if t.pending[msg.serial] != msg.asked {
		return
	}
	delete(t.pending, msg.serial)
	i := t.index(msg.serial)
	if msg.err != nil || i < 0 || t.devices[i].State != msg.device.State {
		return
	}
	t.devices[i] = seenDevice{Info: msg.device, inspected: true}
}

func (t *tracker) poll(c clock) tea.Cmd {
	if t.polling {
		return nil
	}
	t.polling = true
	return after(c, pollTime, polled{})
}

func (t *tracker) index(serial adb.Device) int {
	return slices.IndexFunc(t.devices, func(d seenDevice) bool { return d.Device == serial })
}

const pollTime = 5 * time.Second
