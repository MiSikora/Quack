package cli

import (
	"fmt"
	"io"

	"quack/internal/adb"
	"quack/internal/device"
)

type deviceJSON struct {
	Serial string    `json:"serial"`
	Model  string    `json:"model"`
	State  string    `json:"state"`
	Apps   []appJSON `json:"apps"`
}

type appJSON struct {
	Package string `json:"package"`
}

func printDevices(w io.Writer, serial adb.Device, pkg string, asJSON bool) error {
	devices, err := device.List(serial, pkg)
	if err != nil {
		return err
	}
	if asJSON {
		for _, d := range devices {
			apps := []appJSON{}
			for _, p := range d.Packages {
				apps = append(apps, appJSON{p})
			}
			printJSON(w, deviceJSON{string(d.Device), d.Model, d.State, apps})
		}
		return nil
	}
	serials, models := 0, 0
	for _, d := range devices {
		serials = max(serials, len(d.Device))
		models = max(models, len(d.Model))
	}
	for _, d := range devices {
		fmt.Fprintf(w, "%-*s   %-*s   %s\n", serials, d.Device, models, d.Model, d.State)
		for _, p := range d.Packages {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
	return nil
}
