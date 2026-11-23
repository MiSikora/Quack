package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/config"
	"quack/internal/recipe"
	"quack/internal/wire"
)

type App interface {
	recipe.App
	Describe(ctx context.Context) (wire.Hello, []wire.Action, error)
}

// The app's status lines go into the log, since anything printed around the TUI would break the screen.
func Run(want client.Target, mode client.StartMode, startTimeout time.Duration, version string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	settings, problems := config.Load()
	// A dir that is unknown fails each load of recipes, which says why.
	dir, _ := os.Getwd()
	_, err := tea.NewProgram(newModel(clientSource{startTimeout}, newActivity(ctx), want, mode, version, dir, settings, problems)).Run()
	return err
}

type Source interface {
	// Track returns when ctx ends, or with the error that ended it.
	Track(ctx context.Context, send func([]adb.Listing)) error
	Inspect(listing adb.Listing) (client.Device, error)
	Packages(device adb.Device) ([]string, error)
	Connect(target client.Target, mode client.StartMode, status io.Writer) (App, error)
}

type model struct {
	source   Source
	activity activity
	app      App
	target   client.Target
	// The flags choose how the first pick starts its app, and Wake starts every later one.
	mode    client.StartMode
	devices []seenDevice
	listed  bool
	// asked numbers the questions to the devices, and pending holds each device's last unanswered one. Only the answer
	// to the last question counts, and a poll does not ask a device that has not answered yet.
	asked   int
	pending map[adb.Device]int
	picker  *picker
	polling bool
	hello   wire.Hello
	prompt  prompt
	// The picker keeps its text and selection under the box.
	box  *paramBox
	runs runs
	// Leaves the JSON data of notifications and results out of the log, from the next line on.
	hideData bool
	// The dir quack started in. Recipes load from its project and from the user's config.
	dir       string
	cookbook  *recipe.Cookbook
	quit      notice
	copied    notice
	refused   notice
	log       sessionLog
	header    string
	mouse     mouse
	clipboard func(text string) error
	clock     clock
	width     int
	height    int
}

// shown counts the times it showed, so that the end of an earlier one does not close a later one.
type notice struct {
	text    string
	showing bool
	shown   int
}

type noticeEnded struct {
	notice *notice
	shown  int
}

type command struct {
	name        string
	key         string
	description string
	needsApp    bool
	run         func(*model) tea.Cmd
}

type described struct {
	hello   wire.Hello
	actions []wire.Action
	err     error
	recipes recipes
}

type recipes struct {
	cookbook *recipe.Cookbook
	warnings []string
	err      error
}

type listed struct {
	listings []adb.Listing
}

type (
	inspected struct {
		serial adb.Device
		device client.Device
		asked  int
		err    error
	}
	polled struct{}
)

type connected struct {
	target client.Target
	app    App
	described
}

type clientSource struct {
	startTimeout time.Duration
}

// The problems with the settings file go into the log, under the header.
func newModel(source Source, activity activity, want client.Target, mode client.StartMode, version, dir string, settings config.Config, problems []string) *model {
	m := &model{
		source:    source,
		activity:  activity,
		dir:       dir,
		pending:   map[adb.Device]int{},
		mode:      mode,
		picker:    newPicker(want, 0),
		clock:     wallClock{},
		prompt:    prompt{input: newInput(""), grouped: settings.GroupNamespaces},
		hideData:  !settings.ShowData,
		runs:      newRuns(),
		clipboard: clipboard.WriteAll,
		header:    header(version),
	}
	m.picker.auto, m.picker.skipPackage, m.picker.quiet, m.picker.startup = true, true, true, true
	m.prompt.connect(false)
	m.log.add(m.header)
	for _, problem := range problems {
		m.log.add(problemStyle.Render(problem))
	}
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.activity.listen(), m.track(), m.picker.input.Focus(), m.poll())
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	return m, tea.Batch(cmd, m.focus())
}

// A quiet picker shows and hides as devices come and go, and the input on screen takes the keys.
func (m *model) focus() tea.Cmd {
	switch {
	case m.picker == nil || m.box != nil:
		return nil
	case m.picking() && !m.picker.input.Focused():
		m.prompt.input.Blur()
		return m.picker.input.Focus()
	case !m.picking() && !m.prompt.input.Focused():
		m.picker.input.Blur()
		return m.prompt.input.Focus()
	}
	return nil
}

func (m *model) picking() bool { return m.picker != nil && m.picker.showing(m.devices) }

// The mouse reports clicks, drags and the wheel, so that the TUI scrolls the log itself.
func (m *model) View() tea.View {
	view := tea.NewView(strings.Join(m.screen(), "\n"))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (m *model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.prompt.resize(m.width)
		if m.picker != nil {
			m.picker.resize(m.width)
		}
		if m.box != nil {
			m.box.resize(m.width)
		}
		m.log.resize(m.width, m.logHeight())
		m.mouse.dragging = false
		return nil
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.log.scroll(wheelRows, m.logHeight())
		case tea.MouseWheelDown:
			m.log.scroll(-wheelRows, m.logHeight())
		}
		return nil
	case tea.MouseClickMsg:
		return m.click(msg)
	case tea.MouseMotionMsg:
		m.drag(msg)
		return nil
	case tea.MouseReleaseMsg:
		return m.release()
	case tea.KeyPressMsg:
		m.log.marked = nil
		return m.press(msg)
	case stepStarted:
		m.log.add(m.runs.stepStarted(msg))
		return m.activity.listen()
	case runNotification:
		if m.hideData {
			msg.notification.Data = nil
		}
		m.log.add(notificationLines(msg.notification))
		return m.activity.listen()
	case stepEnded:
		if m.hideData {
			msg.result.Data = nil
		}
		m.log.add(m.runs.stepEnded(msg))
		return m.activity.listen()
	case stepFailed:
		m.log.add(m.runs.stepFailed(msg))
		return m.activity.listen()
	case runEnded:
		if m.hideData {
			msg.data = nil
		}
		if lines := m.runs.ended(msg); lines != "" {
			m.log.add(lines)
		}
		return m.activity.listen()
	case logged:
		m.log.add(msg.text)
		return m.activity.listen()
	case described:
		m.describe(msg)
		return m.activity.listen()
	case listed:
		return tea.Batch(m.list(msg.listings), m.activity.listen())
	case inspected:
		m.inspected(msg)
		return tea.Batch(m.checkApp(), m.advance())
	case polled:
		m.polling = false
		return tea.Batch(m.inspectPicked(), m.poll())
	case connected:
		return tea.Batch(m.connected(msg), m.activity.listen())
	case noticeEnded:
		if msg.shown == msg.notice.shown {
			msg.notice.showing = false
		}
		return nil
	default:
		return tea.Batch(m.runs.tick(msg), m.forward(msg))
	}
}

func (m *model) press(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	switch {
	case key == "pgup":
		m.log.scroll(m.logHeight(), m.logHeight())
		return nil
	case key == "pgdown":
		m.log.scroll(-m.logHeight(), m.logHeight())
		return nil
	case key == "end" && m.log.scrolled > 0:
		m.log.scrolled = 0
		return nil
	}
	if m.picking() {
		return m.pressPicker(msg)
	}
	for _, c := range m.prompt.commands {
		if c.key == key && key != "ctrl+c" && key != "ctrl+d" {
			return c.run(m)
		}
	}
	if m.box != nil {
		return m.pressBox(msg)
	}
	switch key {
	case "ctrl+c":
		return m.interrupt()
	case "ctrl+d":
		if m.prompt.input.Value() == "" {
			return m.exit()
		}
	case "enter":
		return m.choose()
	case "tab":
		if c, ok := m.prompt.selection(); ok && c.command == nil && len(c.runnable.Params) > 0 {
			return m.openBox(c.runnable)
		}
		return nil
	case "esc":
		m.prompt.clear()
		return nil
	}
	return m.prompt.update(msg)
}

func (m *model) pressPicker(msg tea.KeyPressMsg) tea.Cmd {
	p := m.picker
	switch msg.String() {
	case "ctrl+c":
		return m.interrupt()
	case "ctrl+d":
		if p.input.Value() == "" {
			return m.exit()
		}
	}
	if p.connecting != nil {
		return nil
	}
	switch msg.String() {
	case "enter":
		return m.pick()
	case "esc":
		switch {
		case p.step == packageStep && p.back:
			p.auto = false
			p.toStep(deviceStep)
		case m.app != nil:
			m.picker = nil
			return m.prompt.input.Focus()
		case !p.quiet:
			p.quiet = true
			p.clear()
		default:
			p.clear()
		}
		return nil
	case "tab":
		return nil
	}
	return p.update(msg, m.devices)
}

func (m *model) pressBox(msg tea.KeyPressMsg) tea.Cmd {
	switch key := msg.String(); {
	case key == "ctrl+c" && m.runs.active != nil:
		return m.interrupt()
	case key == "esc" && m.box.deselect():
		return nil
	case key == "esc", key == "ctrl+c":
		m.box = nil
		return m.prompt.input.Focus()
	}
	run, cmd := m.box.press(msg)
	switch {
	case !run:
		return cmd
	case m.runs.active != nil:
		return tea.Batch(cmd, m.refused.show(m.clock, refusedDuringRun))
	}
	r, args := m.box.runnable, m.box.args()
	m.box = nil
	m.prompt.clear()
	return tea.Batch(m.start(r, args), m.prompt.input.Focus())
}

// A second ctrl+c while the action stops asks to quit too, so that an action that does not stop cannot keep quack
// open.
func (m *model) interrupt() tea.Cmd {
	switch {
	case m.quit.showing:
		return m.exit()
	case m.runs.stop():
		return nil
	case m.runs.active == nil && m.clearInput():
		return nil
	}
	return m.quit.show(m.clock, "press ctrl+c again to quit")
}

func (m *model) clearInput() bool {
	if m.picking() {
		return m.picker.clear()
	}
	return m.prompt.clear()
}

func (m *model) choose() tea.Cmd {
	c, ok := m.prompt.selection()
	switch {
	case !ok:
		return nil
	case c.group:
		m.prompt.openGroup(c.id)
		return nil
	case c.command != nil:
		m.prompt.clear()
		return c.command.run(m)
	case len(c.runnable.Params) > 0:
		return m.openBox(c.runnable)
	case m.runs.active != nil:
		return m.refused.show(m.clock, refusedDuringRun)
	default:
		m.prompt.clear()
		return m.start(c.runnable, nil)
	}
}

func (m *model) start(r runnable, args map[string]wire.RawArg) tea.Cmd {
	app, cookbook := m.app, m.cookbook
	work := func(ctx context.Context, observer recipe.Observer) (json.RawMessage, error) {
		step := recipe.Call{Action: r.ID, Args: args}
		observer.Started(step)
		result, err := recipe.Do(ctx, app, step, observer.Notified)
		if err == nil {
			observer.Ended(step, result)
		}
		return nil, err
	}
	if r.recipe {
		work = func(ctx context.Context, observer recipe.Observer) (json.RawMessage, error) {
			return cookbook.Run(ctx, app, r.ID, args, observer)
		}
	}
	header, cmd := m.runs.start(m.activity, &run{runnable: r}, m.prompt.actions, work)
	m.log.add(header)
	return cmd
}

func (m *model) openBox(r runnable) tea.Cmd {
	m.box = newParamBox(r, m.width)
	m.prompt.input.Blur()
	return m.box.init()
}

// The answer goes through activity, so that it comes after the status lines on the way to it.
func (m *model) reload() tea.Cmd {
	app, activity, dir, pkg := m.app, m.activity, m.dir, m.target.Package
	return func() tea.Msg {
		hello, actions, err := app.Describe(activity.ctx)
		activity.send(described{hello, actions, err, loadRecipes(dir, pkg)})
		return nil
	}
}

func (m *model) openApp() tea.Cmd {
	app, activity := m.app, m.activity
	return func() tea.Msg {
		line := statusStyle.Render("showed the app's main screen")
		if err := app.Start(client.OpenApp); err != nil {
			line = problemStyle.Render("open-app: " + err.Error())
		}
		activity.send(logged{line})
		return nil
	}
}

// It touches only files, so it works while a run is active.
func (m *model) initRecipes() tea.Cmd {
	pkg := m.target.Package
	written, err := recipe.Init(m.dir, pkg)
	if err != nil {
		m.log.add(problemStyle.Render("init-recipes: " + err.Error()))
		return nil
	}
	for _, path := range written {
		m.log.add(statusStyle.Render("wrote " + path))
	}
	if len(written) == 0 {
		m.log.add(statusStyle.Render("the recipe files are up to date"))
	}
	m.setRecipes(loadRecipes(m.dir, pkg))
	return nil
}

func (m *model) toggleData() tea.Cmd {
	m.hideData = !m.hideData
	line := "showing the JSON data of notifications and results"
	if m.hideData {
		line = "hiding the JSON data of notifications and results"
	}
	m.log.add(statusStyle.Render(line))
	return nil
}

func (m *model) clear() tea.Cmd {
	m.log.clear()
	m.log.add(m.header)
	return nil
}

func (m *model) describe(msg described) {
	m.setRecipes(msg.recipes)
	if msg.err != nil {
		m.log.add(problemStyle.Render("reload: " + msg.err.Error()))
		return
	}
	m.hello = msg.hello
	m.prompt.setActions(msg.actions)
	line := fmt.Sprintf("reloaded %d actions", len(msg.actions))
	if msg.recipes.err == nil {
		line += fmt.Sprintf(" and %d recipes", len(msg.recipes.list()))
	}
	m.log.add(statusStyle.Render(line))
}

func (m *model) setRecipes(r recipes) {
	m.cookbook = r.cookbook
	m.prompt.setRecipes(r.list())
	for _, warning := range r.warnings {
		m.log.add(problemStyle.Render(warning))
	}
	if r.err != nil {
		m.log.add(problemStyle.Render(r.err.Error()))
	}
}

func (m *model) cancel() tea.Cmd {
	m.runs.stop()
	return nil
}

func (m *model) exit() tea.Cmd {
	m.runs.cancel()
	return tea.Quit
}

func (m *model) pickDevice() tea.Cmd {
	if m.runs.active != nil {
		return m.refused.show(m.clock, refusedDuringRun)
	}
	if m.app == nil {
		m.picker.quiet = false
		return nil
	}
	return m.openPicker(func(p *picker) { p.skipPackage = true })
}

func (m *model) pickApp() tea.Cmd {
	if m.runs.active != nil {
		return m.refused.show(m.clock, refusedDuringRun)
	}
	return m.openPicker(func(p *picker) {
		p.device = m.target.Device
		p.toStep(packageStep)
	})
}

func (m *model) openPicker(setup func(*picker)) tea.Cmd {
	m.box = nil
	m.prompt.input.Blur()
	m.picker = newPicker(client.Target{}, m.width)
	setup(m.picker)
	return tea.Batch(m.picker.input.Focus(), m.inspectAll(), m.poll())
}

func (m *model) pick() tea.Cmd {
	p := m.picker
	row, ok := p.selection(m.devices)
	switch {
	case !ok:
		return nil
	case p.step == packageStep:
		return m.connect(client.Target{Device: row.device, Package: row.pkg})
	}
	p.device, p.back, p.skipPackage = row.device, true, true
	p.toStep(packageStep)
	return m.advance()
}

// The only device is taken only once every device answered.
func (m *model) advance() tea.Cmd {
	p := m.picker
	if p == nil || p.connecting != nil {
		return nil
	}
	if row, ok := p.onlyDevice(m.devices); p.step == deviceStep && p.auto && ok {
		p.device, p.back = row.device, false
		p.toStep(packageStep)
	}
	if pkgs := p.packages(m.devices, p.device); p.step == packageStep && p.skipPackage && len(pkgs) == 1 {
		return m.connect(client.Target{Device: p.device, Package: pkgs[0]})
	}
	return nil
}

func (m *model) connect(target client.Target) tea.Cmd {
	m.picker.connecting = &target
	source, activity, mode, dir := m.source, m.activity, m.mode, m.dir
	return func() tea.Msg {
		app, err := source.Connect(target, mode, activity)
		msg := connected{target: target, app: app, described: described{err: err}}
		if err == nil {
			msg.hello, msg.actions, msg.err = app.Describe(activity.ctx)
			msg.recipes = loadRecipes(dir, target.Package)
		}
		activity.send(msg)
		return nil
	}
}

// The picker may have gone on to another pick since. An app that failed to start leaves the picker on its step, which
// then no longer takes the only app by itself, so that the user picks again.
func (m *model) connected(msg connected) tea.Cmd {
	if m.picker == nil || m.picker.connecting == nil || *m.picker.connecting != msg.target {
		return nil
	}
	if msg.err != nil {
		m.picker.connecting, m.picker.skipPackage = nil, false
		m.log.add(problemStyle.Render(msg.target.Package + ": " + msg.err.Error()))
		return m.advance()
	}
	m.app, m.target, m.hello, m.mode = msg.app, msg.target, msg.hello, client.Wake
	m.picker = nil
	m.prompt.clear()
	m.prompt.connect(true)
	m.prompt.setActions(msg.actions)
	where := string(msg.target.Device)
	if i := m.deviceIndex(msg.target.Device); i >= 0 && m.devices[i].Model != "" {
		where = m.devices[i].Model + " · " + where
	}
	m.log.add(statusStyle.Render("switched to " + msg.target.Package + " on " + where))
	m.setRecipes(msg.recipes)
	return m.prompt.input.Focus()
}

func (m *model) track() tea.Cmd {
	source, activity, clock := m.source, m.activity, m.clock
	return func() tea.Msg {
		failing := false
		for activity.ctx.Err() == nil {
			err := source.Track(activity.ctx, func(listings []adb.Listing) {
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

func (m *model) list(listings []adb.Listing) tea.Cmd {
	m.listed = true
	var devices []seenDevice
	var cmds []tea.Cmd
	for _, l := range listings {
		if i := m.deviceIndex(l.Device); i >= 0 && m.devices[i].State == l.State {
			devices = append(devices, m.devices[i])
			continue
		}
		devices = append(devices, seenDevice{Device: client.Device{Listing: l}})
		cmds = append(cmds, m.inspect(l))
	}
	m.devices = devices
	for device := range m.pending {
		if m.deviceIndex(device) < 0 {
			delete(m.pending, device)
		}
	}
	if m.app != nil && m.deviceIndex(m.target.Device) < 0 {
		cmds = append(cmds, m.drop(string(m.target.Device)+" disconnected"))
	}
	if p := m.picker; p != nil && p.step == packageStep && m.deviceIndex(p.device) < 0 {
		p.toStep(deviceStep)
	}
	return tea.Batch(append(cmds, m.advance())...)
}

func (m *model) inspect(listing adb.Listing) tea.Cmd {
	if listing.State != adb.Ready {
		return nil
	}
	source := m.source
	return m.ask(listing.Device, func() (client.Device, error) { return source.Inspect(listing) })
}

func (m *model) ask(serial adb.Device, inspect func() (client.Device, error)) tea.Cmd {
	m.asked++
	asked := m.asked
	m.pending[serial] = asked
	return func() tea.Msg {
		device, err := inspect()
		return inspected{serial, device, asked, err}
	}
}

// While an app is connected, only its device is asked, and only for its apps, so that an app that leaves it is seen.
func (m *model) inspectPicked() tea.Cmd {
	if m.picker != nil {
		return m.inspectAll()
	}
	i := m.deviceIndex(m.target.Device)
	if i < 0 {
		return nil
	}
	if _, waiting := m.pending[m.target.Device]; waiting {
		return nil
	}
	seen := m.devices[i]
	if !seen.inspected || seen.State != adb.Ready {
		return m.inspect(seen.Listing)
	}
	source := m.source
	return m.ask(seen.Device.Device, func() (client.Device, error) {
		device := seen.Device
		packages, err := source.Packages(device.Device)
		device.Packages = packages
		return device, err
	})
}

// An app that its device no longer lists, such as one that was uninstalled, goes as if its device went.
func (m *model) checkApp() tea.Cmd {
	if m.app == nil {
		return nil
	}
	i := m.deviceIndex(m.target.Device)
	if i < 0 || !m.devices[i].inspected || slices.Contains(m.devices[i].Packages, m.target.Package) {
		return nil
	}
	return m.drop(m.target.Package + " left " + string(m.target.Device))
}

// Only the start picks by itself, so that a device or an app that comes back does not take over the prompt.
func (m *model) drop(line string) tea.Cmd {
	m.log.add(statusStyle.Render(line))
	m.runs.stop()
	m.app = nil
	m.picker = nil
	m.refused.showing = false
	m.prompt.clear()
	m.prompt.connect(false)
	m.prompt.setActions(nil)
	m.setRecipes(recipes{})
	return m.openPicker(func(p *picker) { p.quiet = true })
}

func (m *model) inspectAll() tea.Cmd {
	var cmds []tea.Cmd
	for _, d := range m.devices {
		if _, waiting := m.pending[d.Device.Device]; !waiting {
			cmds = append(cmds, m.inspect(d.Listing))
		}
	}
	return tea.Batch(cmds...)
}

// Only the answer to the last question counts, and only while adb still lists the device in the state it was asked
// in. A device that did not answer keeps its old answer, since adb soon lists it as gone when it went away.
func (m *model) inspected(msg inspected) {
	if m.pending[msg.serial] != msg.asked {
		return
	}
	delete(m.pending, msg.serial)
	i := m.deviceIndex(msg.serial)
	if msg.err != nil || i < 0 || m.devices[i].State != msg.device.State {
		return
	}
	m.devices[i] = seenDevice{Device: msg.device, inspected: true}
}

func (m *model) poll() tea.Cmd {
	if m.polling {
		return nil
	}
	m.polling = true
	return after(m.clock, pollTime, polled{})
}

func (m *model) deviceIndex(device adb.Device) int {
	return slices.IndexFunc(m.devices, func(d seenDevice) bool { return d.Device.Device == device })
}

// It depends on nothing but the window, so that the log never moves.
func (m *model) logHeight() int { return max(0, m.height-panelRows) }

func (m *model) logShown() int { return min(m.logHeight(), m.logHeight()+1-len(m.overlay())) }

func (m *model) forward(msg tea.Msg) tea.Cmd {
	cmd := m.prompt.forward(msg)
	if m.picker != nil {
		cmd = tea.Batch(cmd, m.picker.forward(msg))
	}
	if m.box != nil {
		cmd = tea.Batch(cmd, m.box.forward(msg))
	}
	return cmd
}

// Every state goes through screen, so that the panel never changes height and no row ever moves.
func (m *model) screen() []string {
	rows := m.log.view(m.logHeight())
	if m.width < minWidth || m.height < minHeight {
		return fit(append(rows, "Make the window larger."), m.width, m.height)
	}
	rule := ruleStyle.Render(strings.Repeat("─", m.width))
	rows = append(rows, "", m.title())
	if overlay := m.overlay(); overlay != nil {
		copy(rows[len(rows)-len(overlay)-1:], append([]string{rule}, overlay...))
	}
	rows = append(rows, rule, m.inputRow(), rule, footerStyle.Render(" "+m.footer()))
	return fit(rows, m.width, m.height)
}

func (m *model) overlay() []string {
	rows := min(maxListRows, m.logHeight()+1)
	switch {
	case m.picking():
		return m.picker.list(m.devices, m.listed, rows)
	case m.box != nil:
		return m.box.view(rows)
	}
	return m.prompt.list(rows)
}

func (m *model) inputRow() string {
	switch {
	case m.picking():
		return m.picker.view(m.devices)
	case m.box != nil:
		return m.box.preview()
	}
	return m.prompt.view()
}

func (m *model) footer() string {
	switch {
	case m.quit.showing:
		return m.quit.text
	case m.copied.showing:
		return m.copied.text
	case m.refused.showing:
		return m.refused.text
	case m.picking():
		return m.picker.keys(m.app != nil || !m.picker.quiet)
	case m.runs.active != nil:
		return m.runs.footer()
	case m.box != nil:
		return m.box.keys()
	case m.log.scrolled > 0:
		return "scrolled back · end newest · " + m.prompt.keys()
	case m.app == nil && !m.prompt.open():
		return "/ for commands · ctrl+c quit"
	}
	return m.prompt.keys()
}

func (m *model) title() string {
	if m.app == nil && m.picker != nil {
		status := " · " + m.picker.status(m.devices, m.listed)
		alarm := m.picker.alarm(m.devices, m.listed)
		before, after, found := strings.Cut(status, alarm)
		if alarm == "" || !found {
			return selectedStyle.Render("quack") + statusStyle.Render(status)
		}
		return selectedStyle.Render("quack") + statusStyle.Render(before) + problemStyle.Render(alarm) + statusStyle.Render(after)
	}
	if m.app == nil {
		return selectedStyle.Render("quack")
	}
	return selectedStyle.Render("quack") + statusStyle.Render(" · "+m.hello.Package+" "+m.hello.Version+" · "+string(m.target.Device))
}

func fit(lines []string, width, height int) []string {
	lines = append(lines, make([]string, max(0, height-len(lines)))...)[:height]
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return lines
}

func (n *notice) show(c clock, text string) tea.Cmd {
	n.text, n.showing = text, true
	n.shown++
	return after(c, noticeTime, noticeEnded{n, n.shown})
}

type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type wallClock struct{}

func (wallClock) Now() time.Time                         { return time.Now() }
func (wallClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// d counts from now rather than from when the command runs.
func after(c clock, d time.Duration, msg tea.Msg) tea.Cmd {
	ends := c.After(d)
	return func() tea.Msg {
		<-ends
		return msg
	}
}

// ctrl+c and ctrl+d do more than their command, so the picker handles them itself.
func commands() []command {
	return []command{
		{"/cancel", "ctrl+c", "Cancel the running action", false, (*model).cancel},
		{"/clear", "ctrl+l", "Clear the log", false, (*model).clear},
		{"/exit", "ctrl+d", "Cancel the run and exit", false, (*model).exit},
		{"/init-recipes", "", "Start the recipes of the app in .quack/ and let editors check them", true, (*model).initRecipes},
		{"/open-app", "ctrl+o", "Show the app's main screen", true, (*model).openApp},
		{"/pick-app", "", "Pick another app on this device", true, (*model).pickApp},
		{"/pick-device", "", "Pick another device", false, (*model).pickDevice},
		{"/reload", "ctrl+r", "Ask the app for its actions and read its recipes again", true, (*model).reload},
		{"/toggle-data", "", "Show or hide the JSON data of notifications and results", false, (*model).toggleData},
	}
}

func (clientSource) Track(ctx context.Context, send func([]adb.Listing)) error {
	return adb.Track(ctx, send)
}

func (clientSource) Inspect(listing adb.Listing) (client.Device, error) {
	return client.Inspect(listing)
}

func (clientSource) Packages(device adb.Device) ([]string, error) {
	return client.Packages(device)
}

func (s clientSource) Connect(target client.Target, mode client.StartMode, status io.Writer) (App, error) {
	app, err := client.Resolve(client.Options{Target: target, Mode: mode, StartTimeout: s.startTimeout, Status: status})
	if err != nil {
		return nil, err
	}
	return app, nil
}

func loadRecipes(dir, pkg string) recipes {
	cookbook, warnings, err := recipe.Load(dir, pkg)
	return recipes{cookbook, warnings, err}
}

func (r recipes) list() []recipe.Recipe {
	if r.cookbook == nil {
		return nil
	}
	return r.cookbook.Recipes
}

var (
	statusStyle  = lipgloss.NewStyle().Faint(true)
	problemStyle = lipgloss.NewStyle().Foreground(lipgloss.Red)
	footerStyle  = lipgloss.NewStyle().Faint(true)
	ruleStyle    = lipgloss.NewStyle().Faint(true)
)

const (
	maxListRows = 10
	panelRows   = 6
	minWidth    = 24
	// Room over the input for a rule and the three rows of a box with a hint.
	minHeight        = panelRows + 2
	noticeTime       = 2 * time.Second
	pollTime         = 5 * time.Second
	refusedDuringRun = "cancel the running action first (ctrl+c)"
	wheelRows        = 3
)
