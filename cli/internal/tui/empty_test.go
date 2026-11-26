package tui

import (
	"os"
	"quack/internal/wire"
	"reflect"
	"testing"
)

func TestEmptyStringIsExplicitAndSatisfiesRequiredField(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)
	h.typing("app.greet")
	h.press("tab")
	h.waitFor("▸ name", "ctrl+e send empty")
	h.press("ctrl+e")
	h.waitFor("<empty string>", "name=''")
	h.press("enter", "enter")
	h.waitForLog("Hello, !")
	if got := app.Calls()[0].Args["name"]; !reflect.DeepEqual(got, wire.RawArg{""}) {
		t.Errorf("name %v", got)
	}
}

func TestEmptyListOverridesDefaultAndResetRestoresOmission(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "default"}[reset], func(t *testing.T) {
			app := newFakeApp()
			h := start(t, app)
			h.typing("app.params")
			h.press("tab", "tab", "tab", "tab", "tab", "tab", "tab")
			h.waitFor("▸ tag")
			h.press("ctrl+e")
			h.waitFor("<empty list>")
			if reset {
				h.press("ctrl+u")
				h.waitFor("comma-separated")
			}
			h.press("enter")
			h.waitForLog("done · ")
			got, given := app.Calls()[0].Args["tag"]
			if reset {
				if given {
					t.Errorf("reset sent %v", got)
				}
			} else if !given || got == nil || len(got) != 0 {
				t.Errorf("empty list = %#v, given=%t", got, given)
			}
		})
	}
}

func TestTypingReplacesExplicitEmptyValue(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)
	h.typing("app.greet")
	h.press("tab")
	h.waitFor("▸ name")
	h.press("ctrl+e")
	h.typing("Alice")
	h.press("enter", "enter")
	h.waitForLog("Hello, Alice!")
	if got := app.Calls()[0].Args["name"][0]; got != "Alice" {
		t.Errorf("sent %q", got)
	}
}

func TestTUIExpandsHostVariablesAndKeepsTheLiteralEscape(t *testing.T) {
	t.Setenv("QUACK_USER", "Alice")
	for _, tc := range []struct{ input, want string }{{"${QUACK_USER}", "Alice"}, {"$${QUACK_USER}", "${QUACK_USER}"}} {
		t.Run(tc.input, func(t *testing.T) {
			app := newFakeApp()
			h := start(t, app)
			h.typing("app.greet")
			h.press("tab")
			h.waitFor("▸ name")
			h.typing(tc.input)
			h.press("enter", "enter")
			h.waitForLog("Hello, " + tc.want + "!")
			if got := app.Calls()[0].Args["name"][0]; got != tc.want {
				t.Errorf("sent %q", got)
			}
			h.waitForLog("name='" + tc.input + "'")
		})
	}
}

func TestTUIMissingEnvironmentPreventsExecution(t *testing.T) {
	// An unset variable fails; a present empty value is a valid explicit empty string.
	t.Setenv("QUACK_UNSET_PARENT", "")
	if err := os.Unsetenv("QUACK_UNSET_PARENT"); err != nil {
		t.Fatal(err)
	}
	app := newFakeApp()
	h := start(t, app)
	h.typing("app.greet")
	h.press("tab")
	h.waitFor("▸ name")
	h.typing("${QUACK_UNSET_PARENT}")
	h.press("enter", "enter")
	h.waitForLog("environment variable 'QUACK_UNSET_PARENT' is not set")
	if len(app.Calls()) != 0 {
		t.Fatal("missing variable ran action")
	}
}
