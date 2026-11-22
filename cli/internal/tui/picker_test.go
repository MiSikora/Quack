package tui

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"quack/internal/client"
)

func TestOneDeviceWithOneAppGoesStraightToTheActions(t *testing.T) {
	source := newFakeSource(newFakeApp())

	h := startPicking(t, source, client.Target{}, client.Wake)

	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "6/6", "type or ↑ to find an action")
	h.waitForLog("switched to io.mehow.quack.sample on Pixel 8 · emulator-5554")
	if got, want := source.starts(), []string{"emulator-5554 io.mehow.quack.sample wake"}; !reflect.DeepEqual(got, want) {
		t.Errorf("started %q, want %q", got, want)
	}
}

func TestOneDeviceWithTwoAppsStartsAtThePackageStepWithoutBack(t *testing.T) {
	source := newFakeSource(newFakeApp(), device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))

	h := startPicking(t, source, client.Target{}, client.Wake)

	h.waitFor("  com.duckduckgo.mobile", "▸ io.mehow.quack.sample", "> pick a package", "2/2",
		"enter pick · ↑↓ select · ctrl+c quit")
	if lines := h.panel(); slices.ContainsFunc(lines, isTitle) {
		t.Errorf("the panel is %q, want the title hidden under the list", lines)
	}
	h.press("esc")
	h.waitForRunning()
	h.waitFor("> pick a package")
}

func TestTwoDevicesStartAtTheDeviceStepAndAnAppAloneOnItStarts(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		device("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))
	source.appFor("3A251FDH2000XY", "com.duckduckgo.mobile")

	h := startPicking(t, source, client.Target{}, client.Wake)

	h.waitFor("  Pixel 7 · 3A251FDH2000XY   2 packages", "▸ Pixel 8 · emulator-5554    1 package", "> pick a device")
	h.press("up", "enter")
	h.waitFor("▸ io.mehow.quack.sample", "> pick a package", "esc back")
	h.press("up", "enter")
	h.waitFor("quack · com.duckduckgo.mobile 1.0 · 3A251FDH2000XY", "type or ↑ to find an action")
	h.waitForLog("switched to com.duckduckgo.mobile on Pixel 7 · 3A251FDH2000XY")
}

func TestEscOnThePackageStepGoesBackToTheDevices(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		device("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))
	h := startPicking(t, source, client.Target{}, client.Wake)
	h.waitFor("Pixel 7", "> pick a device")

	h.press("up", "enter")
	h.waitFor("> pick a package")
	h.press("esc")

	h.waitFor("Pixel 7", "Pixel 8", "> pick a device")
	h.waitForRunning()
	h.waitFor("> pick a device")
}

func TestTheTextFiltersDevicesOnTheModelAndTheSerial(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		device("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))
	h := startPicking(t, source, client.Target{}, client.Wake)
	h.waitFor("Pixel 7", "> pick a device")

	h.typing("3A25")
	h.waitFor("▸ Pixel 7 · 3A251FDH2000XY", "1/2")
	h.press("esc")
	h.typing("pixel 8")
	h.waitFor("▸ Pixel 8 · emulator-5554", "1/2")
}

func TestSerialFlagLeavesOneDeviceAndSkipsItsStep(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		device("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))

	h := startPicking(t, source, client.Target{Device: "3A251FDH2000XY"}, client.Wake)

	h.waitFor("com.duckduckgo.mobile", "▸ io.mehow.quack.sample", "> pick a package", "2/2")
}

func TestPackageFlagLeavesTheDevicesThatHaveItAndSkipsBothSteps(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		device("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))
	source.appFor("3A251FDH2000XY", "com.duckduckgo.mobile")

	h := startPicking(t, source, client.Target{Package: "com.duckduckgo.mobile"}, client.Wake)

	h.waitFor("quack · com.duckduckgo.mobile 1.0 · 3A251FDH2000XY", "type or ↑ to find an action")
}

func TestFlagsThatMatchNothingSayWhatThePickerWaitsFor(t *testing.T) {
	source := newFakeSource(newFakeApp())

	h := startPicking(t, source, client.Target{Device: "3A251FDH2000XY", Package: "com.duckduckgo.mobile"}, client.Wake)

	h.waitFor("  waiting for com.duckduckgo.mobile on 3A251FDH2000XY", "> pick a device", "0/0")
}

func TestDevicesThatCannotBePickedShowDimmedAndAreSkipped(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		device("R5CT20ABCDE", "", "unauthorized"),
		device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		device("emulator-5556", "Pixel 9", "device"),
		device("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"),
		device("emulator-5558", "", "offline"))
	h := startPicking(t, source, client.Target{}, client.Wake)

	h.waitFor(
		"  emulator-5558              offline · wait for adb to see it again, or reconnect it",
		"  Pixel 7 · 3A251FDH2000XY   2 packages",
		"  Pixel 9 · emulator-5556    no quack package",
		"▸ Pixel 8 · emulator-5554    1 package",
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
	if got := source.starts(); len(got) > 0 {
		t.Errorf("started %q, want nothing", got)
	}
}

func TestWaitingPickerFillsInWhenADeviceConnects(t *testing.T) {
	source := newFakeSource(newFakeApp())
	source.devices = nil
	h := startPicking(t, source, client.Target{}, client.Wake)
	h.waitFor("  no device · waiting for one to connect", "> pick a device", "enter pick")

	source.set(device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"))

	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
}

func TestWaitingPickerFillsInWhenAnAppInstalls(t *testing.T) {
	source := newFakeSource(newFakeApp(), device("emulator-5554", "Pixel 8", "device"))
	h := startPicking(t, source, client.Target{}, client.Wake, withPollTime(20*time.Millisecond))
	h.waitFor("Pixel 8 · emulator-5554   no quack package", "> pick a device")
	// Several polls go by first, so that the picker keeps polling rather than polling once.
	time.Sleep(150 * time.Millisecond)

	source.install("emulator-5554", "io.mehow.quack.sample")

	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
}

func TestFailedDescribeStaysOnThePackageStepWithTheErrorInTheLog(t *testing.T) {
	source := newFakeSource(newFakeApp(), device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))
	source.appFor("emulator-5554", "com.duckduckgo.mobile").setActions(nil)
	h := startPicking(t, source, client.Target{}, client.Wake)
	h.waitFor("com.duckduckgo.mobile", "> pick a package")

	h.press("up", "enter")

	h.waitForLog("com.duckduckgo.mobile: the app is gone")
	h.waitFor("▸ com.duckduckgo.mobile", "> pick a package", "enter pick")
	h.press("down", "enter")
	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
}

func TestFailedStartOfTheOnlyAppIsNotRetriedByItself(t *testing.T) {
	source := newFakeSource(newFakeApp())
	source.app.setActions(nil)
	h := startPicking(t, source, client.Target{}, client.Wake, withPollTime(20*time.Millisecond))

	h.waitForLog("io.mehow.quack.sample: the app is gone")
	h.waitFor("▸ io.mehow.quack.sample", "> pick a package", "enter pick")
	time.Sleep(100 * time.Millisecond)
	if got := source.starts(); len(got) != 1 {
		t.Errorf("started %q, want one start", got)
	}
}

func TestDeviceCommandSwitchesToAnotherDevice(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		device("3A251FDH2000XY", "Pixel 7", "device", "com.duckduckgo.mobile"))
	source.appFor("3A251FDH2000XY", "com.duckduckgo.mobile")
	h := startPicking(t, source, client.Target{Device: "emulator-5554"}, client.Wake)
	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")

	h.typing("/switch-device")
	h.press("enter")
	h.waitFor("Pixel 7", "Pixel 8", "> pick a device", "esc close")
	h.press("up", "enter")

	h.waitFor("quack · com.duckduckgo.mobile 1.0 · 3A251FDH2000XY", "type or ↑ to find an action")
	h.waitForLog("switched to io.mehow.quack.sample on Pixel 8 · emulator-5554", "switched to com.duckduckgo.mobile on Pixel 7 · 3A251FDH2000XY")
}

func TestEscClosesThePickerOfACommand(t *testing.T) {
	h := start(t, newFakeApp())

	h.typing("/switch-device")
	h.press("enter")
	h.waitFor("> pick a device", "esc close")
	h.press("esc")

	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "> find an action", "type or ↑ to find an action")
}

func TestPackageCommandShowsTheAppsOfTheDevice(t *testing.T) {
	source := newFakeSource(newFakeApp(), device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"))
	source.appFor("emulator-5554", "com.duckduckgo.mobile")
	h := startPicking(t, source, client.Target{}, client.Open)
	h.waitFor("type or ↑ to find an action")
	source.install("emulator-5554", "io.mehow.quack.sample", "com.duckduckgo.mobile")

	h.typing("/switch-app")
	h.press("enter")
	h.waitFor("com.duckduckgo.mobile", "> pick a package", "2/2", "esc close")
	h.press("up", "enter")

	h.waitFor("quack · com.duckduckgo.mobile 1.0 · emulator-5554", "type or ↑ to find an action")
	want := []string{"emulator-5554 io.mehow.quack.sample open", "emulator-5554 com.duckduckgo.mobile wake"}
	if got := source.starts(); !reflect.DeepEqual(got, want) {
		t.Errorf("started %q, want the flags' mode only for the first pick: %q", got, want)
	}
}

func TestDeviceAndPackageAreRefusedDuringARun(t *testing.T) {
	h := start(t, newFakeApp())
	h.typing("account.watch")
	h.press("enter")
	h.waitFor("account.watch · ctrl+c stop action")

	for _, command := range []string{"/switch-device", "/switch-app"} {
		h.typing(command)
		h.press("enter")
		h.waitFor("stop the running action first (ctrl+c)")
		h.waitFor("> find an action")
		h.press("esc")
	}
}

func TestDisconnectOpensTheDeviceStepAndWaits(t *testing.T) {
	app := newFakeApp()
	source := newFakeSource(app)
	h := startPicking(t, source, client.Target{}, client.Wake)
	h.waitFor("type or ↑ to find an action")
	h.typing("account.watch")
	h.press("enter")
	h.waitFor("account.watch · ctrl+c stop action")

	source.set()

	h.waitForLog("emulator-5554 disconnected")
	h.waitFor("no device · waiting for one to connect", "> pick a device")
	select {
	case <-app.canceled(0):
	case <-time.After(timeout):
		t.Error("the run was not stopped")
	}
	source.set(device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"))
	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
}

func TestOtherDevicesComingAndGoingKeepTheActions(t *testing.T) {
	source := newFakeSource(newFakeApp())
	h := startPicking(t, source, client.Target{}, client.Wake)
	h.waitFor("type or ↑ to find an action")

	source.set(device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"), device("3A251FDH2000XY", "Pixel 7", "device"))
	source.set(device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"))

	h.waitForRunning()
	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
	if got := source.starts(); len(got) != 1 {
		t.Errorf("started %q, want only the first pick", got)
	}
}

func TestTheOnlyDeviceThatCanBePickedIsTakenByItself(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		device("R5CT20ABCDE", "", "unauthorized"),
		device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		device("emulator-5556", "Pixel 9", "device"))

	h := startPicking(t, source, client.Target{}, client.Wake)

	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
}

func TestAStartThePickerLeftBehindDoesNotTakeOver(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"),
		device("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.other"))
	source.appFor("emulator-5554", "com.duckduckgo.mobile")
	source.appFor("3A251FDH2000XY", "io.mehow.other")
	h := startPicking(t, source, client.Target{Device: "emulator-5554"}, client.Wake)
	h.waitFor("> pick a package")
	h.press("enter")
	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
	releaseLeft := source.hold("emulator-5554", "com.duckduckgo.mobile")
	releaseNew := source.hold("3A251FDH2000XY", "io.mehow.other")
	t.Cleanup(releaseLeft)
	t.Cleanup(releaseNew)

	h.typing("/switch-app")
	h.press("enter")
	h.waitFor("> pick a package")
	h.press("up", "enter")
	h.waitFor("starting com.duckduckgo.mobile on emulator-5554…")
	source.set(device("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.other"))
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
	source.devices = nil
	source.appFor("3A251FDH2000XY", "com.duckduckgo.mobile").setActions(nil)
	h := startPicking(t, source, client.Target{}, client.Wake)
	check := func(state string) {
		t.Helper()
		if got := h.panelRows(); got != 5 {
			t.Errorf("%s: the panel takes the last %d rows, want 5:\n%s", state, got, h.view())
		}
		if got := h.lines(); got != 30 {
			t.Errorf("%s: the view has %d rows, want the window's 30", state, got)
		}
	}

	h.waitFor("no device · waiting")
	h.resetHeights()
	check("waiting for a device")
	devices := []client.Device{
		device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		device("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"),
	}
	for i := range 6 {
		devices = append(devices, device("emulator-56"+string(rune('0'+i))+"0", "", "offline"))
	}
	source.set(devices...)
	h.waitFor("> pick a device", "8/8")
	check("more devices than fit")
	h.typing("zz")
	h.waitFor("no match")
	check("no match")
	h.press("esc")
	h.typing("3A25")
	h.press("enter")
	h.waitFor("> pick a package")
	check("package step")
	h.press("up", "enter")
	h.waitForLog("the app is gone")
	check("failed start")
	h.press("ctrl+c")
	h.waitFor("press ctrl+c again to quit")
	check("quit prompt")
	h.press("down", "enter")
	h.waitFor("type or ↑ to find an action")
	check("actions")

	if heights := h.heights(); !reflect.DeepEqual(heights, []int{5}) {
		t.Errorf("the panel had these heights: %v, want only 5", heights)
	}
}

func TestPackageFlagHidesTheDevicesWithoutIt(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		device("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"),
		device("emulator-5556", "Pixel 9", "device", "com.duckduckgo.mobile"))

	h := startPicking(t, source, client.Target{Package: "com.duckduckgo.mobile"}, client.Wake)

	h.waitFor("Pixel 9 · emulator-5556", "Pixel 7 · 3A251FDH2000XY", "> pick a device", "2/2")
	h.waitForNo("Pixel 8")
}

func TestPackageStepSaysWhenTheDeviceHasNoAppLeft(t *testing.T) {
	source := newFakeSource(newFakeApp(), device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"))
	h := startPicking(t, source, client.Target{}, client.Wake, withPollTime(20*time.Millisecond))
	h.waitFor("> pick a package", "2/2")

	source.install("emulator-5554")

	h.waitFor("  no quack package on emulator-5554 · waiting for one", "> pick a package", "0/0")
}

func TestPackageStepGoesBackToTheDevicesWhenItsDeviceLeaves(t *testing.T) {
	source := newFakeSource(newFakeApp(),
		device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"),
		device("3A251FDH2000XY", "Pixel 7", "device", "io.mehow.quack.sample", "com.duckduckgo.mobile"),
		device("emulator-5556", "Pixel 9", "device", "io.mehow.quack.sample"))
	h := startPicking(t, source, client.Target{}, client.Wake)
	h.waitFor("Pixel 7", "> pick a device")
	h.press("up", "enter")
	h.waitFor("com.duckduckgo.mobile", "> pick a package")

	source.set(device("emulator-5554", "Pixel 8", "device", "io.mehow.quack.sample"), device("emulator-5556", "Pixel 9", "device", "io.mehow.quack.sample"))

	h.waitFor("Pixel 9", "Pixel 8", "> pick a device", "2/2")
}

func TestDeviceListScrollsToTheSelectionAndNewTextSelectsTheBestMatch(t *testing.T) {
	var devices []client.Device
	for i := range 7 {
		devices = append(devices, device("emulator-555"+strconv.Itoa(i), "Pixel "+strconv.Itoa(i), "device", "io.mehow.quack.sample"))
	}
	h := startPicking(t, newFakeSource(newFakeApp(), devices...), client.Target{}, client.Wake)
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
	source.failTracks = 3

	h := startPicking(t, source, client.Target{}, client.Wake, withPollTime(20*time.Millisecond))

	h.waitFor("quack · io.mehow.quack.sample 1.0 · emulator-5554", "type or ↑ to find an action")
	if log := h.logText(); strings.Count(log, "lost track of the devices: EOF") != 1 {
		t.Errorf("the log says the lost track %d times, want once:\n%s", strings.Count(log, "lost track of the devices: EOF"), log)
	}
}
