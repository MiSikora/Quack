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
	"quack/internal/recipe"
	"quack/internal/wire"
)

func TestPanelShowsABlankRowTheTitleTheInputAndTheKeysAtRest(t *testing.T) {
	h := start(t, newFakeApp())

	checkPanel(t, h, []string{
		"",
		"quack · io.mehow.quack.sample 1.0 · emulator-5554",
		strings.Repeat("─", 100),
		"> find an action, $ for recipes, / for commands",
		strings.Repeat("─", 100),
		" type or ↑ to find an action · ctrl+c stop/quit",
	})
	if lines := h.panel(); !strings.HasSuffix(lines[3], " 6/6") {
		t.Errorf("the input line is %q, want the counter at its end", lines[3])
	}
}

func TestUpOnEmptyTextListsEveryActionBestLastUnderARuleAndEscClosesIt(t *testing.T) {
	h := start(t, newFakeApp())

	h.press("up")

	checkPanel(t, h, []string{
		strings.Repeat("─", 100),
		"  app.count        Count to 50",
		"  account.watch    Print the account each time it changes, until stopped",
		"  account.delete   Delete the account",
		"  account.create   Create the account",
		"  app.params       Show the parsed value of each param type",
		"▸ app.greet        Say hello",
		strings.Repeat("─", 100),
		"> find an action, $ for recipes, / for commands",
		strings.Repeat("─", 100),
		" enter run · tab params · ↑↓ select · esc close · ctrl+c stop/quit",
	})
	h.press("esc")
	h.waitFor("quack · ", "> find an action", "type or ↑ to find an action")
	h.press("down")
	h.waitFor("quack · ", "> find an action", "type or ↑ to find an action")
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
	h.waitFor("▸ app.greet", "esc close")
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

	h.press("up", "tab")
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
	h := start(t, newFakeApp())
	h.typing("app.count")
	h.press("enter")
	h.waitForLog("  50\nCounted · ")
	before := strings.Split(h.view(), "\n")

	h.press("up")
	h.waitFor("▸ app.greet")

	after := strings.Split(h.view(), "\n")
	if len(after) != 30 || !reflect.DeepEqual(after[:19], before[:19]) || after[19] != strings.Repeat("─", 100) {
		t.Errorf("the list moved the log:\n%s", h.view())
	}
	if !strings.HasPrefix(after[20], "  app.count") || !strings.HasPrefix(after[25], "▸ app.greet") {
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
	h := start(t, newFakeApp())
	h.typing("app.count")
	h.press("enter")
	h.waitForLog("  50\nCounted · ")
	h.press("up")
	h.waitFor("▸ app.greet")

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
	h.waitFor("▸ /reload")
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
	h.typing("account.watch")
	h.press("enter")
	h.waitFor("account.watch · ctrl+c stop action")
	check("one run")
	h.press("ctrl+c")
	h.waitFor("account.watch stopping… · ctrl+c stop")
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
	m := newModel(newFakeSource(app), newActivity(t.Context()), client.Target{}, client.Wake, "dev")

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
	if !strings.HasSuffix(rows[0], "  quack dev") || !strings.HasSuffix(rows[1], "  type to find an action · $ recipes · / commands") {
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
	h := start(t, newFakeApp())
	// A list of 5 rows, so that the 6 actions scroll.
	h.resize(100, 10)
	h.press("up")
	h.waitForOverlayRows(5)

	h.waitFor("  account.watch", "  account.delete", "  account.create", "  app.params", "▸ app.greet")
	h.press("up", "up", "up", "up", "up", "up")
	h.waitFor("▸ app.count", "  account.watch")
	h.waitForNo("app.greet")
	h.press("down")
	h.waitFor("▸ account.watch")
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

	h.waitFor("/quit", "/stop-action", "/switch-app", "/switch-device", "/open-app", "▸ /reload", "6/6")
	h.waitForNo("app.greet")
	h.typing("q")
	h.waitFor("▸ /quit")
}

func TestQuestionMarkIsAnOrdinaryCharacter(t *testing.T) {
	h := start(t, newFakeApp())

	h.typing("?")

	h.waitFor("no match", "> ?")
}

func TestEnterRunsAnActionWithoutRequiredParamsAndPrintsItsError(t *testing.T) {
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
	h := start(t, newFakeApp(), withClock(0, 1234*time.Millisecond, 0, 65*time.Second))

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

	h.press("up", "enter")

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

	h.press("up", "enter")
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

	h.press("up", "enter")

	h.waitFor("app.greet · Say hello", "  Bob\n", "  Albert\n", "  Alice\n", "▸ name", "Required. Who to greet", "3/3",
		"› app.greet")
	h.waitForNo("times")
	h.press("enter")
	h.waitFor("  name", "▸ times", "Default: 1.")
	h.waitForNo("Albert")
}

func TestHintsNarrowToTheTextAndSayWhenNoneMatches(t *testing.T) {
	h := start(t, withNameHints(newFakeApp(), "Alice", "Albert", "Bob"))
	h.press("up", "enter")
	h.waitFor("3/3")

	h.typing("bo")
	h.waitFor("  Bob\n", "1/3")
	h.waitForNo("Albert")
	h.typing("zz")
	h.waitFor("no match", "0/3")
}

func TestEnterPicksTheSelectedHintOrElseKeepsTheText(t *testing.T) {
	app := withNameHints(newFakeApp(), "Alice", "Albert", "Bob")
	h := start(t, app)
	h.press("up", "enter")
	h.waitFor("3/3")

	h.typing("Bo")
	h.waitFor("› app.greet name=Bo\n", "↑ hints · enter next")
	h.press("up")
	h.waitFor("▸ Bob", "enter pick · ↑↓ select · esc deselect")
	h.press("enter")
	h.waitFor("▸ times", "› app.greet name=Bob\n")
	h.press("shift+tab")
	h.waitFor("▸ name", "1/3")
	h.typing("x")
	h.press("enter")
	h.waitFor("▸ times", "› app.greet name=Bobx\n")
	h.press("enter")
	h.waitForLog("› app.greet name=Bobx\nHello, Bobx! · ")
	want := []wire.Call{{Action: "app.greet", Args: map[string]wire.RawArg{"name": {"Bobx"}}}}
	if got := app.received(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestCallPreviewsTheSelectedHintInsteadOfTheText(t *testing.T) {
	h := start(t, withNameHints(newFakeApp(), "Alice", "Albert", "Bob"))
	h.press("up", "enter")
	h.waitFor("3/3", "› app.greet\n")

	h.press("up")
	h.waitFor("▸ Alice", "› app.greet name=Alice\n")
	h.press("up")
	h.waitFor("▸ Albert", "› app.greet name=Albert\n")
	h.press("esc")
	h.waitFor("▸ name", "› app.greet\n", "↑ hints")
	h.typing("B")
	h.press("up")
	h.waitFor("▸ Bob", "› app.greet name=Bob\n")
	h.press("down")
	h.waitFor("› app.greet name=B\n")
}

func TestUpPastTheFirstHintAndTypingSelectNone(t *testing.T) {
	h := start(t, withNameHints(newFakeApp(), "Alice", "Albert", "Bob"))
	h.press("up", "enter")
	h.waitFor("3/3")

	h.press("up", "up", "up", "up")
	h.waitFor("▸ Bob")
	h.press("down", "down", "down")
	h.waitFor("↑ hints · enter next")
	h.waitForNo("▸ A")
	h.press("up")
	h.waitFor("▸ Alice")
	h.typing("A")
	h.waitFor("↑ hints · enter next")
	h.waitForNo("▸ A")
}

func TestEscDeselectsTheHintAndThenClosesTheBox(t *testing.T) {
	h := start(t, withNameHints(newFakeApp(), "Alice", "Albert", "Bob"))
	h.press("up", "enter")
	h.waitFor("3/3")

	h.press("up")
	h.waitFor("▸ Alice")
	h.press("esc")
	h.waitFor("  Alice\n", "▸ name", "↑ hints · enter next")
	h.press("esc")
	h.waitFor("▸ app.greet", "> find an action")
}

func TestHintsScrollToKeepTheSelectionInSight(t *testing.T) {
	var names []string
	for i := range 20 {
		names = append(names, "user"+strconv.Itoa(10+i))
	}
	h := start(t, withNameHints(newFakeApp(), names...))
	h.press("up", "enter")
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
	h.press("up", "enter", "enter")

	h.waitFor("  name", "▸ times", "enter next · shift+tab back")
	h.waitForNo("2/2")
}

func TestDataOfARunPrintsIndentedUnderItsMessage(t *testing.T) {
	app := newFakeApp()
	app.setActions(append(app.actions(), wire.Action{ID: "account.show", Description: "Show the account"}))
	h := start(t, app, withClock(0, 1234*time.Millisecond))

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
	h.typing("account.watch")
	h.press("enter")
	h.waitForLog("› account.watch\n  Signed out")

	h.typing("account.delete")
	h.press("enter")
	h.waitFor("> account.delete", "stop the running action first (ctrl+c)")
	h.press("esc")
	h.typing("account.create")
	h.press("enter")
	h.waitFor("▸ username")
	h.typing("alice")
	h.press("enter")
	h.waitFor("▸ username", "stop the running action first (ctrl+c)")
	h.press("backspace")
	h.waitFor("▸ username", "› account.create username=alic\n")
	if got, want := app.received(), []wire.Call{{Action: "account.watch"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls while the watch runs: got %v, want %v", got, want)
	}

	// ctrl+c stops the watch and keeps the box, and then the same Enter runs the action.
	h.press("ctrl+c")
	h.waitForLog("\ncanceled · ")
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
	h.typing("account.watch")
	h.press("enter")
	h.waitForLog("  Signed out")

	h.press("esc", "esc")
	app.signIn("alice")

	h.waitForLog("  Signed in as alice")
	h.waitFor("account.watch · ctrl+c stop action")
	select {
	case <-app.canceled(0):
		t.Fatal("esc stopped the watch")
	default:
	}
}

func TestCtrlCStopsTheRunAndKeepsTheTextThenClearsThenAsksThenQuits(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)
	h.typing("account.watch")
	h.press("enter")
	h.waitFor("account.watch · ctrl+c stop action")
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
	h.typing("account.watch")
	h.press("enter")
	h.waitFor("account.watch · ctrl+c stop action")
	h.typing("abc")

	h.press("ctrl+c")
	h.waitFor("account.watch stopping…")
	h.press("ctrl+c")

	h.waitFor("> abc", "press ctrl+c again to quit")
	h.press("ctrl+c")
	h.waitForExit()
}

func TestQuitPromptEndsAfterTwoSeconds(t *testing.T) {
	h := start(t, newFakeApp())

	h.press("ctrl+c")
	h.waitFor("press ctrl+c again to quit")
	shown := time.Now()

	h.waitFor("type or ↑ to find an action")
	if waited := time.Since(shown); waited < noticeTime-200*time.Millisecond {
		t.Errorf("the prompt went after %v, want %v", waited, noticeTime)
	}
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
[login]
description = "Log in as the test user"
steps = [{ action = "app.greet", args = { name = "Bob" } }]

[delete]
steps = [{ action = "account.delete" }]
`))

	h.typing("$")

	h.waitFor("  $login    Log in as the test user", "▸ $delete", "2/2")
	h.waitForNo("app.greet")
	h.typing("lg")
	h.waitFor("▸ $login", "1/2")
	h.press("tab")
	h.waitFor("▸ $login", "> $lg")
}

func TestDollarWithoutRecipesSaysWhereToCreateThem(t *testing.T) {
	h := start(t, newFakeApp())
	h.resize(300, 30)

	h.typing("$")

	h.waitFor("No recipes. Create "+recipe.Path("io.mehow.quack.sample"), "0/0")
}

func TestBrokenRecipeFileSaysWhyAndLeavesTheActions(t *testing.T) {
	h := start(t, newFakeApp(), withRecipes(t, "[login]\nsteps = []\n"))
	h.resize(300, 30)

	h.waitForLog("switched to io.mehow.quack.sample", recipe.Path("io.mehow.quack.sample")+": recipe 'login' has no steps")
	h.waitFor("6/6")
	h.press("up")
	h.waitFor("▸ app.greet", "6/6")
	h.press("esc")
	h.typing("$")
	h.waitFor("No recipes. Create ", "0/0")
}

func TestEnterRunsTheStepsOfARecipeEachUnderItsCommand(t *testing.T) {
	app := newFakeApp()
	h := start(t, app, withRecipes(t, `
[setup]
steps = [
  { open-app = true },
  { wait = "10ms" },
  { action = "account.create", args = { username = "alice" } },
  { action = "app.greet", args = { times = 2, name = "Bob" } },
]
`))

	h.typing("$setup")
	h.press("enter")

	h.waitForLog("\n› $setup\n› /open-app\nstarting io.mehow.quack.sample…\ndone · ",
		"\n› wait 10ms\ndone · ",
		"\n› account.create username=alice\n  Started\n  Creating account\nCreated · ",
		"\n› app.greet name=Bob times=2\nHello, Bob! · ")
	h.waitFor("> find an action", "type or ↑ to find an action")
	want := []wire.Call{
		{Action: "account.create", Args: map[string]wire.RawArg{"username": {"alice"}}},
		{Action: "app.greet", Args: map[string]wire.RawArg{"name": {"Bob"}, "times": {"2"}}},
	}
	if got := app.received(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestRecipeStopsAtTheFirstStepThatFails(t *testing.T) {
	app := newFakeApp()
	h := start(t, app, withRecipes(t, `
[clean]
steps = [{ action = "account.delete" }, { action = "app.count" }]
`))

	h.typing("$clean")
	h.press("enter")

	h.waitForLog("\n› $clean\n› account.delete\n[rejected] there is no account · ")
	h.waitFor("type or ↑ to find an action")
	if strings.Contains(h.logText(), "app.count") {
		t.Errorf("the log runs the step after the failed one:\n%s", h.logText())
	}
	if got, want := app.received(), []wire.Call{{Action: "account.delete"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestRecipeThatTheAppWouldRefuseRunsNothing(t *testing.T) {
	app := newFakeApp()
	h := start(t, app, withRecipes(t, `
[greet]
steps = [{ action = "account.delete" }, { action = "app.greet", args = { times = "twice" } }]
`))

	h.typing("$greet")
	h.press("enter")

	h.waitForLog("[invalid_args] recipe 'greet' step 2: missing argument 'name'")
	h.waitFor("▸ $greet", "> $greet")
	if got := app.received(); got != nil {
		t.Errorf("calls: got %v, want none", got)
	}
}

func TestFooterCountsTheStepsAndCtrlCSkipsTheRest(t *testing.T) {
	app := newFakeApp()
	h := start(t, app, withRecipes(t, `
[watch]
steps = [{ wait = "1ms" }, { action = "account.watch" }, { action = "app.count" }]
`))

	h.typing("$watch")
	h.press("enter")
	h.waitForLog("› account.watch\n  Signed out")
	h.waitFor("$watch · step 2/3 account.watch · ctrl+c stop action")
	h.press("ctrl+c")

	h.waitForLog("\n› $watch\n› wait 1ms\ndone · ", "\n› account.watch\n  Signed out\ncanceled · ")
	h.waitFor("type or ↑ to find an action")
	if strings.Contains(h.logText(), "app.count") {
		t.Errorf("the log runs the step after the canceled one:\n%s", h.logText())
	}
	if got, want := app.received(), []wire.Call{{Action: "account.watch"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestRecipesDoNotRunWhileARunRuns(t *testing.T) {
	app := newFakeApp()
	h := start(t, app, withRecipes(t, `
[clean]
steps = [{ action = "account.delete" }]
`))
	h.typing("account.watch")
	h.press("enter")
	h.waitForLog("› account.watch\n  Signed out")

	h.typing("$clean")
	h.press("enter")

	h.waitFor("> $clean", "stop the running action first (ctrl+c)")
	if got, want := app.received(), []wire.Call{{Action: "account.watch"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestReloadReadsTheRecipesAgain(t *testing.T) {
	h := start(t, newFakeApp())
	h.typing("$")
	h.waitFor("No recipes", "0/0")

	writeRecipes(t, "[clean]\nsteps = [{ action = \"account.delete\" }]\n")
	h.press("ctrl+r")

	h.waitForLog("reloaded 6 actions and 1 recipes")
	h.waitFor("▸ $clean", "1/1")
	writeRecipes(t, "[clean]\n")
	h.press("ctrl+r")
	h.waitForLog(": recipe 'clean' has no steps\nreloaded 6 actions")
	h.waitFor("No recipes", "0/0")
}

func TestReloadKeepsTheSelectedAction(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)
	h.press("up", "up", "up")
	h.waitFor("▸ account.create")

	app.setActions(append([]wire.Action{{ID: "aaa.new", Description: "A new action"}}, app.actions()...))
	h.press("ctrl+r")

	h.waitForLog("reloaded 7 actions and 0 recipes")
	h.waitFor("▸ account.create", "aaa.new", "7/7")

	app.setActions(append([]wire.Action{{ID: "aab.newer", Description: "A newer action"}}, app.actions()...))
	h.typing("/reload")
	h.press("enter")
	h.waitForLog("reloaded 8 actions")
	h.waitFor("8/8")
	h.press("up")
	h.waitFor("▸ aab.newer", "8/8")
}

func TestFailedReloadKeepsTheActions(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)

	app.setActions(nil)
	h.press("ctrl+r")

	h.waitForLog("reload: the app is gone")
	h.waitFor("6/6")
	h.press("up")
	h.waitFor("▸ app.greet", "6/6")
}

func TestStatusLinesGoIntoTheLog(t *testing.T) {
	app := newFakeApp()
	h := start(t, app)

	h.press("ctrl+o")

	h.waitForLog("starting io.mehow.quack.sample…\nshowed the app's main screen")
}

func TestSmallWindowsShrinkTheList(t *testing.T) {
	h := start(t, newFakeApp())

	h.press("up")
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

// harness runs the TUI as a program without a terminal and keeps its latest view.
type harness struct {
	t       *testing.T
	program *tea.Program
	// exited is closed once the program ends.
	exited chan struct{}

	mu        sync.Mutex
	latest    string
	latestRaw string
	// height is the height of the window the harness gave the TUI last.
	height int
	// seen are the numbers of rows the panel took at the bottom of the views since the last resetHeights.
	seen map[int]bool
	// copied is what the TUI wrote to the system's clipboard.
	copied []string
}

// settled is a message the probe closes instead of handing it to the model. The program handles messages in order, so
// once it is closed, every message sent before it has been handled.
type settled chan struct{}

// probe records the view after every update.
type probe struct {
	h     *harness
	model *model
}

// start runs the TUI for app, the only app on the only device, in a window 100 columns wide and 30 rows high, after
// options change its model, and waits for the actions.
func start(t *testing.T, app *fakeApp, options ...func(*model)) *harness {
	h := startPicking(t, newFakeSource(app), client.Target{}, client.Wake, options...)
	h.waitFor("quack · ", "type or ↑ to find an action")
	return h
}

// startPicking runs the TUI on the devices of source, picking among those that want leaves and starting the first app
// in mode, in a window 100 columns wide and 30 rows high, after options change its model. The apps read their recipes
// from a config dir of the test's own, which has none.
func startPicking(t *testing.T, source *fakeSource, want client.Target, mode client.StartMode, options ...func(*model)) *harness {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	activity := newActivity(ctx)
	h := &harness{t: t, exited: make(chan struct{}), height: 30, seen: map[int]bool{}}
	m := newModel(source, activity, want, mode, "dev")
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

// withRecipes gives the sample app a recipe file that holds file.
func withRecipes(t *testing.T, file string) func(*model) {
	return func(*model) { writeRecipes(t, file) }
}

// writeRecipes writes file as the recipe file of the sample app, and returns its path.
func writeRecipes(t *testing.T, file string) string {
	t.Helper()
	path := recipe.Path("io.mehow.quack.sample")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// withPollTime has an open picker ask the devices for their apps every d.
func withPollTime(d time.Duration) func(*model) {
	return func(m *model) { m.pollTime = d }
}

// withClock has the runs tell the time from a clock that moves on by the next of steps each time it is read.
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

// mouse sends a mouse message, at a cell of the view.
func (h *harness) mouse(msg tea.Msg) { h.program.Send(msg) }

// logRow is the row of the view of the first row of the log that starts with text, or -1 without one.
func (h *harness) logRow(text string) int {
	lines, _ := h.split()
	for y, line := range lines {
		if strings.HasPrefix(line, text) {
			return y
		}
	}
	return -1
}

// reversed are the cells of the log, as x and y in the view, drawn in reverse video.
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

// waitForCopied waits until the TUI wrote text to the system's clipboard last.
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

// resize gives the TUI a window of width and height.
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

// raw is the latest view with its styles.
func (h *harness) raw() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.latestRaw
}

// checkPanel checks that the rows of the panel start with those of want, and that the blank ones in want are blank.
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

// split cuts the view into the log that shows and the panel: the last rows, from the blank row over the title, or from
// the rule over the list or the box that covers the title.
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

// panel is the rows of the panel without the spaces at their ends.
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

// logText is the rows of the log the view shows, without the spaces at their ends.
func (h *harness) logText() string {
	lines, _ := h.split()
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

func (h *harness) lines() int { return strings.Count(h.view(), "\n") + 1 }

// overlayRows is how many rows the list or the box takes over the input, or 0 while none shows.
func (h *harness) overlayRows() int {
	panel := h.panel()
	if len(panel) == 0 || !isRule(panel[0]) {
		return 0
	}
	return len(panel) - 5
}

// panelRows is how many rows the panel takes at the bottom of the view: from two rows over the rule over the input down.
func (h *harness) panelRows() int { return panelFromBottom(h.view()) }

// panelFromBottom is the number of rows from two rows over the rule over the input down to the end of view, or 0 without
// that rule.
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

// heights are the numbers of rows the panel took in the views since the last resetHeights, lowest first.
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

// press sends keys by their names, as tea.Key.String writes them.
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

func (h *harness) typing(text string) {
	for _, r := range text {
		h.program.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	h.settle()
}

// settle waits until the TUI has handled every key sent so far, so that what a test reads next comes after them. It
// returns at once when the program has ended, as a key that quits ends it.
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

// waitFor waits until the panel holds each text, in order.
func (h *harness) waitFor(texts ...string) {
	h.t.Helper()
	h.wait(h.panelText, func(panel string) bool { return holds(panel, texts) }, "want "+strings.Join(texts, " then "))
}

func (h *harness) waitForNo(text string) {
	h.t.Helper()
	h.wait(h.panelText, func(panel string) bool { return !strings.Contains(panel, text) }, "want no "+text)
}

// waitForLines waits until the view is count lines high.
func (h *harness) waitForLines(count int) {
	h.t.Helper()
	h.wait(h.view, func(view string) bool { return strings.Count(view, "\n")+1 == count }, strconv.Itoa(count)+" lines")
}

// waitForOverlayRows waits until the list or the box takes count rows over the input.
func (h *harness) waitForOverlayRows(count int) {
	h.t.Helper()
	rows := func() string { return strconv.Itoa(h.overlayRows()) }
	h.wait(rows, func(rows string) bool { return rows == strconv.Itoa(count) }, "a list of "+strconv.Itoa(count)+" rows")
}

// waitForLog waits until the log the view shows holds each text, in order.
func (h *harness) waitForLog(texts ...string) {
	h.t.Helper()
	h.wait(h.logText, func(log string) bool { return holds(log, texts) }, "want in the log "+strings.Join(texts, " then "))
}

func (h *harness) waitForExit() {
	h.t.Helper()
	select {
	case <-h.exited:
	case <-time.After(timeout):
		h.t.Fatalf("the TUI did not quit:\n%s", h.view())
	}
}

// waitForRunning fails if the program ends within a moment.
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
		time.Sleep(10 * time.Millisecond)
	}
}

// holds is whether s holds each text, in order.
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

// fakeApp answers like the sample app. account.watch prints the account each time account.create signs in, until
// it is canceled.
type fakeApp struct {
	status io.Writer
	pkg    string

	mu            sync.Mutex
	described     []wire.Action
	calls         []wire.Call
	watchers      []func(string)
	watchCanceled []chan struct{}
	// stopsHeld, while not nil, keeps canceled watches from ending until it closes.
	stopsHeld chan struct{}
}

// withNameHints gives the name of app.greet hints.
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
	if call.Action == "account.show" {
		return wire.MessageResult{Message: "Signed in as alice", Data: json.RawMessage(`{"username":"alice","tags":["a"]}`)}, nil
	}
	message, err := a.reply(ctx, call, notify)
	return wire.MessageResult{Message: message}, err
}

// reply runs call and returns its message.
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
		notify("Started")
		notify("Creating account")
		a.mu.Lock()
		for _, watch := range a.watchers {
			if watch == nil {
				continue
			}
			watch("Signed in as " + call.Args["username"][0])
		}
		a.mu.Unlock()
		return "Created", nil
	case "app.count":
		for i := 1; i <= 50; i++ {
			notify(strconv.Itoa(i))
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
		notify("Signed out")
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

func (a *fakeApp) Start(mode client.StartMode) error {
	if mode != client.Open {
		return errors.New("unexpected mode " + string(mode))
	}
	io.WriteString(a.status, "starting "+a.pkg+"…\n")
	return nil
}

// holdStops keeps watches from ending when they are canceled, until release.
func (a *fakeApp) holdStops() (release func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	held := make(chan struct{})
	a.stopsHeld = held
	return sync.OnceFunc(func() { close(held) })
}

// signIn tells the watches that username signed in.
func (a *fakeApp) signIn(username string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, watch := range a.watchers {
		if watch != nil {
			watch("Signed in as " + username)
		}
	}
}

func (a *fakeApp) received() []wire.Call {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]wire.Call(nil), a.calls...)
}

// canceled is closed once the watch call number i is canceled.
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

// fakeSource is adb with devices on which fake apps run. Each app is the default one unless apps has one for its
// target. It records each app it starts.
type fakeSource struct {
	app *fakeApp

	mu      sync.Mutex
	devices []client.Device
	apps    map[client.Target]*fakeApp
	// send is Track's, once it tracks.
	send func([]adb.Listing)
	// started is each app the source started, as its device, its package and the mode.
	started []string
	// failTracks is how many times Track fails before it tracks.
	failTracks int
	// held keeps Connect of a target from returning until its channel closes.
	held map[client.Target]chan struct{}
}

// newFakeSource is a Pixel 8, emulator-5554, on which app is the only quack-enabled app.
func newFakeSource(app *fakeApp, devices ...client.Device) *fakeSource {
	if len(devices) == 0 {
		devices = []client.Device{device("emulator-5554", "Pixel 8", "device", app.pkg)}
	}
	return &fakeSource{app: app, devices: devices, apps: map[client.Target]*fakeApp{}, held: map[client.Target]chan struct{}{}}
}

// device is a device in state, with model and pkgs once it is ready.
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
	for _, d := range s.devices {
		if d.Device == listing.Device {
			d.Listing = listing
			return d, nil
		}
	}
	return client.Device{}, errors.New(string(listing.Device) + " is not connected")
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

// hold keeps the start of pkg on serial from answering until release.
func (s *fakeSource) hold(serial, pkg string) (release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := make(chan struct{})
	s.held[client.Target{Device: adb.Device(serial), Package: pkg}] = held
	return sync.OnceFunc(func() { close(held) })
}

func (s *fakeSource) starts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.started)
}

// set connects devices instead of the ones there are, and tells Track.
func (s *fakeSource) set(devices ...client.Device) {
	s.mu.Lock()
	s.devices = devices
	send, listings := s.send, s.listings()
	s.mu.Unlock()
	if send != nil {
		send(listings)
	}
}

// install changes the apps on a device without adb listing anything new, as installing an app does.
func (s *fakeSource) install(serial string, pkgs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.devices {
		if d.Device == adb.Device(serial) {
			s.devices[i].Packages = pkgs
		}
	}
}

// appFor runs a fake app of its own for pkg on serial.
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
