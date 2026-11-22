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
	// recipes is the sample app's recipe file, which is missing when recipes is empty.
	recipes string
}

var scenarios = []scenario{
	// Usage.
	{name: "help", args: []string{"--help"}},
	{name: "no-action"},
	{name: "no-action-with-device-flag", args: []string{"-d", "emulator-5554"}},
	{name: "no-action-with-json", args: []string{"--json"}},
	{name: "unknown-flag", args: []string{"--nope", "app.greet"}},
	{name: "short-flag", args: []string{"-d", "emulator-5554", "app.greet"}},
	{name: "short-help", args: []string{"-h"}},
	{name: "version", args: []string{"--version"}},
	{name: "short-flags-call", args: []string{"-j", "-a", "io.mehow.quack.sample", "app.greet", "name=Bob"}},
	{name: "short-flags-list-actions", args: []string{"-l", "account"}},
	{name: "flag-turned-off", args: []string{"--json=false", "app.greet", "name=Bob"}},
	{name: "flag-invalid-value", args: []string{"--json=maybe", "app.greet", "name=Bob"}},
	{name: "invalid-app", args: []string{"--app", "nope", "app.greet"}},
	{name: "arg-not-key-value", args: []string{"app.greet", "Bob"}},
	{name: "list-actions-two-regexes", args: []string{"--list-actions", "a", "b"}},
	{name: "list-actions-invalid-regex", args: []string{"--list-actions", "acc("}},

	// Calls.
	{name: "call", args: []string{"app.greet", "name=Bob"}},
	{name: "call-repeated-key", args: []string{"app.params", "tag=a", "tag=b c"}},
	{name: "call-variable", args: []string{"app.greet", "name=${QUACK_NAME} on ${ANDROID_SERIAL}"}, env: []string{"QUACK_NAME=Bob"}},
	{name: "call-variable-unset", args: []string{"app.greet", "name=${QUACK_NAME}"}},
	{name: "call-streams-notifications", args: []string{"account.create", "username=alice"}},
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

	// Without an action, quack prints its help instead of opening the TUI, since stdin is not a terminal here.
	{name: "no-action-from-dev-null", stdinDevNull: true},

	// Recipes.
	{name: "recipe", args: []string{"--recipe", "setup"}, recipes: sampleRecipes},
	{name: "recipe-json", args: []string{"--json", "--recipe", "setup"}, recipes: sampleRecipes},
	{name: "recipe-open-app-flag", args: []string{"--open-app", "--recipe", "greet"}, recipes: sampleRecipes},
	{name: "recipe-args", args: []string{"--recipe", "greet", "name=Alice"}, recipes: sampleRecipes},
	{name: "recipe-args-variable", args: []string{"--recipe", "greet", "name=${QUACK_NAME}"}, recipes: sampleRecipes, env: []string{"QUACK_NAME=Carol"}},
	{name: "recipe-args-refused", args: []string{"--recipe", "greet", "nope=1"}, recipes: sampleRecipes},
	{name: "recipe-args-refused-json", args: []string{"--json", "--recipe", "greet", "name=a", "name=b"}, recipes: sampleRecipes},
	{name: "recipe-failing-step", args: []string{"--recipe", "clean"}, recipes: sampleRecipes},
	{name: "recipe-failing-step-json", args: []string{"--json", "--recipe", "clean"}, recipes: sampleRecipes},
	{name: "recipe-catching-a-step", args: []string{"--recipe", "careful"}, recipes: sampleRecipes},
	{name: "recipe-catching-a-step-json", args: []string{"--json", "--recipe", "careful"}, recipes: sampleRecipes},
	{name: "recipe-script-error", args: []string{"--recipe", "typo"}, recipes: sampleRecipes},
	{name: "recipe-script-error-json", args: []string{"--json", "--recipe", "typo"}, recipes: sampleRecipes},
	{name: "recipe-environment", args: []string{"--recipe", "greetEnv"}, recipes: sampleRecipes, env: []string{"QUACK_NAME=Bob"}},
	{name: "recipe-environment-unset", args: []string{"--recipe", "greetEnv"}, recipes: sampleRecipes},
	{name: "recipe-commands", args: []string{"--recipe", "prepare"}, recipes: sampleRecipes, env: []string{"QUACK_NAME=Bob"}},
	{name: "recipe-commands-json", args: []string{"--json", "--recipe", "prepare"}, recipes: sampleRecipes, env: []string{"QUACK_NAME=Bob"}},
	{name: "recipe-failing-shell", args: []string{"--recipe", "failShell"}, recipes: sampleRecipes},
	{name: "recipe-failing-run-json", args: []string{"--json", "--recipe", "failRun"}, recipes: sampleRecipes},
	{name: "recipe-unknown", args: []string{"--recipe", "nope"}, recipes: sampleRecipes},
	{name: "recipe-without-file", args: []string{"--recipe", "setup"}},
	{name: "recipe-broken-file", args: []string{"--recipe", "setup"}, recipes: "export const setup = (\n"},
	{name: "recipe-with-action", args: []string{"--recipe", "setup", "app.greet", "name=Bob"}, recipes: sampleRecipes},
	{name: "recipe-with-list-actions", args: []string{"--recipe", "setup", "--list-actions"}, recipes: sampleRecipes},
	{name: "list-recipes", args: []string{"--list-recipes"}, recipes: sampleRecipes},
	{name: "list-recipes-filter", args: []string{"--list-recipes", "^s"}, recipes: sampleRecipes},
	{name: "list-recipes-no-match", args: []string{"--list-recipes", "nope"}, recipes: sampleRecipes},
	{name: "list-recipes-json", args: []string{"--json", "--list-recipes"}, recipes: sampleRecipes},
	{name: "list-recipes-none", args: []string{"--list-recipes"}},
	{name: "list-recipes-none-json", args: []string{"--json", "--list-recipes"}},
	{name: "list-recipes-broken-file", args: []string{"--list-recipes"}, recipes: "export const setup = (\n"},
	{name: "list-recipes-with-list-actions", args: []string{"--list-recipes", "--list-actions"}},
	{name: "init-recipes", args: []string{"--init-recipes"}},
	{name: "init-recipes-with-json", args: []string{"--init-recipes", "--json"}},
	{name: "init-recipes-with-app", args: []string{"--init-recipes", "-a", "io.mehow.quack.sample"}},
	{name: "init-recipes-with-app-keeps-its-file", args: []string{"--init-recipes", "-a", "io.mehow.quack.sample"}, recipes: sampleRecipes},
	{name: "init-recipes-with-device", args: []string{"--init-recipes", "-d", "emulator-5554"}},
	{name: "init-recipes-with-android-serial", args: []string{"--init-recipes"}, env: []string{"ANDROID_SERIAL=emulator-5554"}},
}

// sampleRecipes is a recipes file for the sample app.
const sampleRecipes = `import quack from "quack"

export const setup = quack.recipe({
  description: "Create the account and greet",
  run() {
    quack.openApp()
    quack.wait("10ms")
    quack.runAction("account.create", { username: "alice" })
    quack.runAction("app.params", { tag: ["a", "b c"], count: 3, flag: true })
  },
})

export const greet = quack.recipe({
  params: { name: { type: "string", default: "Bob", hints: ["Alice", "Bob"], description: "Who to greet" } },
  run({ name }) {
    quack.runAction("app.greet", { name })
  },
})

export const greetEnv = quack.recipe({
  run() {
    quack.runAction("app.greet", { name: quack.env.QUACK_NAME ?? "nobody" })
  },
})

export const prepare = quack.recipe({
  run() {
    quack.shell("pm clear " + quack.app.package)
    quack.run("echo seeded " + quack.env.QUACK_NAME + " on $ANDROID_SERIAL")
    quack.runAction("app.greet", { name: "Bob" })
  },
})

export const failShell = quack.recipe({
  run() {
    quack.shell("fail now")
    quack.runAction("app.greet", { name: "Bob" })
  },
})

export const failRun = quack.recipe({
  run() {
    quack.run("echo oops >&2; exit 3")
    quack.runAction("app.greet", { name: "Bob" })
  },
})

export const clean = quack.recipe({
  description: "Delete the account, then greet",
  run() {
    quack.runAction("account.delete")
    quack.runAction("app.greet", { name: "Bob" })
  },
})

export const careful = quack.recipe({
  description: "Delete the account if there is one, then greet",
  run() {
    try {
      quack.runAction("account.delete")
    } catch (e) {
      console.log("kept going: " + (e as Error).message)
    }
    quack.runAction("app.greet", { name: "Bob" })
  },
})

export const typo = quack.recipe({
  run() {
    const result = quack.runAction<{ user: { name: string } }>("app.greet", { name: "Bob" })
    console.log(result.data.user.name)
  },
})
`

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
	config := t.TempDir()
	if s.recipes != "" {
		dir := filepath.Join(config, "quack", "recipes")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "io.mehow.quack.sample.ts"), []byte(s.recipes), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], s.args...)
	cmd.Env = append(cleanEnv(), runAsQuack+"=1", "ANDROID_ADB_SERVER_PORT="+adb.port(), "XDG_CONFIG_HOME="+config)
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
	// The config dir differs on each run, so the output names it as the variable that points to it.
	hideConfig := strings.NewReplacer(config, "$XDG_CONFIG_HOME")
	fmt.Fprintf(&out, "--- stdout\n%s", ensureNewline(hideConfig.Replace(stdout.String())))
	fmt.Fprintf(&out, "--- stderr\n%s", ensureNewline(hideConfig.Replace(stderr.String())))
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
		case "ANDROID_SERIAL", "ANDROID_ADB_SERVER_PORT", "NO_COLOR", "XDG_CONFIG_HOME":
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
