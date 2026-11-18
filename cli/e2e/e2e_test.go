// Package e2e runs quack against a fake adb server and compares everything it does with golden files.
// Run go test ./e2e -update to record the current behavior.
package e2e

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"quack/internal/cli"
)

var update = flag.Bool("update", false, "rewrite the golden files with the current behavior")

// TestMain runs the tests, or quack itself when a scenario starts this test binary with runAsQuack set. Each scenario
// then runs quack in a process of its own, with its own environment and stdin, and go test sees the CLI's code, so
// that a change to it reruns the tests.
func TestMain(m *testing.M) {
	if os.Getenv(runAsQuack) != "" {
		os.Exit(cli.Run(os.Args[1:], cli.StdTerminal()))
	}
	flag.Parse()
	os.Exit(m.Run())
}

// runAsQuack is the environment variable that makes this test binary run quack.
const runAsQuack = "QUACK_E2E_RUN_AS_QUACK"

type scenario struct {
	name string
	args []string
	// stdinDevNull reads stdin from /dev/null, a character device that is not a terminal, instead of a pipe.
	stdinDevNull bool
	env          []string
	// device changes the default device: emulator-5554, a Pixel 8, user 0, with the sample app running.
	device func(*fakeDevice)
	// others are devices connected besides the default one.
	others func() []*fakeDevice
}

var scenarios = []scenario{
	// Usage.
	{name: "help", args: []string{"--help"}},
	{name: "missing-action"},
	{name: "unknown-flag", args: []string{"--nope", "app.greet"}},
	{name: "short-flag", args: []string{"-d", "emulator-5554", "app.greet"}},
	{name: "short-help", args: []string{"-h"}},
	{name: "short-flags-call", args: []string{"-j", "-a", "io.mehow.quack.sample", "app.greet", "name=Bob"}},
	{name: "short-flags-list-actions", args: []string{"-l", "account"}},
	{name: "short-flags-interactive", args: []string{"-i"}},
	{name: "flag-turned-off", args: []string{"--json=false", "app.greet", "name=Bob"}},
	{name: "flag-invalid-value", args: []string{"--json=maybe", "app.greet", "name=Bob"}},
	{name: "invalid-app", args: []string{"--app", "nope", "app.greet"}},
	{name: "arg-not-key-value", args: []string{"app.greet", "Bob"}},
	{name: "interactive-with-action", args: []string{"--interactive", "app.greet"}},
	{name: "list-actions-two-regexes", args: []string{"--list-actions", "a", "b"}},
	{name: "list-actions-invalid-regex", args: []string{"--list-actions", "acc("}},

	// Calls.
	{name: "call", args: []string{"app.greet", "name=Bob"}},
	{name: "call-repeated-key", args: []string{"app.params", "tag=a", "tag=b c"}},
	{name: "call-streams-events", args: []string{"account.create", "username=alice"}},
	{name: "call-json", args: []string{"--json", "account.create", "username=alice"}},
	{name: "call-data", args: []string{"account.show"}},
	{name: "call-data-json", args: []string{"--json", "account.show"}},
	{name: "flags-after-action", args: []string{"app.greet", "name=Bob", "--json"}},
	{name: "error-invalid-args", args: []string{"app.greet"}},
	{name: "error-invalid-args-json", args: []string{"--json", "app.greet"}},
	{name: "error-unknown-action", args: []string{"nope"}},
	{name: "error-unknown-code", args: []string{"app.teapot"}},
	{name: "error-unknown-code-json", args: []string{"--json", "app.teapot"}},
	{name: "error-rejected", args: []string{"app.reject"}},
	{name: "error-failed", args: []string{"app.crash"}},
	{name: "error-bad-frame", args: []string{"app.misread"}},
	{name: "closed-without-reply", args: []string{"app.hangup"}},
	{name: "closed-without-reply-json", args: []string{"--json", "app.hangup"}},
	{name: "closed-before-result", args: []string{"app.dropout"}},
	{name: "malformed-reply", args: []string{"app.garbled"}},
	{name: "keep-alive", args: []string{"account.sync"}},

	// Help from the app.
	{name: "list-actions", args: []string{"--list-actions"}},
	{name: "list-actions-filter", args: []string{"--list-actions", "account"}},
	{name: "list-actions-no-match", args: []string{"--list-actions", "nope"}},
	{name: "list-actions-json", args: []string{"--json", "--list-actions"}},
	{name: "list-actions-filter-json", args: []string{"--json", "--list-actions", "account"}},
	{name: "list-actions-no-match-json", args: []string{"--json", "--list-actions", "nope"}},

	// Starting the app.
	{name: "start-stopped-app", args: []string{"app.greet", "name=Bob"}, device: stopped},
	{name: "open-stopped-app", args: []string{"--open-app", "app.greet", "name=Bob"}, device: stopped},
	{name: "open-without-launcher", args: []string{"--open-app", "app.greet", "name=Bob"}, device: func(d *fakeDevice) {
		d.apps[0].hasLauncher = false
	}},
	{name: "not-installed", args: []string{"--app", "io.mehow.nope", "app.greet"}},
	{name: "not-installed-json", args: []string{"--json", "--app", "io.mehow.nope", "app.greet"}},
	{name: "no-quack-app", args: []string{"app.greet"}, device: func(d *fakeDevice) { d.apps = nil }},
	{name: "several-apps", args: []string{"app.greet"}, device: twoApps},
	{name: "several-apps-picked", args: []string{"--app", "io.mehow.other", "app.greet", "name=Bob"}, device: twoApps},
	{name: "protocol-mismatch", args: []string{"app.greet"}, device: func(d *fakeDevice) { d.apps[0].protocol = 2 }},
	{name: "refused-without-dump", args: []string{"--start-timeout", "300ms", "app.greet"}, device: func(d *fakeDevice) {
		d.apps[0].noDump = true
	}},
	{name: "app-never-answers", args: []string{"--start-timeout", "300ms", "app.greet"}, device: func(d *fakeDevice) {
		d.apps[0].deaf = true
	}},

	// Devices.
	{name: "device-flag", args: []string{"--device", "3A251FDH2000XY", "--app", "io.mehow.quack.sample", "app.greet", "name=Bob"}, others: phone},
	{name: "device-from-env", args: []string{"--app", "io.mehow.quack.sample", "app.greet", "name=Bob"}, env: []string{"ANDROID_SERIAL=3A251FDH2000XY"}, others: phone},
	{name: "device-flag-over-env", args: []string{"--device", "emulator-5554", "app.greet", "name=Bob"}, env: []string{"ANDROID_SERIAL=3A251FDH2000XY"}, others: phone},
	{name: "device-not-connected", args: []string{"--device", "emulator-5599", "app.greet"}},
	{name: "device-offline", args: []string{"--device", "emulator-5554", "app.greet"}, device: func(d *fakeDevice) {
		d.state = "offline"
	}},
	{name: "device-unauthorized", args: []string{"--device", "emulator-5554", "app.greet"}, device: func(d *fakeDevice) {
		d.state = "unauthorized"
	}},
	{name: "no-device", args: []string{"app.greet"}, device: func(d *fakeDevice) { d.state = "" }},
	{name: "no-device-json", args: []string{"--json", "app.greet"}, device: func(d *fakeDevice) { d.state = "" }},
	{name: "only-device-unauthorized", args: []string{"app.greet"}, device: func(d *fakeDevice) { d.state = "unauthorized" }},
	{name: "only-device-offline", args: []string{"app.greet"}, device: func(d *fakeDevice) { d.state = "offline" }},
	{name: "adb-server-hangs", args: []string{"app.greet"}, device: func(d *fakeDevice) { d.adbHangs = true }},

	// Several devices.
	{name: "several-devices", args: []string{"app.greet"}, others: phone},
	{name: "several-devices-device-flag", args: []string{"--device", "3A251FDH2000XY", "app.greet"}, others: phone},
	{name: "several-devices-app-flag", args: []string{"--app", "com.duckduckgo.mobile", "--list-actions"}, others: phone},
	{name: "several-devices-app-on-none", args: []string{"--app", "io.mehow.nope", "app.greet"}, others: phone},
	{name: "several-devices-one-ready", args: []string{"app.greet", "name=Bob"}, others: unauthorizedPhone},
	{name: "no-device-ready", args: []string{"app.greet"}, device: func(d *fakeDevice) { d.state = "offline" }, others: unauthorizedPhone},
	{name: "no-quack-app-on-any-device", args: []string{"app.greet"}, device: func(d *fakeDevice) { d.apps = nil }, others: func() []*fakeDevice {
		return []*fakeDevice{{serial: "3A251FDH2000XY", state: "device", user: "0"}}
	}},

	// Listing devices.
	{name: "list-devices", args: []string{"--list-devices"}, others: phone},
	{name: "list-devices-json", args: []string{"--list-devices", "--json"}, others: phone},
	{name: "list-devices-unauthorized", args: []string{"--list-devices"}, others: unauthorizedPhone},
	{name: "list-devices-unauthorized-json", args: []string{"--list-devices", "--json"}, others: unauthorizedPhone},
	{name: "list-devices-device-flag", args: []string{"--list-devices", "--device", "3A251FDH2000XY"}, others: phone},
	{name: "list-devices-app-flag", args: []string{"--list-devices", "--app", "com.duckduckgo.mobile"}, others: phone},
	{name: "list-devices-none", args: []string{"--list-devices"}, device: func(d *fakeDevice) { d.state = "" }},
	{name: "list-devices-with-action", args: []string{"--list-devices", "app.greet"}},
	{name: "list-devices-with-list-actions", args: []string{"--list-devices", "--list-actions"}},
	{name: "list-devices-with-interactive", args: []string{"--list-devices", "--interactive"}},

	// The TUI needs a terminal, and stdin is a pipe here.
	{name: "interactive-piped", args: []string{"--interactive"}},
	{name: "interactive-with-json", args: []string{"--interactive", "--json"}},
	{name: "interactive-from-dev-null", args: []string{"--interactive"}, stdinDevNull: true},
}

func TestScenarios(t *testing.T) {
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			devices := []*fakeDevice{defaultDevice()}
			if s.device != nil {
				s.device(devices[0])
			}
			if s.others != nil {
				devices = append(devices, s.others()...)
			}
			adb := startFakeAdb(t, devices)
			got := run(t, adb, s)
			path := filepath.Join("testdata", s.name+".golden")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v. Run go test ./e2e -update to record it.", err)
			}
			if got != string(want) {
				t.Errorf("got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// run runs quack for s and describes everything it did: its exit code, its output and what reached the device.
func run(t *testing.T, adb *fakeAdb, s scenario) string {
	cmd := exec.Command(os.Args[0], s.args...)
	cmd.Env = append(cleanEnv(), runAsQuack+"=1", "ANDROID_ADB_SERVER_PORT="+adb.port())
	cmd.Env = append(cmd.Env, s.env...)
	cmd.Stdin = strings.NewReader("")
	if s.stdinDevNull {
		devNull, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		defer devNull.Close()
		cmd.Stdin = devNull
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "$ quack %s\n", strings.Join(s.args, " "))
	if s.stdinDevNull {
		fmt.Fprintf(&out, "--- stdin from %s\n", os.DevNull)
	}
	fmt.Fprintf(&out, "--- exit %d\n", code)
	fmt.Fprintf(&out, "--- stdout\n%s", ensureNewline(stdout.String()))
	fmt.Fprintf(&out, "--- stderr\n%s", ensureNewline(stderr.String()))
	out.WriteString("--- device\n")
	for _, entry := range adb.recorded() {
		out.WriteString(entry + "\n")
	}
	return out.String()
}

// cleanEnv is the test's environment without what would change quack's behavior.
func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "ANDROID_SERIAL", "ANDROID_ADB_SERVER_PORT", "NO_COLOR":
			continue
		}
		env = append(env, kv)
	}
	return env
}

func defaultDevice() *fakeDevice {
	return &fakeDevice{serial: "emulator-5554", state: "device", model: "Pixel 8", user: "0", apps: []*fakeApp{sampleApp()}}
}

// phone is a second device, with the sample app and another quack-enabled app.
func phone() []*fakeDevice {
	other := sampleApp()
	other.pkg = "com.duckduckgo.mobile"
	return []*fakeDevice{{serial: "3A251FDH2000XY", state: "device", model: "Pixel 7", user: "0", apps: []*fakeApp{sampleApp(), other}}}
}

// unauthorizedPhone is a second device that does not trust this computer yet.
func unauthorizedPhone() []*fakeDevice {
	return []*fakeDevice{{serial: "3A251FDH2000XY", state: "unauthorized", user: "0"}}
}

func stopped(d *fakeDevice) {
	d.apps[0].running = false
}

func twoApps(d *fakeDevice) {
	other := sampleApp()
	other.pkg = "io.mehow.other"
	d.apps = append(d.apps, other)
}

func ensureNewline(text string) string {
	if text == "" || strings.HasSuffix(text, "\n") {
		return text
	}
	return text + "\n(no newline at end)\n"
}
