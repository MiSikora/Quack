package tui

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"quack/internal/device"
)

func TestOneDeviceWithOneAppGoesStraightToTheActions(t *testing.T) {
	source := newFakeSource(newFakeApp())

	h := startPicking(t, source, device.Target{}, false)

	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "6/6", "type or ↑ to find an action")
	h.waitForLog("switched to io.mehow.quack.sample on Pixel 8 · emulator-5554")
	if got, want := source.Connects(), []string{"emulator-5554 io.mehow.quack.sample wake"}; !reflect.DeepEqual(got, want) {
		t.Errorf("started %q, want %q", got, want)
	}
}

func TestOneDeviceWithTwoAppsStartsAtThePackageStepWithoutBack(t *testing.T) {
	source := newFakeSource(newFakeApp(), deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))

	h := startPicking(t, source, device.Target{}, false)

	h.waitFor("  com.duckduckgo.mobile", "▸ io.mehow.quack.sample", "> pick an app", "2/2",
		"enter pick · ↑↓ select · esc close · ctrl+c quit")
	if lines := h.panel(); slices.ContainsFunc(lines, isTitle) {
		t.Errorf("the panel is %q, want the title hidden under the list", lines)
	}
}

func TestEscClosesThePickerOfTheStartAndPickDeviceShowsItAgain(t *testing.T) {
	source := newFakeSource(newFakeApp(), deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("> pick an app", "2/2")

	h.press("esc")

	h.waitFor("> / for commands", "/ for commands · ctrl+c quit")
	h.typing("/toggle-data")
	h.press("enter")
	h.waitForLog("showing the JSON data of notifications and results")
	h.typing("/pick-device")
	h.press("enter")
	h.waitFor("▸ io.mehow.quack.sample", "> pick an app", "esc close")
}

func TestTwoDevicesStartAtTheDeviceStepAndAnAppAloneOnItStarts(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		deviceInfo("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))
	appFor(source, "3A251FDH2000XY", "com.duckduckgo.mobile")

	h := startPicking(t, source, device.Target{}, false)

	h.waitFor("  Pixel 7 · 3A251FDH2000XY   2 apps", "▸ Pixel 8 · emulator-5554    1 app", "> pick a device")
	h.press("up", "enter")
	h.waitFor("▸ io.mehow.quack.sample", "> pick an app", "esc back")
	h.press("up", "enter")
	h.waitFor("quack · com.duckduckgo.mobile 1.0 · 3A251FDH2000XY", "type or ↑ to find an action")
	h.waitForLog("switched to com.duckduckgo.mobile on Pixel 7 · 3A251FDH2000XY")
}

func TestEscOnThePackageStepGoesBackToTheDevices(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		deviceInfo("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("Pixel 7", "> pick a device")

	h.press("up", "enter")
	h.waitFor("> pick an app")
	h.press("esc")

	h.waitFor("Pixel 7", "Pixel 8", "> pick a device")
	h.waitForRunning()
	h.waitFor("> pick a device")
}

func TestTheTextFiltersDevicesOnTheModelAndTheSerial(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		deviceInfo("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("Pixel 7", "> pick a device")

	h.typing("3A25")
	h.waitFor("▸ Pixel 7 · 3A251FDH2000XY", "1/2")
	h.press("esc")
	h.typing("pixel 8")
	h.waitFor("▸ Pixel 8 · emulator-5554", "1/2")
}

func TestSerialFlagLeavesOneDeviceAndSkipsItsStep(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		deviceInfo("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))

	h := startPicking(t, source, device.Target{Device: "3A251FDH2000XY"}, false)

	h.waitFor("com.duckduckgo.mobile", "▸ io.mehow.quack.sample", "> pick an app", "2/2")
}

func TestPackageFlagLeavesTheDevicesThatHaveItAndSkipsBothSteps(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		deviceInfo("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))
	appFor(source, "3A251FDH2000XY", "com.duckduckgo.mobile")

	h := startPicking(t, source, device.Target{Package: "com.duckduckgo.mobile"}, false)

	h.waitFor("quack · com.duckduckgo.mobile 1.0 · 3A251FDH2000XY", "type or ↑ to find an action")
}

func TestFlagsThatMatchNothingSayWhatQuackWaitsFor(t *testing.T) {
	source := newFakeSource(newFakeApp())

	h := startPicking(t, source, device.Target{Device: "3A251FDH2000XY", Package: "com.duckduckgo.mobile"}, false)

	h.waitFor("quack · waiting for com.duckduckgo.mobile on 3A251FDH2000XY", "> / for commands", "/ for commands · ctrl+c quit")
}

func TestNoAppLeavesTheCommandsThatNeedNone(t *testing.T) {
	source := newFakeSource(newFakeApp())
	source.Set()
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("quack · no device · waiting for one to connect", "> / for commands")
	h.checkRed(" · ", "no device", " · waiting for one to connect")

	h.typing("/")

	h.waitFor("/toggle-data", "/pick-device", "/exit", "/clear", "/cancel", "5/5")
	h.waitForNo("/open-app")
	h.typing("toggle-data")
	h.press("enter")
	h.waitForLog("showing the JSON data of notifications and results")
}

func TestNoAppSaysWhyInTheInfoLine(t *testing.T) {
	for _, tc := range []struct {
		devices []device.Info
		want    string
		red     string
	}{
		{nil, "quack · no device · waiting for one to connect", "no device"},
		{[]device.Info{deviceInfo("emulator-5554", "Pixel 8", "device")}, "quack · Pixel 8 · emulator-5554 · no quack-enabled app", "no quack-enabled app"},
		{[]device.Info{deviceInfo("R5CT20ABCDE", "", "unauthorized")}, "quack · R5CT20ABCDE · unauthorized · accept the USB debugging prompt", ""},
		{
			[]device.Info{deviceInfo("emulator-5554", "Pixel 8", "device"), deviceInfo("R5CT20ABCDE", "", "unauthorized")},
			"quack · 2 devices · no quack-enabled app · /pick-device to see them",
			"no quack-enabled app",
		},
	} {
		t.Run(tc.want, func(t *testing.T) {
			source := newFakeSource(newFakeApp())
			source.Set(tc.devices...)

			h := startPicking(t, source, device.Target{}, false)

			h.waitFor(tc.want, "> / for commands")
			before, after, _ := strings.Cut(strings.TrimPrefix(tc.want, "quack"), tc.red)
			if tc.red == "" {
				before, after = "", strings.TrimPrefix(tc.want, "quack")
			}
			h.checkRed(before, tc.red, after)
		})
	}
}

func TestFlagsThatMatchNoDeviceYetAreNotRed(t *testing.T) {
	source := newFakeSource(newFakeApp(), deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"))

	h := startPicking(t, source, device.Target{Device: "3A251FDH2000XY"}, false)

	h.waitFor("quack · waiting for 3A251FDH2000XY", "> / for commands")
	if !strings.Contains(h.raw(), statusStyle.Render(" · waiting for 3A251FDH2000XY")) {
		t.Errorf("the status is not dimmed in:\n%q", h.raw())
	}
}

func TestPickDeviceWithoutAnAppShowsTheDevicesAndEscHidesThem(t *testing.T) {
	source := newFakeSource(newFakeApp(), deviceInfo("emulator-5554", "Pixel 8", "device"), deviceInfo("R5CT20ABCDE", "", "unauthorized"))
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("quack · 2 devices")

	h.typing("/pick-device")
	h.press("enter")
	h.waitFor("  Pixel 8 · emulator-5554   no quack-enabled app", "> pick a device", "esc close")
	h.press("esc")

	h.waitFor("quack · 2 devices", "> / for commands", "/ for commands · ctrl+c quit")
}

func TestDevicesThatCannotBePickedShowDimmedAndAreSkipped(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		deviceInfo("R5CT20ABCDE", "", "unauthorized"),
		deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		deviceInfo("emulator-5556", "Pixel 9", "device"),
		deviceInfo("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"),
		deviceInfo("emulator-5558", "", "offline"))
	h := startPicking(t, source, device.Target{}, false)

	h.waitFor(
		"  emulator-5558              offline · wait for adb to see it again, or reconnect it",
		"  Pixel 7 · 3A251FDH2000XY   2 apps",
		"  Pixel 9 · emulator-5556    no quack-enabled app",
		"▸ Pixel 8 · emulator-5554    1 app",
		"  R5CT20ABCDE                unauthorized · accept the USB debugging prompt",
	)
	if !strings.Contains(h.raw(), statusStyle.Render("  R5CT20ABCDE                unauthorized · accept the USB debugging prompt")) {
		t.Errorf("the unauthorized row is not dimmed in:\n%q", h.raw())
	}
	h.press("up")
	h.waitFor("▸ Pixel 7 · 3A251FDH2000XY")
	h.press("up", "up")
	h.waitFor("▸ Pixel 7 · 3A251FDH2000XY")
	h.press("down", "down", "down")
	h.waitFor("▸ Pixel 8 · emulator-5554")

	h.typing("R5CT")
	h.waitFor("  R5CT20ABCDE", "1/5")
	h.press("enter")
	h.waitForRunning()
	h.waitFor("R5CT20ABCDE", "> R5CT")
	if got := source.Connects(); len(got) > 0 {
		t.Errorf("started %q, want nothing", got)
	}
}

func TestWaitingPickerFillsInWhenADeviceConnects(t *testing.T) {
	source := newFakeSource(newFakeApp())
	source.Set()
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("quack · no device · waiting for one to connect", "> / for commands")

	source.Set(deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"))

	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
}

func TestWaitingPickerFillsInWhenAnAppInstalls(t *testing.T) {
	source := newFakeSource(newFakeApp(), deviceInfo("emulator-5554", "Pixel 8", "device"))
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("quack · Pixel 8 · emulator-5554 · no quack-enabled app", "> / for commands")
	h.keepPolling()
	// Several polls go by first, so that the picker keeps polling rather than polling once.
	h.waitForInspects(source, source.Inspects()+3)

	source.Install("emulator-5554", "io.mehow.quack.sample")

	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
}

func TestFailedDescribeStaysOnThePackageStepWithTheErrorInTheLog(t *testing.T) {
	source := newFakeSource(newFakeApp(), deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))
	appFor(source, "emulator-5554", "com.duckduckgo.mobile").SetDescribeErr(gone)
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("com.duckduckgo.mobile", "> pick an app")

	h.press("up", "enter")

	h.waitForLog("com.duckduckgo.mobile: the app is gone")
	h.waitFor("▸ com.duckduckgo.mobile", "> pick an app", "enter pick")
	h.press("down", "enter")
	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
}

func TestFailedStartOfTheOnlyAppIsNotRetriedByItself(t *testing.T) {
	source := newFakeSource(newFakeApp())
	source.App.SetDescribeErr(gone)
	h := startPicking(t, source, device.Target{}, false)

	h.waitForLog("io.mehow.quack.sample: the app is gone")
	h.waitFor("▸ io.mehow.quack.sample", "> pick an app", "enter pick")
	h.keepPolling()
	h.waitForInspects(source, source.Inspects()+3)
	if got := source.Connects(); len(got) != 1 {
		t.Errorf("started %q, want one start", got)
	}
}

func TestDeviceCommandSwitchesToAnotherDevice(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		deviceInfo("3A251FDH2000XY", "Pixel 7", "device", "com.duckduckgo.mobile"))
	appFor(source, "3A251FDH2000XY", "com.duckduckgo.mobile")
	h := startPicking(t, source, device.Target{Device: "emulator-5554"}, false)
	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")

	h.typing("/pick-device")
	h.press("enter")
	h.waitFor("Pixel 7", "Pixel 8", "> pick a device", "esc close")
	h.press("up", "enter")

	h.waitFor("quack · com.duckduckgo.mobile 1.0 · 3A251FDH2000XY", "type or ↑ to find an action")
	h.waitForLog("switched to io.mehow.quack.sample on Pixel 8 · emulator-5554", "switched to com.duckduckgo.mobile on Pixel 7 · 3A251FDH2000XY")
}

func TestEscClosesThePickerOfACommand(t *testing.T) {
	h := start(t, newFakeApp())

	h.typing("/pick-device")
	h.press("enter")
	h.waitFor("> pick a device", "esc close")
	h.press("esc")

	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "> find an action", "type or ↑ to find an action")
}

func TestPackageCommandShowsTheAppsOfTheDevice(t *testing.T) {
	source := newFakeSource(newFakeApp(), deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"))
	appFor(source, "emulator-5554", "com.duckduckgo.mobile")
	h := startPicking(t, source, device.Target{}, true)
	h.waitFor("type or ↑ to find an action")
	source.Install("emulator-5554", "io.mehow.quack.sample", "com.duckduckgo.mobile")

	h.typing("/pick-app")
	h.press("enter")
	h.waitFor("com.duckduckgo.mobile", "> pick an app", "2/2", "esc close")
	h.press("up", "enter")

	h.waitFor("quack · com.duckduckgo.mobile 1.0 · emulator-5554", "type or ↑ to find an action")
	want := []string{"emulator-5554 io.mehow.quack.sample open-app", "emulator-5554 com.duckduckgo.mobile wake"}
	if got := source.Connects(); !reflect.DeepEqual(got, want) {
		t.Errorf("started %q, want the flags' mode only for the first pick: %q", got, want)
	}
}

func TestDeviceAndPackageAreRefusedDuringARun(t *testing.T) {
	h := start(t, newFakeApp())
	h.typing("account.watch")
	h.press("enter")
	h.waitFor("account.watch · ctrl+c cancel action")

	for _, command := range []string{"/pick-device", "/pick-app"} {
		h.typing(command)
		h.press("enter")
		h.waitFor("cancel the running action first (ctrl+c)")
		h.waitFor("> find an action")
		h.press("esc")
	}
}

func TestDisconnectWaitsForPickDevice(t *testing.T) {
	app := newFakeApp()
	source := newFakeSource(app)
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("type or ↑ to find an action")
	h.typing("account.watch")
	h.press("enter")
	h.waitFor("account.watch · ctrl+c cancel action")

	source.Set()

	h.waitForLog("emulator-5554 disconnected")
	h.waitFor("quack · no device", "> / for commands", "0/0")
	select {
	case <-app.canceled(0):
	case <-time.After(timeout):
		t.Error("the run was not stopped")
	}
	source.Set(deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"))
	h.waitFor("quack · Pixel 8 · emulator-5554 · 1 app · /pick-device to connect", "> / for commands")
	if !strings.Contains(h.raw(), statusStyle.Render(" · Pixel 8 · emulator-5554 · 1 app · /pick-device to connect")) {
		t.Errorf("a device that is there is not dimmed in:\n%q", h.raw())
	}
	if got := source.Connects(); len(got) != 1 {
		t.Errorf("started %q, want only the first pick", got)
	}

	h.typing("/pick-device")
	h.press("enter")
	h.waitFor("▸ Pixel 8 · emulator-5554   1 app", "> pick a device", "esc close")
	h.press("enter")
	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
}

func TestConnectedAppIsPolledOnlyForItsDevicesApps(t *testing.T) {
	source := newFakeSource(newFakeApp())
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("type or ↑ to find an action")
	inspects := source.Inspects()
	h.keepPolling()

	lists := func() string { return strconv.Itoa(source.PackageLists()) }
	h.wait(lists, func(n string) bool { got, _ := strconv.Atoi(n); return got >= 2 }, "2 package lists")

	if got := source.Inspects(); got != inspects {
		t.Errorf("got %d inspects while connected, want %d", got, inspects)
	}
}

func TestUninstalledAppGoesAsIfItsDeviceWent(t *testing.T) {
	app := newFakeApp()
	source := newFakeSource(app)
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("type or ↑ to find an action")
	h.typing("account.watch")
	h.press("enter")
	h.waitFor("account.watch · ctrl+c cancel action")
	h.keepPolling()

	source.Install("emulator-5554")

	h.waitForLog("io.mehow.quack.sample left emulator-5554")
	h.waitFor("quack · Pixel 8 · emulator-5554 · no quack-enabled app", "> / for commands")
	h.checkRed(" · Pixel 8 · emulator-5554 · ", "no quack-enabled app", "")
	select {
	case <-app.canceled(0):
	case <-time.After(timeout):
		t.Error("the run was not stopped")
	}
	h.typing("/")
	h.waitFor("/pick-device", "5/5")

	h.press("esc")
	source.Install("emulator-5554", "io.mehow.quack.sample")
	h.waitFor("quack · Pixel 8 · emulator-5554 · 1 app · /pick-device to connect")
	if got := source.Connects(); len(got) != 1 {
		t.Errorf("started %q, want only the first pick", got)
	}
}

func TestDisconnectSaysWhichDevicesCanBePicked(t *testing.T) {
	source := newFakeSource(newFakeApp())
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("type or ↑ to find an action")

	source.Set(deviceInfo("emulator-5556", "Pixel 9", "device", "io.mehow.quack.sample"), deviceInfo("3A251FDH2000XY", "Pixel 7", "device"))

	h.waitFor("quack · 2 devices · /pick-device to pick one", "> / for commands")
}

func TestOtherDevicesComingAndGoingKeepTheActions(t *testing.T) {
	source := newFakeSource(newFakeApp())
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("type or ↑ to find an action")

	source.Set(deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"), deviceInfo("3A251FDH2000XY", "Pixel 7", "device"))
	source.Set(deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"))

	h.waitForRunning()
	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
	if got := source.Connects(); len(got) != 1 {
		t.Errorf("started %q, want only the first pick", got)
	}
}

func TestTheOnlyDeviceThatCanBePickedIsTakenByItself(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		deviceInfo("R5CT20ABCDE", "", "unauthorized"),
		deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		deviceInfo("emulator-5556", "Pixel 9", "device"))

	h := startPicking(t, source, device.Target{}, false)

	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
}

func TestAStartThePickerLeftBehindDoesNotTakeOver(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"),
		deviceInfo("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.other"))
	appFor(source, "emulator-5554", "com.duckduckgo.mobile")
	appFor(source, "3A251FDH2000XY", "io.mehow.other")
	h := startPicking(t, source, device.Target{Device: "emulator-5554"}, false)
	h.waitFor("> pick an app")
	h.press("enter")
	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
	releaseLeft := source.Hold(device.Target{Device: "emulator-5554", Package: "com.duckduckgo.mobile"})
	releaseNew := source.Hold(device.Target{Device: "3A251FDH2000XY", Package: "io.mehow.other"})
	t.Cleanup(releaseLeft)
	t.Cleanup(releaseNew)

	h.typing("/pick-app")
	h.press("enter")
	h.waitFor("> pick an app")
	h.press("up", "enter")
	h.waitFor("starting com.duckduckgo.mobile on emulator-5554…")
	source.Set(deviceInfo("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.other"))
	h.waitFor("quack · Pixel 7 · 3A251FDH2000XY · 1 app · /pick-device to connect")
	h.typing("/pick-device")
	h.press("enter")
	h.waitFor("> pick a device")
	h.press("enter")
	h.waitFor("starting io.mehow.other on 3A251FDH2000XY…")

	// The start of the device that went away answers first, while the new one still starts.
	releaseLeft()
	h.waitForRunning()
	h.waitFor("starting io.mehow.other on 3A251FDH2000XY…")
	releaseNew()
	h.waitFor("quack · io.mehow.other 1.0 · 3A251FDH2000XY", "type or ↑ to find an action")
}

func TestPanelKeepsItsHeightInEveryPickerState(t *testing.T) {
	source := newFakeSource(newFakeApp())
	source.Set()
	appFor(source, "3A251FDH2000XY", "com.duckduckgo.mobile").SetDescribeErr(gone)
	h := startPicking(t, source, device.Target{}, false)
	check := func(state string) {
		t.Helper()
		if got := h.panelRows(); got != 6 {
			t.Errorf("%s: the panel takes the last %d rows, want 6:\n%s", state, got, h.view())
		}
		if got := h.lines(); got != 30 {
			t.Errorf("%s: the view has %d rows, want the window's 30", state, got)
		}
	}

	h.waitFor("no device · waiting")
	h.resetHeights()
	check("waiting for a device")
	devices := []device.Info{
		deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		deviceInfo("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"),
	}
	for i := range 6 {
		devices = append(devices, deviceInfo("emulator-56"+string(rune('0'+i))+"0", "", "offline"))
	}
	source.Set(devices...)
	h.waitFor("> pick a device", "8/8")
	check("more devices than fit")
	h.typing("zz")
	h.waitFor("no match")
	check("no match")
	h.press("esc")
	h.typing("3A25")
	h.press("enter")
	h.waitFor("> pick an app")
	check("package step")
	h.press("up", "enter")
	h.waitForLog("the app is gone")
	check("failed start")
	h.press("ctrl+c")
	h.waitFor("press ctrl+c again to quit")
	check("quit prompt")
	h.advance(noticeTime)
	h.press("down", "enter")
	h.waitFor("type or ↑ to find an action")
	check("actions")

	if heights := h.heights(); !reflect.DeepEqual(heights, []int{6}) {
		t.Errorf("the panel had these heights: %v, want only 6", heights)
	}
}

func TestPackageFlagHidesTheDevicesWithoutIt(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		deviceInfo("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"),
		deviceInfo("emulator-5556", "Pixel 9", "device", "com.duckduckgo.mobile"))

	h := startPicking(t, source, device.Target{Package: "com.duckduckgo.mobile"}, false)

	h.waitFor("Pixel 9 · emulator-5556", "Pixel 7 · 3A251FDH2000XY", "> pick a device", "2/2")
	h.waitForNo("Pixel 8")
}

func TestPackageStepSaysWhenTheDeviceHasNoAppLeft(t *testing.T) {
	source := newFakeSource(newFakeApp(), deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("> pick an app", "2/2")

	source.Install("emulator-5554")
	h.keepPolling()

	h.waitFor("quack · no quack-enabled app on emulator-5554 · waiting for one", "> / for commands")
}

func TestPackageStepGoesBackToTheDevicesWhenItsDeviceLeaves(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		deviceInfo("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"),
		deviceInfo("emulator-5556", "Pixel 9", "device", "io.mehow.quack.sample"))
	h := startPicking(t, source, device.Target{}, false)
	h.waitFor("Pixel 7", "> pick a device")
	h.press("up", "enter")
	h.waitFor("com.duckduckgo.mobile", "> pick an app")

	source.Set(deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"), deviceInfo("emulator-5556", "Pixel 9", "device", "io.mehow.quack.sample"))

	h.waitFor("Pixel 9", "Pixel 8", "> pick a device", "2/2")
}

func TestDeviceListScrollsToTheSelectionAndNewTextSelectsTheBestMatch(t *testing.T) {
	var devices []device.Info
	for i := range 7 {
		devices = append(devices, deviceInfo("emulator-555"+strconv.Itoa(i), "Pixel "+strconv.Itoa(i), "device", "io.mehow.quack.sample"))
	}
	h := startPicking(t, newFakeSource(newFakeApp(), devices...), device.Target{}, false)
	h.waitFor("▸ Pixel 0", "> pick a device", "7/7")
	// A list of 5 rows, so that the 7 devices scroll.
	h.resize(100, 10)
	h.waitForOverlayRows(5)

	h.press("up", "up", "up", "up", "up", "up")
	h.waitFor("▸ Pixel 6 · emulator-5556")
	h.waitForNo("Pixel 0")
	h.typing("Pixel")
	h.waitFor("▸ Pixel 0", "> Pixel")
}

func TestLostTrackOfTheDevicesSaysSoOnceAndTracksAgain(t *testing.T) {
	source := newFakeSource(newFakeApp())
	source.FailTracks = 3

	h := startPicking(t, source, device.Target{}, false)
	h.keepPolling()

	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
	if log := h.logText(); strings.Count(log, "lost track of the devices: EOF") != 1 {
		t.Errorf("the log says the lost track %d times, want once:\n%s", strings.Count(log, "lost track of the devices: EOF"), log)
	}
}

// checkRed checks that the info line shows red between dimmed before and after, or all dimmed without red.
func (h *harness) checkRed(before, red, after string) {
	h.t.Helper()
	var want string
	for _, part := range []struct {
		text string
		red  bool
	}{{before, false}, {red, true}, {after, false}} {
		switch {
		case part.text == "":
		case part.red:
			want += problemStyle.Render(part.text)
		default:
			want += statusStyle.Render(part.text)
		}
	}
	if !strings.Contains(h.raw(), selectedStyle.Render("quack")+want) {
		h.t.Errorf("the info line is not %q, %q in red, then %q in:\n%q", before, red, after, h.raw())
	}
}

func TestDevicePickerInitiallySelectsCurrentAndKeepsMarkerWhenCursorMoves(t *testing.T) {
	source := newFakeSource(newFakeApp(), deviceInfo("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"), deviceInfo("second", "Pixel 7", "device", "io.mehow.other"))
	appFor(source, "second", "io.mehow.other")
	h := startPicking(t, source, device.Target{Device: "second", Package: "io.mehow.other"}, false)
	h.waitFor("quack · io.mehow.other 1.0 · second", "type or ↑")
	h.typing("/pick-device")
	h.press("enter")
	h.waitFor("▸ Pixel 7", "(current)")
	h.press("down")
	h.waitFor("  Pixel 7", "(current)", "▸ Pixel 8")
	h.press("enter")
	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑")
	h.typing("/pick-device")
	h.press("enter")
	h.waitFor("▸ Pixel 8", "(current)")
}

func TestPackagePickerSelectsCurrentAndDoesNotSearchItsMarker(t *testing.T) {
	source := newFakeSource(newFakeApp(), deviceInfo("emulator-5554", "Pixel 8", "device", "com.example.other", "io.mehow.quack.sample"))
	appFor(source, "emulator-5554", "com.example.other")
	h := startPicking(t, source, device.Target{Device: "emulator-5554", Package: "io.mehow.quack.sample"}, false)
	h.waitFor("type or ↑")
	h.typing("/pick-app")
	h.press("enter")
	h.waitFor("▸ io.mehow.quack.sample", "(current)")
	h.press("down")
	h.waitFor("  io.mehow.quack.sample", "(current)", "▸ com.example.other")
	h.typing("current")
	h.waitFor("no match", "0/2")
	if strings.Contains(strings.Join(h.panel(), "\n"), "(current)") {
		t.Fatal("current marker must not be a searchable label")
	}
}
