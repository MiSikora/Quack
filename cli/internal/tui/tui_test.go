package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/config"
	"quack/internal/recipe"
	"quack/internal/wire"
)

func TestPanelShowsABlankRowTheTitleTheInputAndTheKeysAtRest(t *testing.T) {
	h := start(t, newFakeApp())

	checkPanel(t, h, []string{
		"",
		"quack · io.mehow.quack.sample 1.0 · emulator-5554",
		strings.Repeat("─", 100),
		"> find an action or a recipe, $ for recipes only, / for commands",
		strings.Repeat("─", 100),
		" type or ↑ to find an action · ctrl+o open app · ctrl+c cancel/quit",
	})
	if lines := h.panel(); !strings.HasSuffix(lines[3], " 6/6") {
		t.Errorf("the input line is %q, want the counter at its end", lines[3])
	}
}

func TestUpOnEmptyTextListsTheNamespacesUnderARuleAndEscClosesIt(t *testing.T) {
	h := start(t, newFakeApp())

	h.press("up")

	checkPanel(t, h, []string{
		strings.Repeat("─", 100),
		"  app.       3",
		"▸ account.   3",
		strings.Repeat("─", 100),
		"> find an action or a recipe, $ for recipes only, / for commands",
		strings.Repeat("─", 100),
		" enter or → open · ↑↓ select · esc close · ctrl+c cancel/quit",
	})
	if lines := h.panel(); !strings.HasSuffix(lines[4], " 2/6") {
		t.Errorf("the input line is %q, want the counter of the rows at its end", lines[4])
	}
	h.press("esc")
	h.waitFor("quack · ", "> find an action", "type or ↑ to find an action")
	h.press("down")
	h.waitFor("quack · ", "> find an action", "type or ↑ to find an action")
}

// Groups of 3 and 3 actions, a group of a recipe and two actions, a dotted id alone in its namespace, and two ids
// without a dot.
func startNamespaces(t *testing.T) (*harness, *fakeApp) {
	app := newFakeApp()
	app.setActions(append(app.actions(),
		wire.Action{ID: "sync.restore", Description: "Restore a backup"},
		wire.Action{ID: "sync.backup", Description: "Back up"},
		wire.Action{ID: "feature.toggle", Description: "Toggle a feature"},
		wire.Action{ID: "ping", Description: "Ping the app"},
	))
	h := start(t, app, withRecipes(t, `
import quack from "quack"

quack.recipe({ name: "sync.backup", description: "Back up with a recipe", run() { console.log("recipe backed up") } })
export const login = quack.recipe({ description: "Log in", run() {} })
`))
	return h, app
}

func TestBrowsingGroupsTheIdsThatShareANamespaceWithAnother(t *testing.T) {
	h, _ := startNamespaces(t)

	h.press("up")

	checkPanel(t, h, []string{
		strings.Repeat("─", 100),
		"  sync.            3",
		"  ping             Ping the app",
		"  login ƒ          Log in",
		"  feature.toggle   Toggle a feature",
		"  app.             3",
		"▸ account.         3",
		strings.Repeat("─", 100),
		"> find an action or a recipe, $ for recipes only, / for commands",
		strings.Repeat("─", 100),
		" enter or → open · ↑↓ select · esc close · ctrl+c cancel/quit",
	})
	if lines := h.panel(); !strings.HasSuffix(lines[8], " 6/12") {
		t.Errorf("the input line is %q, want the counter of the rows at its end", lines[8])
	}
	h.press("up", "up")
	h.waitFor("▸ feature.toggle", "enter run · tab params")
}

func TestEnterOrRightOpensAGroupWithTheRecipeFirstAndLeftOrBackspaceGoesBackToIt(t *testing.T) {
	h, _ := startNamespaces(t)
	h.press("up", "up", "up", "up", "up", "up")
	h.waitFor("▸ sync.")

	h.press("enter")
	checkPanel(t, h, []string{
		strings.Repeat("─", 100),
		"  sync.restore    Restore a backup",
		"  sync.backup     Back up",
		"▸ sync.backup ƒ   Back up with a recipe",
		strings.Repeat("─", 100),
		"> find an action or a recipe, $ for recipes only, / for commands",
		strings.Repeat("─", 100),
		" enter run · tab params · ↑↓ select · ← back · esc close · ctrl+c cancel/quit",
	})
	if lines := h.panel(); !strings.HasSuffix(lines[5], " sync. 3/12") {
		t.Errorf("the input line is %q, want the group and the counter at its end", lines[5])
	}
	h.press("left")
	h.waitFor("▸ sync.", "  ping", "6/12")
	h.waitForNo("sync.restore")
	h.press("right")
	h.waitFor("▸ sync.backup ƒ", "sync. 3/12")
	h.press("backspace")
	h.waitFor("▸ sync.", "6/12", "enter or → open")
}

func TestEnterInAGroupRunsTheSelectedRecipeOrAction(t *testing.T) {
	h, app := startNamespaces(t)
	h.press("up", "up", "up", "up", "up", "up", "enter")
	h.waitFor("▸ sync.backup ƒ")

	h.press("enter")
	h.waitForLog("\n› --recipe sync.backup\nrecipe backed up\n")
	h.waitFor("type or ↑ to find an action")
	if got := app.received(); len(got) != 0 {
		t.Errorf("calls: got %v, want none", got)
	}
	h.press("up", "up", "up", "up", "up", "up", "enter", "up")
	h.waitFor("▸ sync.backup     Back up")
	h.press("enter")

	h.waitForLog("\n› sync.backup\n")
	if got, want := app.received(), []wire.Call{{Action: "sync.backup"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestTypingInAGroupSearchesEverythingAndForgetsTheGroup(t *testing.T) {
	h, _ := startNamespaces(t)
	h.press("up", "up", "up", "up", "up", "up", "enter")
	h.waitFor("▸ sync.backup ƒ", "sync. 3/12")

	h.typing("acc")
	h.waitFor("▸ account.", "> acc", "3/12")
	h.waitForNo("sync.")
	h.press("backspace", "backspace", "backspace")
	h.waitFor("type or ↑ to find an action")
	h.press("up")
	h.waitFor("▸ account.         3", "6/12")
}

func TestEscInAGroupClosesTheListAndUpStartsAtTheTop(t *testing.T) {
	h, _ := startNamespaces(t)
	h.press("up", "enter")
	h.waitFor("▸ account.create", "account. 3/12")

	h.press("esc")
	h.waitFor("> find an action", "type or ↑ to find an action")
	h.press("up")

	h.waitFor("  app.             3", "▸ account.         3", "6/12")
}

func TestBrowsingWithoutGroupsListsEveryIdSorted(t *testing.T) {
	h := startWithConfig(t, newFakeApp(), flat)

	h.press("up")

	h.waitFor("  app.params", "  app.greet", "  app.count", "  account.watch", "  account.delete", "▸ account.create", "6/6")
}

func TestEnterAndTabDoNothingWhileNoListShows(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)

	h.press("enter", "tab")

	h.waitForRunning()
	h.waitFor("quack · ", "> find an action", "type or ↑ to find an action")
	if got := app.received(); len(got) != 0 {
		t.Errorf("calls: got %v, want none", got)
	}
}

func TestBackspaceToEmptyTextClosesTheList(t *testing.T) {
	h := start(t, newFakeApp())

	h.typing("a")
	h.waitFor("▸ app.", "> a", "enter run")
	h.press("backspace")
	h.waitFor("quack · ", "> find an action", "type or ↑ to find an action")
	h.press("up")
	h.waitFor("▸ account.", "esc close")
	h.typing("a")
	h.press("backspace")
	h.waitFor("quack · ", "> find an action", "type or ↑ to find an action")
}

func TestBestMatchSitsRightOverTheInputAndTheListTakesOnlyTheMatches(t *testing.T) {
	h := start(t, newFakeApp())

	h.typing("acc")
	h.waitFor("3/6")

	lines := h.panel()
	if len(lines) != 8 || lines[0] != strings.Repeat("─", 100) {
		t.Fatalf("the panel is %q, want a rule, the three matches and the last four rows", lines)
	}
	if !strings.HasPrefix(lines[1], "  account.") || !strings.HasPrefix(lines[2], "  account.") {
		t.Errorf("the list is %q, want the other matches over the best", lines[1:3])
	}
	rule := strings.Repeat("─", 100)
	if !strings.HasPrefix(lines[3], "▸ account.") || lines[4] != rule || !strings.HasPrefix(lines[5], "> acc") {
		t.Errorf("the rows over the input are %q and %q, want the best match right over the rule", lines[3], lines[4])
	}
}

func TestNoMatchIsTheOnlyRowOfTheList(t *testing.T) {
	h := start(t, newFakeApp())

	h.typing("zz")
	h.waitFor("0/6")

	if lines := h.panel(); len(lines) != 6 || lines[1] != "  no match" {
		t.Errorf("the panel is %q, want no match alone over the input", lines)
	}
}

func TestBoxShowsOverTheInputWhichPreviewsTheCall(t *testing.T) {
	h := start(t, newFakeApp())

	h.typing("app.greet")
	h.press("tab")
	h.waitFor("▸ name")

	lines := h.panel()
	if len(lines) != 8 || !strings.HasPrefix(lines[1], "app.greet · Say hello") {
		t.Fatalf("the panel is %q, want the action over its two params", lines)
	}
	if !strings.HasPrefix(lines[2], "▸ name") || !strings.HasPrefix(lines[3], "  times") {
		t.Errorf("the rows of the params are %q and %q", lines[2], lines[3])
	}
	if lines[4] != strings.Repeat("─", 100) || lines[5] != "› app.greet" {
		t.Errorf("the rows under the box are %q and %q, want a rule over the preview", lines[4], lines[5])
	}
}

func TestListCoversTheBottomOfTheLogWithoutMovingIt(t *testing.T) {
	h := startWithConfig(t, newFakeApp(), flat)
	h.typing("app.count")
	h.press("enter")
	h.waitForLog("  50\nCounted · ")
	// The run ends after its step does, which puts the footer back at rest.
	h.waitFor("type or ↑ to find an action")
	before := strings.Split(h.view(), "\n")

	h.press("up")
	h.waitFor("▸ account.create")

	after := strings.Split(h.view(), "\n")
	if len(after) != 30 || !reflect.DeepEqual(after[:19], before[:19]) || after[19] != strings.Repeat("─", 100) {
		t.Errorf("the list moved the log:\n%s", h.view())
	}
	if !strings.HasPrefix(after[20], "  app.params") || !strings.HasPrefix(after[25], "▸ account.create") {
		t.Errorf("the list is not right over the input:\n%s", h.view())
	}
	h.program.Send(tea.MouseWheelMsg{X: 10, Y: 5, Button: tea.MouseWheelUp})
	h.settle()
	scrolled := strings.Split(h.view(), "\n")
	if scrolled[0] == after[0] || !reflect.DeepEqual(scrolled[19:29], after[19:29]) {
		t.Errorf("the wheel did not scroll the log behind the list:\n%s", h.view())
	}
	h.press("end", "esc")
	h.waitFor("quack · ")
	if got := strings.Split(h.view(), "\n"); !reflect.DeepEqual(got, before) {
		t.Errorf("closing the list left:\n%s\nwant:\n%s", h.view(), strings.Join(before, "\n"))
	}
}

func TestMouseOverTheListSelectsNothing(t *testing.T) {
	h := startWithConfig(t, newFakeApp(), flat)
	h.typing("app.count")
	h.press("enter")
	h.waitForLog("  50\nCounted · ")
	h.press("up")
	h.waitFor("▸ account.create")

	for range 2 {
		h.mouse(tea.MouseClickMsg{X: 4, Y: 22, Button: tea.MouseLeft})
		h.mouse(tea.MouseMotionMsg{X: 9, Y: 23, Button: tea.MouseLeft})
		h.mouse(tea.MouseReleaseMsg{X: 9, Y: 23, Button: tea.MouseLeft})
	}
	h.settle()

	if got := h.clipboard(); len(got) != 0 {
		t.Errorf("copied %q, want nothing", got)
	}
	if got := h.reversed(); len(got) != 0 {
		t.Errorf("the cells %v are reversed, want none", got)
	}
}

func TestPanelKeepsItsHeightInEveryState(t *testing.T) {
	app := newFakeApp()
	releaseStops := app.holdStops()
	h := start(t, app)
	h.resetHeights()
	check := func(state string) {
		t.Helper()
		if got := h.panelRows(); got != 6 {
			t.Errorf("%s: the panel takes the last %d rows, want 6:\n%s", state, got, h.view())
		}
		if got := h.lines(); got != 30 {
			t.Errorf("%s: the view has %d rows, want the window's 30", state, got)
		}
	}

	check("idle")
	h.typing("acc")
	h.waitFor("3/6")
	check("filtering")
	h.typing("zz")
	h.waitFor("no match")
	check("no match")
	h.press("esc")
	h.typing("/")
	h.waitFor("▸ /cancel")
	check("commands")
	h.press("esc")
	h.typing("account.create")
	h.press("tab")
	h.waitFor("▸ username")
	check("box with fewer params than fit")
	h.press("esc", "esc")
	h.typing("app.params")
	h.press("tab", "tab", "tab", "tab", "tab", "tab", "tab")
	h.waitFor("▸ tag")
	check("box with more params")
	h.press("esc", "esc")
	h.startWatch()
	check("one run")
	h.press("ctrl+c")
	h.waitFor("account.watch canceling… · ctrl+c cancel")
	check("stopping")
	releaseStops()
	h.waitForLog("\ncanceled · ")
	h.waitFor("type or ↑ to find an action")
	h.press("ctrl+c")
	h.waitFor("press ctrl+c again to quit")
	check("quit prompt")

	if heights := h.heights(); !reflect.DeepEqual(heights, []int{6}) {
		t.Errorf("the panel had these heights: %v, want only 6", heights)
	}
}

func TestViewTakesTheAlternateScreenAndTheMouse(t *testing.T) {
	app := newFakeApp()
	m := newModel(newFakeSource(app), newActivity(t.Context()), client.Target{}, client.Wake, "dev", t.TempDir(), config.Default(), nil)

	view := m.View()

	if !view.AltScreen || view.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("the view has AltScreen %v and MouseMode %v, want the alternate screen and cell motion", view.AltScreen, view.MouseMode)
	}
}

func TestLogStartsAtTheTopWithTheDuckAndTheVersionByIt(t *testing.T) {
	h := start(t, newFakeApp())

	h.typing("account.delete")
	h.press("enter")
	h.waitForLog("there is no account")

	rows := strings.Split(h.logText(), "\n")
	for i, row := range rows[:9] {
		if !strings.ContainsAny(row, "▀▄") {
			t.Errorf("log row %d is %q, want the duck", i, row)
		}
	}
	if !strings.HasSuffix(rows[0], "  quack dev") || !strings.HasSuffix(rows[1], "  type to find an action or a recipe · $ recipes only · / commands") {
		t.Errorf("the first rows are %q and %q, want quack's version and what to type by the duck", rows[0], rows[1])
	}
	want := []string{"switched to io.mehow.quack.sample on Pixel 8 · emulator-5554", "", "› account.delete"}
	if got := rows[9:12]; !reflect.DeepEqual(got, want) {
		t.Errorf("the rows after the duck are %q, want the pick and the run", got)
	}
	if got := rows[len(rows)-1]; got != "" {
		t.Errorf("the last row of the log is %q, want blank rows under the output while it is short", got)
	}
}

func TestWheelAndPageUpScrollBackAndEndReturnsToTheNewest(t *testing.T) {
	h := start(t, newFakeApp())
	h.typing("app.count")
	h.press("enter")
	h.waitForLog("  50\nCounted · ")
	bottom := func() string {
		rows := strings.Split(h.logText(), "\n")
		return rows[len(rows)-1]
	}

	h.program.Send(tea.MouseWheelMsg{X: 10, Y: 5, Button: tea.MouseWheelUp})
	h.wait(bottom, func(row string) bool { return row == "  48" }, "a turn of the wheel 3 rows back")
	h.waitFor("scrolled back · end newest · type or ↑ to find an action")
	h.press("pgup")
	h.wait(bottom, func(row string) bool { return row == "  24" }, "pgup a page of 24 rows further back")

	h.typing("account.delete")
	h.press("enter")
	h.waitForRunning()
	if got := bottom(); got != "  24" {
		t.Errorf("new output moved the view scrolled back to %q", got)
	}

	h.press("end")
	h.waitForLog("› account.delete\n[rejected] there is no account · ")
	h.waitFor(" type or ↑ to find an action")
	h.waitForNo("scrolled back")
	h.program.Send(tea.MouseWheelMsg{X: 10, Y: 5, Button: tea.MouseWheelDown})
	h.settle()
	if got := bottom(); !strings.HasPrefix(got, "[rejected]") {
		t.Errorf("the wheel scrolled past the newest output to %q", got)
	}
}

func TestResizeCutsTheLogAgainAtTheNewWidth(t *testing.T) {
	h := start(t, newFakeApp())
	greeting := "Hello, " + strings.Repeat("a", 70) + "!"
	h.typing("app.greet")
	h.press("tab")
	h.typing(strings.Repeat("a", 70))
	h.press("enter", "enter")
	h.waitForLog(greeting)

	h.resize(50, 30)
	h.waitForLog("Hello, " + strings.Repeat("a", 43) + "\n" + strings.Repeat("a", 27) + "! · ")
	for _, row := range strings.Split(h.view(), "\n") {
		if ansi.StringWidth(row) > 50 {
			t.Errorf("row wider than the window: %q", row)
		}
	}
	if got := h.panelRows(); got != 6 {
		t.Errorf("the panel takes the last %d rows, want 6", got)
	}

	h.resize(100, 30)
	h.waitForLog(greeting + " · ")
}

func TestDragOverTwoRowsCopiesTheirTextAndHighlightsExactlyThem(t *testing.T) {
	h := start(t, newFakeApp())
	h.typing("account.create")
	h.press("enter")
	h.typing("alice")
	h.press("enter")
	h.waitForLog("  Started\n  Creating account\nCreated · ")
	started, creating := h.logRow("  Started"), h.logRow("  Creating account")

	h.mouse(tea.MouseClickMsg{X: 2, Y: started, Button: tea.MouseLeft})
	h.mouse(tea.MouseMotionMsg{X: 5, Y: started, Button: tea.MouseLeft})
	h.mouse(tea.MouseMotionMsg{X: 9, Y: creating, Button: tea.MouseLeft})
	h.mouse(tea.MouseReleaseMsg{X: 9, Y: creating, Button: tea.MouseLeft})

	h.waitForCopied("Started\n  Creating")
	h.waitFor(" copied 18 characters")
	want := map[[2]int]bool{}
	for x := 2; x <= 8; x++ {
		want[[2]int{x, started}] = true
	}
	for x := 0; x <= 9; x++ {
		want[[2]int{x, creating}] = true
	}
	if got := h.reversed(); !reflect.DeepEqual(got, want) {
		t.Errorf("the reversed cells are %v, want %v", got, want)
	}

	h.typing("x")
	h.waitFor("> x")
	if got := h.reversed(); len(got) != 0 {
		t.Errorf("a key left the cells %v reversed, want none", got)
	}
	h.advance(noticeTime)
	h.waitFor(" enter run")
}

func TestSelectionJoinsARowCutAtTheWidthWithoutALineBreak(t *testing.T) {
	h := start(t, newFakeApp())
	h.resize(50, 30)
	greeting := "Hello, " + strings.Repeat("a", 70) + "!"
	h.typing("app.greet")
	h.press("tab")
	h.typing(strings.Repeat("a", 70))
	h.press("enter", "enter")
	h.waitForLog("Hello, " + strings.Repeat("a", 43) + "\n" + strings.Repeat("a", 27) + "! · ")
	hello := h.logRow("Hello, ")

	h.mouse(tea.MouseClickMsg{X: 0, Y: hello, Button: tea.MouseLeft})
	h.mouse(tea.MouseMotionMsg{X: 27, Y: hello + 1, Button: tea.MouseLeft})
	h.mouse(tea.MouseReleaseMsg{X: 27, Y: hello + 1, Button: tea.MouseLeft})

	h.waitForCopied(greeting)
}

func TestDoubleClickCopiesTheWordAndAClickClearsTheSelection(t *testing.T) {
	h := start(t, newFakeApp())
	h.typing("account.create")
	h.press("enter")
	h.typing("alice")
	h.press("enter")
	h.waitForLog("  Creating account\nCreated · ")
	creating := h.logRow("  Creating account")

	for range 2 {
		h.mouse(tea.MouseClickMsg{X: 13, Y: creating, Button: tea.MouseLeft})
		h.mouse(tea.MouseReleaseMsg{X: 13, Y: creating, Button: tea.MouseLeft})
	}

	h.waitForCopied("account")
	h.wait(func() string { return strconv.Itoa(len(h.reversed())) }, func(n string) bool { return n == "7" }, "the word highlighted")
	h.mouse(tea.MouseClickMsg{X: 3, Y: creating, Button: tea.MouseLeft})
	h.mouse(tea.MouseReleaseMsg{X: 3, Y: creating, Button: tea.MouseLeft})
	h.wait(func() string { return strconv.Itoa(len(h.reversed())) }, func(n string) bool { return n == "0" }, "no highlight after a click")
	if got := h.clipboard(); len(got) != 1 {
		t.Errorf("copied %q, want only the word", got)
	}
}

func TestListScrollsToKeepTheSelectionInSight(t *testing.T) {
	h := startWithConfig(t, newFakeApp(), flat)
	// A list of 5 rows, so that the 6 actions scroll.
	h.resize(100, 10)
	h.press("up")
	h.waitForOverlayRows(5)

	h.waitFor("  app.greet", "  app.count", "  account.watch", "  account.delete", "▸ account.create")
	h.press("up", "up", "up", "up", "up", "up")
	h.waitFor("▸ app.params", "  account.delete")
	h.waitForNo("account.create")
	h.press("down")
	h.waitFor("▸ app.greet")
}

func TestUpMovesToTheNextMatchAndDownBackToTheBest(t *testing.T) {
	h := start(t, newFakeApp())

	h.typing("count")

	h.waitFor("  account.", "▸ app.count", "> count", "4/6")
	h.press("up")
	h.waitFor("▸ account.", "  app.count")
	h.press("ctrl+n")
	h.waitFor("▸ app.count")
	h.press("ctrl+p", "down", "down")
	h.waitFor("▸ app.count")
	h.typing("x")
	h.waitFor("  no match", "> countx", "0/6")
	h.press("esc")
	h.waitFor("quack · ", "> find an action", "type or ↑ to find an action")
}

func TestSlashListsTheCommands(t *testing.T) {
	h := start(t, newFakeApp())

	h.typing("/")

	h.waitFor("/toggle-data", "/reload", "/pick-device", "/pick-app", "/open-app", "/init-recipes", "/exit", "/clear", "▸ /cancel", "9/9")
	h.waitForNo("app.greet")
	h.typing("x")
	h.waitFor("▸ /exit")
}

func TestQuestionMarkIsAnOrdinaryCharacter(t *testing.T) {
	h := start(t, newFakeApp())

	h.typing("?")

	h.waitFor("no match", "> ?")
}

func TestEnterRunsAnActionWithoutParamsAndPrintsItsError(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)

	h.typing("account.delete")
	h.press("enter")

	h.waitForLog("› account.delete\n[rejected] there is no account · ")
	h.waitFor("quack · ", "> find an action", "type or ↑ to find an action")
	if got, want := app.received(), []wire.Call{{Action: "account.delete"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestRunPrintsABlankLineBeforeItsHeaderAndItsDurationAfterItsEnd(t *testing.T) {
	h := start(t, newFakeApp(), withClock(0, 0, 1234*time.Millisecond, 0, 0, 65*time.Second))

	h.typing("account.delete")
	h.press("enter")
	h.waitForLog("there is no account · 1.23s")
	h.typing("account.delete")
	h.press("enter")

	h.waitForLog("\n› account.delete\n[rejected] there is no account · 1.23s\n" +
		"\n› account.delete\n[rejected] there is no account · 1m05s")
}

func TestEnterOnAnActionWithARequiredParamOpensTheBox(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)

	h.typing("app.greet")
	h.press("enter")

	h.waitFor("app.greet · Say hello", "▸ name", "Required. Who to greet", "  times", "Default: 1.", "› app.greet")
	h.typing("Bob Smith")
	h.waitFor(`› app.greet name='Bob Smith'`)
	h.press("enter")
	h.waitFor("▸ times")
	h.press("enter")
	h.waitForLog("› app.greet name='Bob Smith'\nHello, Bob Smith! · ")
	h.waitFor("> find an action")
	want := []wire.Call{{Action: "app.greet", Args: map[string]wire.RawArg{"name": {"Bob Smith"}}}}
	if got := app.received(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestMissingRequiredParamBlocksTheRunAndTurnsItsNoteRed(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)
	red := lipgloss.NewStyle().Foreground(lipgloss.Red).Render("Required. Who to greet")

	h.typing("app.greet")
	h.press("enter")
	h.waitFor("▸ name")
	h.press("enter")
	h.waitFor("▸ times")
	h.press("enter")

	h.waitFor("▸ name")
	if !strings.Contains(h.raw(), red) {
		t.Errorf("the note of the missing param is not red:\n%q", h.raw())
	}
	h.typing("x")
	h.waitFor("› app.greet name=x")
	if strings.Contains(h.raw(), red) {
		t.Errorf("the note stayed red after the param was set")
	}
	if got := app.received(); len(got) != 0 {
		t.Errorf("calls: got %v, want none", got)
	}
}

func TestEscAndCtrlCCloseTheBoxAndKeepTheQuery(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)
	h.typing("app.p")
	h.waitFor("▸ app.params")

	for _, key := range []string{"esc", "ctrl+c"} {
		h.press("tab")
		h.waitFor("app.params · Show")
		h.press(key)

		h.waitFor("▸ app.params", "> app.p")
	}
	h.waitFor("enter run")
	if got := app.received(); len(got) != 0 {
		t.Errorf("calls: got %v, want none", got)
	}
}

func TestChoicesCycleWithLeftAndRight(t *testing.T) {
	h := start(t, newFakeApp())
	h.typing("app.params")
	h.press("tab", "tab", "tab", "tab", "tab")
	h.waitFor("▸ flag   ‹not set› true false")

	h.press("right")
	h.waitFor("not set ‹true› false", "› app.params flag=true")
	h.press("right", "right")
	h.waitFor("‹not set› true false", "› app.params\n")
	h.press("left")
	h.waitFor("not set true ‹false›", "› app.params flag=false")
}

func TestStringListSplitsOnCommasAndKeepsEscapedOnes(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)
	h.typing("app.params")
	h.press("tab", "tab", "tab", "tab", "tab", "tab", "tab")
	h.waitFor("▸ tag")

	h.typing(`a, b\,c, ,d `)

	h.waitFor("› app.params tag=a tag=b,c tag=d")
	h.press("enter")
	h.waitForLog("› app.params tag=a tag=b,c tag=d\ndone · ")
	want := []wire.Call{{Action: "app.params", Args: map[string]wire.RawArg{"tag": {"a", "b,c", "d"}}}}
	if got := app.received(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestBoxSendsOnlyTheParamsTheUserSet(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)

	h.typing("app.params")
	h.press("tab")
	h.waitFor("▸ text")
	h.typing("hi")
	h.press("enter", "enter", "enter", "enter")
	h.waitFor("▸ flag")
	h.press("right", "enter")
	h.waitFor("▸ theme")
	h.press("left", "enter")
	h.waitFor("▸ tag")
	h.typing("a, b c")
	h.press("enter")

	h.waitForLog("› app.params text=hi flag=true theme=dark tag=a tag='b c'\nhi · ")
	want := []wire.Call{{Action: "app.params", Args: map[string]wire.RawArg{
		"text":  {"hi"},
		"flag":  {"true"},
		"theme": {"dark"},
		"tag":   {"a", "b c"},
	}}}
	if got := app.received(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestFocusedParamWithHintsTakesTheBoxWithTheHintsInTheAppsOrder(t *testing.T) {
	h := start(t, withNameHints(newFakeApp(), "Alice", "Albert", "Bob"))

	h.typing("app.greet")
	h.press("enter")

	h.waitFor("app.greet · Say hello", "  Bob\n", "  Albert\n", "  Alice\n", "▸ name", "Required. Who to greet", "3/3",
		"› app.greet")
	h.waitForNo("times")
	h.press("enter")
	h.waitFor("  name", "▸ times", "Default: 1.")
	h.waitForNo("Albert")
}

func TestHintsNarrowToTheTextAndSayWhenNoneMatches(t *testing.T) {
	h := start(t, withNameHints(newFakeApp(), "Alice", "Albert", "Bob"))
	h.typing("app.greet")
	h.press("enter")
	h.waitFor("3/3")

	h.typing("bo")
	h.waitFor("▸ Bob\n", "1/3")
	h.waitForNo("Albert")
	h.typing("zz")
	h.waitFor("no match", "0/3")
}

func TestTypingSelectsTheBestHintSoThatEnterPicksIt(t *testing.T) {
	app := withNameHints(newFakeApp(), "Alice", "Albert", "Bob")
	h := start(t, app)
	h.typing("app.greet")
	h.press("enter")
	h.waitFor("3/3")

	h.typing("Bo")
	h.waitFor("▸ Bob", "› app.greet name=Bob\n", "enter pick · ↑↓ select · esc deselect")
	h.press("enter")
	h.waitFor("▸ times", "› app.greet name=Bob\n")
	h.press("enter")

	h.waitForLog("› app.greet name=Bob\nHello, Bob! · ")
	want := []wire.Call{{Action: "app.greet", Args: map[string]wire.RawArg{"name": {"Bob"}}}}
	if got := app.received(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestDownFromTheBestHintKeepsTheTextAsTyped(t *testing.T) {
	app := withNameHints(newFakeApp(), "Alice", "Albert", "Bob")
	h := start(t, app)
	h.typing("app.greet")
	h.press("enter")
	h.waitFor("3/3")

	h.typing("Bo")
	h.waitFor("▸ Bob", "› app.greet name=Bob\n")
	h.press("down")
	h.waitFor("  Bob", "› app.greet name=Bo\n", "↑ hints · enter next")
	h.press("up")
	h.waitFor("▸ Bob", "› app.greet name=Bob\n")
	h.press("down", "enter")
	h.waitFor("▸ times", "› app.greet name=Bo\n")
	h.press("enter")

	h.waitForLog("› app.greet name=Bo\nHello, Bo! · ")
	want := []wire.Call{{Action: "app.greet", Args: map[string]wire.RawArg{"name": {"Bo"}}}}
	if got := app.received(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestTextThatNoHintMatchesStaysAsTyped(t *testing.T) {
	app := withNameHints(newFakeApp(), "Alice", "Albert", "Bob")
	h := start(t, app)
	h.typing("app.greet")
	h.press("enter")
	h.waitFor("3/3")

	h.typing("Bobx")
	h.waitFor("no match", "0/3", "› app.greet name=Bobx\n", "↑ hints · enter next")
	h.press("enter", "enter")

	h.waitForLog("› app.greet name=Bobx\nHello, Bobx! · ")
	want := []wire.Call{{Action: "app.greet", Args: map[string]wire.RawArg{"name": {"Bobx"}}}}
	if got := app.received(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestEmptyTextSelectsNoHint(t *testing.T) {
	h := start(t, withNameHints(newFakeApp(), "Alice", "Albert", "Bob"))
	h.typing("app.greet")
	h.press("enter")
	h.waitFor("3/3", "› app.greet\n", "↑ hints · enter next")
	h.waitForNo("▸ A")

	h.typing("B")
	h.waitFor("▸ Bob")
	h.press("backspace")
	h.waitFor("  Bob\n", "› app.greet\n", "↑ hints · enter next")
	h.waitForNo("▸ Bob")
}

func TestCallPreviewsTheSelectedHintInsteadOfTheText(t *testing.T) {
	h := start(t, withNameHints(newFakeApp(), "Alice", "Albert", "Bob"))
	h.typing("app.greet")
	h.press("enter")
	h.waitFor("3/3", "› app.greet\n")

	h.press("up")
	h.waitFor("▸ Alice", "› app.greet name=Alice\n")
	h.press("up")
	h.waitFor("▸ Albert", "› app.greet name=Albert\n")
	h.press("esc")
	h.waitFor("▸ name", "› app.greet\n", "↑ hints")
}

func TestUpPastTheFirstHintSelectsNone(t *testing.T) {
	h := start(t, withNameHints(newFakeApp(), "Alice", "Albert", "Bob"))
	h.typing("app.greet")
	h.press("enter")
	h.waitFor("3/3")

	h.press("up", "up", "up", "up")
	h.waitFor("▸ Bob")
	h.press("down", "down", "down")
	h.waitFor("↑ hints · enter next")
	h.waitForNo("▸ A")
	h.press("up")
	h.waitFor("▸ Alice")
}

func TestEscDeselectsTheHintAndThenClosesTheBox(t *testing.T) {
	h := start(t, withNameHints(newFakeApp(), "Alice", "Albert", "Bob"))
	h.typing("app.greet")
	h.press("enter")
	h.waitFor("3/3")

	h.press("up")
	h.waitFor("▸ Alice")
	h.press("esc")
	h.waitFor("  Alice\n", "▸ name", "↑ hints · enter next")
	h.press("esc")
	h.waitFor("▸ app.greet", "> app.greet")
}

func TestHintsScrollToKeepTheSelectionInSight(t *testing.T) {
	var names []string
	for i := range 20 {
		names = append(names, "user"+strconv.Itoa(10+i))
	}
	h := start(t, withNameHints(newFakeApp(), names...))
	h.typing("app.greet")
	h.press("enter")
	h.waitFor("  user17", "  user10", "20/20")
	h.waitForNo("user18")

	h.press("up", "up", "up", "up", "up", "up", "up", "up", "up", "up")

	h.waitFor("▸ user19", "  user12")
	h.waitForNo("user11")
}

func TestHintsOfANonStringParamDoNotShow(t *testing.T) {
	app := newFakeApp()
	app.described[0].Params[1].Hints = []string{"1", "2"}
	h := start(t, app)
	h.typing("app.greet")
	h.press("enter", "enter")

	h.waitFor("  name", "▸ times", "enter next · shift+tab back")
	h.waitForNo("2/2")
}

func TestDataOfARunPrintsIndentedUnderItsMessage(t *testing.T) {
	app := newFakeApp()
	app.setActions(append(app.actions(), wire.Action{ID: "account.show", Description: "Show the account"}))
	h := start(t, app, withClock(0, 0, 1234*time.Millisecond))

	h.typing("account.show")
	h.press("enter")

	h.waitForLog("\n› account.show\nSigned in as alice · 1.23s\n" +
		"  {\n" +
		"    \"username\": \"alice\",\n" +
		"    \"tags\": [\n" +
		"      \"a\"\n" +
		"    ]\n" +
		"  }")
}

func TestNotificationsOfARunPrintUnderItsCommand(t *testing.T) {
	h := start(t, newFakeApp())

	h.typing("account.create")
	h.press("enter")
	h.typing("alice")
	h.press("enter")

	h.waitForLog("\n› account.create username=alice\n  Started\n  Creating account\nCreated · ")
}

func TestDataOfANotificationPrintsIndentedUnderIt(t *testing.T) {
	app := newFakeApp()
	app.setActions(append(app.actions(), wire.Action{ID: "sync.pair", Description: "Pair with another device"}))
	h := start(t, app)

	h.typing("sync.pair")
	h.press("enter")

	h.waitForLog("\n› sync.pair\n  Pairing code\n" +
		"    {\n" +
		"      \"code\": \"abc123\"\n" +
		"    }\nPaired · ")
}

func TestToggleDataHidesTheDataOfNotificationsAndResultsUntilToggledBack(t *testing.T) {
	app := newFakeApp()
	app.setActions(append(app.actions(), wire.Action{ID: "sync.pair", Description: "Pair with another device"}))
	h := start(t, app)

	h.typing("/toggle-data")
	h.press("enter")
	h.waitForLog("hiding the JSON data of notifications and results")
	h.typing("sync.pair")
	h.press("enter")
	h.waitForLog("\n› sync.pair\n  Pairing code\nPaired · ")
	h.typing("/toggle-data")
	h.press("enter")
	h.waitForLog("showing the JSON data of notifications and results")
	h.typing("sync.pair")
	h.press("enter")

	h.waitForLog("\n› sync.pair\n  Pairing code\n    {\n      \"code\": \"abc123\"\n    }\nPaired · ")
}

func TestShowDataFalseInTheConfigHidesTheDataUntilToggled(t *testing.T) {
	app := newFakeApp()
	app.setActions(append(app.actions(), wire.Action{ID: "sync.pair", Description: "Pair with another device"}))
	h := startWithConfig(t, app, "show-data = false\n")

	h.typing("sync.pair")
	h.press("enter")
	h.waitForLog("\n› sync.pair\n  Pairing code\nPaired · ")
	h.typing("/toggle-data")
	h.press("enter")
	h.waitForLog("showing the JSON data of notifications and results")
	h.typing("sync.pair")
	h.press("enter")

	h.waitForLog("\n› sync.pair\n  Pairing code\n    {\n      \"code\": \"abc123\"\n    }\nPaired · ")
	if got := strings.Count(h.logText(), "abc123"); got != 1 {
		t.Errorf("the data shows %d times, want only in the run after the toggle:\n%s", got, h.logText())
	}
}

func TestUnknownConfigKeyGoesIntoTheLogAndTheOtherKeysApply(t *testing.T) {
	h := startWithConfig(t, newFakeApp(), "show-dta = false\ngroup-namespaces = false\n")

	h.waitForLog("config.toml:1: unknown key 'show-dta'\nswitched to io.mehow.quack.sample")
	h.press("up")
	h.waitFor("  app.params", "▸ account.create", "6/6")
}

func TestConfigValueOfTheWrongTypeGoesIntoTheLogAndKeepsItsDefault(t *testing.T) {
	app := newFakeApp()
	app.setActions(append(app.actions(), wire.Action{ID: "sync.pair", Description: "Pair with another device"}))
	h := startWithConfig(t, app, "show-data = false\ngroup-namespaces = \"no\"\n")

	h.waitForLog("config.toml:2: 'group-namespaces' must be true or false")
	h.press("up")
	h.waitFor("  app.        3", "▸ account.    3", "3/7")
	h.press("esc")
	h.typing("sync.pair")
	h.press("enter")
	h.waitForLog("\n› sync.pair\n  Pairing code\nPaired · ")
	if strings.Contains(h.logText(), "abc123") {
		t.Errorf("the data shows although show-data is false:\n%s", h.logText())
	}
}

func TestConfigThatDoesNotParseGoesIntoTheLogAndEveryKeyKeepsItsDefault(t *testing.T) {
	app := newFakeApp()
	app.setActions(append(app.actions(), wire.Action{ID: "sync.pair", Description: "Pair with another device"}))
	h := startWithConfig(t, app, "show-data = false\ngroup-namespaces =\n")

	h.waitForLog("config.toml:2:19: unexpected character U+000A at start of value")
	h.typing("sync.pair")
	h.press("enter")
	h.waitForLog("\n› sync.pair\n  Pairing code\n    {\n      \"code\": \"abc123\"\n    }\nPaired · ")
	h.press("up")
	h.waitFor("▸ account.    3", "3/7")
	// Recipes also fail on the file, and say so with its full path.
	if got := strings.Count(h.logText(), "\nconfig.toml:"); got != 1 {
		t.Errorf("the log holds %d settings problems, want one:\n%s", got, h.logText())
	}
}

func TestOutputKeepsItsOrder(t *testing.T) {
	h := start(t, newFakeApp())
	var want []string
	for i := 1; i <= 50; i++ {
		want = append(want, "  "+strconv.Itoa(i))
	}

	h.resize(100, 100)
	h.typing("app.count")
	h.press("enter")

	h.waitForLog("› app.count\n" + strings.Join(want, "\n") + "\nCounted")
}

func TestActionsDoNotRunWhileARunRunsButTheBoxOpens(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)
	h.startWatch()

	h.typing("account.delete")
	h.press("enter")
	h.waitFor("> account.delete", "cancel the running action first (ctrl+c)")
	h.press("esc")
	h.typing("account.create")
	h.press("enter")
	h.waitFor("▸ username")
	h.typing("alice")
	h.press("enter")
	h.waitFor("▸ username", "cancel the running action first (ctrl+c)")
	h.press("backspace")
	h.waitFor("▸ username", "› account.create username=alic\n")
	if got, want := app.received(), []wire.Call{{Action: "account.watch"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls while the watch runs: got %v, want %v", got, want)
	}

	// ctrl+c stops the watch and keeps the box, and then the same Enter runs the action.
	h.press("ctrl+c")
	h.waitForLog("\ncanceled · ")
	h.advance(noticeTime)
	h.waitFor("▸ username", "› account.create username=alic", "enter next")
	h.press("enter")
	h.waitForLog("\n› account.create username=alic\n  Started\n  Creating account\nCreated · ")
	h.typing("account.delete")
	h.press("enter")
	h.waitForLog("\n› account.delete\n[rejected] there is no account · ")
	want := []wire.Call{
		{Action: "account.watch"},
		{Action: "account.create", Args: map[string]wire.RawArg{"username": {"alic"}}},
		{Action: "account.delete"},
	}
	if got := app.received(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestEscNeverStopsARun(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)
	h.startWatch()

	h.press("esc", "esc")
	app.signIn("alice")

	h.waitForLog("  Signed in as alice")
	h.waitFor("account.watch · ctrl+c cancel action")
	select {
	case <-app.canceled(0):
		t.Fatal("esc stopped the watch")
	default:
	}
}

func TestCtrlCStopsTheRunAndKeepsTheTextThenClearsThenAsksThenQuits(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)
	h.startWatch()
	h.typing("abc")
	h.waitFor("> abc")

	h.press("ctrl+c")
	h.waitForLog("› account.watch\n  Signed out\ncanceled · ")
	h.waitFor("> abc", "enter run")
	h.press("ctrl+c")
	h.waitFor("> find an action", "type or ↑ to find an action")
	h.press("ctrl+c")
	h.waitFor("press ctrl+c again to quit")
	h.waitForRunning()
	h.press("ctrl+c")

	h.waitForExit()
}

func TestCtrlCWhileTheRunStopsAsksToQuit(t *testing.T) {
	app := newFakeApp()
	release := app.holdStops()
	h := start(t, app)
	t.Cleanup(release)
	h.startWatch()
	h.typing("abc")

	h.press("ctrl+c")
	h.waitFor("account.watch canceling…")
	h.press("ctrl+c")

	h.waitFor("> abc", "press ctrl+c again to quit")
	h.press("ctrl+c")
	h.waitForExit()
}

func TestQuitPromptEndsAfterTwoSeconds(t *testing.T) {
	h := start(t, newFakeApp())

	h.press("ctrl+c")
	h.waitFor("press ctrl+c again to quit")

	h.advance(noticeTime - time.Millisecond)
	h.waitFor("press ctrl+c again to quit")
	h.advance(time.Millisecond)
	h.waitFor("type or ↑ to find an action")
	h.press("ctrl+c")
	h.waitFor("press ctrl+c again to quit")
	h.waitForRunning()
}

func TestCtrlDQuitsOnlyOnAnEmptyQuery(t *testing.T) {
	h := start(t, newFakeApp())
	h.typing("ab")
	h.press("left")

	h.press("ctrl+d")
	h.waitFor("> a ")
	h.waitForRunning()
	h.press("esc", "ctrl+d")

	h.waitForExit()
}

func TestDollarListsTheRecipesByName(t *testing.T) {
	h := start(t, newFakeApp(), withRecipes(t, `
import quack from "quack"

export const login = quack.recipe({
  description: "Log in as the test user",
  run() { quack.runAction("app.greet", { name: "Bob" }) },
})
export const clean = quack.recipe({ run() { quack.runAction("account.delete") } })
`))

	h.typing("$")

	h.waitFor("  login ƒ   Log in as the test user", "▸ clean ƒ", "2/2")
	h.waitForNo("app.greet")
	h.typing("lg")
	h.waitFor("▸ login ƒ", "1/2")
	h.press("tab")
	h.waitFor("▸ login ƒ", "> $lg")
}

func TestRecipesShareTheListWithTheActionsMarkedByADimF(t *testing.T) {
	h := start(t, newFakeApp(), withRecipes(t, `
import quack from "quack"

export const login = quack.recipe({
  description: "Log in as the test user",
  run() { quack.runAction("app.greet", { name: "Bob" }) },
})
export const clean = quack.recipe({ run() { quack.runAction("account.delete") } })
`))
	h.waitFor("8/8")

	h.typing("lo")

	h.waitFor("▸ login ƒ   Log in as the test user", "> lo", "1/8")
	if mark := statusStyle.Render(" ƒ"); !strings.Contains(h.raw(), mark) {
		t.Errorf("the recipe's mark is not dim:\n%q", h.raw())
	}
	h.press("backspace", "backspace")
	h.typing("acc")
	h.waitFor("▸ account.", "3/8")
	h.waitForNo("ƒ")
}

func TestRecipeAndActionOfTheSameNameBothShowWithTheRecipeFirstAndEachRunsItself(t *testing.T) {
	app := newFakeApp()
	h := start(t, app, withRecipes(t, `
import quack from "quack"

export const count = quack.recipe({
  name: "app.count",
  description: "Count with a recipe",
  run() { quack.runAction("account.delete") },
})
`))

	h.typing("app.count")
	h.waitFor("  app.count     Count to 50", "▸ app.count ƒ   Count with a recipe", "2/7")
	h.press("enter")
	h.waitForLog("\n› --recipe app.count\n› account.delete\n")
	h.waitFor("type or ↑ to find an action")
	h.typing("app.count")
	h.press("up")
	h.waitFor("▸ app.count     Count to 50")
	h.press("enter")

	h.waitForLog("  50\nCounted · ")
	want := []wire.Call{{Action: "account.delete"}, {Action: "app.count"}}
	if got := app.received(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestReloadKeepsTheRecipeOrTheActionOfTheSameNameSelected(t *testing.T) {
	h := start(t, newFakeApp(), withRecipes(t, `
import quack from "quack"

export const count = quack.recipe({ name: "app.count", run() {} })
`))
	h.typing("app.count")
	h.waitFor("  app.count  ", "▸ app.count ƒ")

	h.press("ctrl+r")
	h.waitForLog("reloaded 6 actions and 1 recipes")
	h.waitFor("  app.count  ", "▸ app.count ƒ")
	h.press("up")
	h.waitFor("▸ app.count     Count to 50")
	h.press("ctrl+r")
	h.waitForLog("reloaded 6 actions and 1 recipes\nreloaded 6 actions and 1 recipes")

	h.waitFor("▸ app.count     Count to 50", "  app.count ƒ")
}

func TestBoxOfAnActionPreviewsItsIdAndARecipesItsCommandLine(t *testing.T) {
	h := start(t, newFakeApp(), withRecipes(t, `
import quack from "quack"

export const login = quack.recipe({
  description: "Log in",
  params: { user: { type: "string" } },
  run() {},
})
`))

	h.typing("login")
	h.press("tab")
	h.waitFor("login ƒ · Log in", "▸ user", "› --recipe login\n")
	h.typing("qa")
	h.waitFor("› --recipe login user=qa\n")
	h.press("esc", "esc")
	h.typing("app.greet")
	h.press("tab")
	h.typing("Bob")
	h.waitFor("app.greet · Say hello", "› app.greet name=Bob\n")
}

func TestDollarWithoutRecipesSaysWhereToWriteThem(t *testing.T) {
	h := start(t, newFakeApp())
	h.resize(300, 30)

	h.typing("$")

	h.waitFor("  No recipes. /init-recipes starts .quack/ here, or map the app in config.toml", "0/0")
}

func TestBrokenRecipeFileSaysWhyAndLeavesTheActions(t *testing.T) {
	h := start(t, newFakeApp(), withRecipes(t, "export const a = ("))
	h.resize(300, 30)

	h.waitForLog("switched to io.mehow.quack.sample", "io.mehow.quack.sample.ts:1:19: Unexpected end of file")
	h.waitFor("6/6")
	h.press("up")
	h.waitFor("▸ account.", "2/6")
	h.press("esc")
	h.typing("$")
	h.waitFor("No recipes. /init-recipes starts .quack/ here", "0/0")
}

func TestRecipeWarningsGoIntoTheLogAndTheOtherRecipesLoad(t *testing.T) {
	h := start(t, newFakeApp(), func(*model) {
		path := writeRecipes(t, `import quack from "quack"
export const clean = quack.recipe({ run() {} })`)
		project := filepath.Dir(filepath.Dir(path))
		mapping := "[recipes]\n'.*' = [\"io.mehow.quack.sample.ts\", \"gone.ts\"]\n"
		if err := os.WriteFile(filepath.Join(project, "quack.toml"), []byte(mapping), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	h.resize(300, 30)

	h.waitForLog("switched to io.mehow.quack.sample", "quack.toml: [recipes] maps io.mehow.quack.sample to gone.ts, which does not exist")
	h.typing("$")
	h.waitFor("▸ clean ƒ", "1/1")
}

func TestEnterRunsARecipeWritingEachStepUnderItsCommandAndWhatItLogs(t *testing.T) {
	app := newFakeApp()
	h := start(t, app, withRecipes(t, `
import quack from "quack"

export const setup = quack.recipe({
  run() {
    quack.openApp()
    quack.wait("10ms")
    quack.runAction("account.create", { username: "alice" })
    console.log("created", 1)
    quack.runAction("app.greet", { times: 2, name: "Bob" })
    quack.shell("echo hi")
  },
})
`))

	h.typing("$setup")
	h.press("enter")

	h.waitForLog("\n› --recipe setup\n› /open-app\nstarting io.mehow.quack.sample…\ndone · ",
		"\n› wait 10ms\ndone · ",
		"\n› account.create username=alice\n  Started\n  Creating account\nCreated · ",
		"\ncreated 1\n› app.greet name=Bob times=2\nHello, Bob! · ",
		"\n› shell echo hi\nran echo hi · ")
	h.waitFor("> find an action", "type or ↑ to find an action")
	want := []wire.Call{
		{Action: "account.create", Args: map[string]wire.RawArg{"username": {"alice"}}},
		{Action: "app.greet", Args: map[string]wire.RawArg{"name": {"Bob"}, "times": {"2"}}},
	}
	if got := app.received(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestARecipeThatRunsAnotherShowsItsStepsAndWhatItReturnedUnlessDataIsHidden(t *testing.T) {
	app := newFakeApp()
	h := start(t, app, withRecipes(t, `
import quack from "quack"

const greet = quack.recipe({
  name: "greet",
  params: { name: { type: "string", required: true } },
  run({ name }) { return quack.runAction("app.greet", { name }).message },
})

export const setup = quack.recipe({
  run() { return { greeting: quack.runRecipe(greet, { name: "Bob" }) } },
})
`))

	h.typing("$setup")
	h.press("enter")
	h.waitForLog("\n› --recipe setup\n› recipe greet name=Bob\n› app.greet name=Bob\nHello, Bob! · ",
		"\n  {\n    \"greeting\": \"Hello, Bob!\"\n  }")
	h.waitFor("> find an action")
	h.typing("/toggle-data")
	h.press("enter")
	h.waitForLog("hiding the JSON data of notifications and results")
	h.typing("$setup")
	h.press("enter")

	h.waitForLog("hiding the JSON data of notifications and results\n\n› --recipe setup\n› recipe greet name=Bob\n› app.greet name=Bob\nHello, Bob! · ")
	h.waitFor("> find an action")
	if _, after, _ := strings.Cut(h.logText(), "hiding the JSON data"); strings.Contains(after, "greeting") {
		t.Errorf("got the data after /toggle-data hid it:\n%s", after)
	}
}

func TestEnterOnARecipeWithARequiredParamOpensTheBoxWithItsHintsAndRunsWithTheArgs(t *testing.T) {
	app := newFakeApp()
	h := start(t, app, withRecipes(t, `
import quack from "quack"

export const login = quack.recipe({
  description: "Log in",
  params: {
    user: { type: "string", required: true, hints: ["alice", "bob"] },
    times: { type: "int", default: 1 },
  },
  run({ user, times }) { quack.runAction("app.greet", { name: user, times }) },
})
`))

	h.typing("$login")
	h.press("enter")
	h.waitFor("login ƒ · Log in", "bob", "alice", "▸ user", "2/2", "› --recipe login\n")
	h.typing("bo")
	h.waitFor("▸ bob", "▸ user  bo", "1/2", "› --recipe login user=bob\n")
	h.press("enter")
	h.waitFor("▸ times", "› --recipe login user=bob\n")
	h.press("enter")

	h.waitForLog("\n› --recipe login\n› app.greet name=bob times=1\nHello, bob! · ")
	want := []wire.Call{{Action: "app.greet", Args: map[string]wire.RawArg{"name": {"bob"}, "times": {"1"}}}}
	if got := app.received(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestEnterOnAnActionWithOnlyOptionalParamsOpensTheBoxAndRunsWithoutThem(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)

	h.typing("app.params")
	h.press("enter")
	h.waitFor("▸ text", "› app.params\n")
	h.press("enter", "enter", "enter", "enter", "enter", "enter", "enter")

	h.waitForLog("› app.params\ndone · ")
	if got := app.received(); len(got) != 1 || len(got[0].Args) != 0 {
		t.Errorf("calls: got %v, want app.params without args", got)
	}
}

func TestEnterOnARecipeWithOnlyOptionalParamsOpensTheBoxToo(t *testing.T) {
	h := start(t, newFakeApp(), withRecipes(t, `
import quack from "quack"

export const pair = quack.recipe({
  params: { code: { type: "string" } },
  run({ code }) { console.log("code", code ?? "from env") },
})
`))

	h.typing("$pair")
	h.press("enter")
	h.waitFor("▸ code", "› --recipe pair\n")
	h.press("enter")

	h.waitForLog("\n› --recipe pair\ncode from env\n")
}

func TestFailedRecipeEndsWithTheErrorAndWhereItThrew(t *testing.T) {
	app := newFakeApp()
	h := start(t, app, withRecipes(t, `
import quack from "quack"

export const clean = quack.recipe({
  run() {
    quack.runAction("account.delete")
    quack.runAction("app.count")
  },
})
`))

	h.typing("$clean")
	h.press("enter")

	h.waitForLog("\n› --recipe clean\n› account.delete\n[rejected] there is no account · ", "\n    at run (io.mehow.quack.sample.ts:6:20)\n")
	h.waitFor("type or ↑ to find an action")
	if got, want := app.received(), []wire.Call{{Action: "account.delete"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
	if got := strings.Count(h.logText(), "there is no account"); got != 1 {
		t.Errorf("the error shows %d times, want once:\n%s", got, h.logText())
	}
}

func TestARecipeThatCatchesAFailedStepGoesOn(t *testing.T) {
	app := newFakeApp()
	h := start(t, app, withRecipes(t, `
import quack from "quack"

export const careful = quack.recipe({
  run() {
    try {
      quack.runAction("account.delete")
    } catch (e) {
      console.log("kept going")
    }
    quack.wait("1m")
  },
})
`))

	h.typing("$careful")
	h.press("enter")

	h.waitForLog("\n› --recipe careful\n› account.delete\n[rejected] there is no account · ", "\nkept going\n› wait 1m0s\n")
	h.waitFor("careful ƒ · wait 1m0s")
	h.press("ctrl+c")
	h.waitForLog("\ncanceled · ")
}

func TestFooterNamesTheRecipeAndItsStepAndCtrlCStopsTheRecipe(t *testing.T) {
	app := newFakeApp()
	h := start(t, app, withRecipes(t, `
import quack from "quack"

export const watch = quack.recipe({
  run() {
    quack.wait("1ms")
    quack.runAction("account.watch")
    quack.runAction("app.count")
  },
})
`))

	h.typing("$watch")
	h.press("enter")
	h.waitForLog("› account.watch\n  Signed out")
	h.waitFor("watch ƒ · account.watch · ctrl+c cancel action")
	h.press("ctrl+c")

	h.waitForLog("\n› --recipe watch\n› wait 1ms\ndone · ", "\n› account.watch\n  Signed out\ncanceled · ")
	h.waitFor("type or ↑ to find an action")
	if strings.Contains(h.logText(), "app.count") {
		t.Errorf("the log runs the step after the canceled one:\n%s", h.logText())
	}
	if got, want := app.received(), []wire.Call{{Action: "account.watch"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestCtrlCStopsARecipeBetweenSteps(t *testing.T) {
	h := start(t, newFakeApp(), withRecipes(t, `
import quack from "quack"

export const spin = quack.recipe({ run() { while (true) {} } })
`))

	h.typing("$spin")
	h.press("enter")
	h.waitFor("spin ƒ · ctrl+c cancel action")
	h.press("ctrl+c")

	h.waitForLog("\n› --recipe spin\ncanceled · ")
	h.waitFor("type or ↑ to find an action")
}

func TestRecipesDoNotRunWhileARunRuns(t *testing.T) {
	app := newFakeApp()
	h := start(t, app, withRecipes(t, `
import quack from "quack"

export const clean = quack.recipe({ run() { quack.runAction("account.delete") } })
`))
	h.startWatch()

	h.typing("$clean")
	h.press("enter")

	h.waitFor("> $clean", "cancel the running action first (ctrl+c)")
	if got, want := app.received(), []wire.Call{{Action: "account.watch"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestReloadReadsTheRecipesAgain(t *testing.T) {
	h := start(t, newFakeApp())
	h.resize(300, 30)
	h.typing("$")
	h.waitFor("No recipes", "0/0")

	writeRecipes(t, `import quack from "quack"
export const clean = quack.recipe({ run() {} })`)
	h.press("ctrl+r")

	h.waitForLog("reloaded 6 actions and 1 recipes")
	h.waitFor("▸ clean ƒ", "1/1")
	writeRecipes(t, "export const a = (")
	h.press("ctrl+r")
	h.waitForLog("io.mehow.quack.sample.ts:1:19: Unexpected end of file\nreloaded 6 actions\n")
	h.waitFor("No recipes", "0/0")
}

func TestReloadKeepsTheSelectedAction(t *testing.T) {
	app := newFakeApp()
	h := startWithConfig(t, app, flat)
	h.press("up", "up", "up")
	h.waitFor("▸ account.watch")

	app.setActions(append([]wire.Action{{ID: "aaa.new", Description: "A new action"}}, app.actions()...))
	h.press("ctrl+r")

	h.waitForLog("reloaded 7 actions and 0 recipes")
	h.waitFor("▸ account.watch", "aaa.new", "7/7")

	app.setActions(append([]wire.Action{{ID: "aab.newer", Description: "A newer action"}}, app.actions()...))
	h.typing("/reload")
	h.press("enter")
	h.waitForLog("reloaded 8 actions")
	h.waitFor("8/8")
	h.press("up")
	h.waitFor("aab.newer", "▸ aaa.new", "8/8")
}

func TestFailedReloadKeepsTheActions(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)

	app.setActions(nil)
	h.press("ctrl+r")

	h.waitForLog("reload: the app is gone")
	h.waitFor("6/6")
	h.press("up")
	h.waitFor("▸ account.", "2/6")
}

func TestClearLeavesOnlyTheDuck(t *testing.T) {
	h := start(t, newFakeApp())
	h.press("ctrl+r")
	h.waitForLog("reloaded 6 actions")

	h.press("ctrl+l")

	h.waitForOnlyHeader()
	h.press("ctrl+r")
	h.waitForLog("reloaded 6 actions")
	h.typing("/clear")
	h.press("enter")
	h.waitForOnlyHeader()
}

func TestStatusLinesGoIntoTheLog(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)

	h.typing("/open-app")
	h.press("enter")

	h.waitForLog("starting io.mehow.quack.sample…\nshowed the app's main screen")
}

func TestInitRecipesStartsTheRecipesOfTheAppAndListsThem(t *testing.T) {
	h := start(t, newFakeApp())
	h.resize(300, 30)

	h.typing("/init-recipes")
	h.press("enter")

	project := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), ".quack")
	h.waitForLog(
		"wrote "+filepath.Join(recipe.Dir(), "quack.d.ts"),
		"wrote "+filepath.Join(recipe.Dir(), "tsconfig.json"),
		"wrote "+filepath.Join(project, "recipes", "quack.d.ts"),
		"wrote "+filepath.Join(project, "recipes", "tsconfig.json"),
		"wrote "+filepath.Join(project, "quack.toml"),
		"wrote "+filepath.Join(project, "recipes", "recipes.ts"),
	)
	h.typing("$")
	h.waitFor("▸ open ƒ", "Show the app's main screen", "1/1")
	h.press("esc")
	h.typing("/init-recipes")
	h.press("enter")
	h.waitForLog("the recipe files are up to date")
}

func TestCtrlOOpensTheAppEvenDuringARun(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)
	h.startWatch()

	h.press("ctrl+o")

	h.waitForLog("starting io.mehow.quack.sample…\nshowed the app's main screen")
	h.waitFor("account.watch · ctrl+c cancel action")
}

func TestSmallWindowsShrinkTheList(t *testing.T) {
	h := startWithConfig(t, newFakeApp(), flat)

	h.press("up", "up", "up", "up", "up")
	for _, size := range []struct{ width, height, rows int }{{100, 30, 6}, {100, 10, 5}, {100, 8, 3}} {
		h.resize(size.width, size.height)
		h.waitForOverlayRows(size.rows)
		h.waitFor("▸ app.greet", "> find an action", "enter run")
		if got := h.panelRows(); got != 6 {
			t.Errorf("at %d rows the panel takes %d, want 6", size.height, got)
		}
	}
	h.press("tab")
	h.waitFor("▸ name", "› app.greet", "enter next")
	h.waitForOverlayRows(3)
}

func TestTinyWindowsShowOnlyTheMessage(t *testing.T) {
	h := start(t, newFakeApp())

	for _, size := range []struct{ width, height int }{{100, 7}, {100, 1}, {23, 30}, {10, 8}} {
		h.resize(size.width, size.height)
		h.waitForLines(size.height)
		h.waitFor(ansi.Truncate("Make the window larger.", size.width, "…"))
		for _, line := range strings.Split(h.view(), "\n") {
			if w := ansi.StringWidth(line); w > size.width {
				t.Errorf("line wider than the window: %q", line)
			}
		}
	}
	h.waitForNo("find an action")
}

type harness struct {
	t       *testing.T
	program *tea.Program
	exited  chan struct{}

	mu        sync.Mutex
	latest    string
	latestRaw string
	height    int
	seen      map[int]bool
	copied    []string
	// Moves only when advance moves it. While step is not 0, each wait moves it on by step as it looks.
	clock *fakeClock
	step  time.Duration
}

// The probe closes a settled message instead of handing it to the model. The program handles messages in order, so
// once it is closed, every message sent before it has been handled.
type settled chan struct{}

type probe struct {
	h     *harness
	model *model
}

func start(t *testing.T, app *fakeApp, options ...func(*model)) *harness {
	h := startPicking(t, newFakeSource(app), client.Target{}, client.Wake, options...)
	h.waitFor("quack · ", "type or ↑ to find an action")
	return h
}

// The settings file of the test's own config dir holds file.
func startWithConfig(t *testing.T, app *fakeApp, file string, options ...func(*model)) *harness {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "quack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "quack", "config.toml"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	h := startIn(t, dir, newFakeSource(app), client.Target{}, client.Wake, options...)
	h.waitFor("quack · ", "type or ↑ to find an action")
	return h
}

// The apps read their recipes and settings from a config dir of the test's own, which has none.
func startPicking(t *testing.T, source *fakeSource, want client.Target, mode client.StartMode, options ...func(*model)) *harness {
	return startIn(t, t.TempDir(), source, want, mode, options...)
}

// The settings come from the config dir as Run reads them, and quack starts in it too, so that its .quack is the
// project.
func startIn(t *testing.T, configDir string, source *fakeSource, want client.Target, mode client.StartMode, options ...func(*model)) *harness {
	t.Setenv("XDG_CONFIG_HOME", configDir)
	ctx, cancel := context.WithCancel(context.Background())
	activity := newActivity(ctx)
	h := &harness{t: t, exited: make(chan struct{}), height: 30, seen: map[int]bool{}, clock: &fakeClock{now: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)}}
	settings, problems := config.Load()
	m := newModel(source, activity, want, mode, "dev", configDir, settings, problems)
	m.clock = h.clock
	m.clipboard = func(text string) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.copied = append(h.copied, text)
		return nil
	}
	for _, option := range options {
		option(m)
	}
	h.program = tea.NewProgram(probe{h, m},
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithWindowSize(100, 30),
		tea.WithoutSignals(),
	)
	go func() {
		defer close(h.exited)
		h.program.Run()
	}()
	t.Cleanup(func() {
		cancel()
		h.program.Kill()
		<-h.exited
	})
	return h
}

func withRecipes(t *testing.T, file string) func(*model) {
	return func(*model) { writeRecipes(t, file) }
}

// writeRecipes keeps file as the recipes of the sample app in the project of the dir quack starts in, with
// up-to-date types.
func writeRecipes(t *testing.T, file string) string {
	t.Helper()
	dir := os.Getenv("XDG_CONFIG_HOME")
	project := filepath.Join(dir, ".quack")
	path := filepath.Join(project, "recipes", "io.mehow.quack.sample.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := recipe.Init(dir, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "quack.toml"), []byte("[recipes]\n'io\\.mehow\\.quack\\.sample' = \"io.mehow.quack.sample.ts\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Browsing lists every runnable without groups.
const flat = "group-namespaces = false\n"

// The runs' clock moves on by the next of steps each time it is read.
func withClock(steps ...time.Duration) func(*model) {
	return func(m *model) {
		now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		m.runs.now = func() time.Time {
			now = now.Add(steps[0])
			steps = steps[1:]
			return now
		}
	}
}

func (p probe) Init() tea.Cmd { return p.model.Init() }

func (p probe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if s, ok := msg.(settled); ok {
		close(s)
		return p, nil
	}
	_, cmd := p.model.Update(msg)
	raw := p.model.View().Content
	p.h.mu.Lock()
	p.h.latestRaw = raw
	p.h.latest = ansi.Strip(raw)
	p.h.seen[panelFromBottom(p.h.latest)] = true
	p.h.mu.Unlock()
	return p, cmd
}

func (p probe) View() tea.View { return p.model.View() }

func (h *harness) mouse(msg tea.Msg) { h.program.Send(msg) }

func (h *harness) logRow(text string) int {
	lines, _ := h.split()
	for y, line := range lines {
		if strings.HasPrefix(line, text) {
			return y
		}
	}
	return -1
}

func (h *harness) reversed() map[[2]int]bool {
	h.mu.Lock()
	raw, height := h.latestRaw, h.height
	h.mu.Unlock()
	screen := uv.NewScreenBuffer(200, height)
	uv.NewStyledString(raw).Draw(screen, screen.Bounds())
	cells := map[[2]int]bool{}
	for y := range height - panelRows {
		for x := range 200 {
			if cell := screen.CellAt(x, y); cell != nil && cell.Style.Attrs&uv.AttrReverse != 0 {
				cells[[2]int{x, y}] = true
			}
		}
	}
	return cells
}

func (h *harness) clipboard() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.copied)
}

func (h *harness) waitForCopied(text string) {
	h.t.Helper()
	last := func() string {
		copied := h.clipboard()
		if len(copied) == 0 {
			return ""
		}
		return copied[len(copied)-1]
	}
	h.wait(last, func(got string) bool { return got == text }, "want copied "+strconv.Quote(text))
}

func (h *harness) resize(width, height int) {
	h.mu.Lock()
	h.height = height
	h.mu.Unlock()
	h.program.Send(tea.WindowSizeMsg{Width: width, Height: height})
}

func (h *harness) view() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.latest
}

func (h *harness) raw() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.latestRaw
}

func checkPanel(t *testing.T, h *harness, want []string) {
	t.Helper()
	lines := h.panel()
	if len(lines) != len(want) {
		t.Fatalf("the panel has %d lines, want %d:\n%s", len(lines), len(want), h.view())
	}
	for i := range want {
		if !strings.HasPrefix(lines[i], want[i]) || want[i] == "" && lines[i] != "" {
			t.Errorf("line %d is %q, want %q", i, lines[i], want[i])
		}
	}
}

func (h *harness) split() (log, panel []string) {
	h.mu.Lock()
	lines := strings.Split(h.latest, "\n")
	h.mu.Unlock()
	at := max(0, len(lines)-panelRows)
	if at+1 >= len(lines) || !isTitle(lines[at+1]) {
		for at > 0 && !isRule(lines[at]) {
			at--
		}
	}
	return lines[:at], lines[at:]
}

func isRule(line string) bool { return line != "" && strings.Trim(line, "─") == "" }

func isTitle(line string) bool {
	return strings.HasPrefix(line, "quack · ") || strings.TrimRight(line, " ") == "quack"
}

func (h *harness) panel() []string {
	_, lines := h.split()
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return lines
}

func (h *harness) panelText() string {
	_, lines := h.split()
	return strings.Join(lines, "\n")
}

func (h *harness) logText() string {
	lines, _ := h.split()
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

func (h *harness) lines() int { return strings.Count(h.view(), "\n") + 1 }

func (h *harness) overlayRows() int {
	panel := h.panel()
	if len(panel) == 0 || !isRule(panel[0]) {
		return 0
	}
	return len(panel) - 5
}

func (h *harness) panelRows() int { return panelFromBottom(h.view()) }

func panelFromBottom(view string) int {
	lines := strings.Split(view, "\n")
	rules := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if isRule(lines[i]) {
			if rules++; rules == 2 {
				return len(lines) - i + 2
			}
		}
	}
	return 0
}

func (h *harness) resetHeights() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen = map[int]bool{}
}

func (h *harness) heights() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	var heights []int
	for height := range h.seen {
		heights = append(heights, height)
	}
	slices.Sort(heights)
	return heights
}

func (h *harness) press(keys ...string) {
	for _, name := range keys {
		var k tea.Key
		mod, base, ok := strings.Cut(name, "+")
		if !ok {
			base = name
		}
		switch mod {
		case "ctrl":
			k.Mod = tea.ModCtrl
		case "alt":
			k.Mod = tea.ModAlt
		case "shift":
			k.Mod = tea.ModShift
		}
		switch base {
		case "enter":
			k.Code = tea.KeyEnter
		case "tab":
			k.Code = tea.KeyTab
		case "esc":
			k.Code = tea.KeyEscape
		case "up":
			k.Code = tea.KeyUp
		case "down":
			k.Code = tea.KeyDown
		case "left":
			k.Code = tea.KeyLeft
		case "right":
			k.Code = tea.KeyRight
		case "pgup":
			k.Code = tea.KeyPgUp
		case "pgdown":
			k.Code = tea.KeyPgDown
		case "end":
			k.Code = tea.KeyEnd
		case "backspace":
			k.Code = tea.KeyBackspace
		default:
			k.Code = []rune(base)[0]
		}
		h.program.Send(tea.KeyPressMsg(k))
	}
	h.settle()
}

func (h *harness) advance(d time.Duration) { h.clock.advance(d) }

// The waits that follow move the clock on by pollTime each time they look, so that polls and the tracker's retries go
// on. A poll skips a device whose last answer has not come yet, so a single poll does not always ask it.
func (h *harness) keepPolling() { h.step = pollTime }

func (h *harness) waitForInspects(source *fakeSource, count int) {
	h.t.Helper()
	inspects := func() string { return strconv.Itoa(source.inspects()) }
	h.wait(inspects, func(n string) bool { got, _ := strconv.Atoi(n); return got >= count }, strconv.Itoa(count)+" inspects")
}

// The watch says nothing more until an account signs in, so that what the test does next has the log to itself.
func (h *harness) startWatch() {
	h.t.Helper()
	h.typing("account.watch")
	h.press("enter")
	h.waitForLog("› account.watch\n  Signed out")
}

func (h *harness) typing(text string) {
	for _, r := range text {
		h.program.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	h.settle()
}

// Returns at once when the program has ended, as a key that quits ends it.
func (h *harness) settle() {
	h.t.Helper()
	done := make(settled)
	go h.program.Send(done)
	select {
	case <-done:
	case <-h.exited:
	case <-time.After(timeout):
		h.t.Fatalf("the TUI did not handle the keys:\n%s", h.view())
	}
}

func (h *harness) waitFor(texts ...string) {
	h.t.Helper()
	h.wait(h.panelText, func(panel string) bool { return holds(panel, texts) }, "want "+strings.Join(texts, " then "))
}

func (h *harness) waitForNo(text string) {
	h.t.Helper()
	h.wait(h.panelText, func(panel string) bool { return !strings.Contains(panel, text) }, "want no "+text)
}

func (h *harness) waitForLines(count int) {
	h.t.Helper()
	h.wait(h.view, func(view string) bool { return strings.Count(view, "\n")+1 == count }, strconv.Itoa(count)+" lines")
}

func (h *harness) waitForOverlayRows(count int) {
	h.t.Helper()
	rows := func() string { return strconv.Itoa(h.overlayRows()) }
	h.wait(rows, func(rows string) bool { return rows == strconv.Itoa(count) }, "a list of "+strconv.Itoa(count)+" rows")
}

func (h *harness) waitForLog(texts ...string) {
	h.t.Helper()
	h.wait(h.logText, func(log string) bool { return holds(log, texts) }, "want in the log "+strings.Join(texts, " then "))
}

func (h *harness) waitForOnlyHeader() {
	h.t.Helper()
	header := ansi.Strip(header("dev"))
	h.wait(h.logText, func(log string) bool { return strings.TrimRight(log, "\n") == header }, "want only the duck in the log")
}

func (h *harness) waitForExit() {
	h.t.Helper()
	select {
	case <-h.exited:
	case <-time.After(timeout):
		h.t.Fatalf("the TUI did not quit:\n%s", h.view())
	}
}

// Fails if the program ends within a moment.
func (h *harness) waitForRunning() {
	h.t.Helper()
	select {
	case <-h.exited:
		h.t.Fatal("the TUI quit")
	case <-time.After(200 * time.Millisecond):
	}
}

func (h *harness) wait(read func() string, done func(string) bool, want string) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for !done(read()) {
		if time.Now().After(deadline) {
			h.t.Fatalf("%s in:\n%s", want, read())
		}
		h.advance(h.step)
		time.Sleep(10 * time.Millisecond)
	}
}

func holds(s string, texts []string) bool {
	for _, text := range texts {
		i := strings.Index(s, text)
		if i < 0 {
			return false
		}
		s = s[i+len(text):]
	}
	return true
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	at   time.Time
	ends chan time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ends := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{c.now.Add(d), ends})
	return ends
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.timers = slices.DeleteFunc(c.timers, func(timer fakeTimer) bool {
		if timer.at.After(c.now) {
			return false
		}
		timer.ends <- c.now
		return true
	})
}

// account.watch prints the account each time account.create signs in, until it is canceled.
type fakeApp struct {
	status io.Writer
	pkg    string

	mu            sync.Mutex
	described     []wire.Action
	calls         []wire.Call
	watchers      []client.NotificationFunc
	watchCanceled []chan struct{}
	stopsHeld     chan struct{}
}

func withNameHints(app *fakeApp, hints ...string) *fakeApp {
	app.described[0].Params[0].Hints = hints
	return app
}

func newFakeApp() *fakeApp {
	return &fakeApp{
		pkg: "io.mehow.quack.sample",
		described: []wire.Action{
			{ID: "app.greet", Description: "Say hello", Params: []wire.Param{
				{Name: "name", Type: wire.ParamTypeString, Presence: wire.Required{}, Description: "Who to greet"},
				{Name: "times", Type: wire.ParamTypeInt, Presence: wire.Default{Values: []string{"1"}}},
			}},
			{ID: "app.params", Description: "Show the parsed value of each param type", Params: []wire.Param{
				{Name: "text", Type: wire.ParamTypeString, Presence: wire.Optional{}},
				{Name: "count", Type: wire.ParamTypeInt, Presence: wire.Optional{}},
				{Name: "size", Type: wire.ParamTypeLong, Presence: wire.Optional{}},
				{Name: "ratio", Type: wire.ParamTypeDouble, Presence: wire.Optional{}},
				{Name: "flag", Type: wire.ParamTypeBoolean, Presence: wire.Optional{}},
				{Name: "theme", Type: wire.ParamTypeEnum, Choices: []string{"light", "dark"}, Presence: wire.Optional{}},
				{Name: "tag", Type: wire.ParamTypeStringList, Presence: wire.Default{Values: []string{"a", "b"}}, Description: "Labels to add"},
			}},
			{ID: "account.create", Description: "Create the account", Params: []wire.Param{
				{Name: "username", Type: wire.ParamTypeString, Presence: wire.Required{}},
			}},
			{ID: "account.delete", Description: "Delete the account"},
			{ID: "account.watch", Description: "Print the account each time it changes, until stopped"},
			{ID: "app.count", Description: "Count to 50"},
		},
	}
}

func (a *fakeApp) Describe(context.Context) (wire.Hello, []wire.Action, error) {
	actions := a.actions()
	if actions == nil {
		return wire.Hello{}, nil, &client.AppError{Message: "the app is gone"}
	}
	return wire.Hello{Protocol: 1, Package: a.pkg, Version: "1.0"}, actions, nil
}

func (a *fakeApp) Call(ctx context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error) {
	switch call.Action {
	case "account.show":
		return wire.MessageResult{Message: "Signed in as alice", Data: json.RawMessage(`{"username":"alice","tags":["a"]}`)}, nil
	case "sync.pair":
		notify(wire.Notification{Message: "Pairing code", Data: json.RawMessage(`{"code":"abc123"}`)})
		return wire.MessageResult{Message: "Paired"}, nil
	}
	message, err := a.reply(ctx, call, notify)
	return wire.MessageResult{Message: message}, err
}

func (a *fakeApp) reply(ctx context.Context, call wire.Call, notify client.NotificationFunc) (string, error) {
	a.mu.Lock()
	a.calls = append(a.calls, call)
	a.mu.Unlock()
	switch call.Action {
	case "app.greet":
		return "Hello, " + call.Args["name"][0] + "!", nil
	case "app.params":
		return strings.Join(call.Args["text"], ""), nil
	case "account.create":
		notify(wire.Notification{Message: "Started"})
		notify(wire.Notification{Message: "Creating account"})
		a.mu.Lock()
		for _, watch := range a.watchers {
			if watch == nil {
				continue
			}
			watch(wire.Notification{Message: "Signed in as " + call.Args["username"][0]})
		}
		a.mu.Unlock()
		return "Created", nil
	case "app.count":
		for i := 1; i <= 50; i++ {
			notify(wire.Notification{Message: strconv.Itoa(i)})
		}
		return "Counted", nil
	case "account.delete":
		return "", wire.Error{Code: wire.ErrorCodeRejected, Message: "there is no account"}
	case "account.watch":
		done := make(chan struct{})
		a.mu.Lock()
		watcher := len(a.watchers)
		a.watchers = append(a.watchers, notify)
		a.watchCanceled = append(a.watchCanceled, done)
		held := a.stopsHeld
		a.mu.Unlock()
		notify(wire.Notification{Message: "Signed out"})
		<-ctx.Done()
		a.mu.Lock()
		a.watchers[watcher] = nil
		a.mu.Unlock()
		close(done)
		if held != nil {
			<-held
		}
		return "", &client.CallError{Message: "canceled"}
	}
	return "", wire.Error{Code: wire.ErrorCodeUnknownAction, Message: "no action '" + call.Action + "'"}
}

func (a *fakeApp) Target() client.Target {
	return client.Target{Device: "emulator-5554", Package: a.pkg}
}

func (a *fakeApp) Command(_ context.Context, cmd string) (adb.Output, error) {
	return adb.Output{Stdout: "ran " + cmd + "\n"}, nil
}

func (a *fakeApp) Start(mode client.StartMode) error {
	if mode != client.OpenApp {
		return errors.New("unexpected mode " + string(mode))
	}
	io.WriteString(a.status, "starting "+a.pkg+"…\n")
	return nil
}

func (a *fakeApp) holdStops() (release func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	held := make(chan struct{})
	a.stopsHeld = held
	return sync.OnceFunc(func() { close(held) })
}

func (a *fakeApp) signIn(username string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, watch := range a.watchers {
		if watch != nil {
			watch(wire.Notification{Message: "Signed in as " + username})
		}
	}
}

func (a *fakeApp) received() []wire.Call {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]wire.Call(nil), a.calls...)
}

func (a *fakeApp) canceled(i int) <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.watchCanceled[i]
}

func (a *fakeApp) actions() []wire.Action {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.described
}

func (a *fakeApp) setActions(actions []wire.Action) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.described = actions
}

const timeout = 5 * time.Second

// Each app is the default one unless apps has one for its target.
type fakeSource struct {
	app *fakeApp

	mu             sync.Mutex
	devices        []client.Device
	apps           map[client.Target]*fakeApp
	send           func([]adb.Listing)
	started        []string
	failTracks     int
	held           map[client.Target]chan struct{}
	inspected      int
	listedPackages int
}

func newFakeSource(app *fakeApp, devices ...client.Device) *fakeSource {
	if len(devices) == 0 {
		devices = []client.Device{device("emulator-5554", "Pixel 8", "device", app.pkg)}
	}
	return &fakeSource{app: app, devices: devices, apps: map[client.Target]*fakeApp{}, held: map[client.Target]chan struct{}{}}
}

func device(serial, model, state string, pkgs ...string) client.Device {
	return client.Device{Listing: adb.Listing{Device: adb.Device(serial), State: state}, Model: model, Packages: pkgs}
}

func (s *fakeSource) Track(ctx context.Context, send func([]adb.Listing)) error {
	s.mu.Lock()
	if s.failTracks > 0 {
		s.failTracks--
		s.mu.Unlock()
		return &adb.Error{Message: "lost track of the devices: EOF"}
	}
	s.send = send
	listings := s.listings()
	s.mu.Unlock()
	send(listings)
	<-ctx.Done()
	return nil
}

func (s *fakeSource) Inspect(listing adb.Listing) (client.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inspected++
	for _, d := range s.devices {
		if d.Device == listing.Device {
			d.Listing = listing
			return d, nil
		}
	}
	return client.Device{}, errors.New(string(listing.Device) + " is not connected")
}

func (s *fakeSource) Packages(device adb.Device) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listedPackages++
	for _, d := range s.devices {
		if d.Device == device {
			return d.Packages, nil
		}
	}
	return nil, errors.New(string(device) + " is not connected")
}

func (s *fakeSource) Connect(target client.Target, mode client.StartMode, status io.Writer) (App, error) {
	s.mu.Lock()
	held := s.held[target]
	s.started = append(s.started, string(target.Device)+" "+target.Package+" "+string(mode))
	s.mu.Unlock()
	if held != nil {
		<-held
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.apps[target]
	if !ok {
		app = s.app
	}
	app.status = status
	return app, nil
}

func (s *fakeSource) hold(serial, pkg string) (release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := make(chan struct{})
	s.held[client.Target{Device: adb.Device(serial), Package: pkg}] = held
	return sync.OnceFunc(func() { close(held) })
}

func (s *fakeSource) inspects() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inspected
}

func (s *fakeSource) packageLists() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listedPackages
}

func (s *fakeSource) starts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.started)
}

func (s *fakeSource) set(devices ...client.Device) {
	s.mu.Lock()
	s.devices = devices
	send, listings := s.send, s.listings()
	s.mu.Unlock()
	if send != nil {
		send(listings)
	}
}

// Installing an app changes nothing that adb lists.
func (s *fakeSource) install(serial string, pkgs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.devices {
		if d.Device == adb.Device(serial) {
			s.devices[i].Packages = pkgs
		}
	}
}

func (s *fakeSource) appFor(serial, pkg string) *fakeApp {
	app := newFakeApp()
	app.pkg = pkg
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apps[client.Target{Device: adb.Device(serial), Package: pkg}] = app
	return app
}

func (s *fakeSource) listings() []adb.Listing {
	var listings []adb.Listing
	for _, d := range s.devices {
		listings = append(listings, d.Listing)
	}
	return listings
}
