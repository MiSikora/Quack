package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"

	"quack/internal/adb"
	"quack/internal/config"
	"quack/internal/device"
	"quack/internal/recipe"
	"quack/internal/session"
	"quack/internal/step"
	"quack/internal/wire"
)

// The app's status lines go into the log, since anything printed around the TUI would break the screen.
func Run(want device.Target, openApp bool, startTimeout time.Duration, version string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	settings, problems := config.Load()
	dir, _ := os.Getwd()
	activity := newActivity(ctx)
	backend := session.ADB{StartTimeout: startTimeout, Status: activity}
	_, err := tea.NewProgram(newModel(backend, activity, want, openApp, version, dir, settings, problems)).Run()
	return err
}

type model struct {
	activity   activity
	tracker    tracker
	connection connection
	picker     *picker
	prompt     prompt
	box        *paramBox
	runs       runs
	hideData   bool
	dir        string
	quit       notice
	copied     notice
	refused    notice
	log        sessionLog
	header     string
	mouse      mouse
	clipboard  func(text string) error
	clock      clock
	width      int
	height     int
}

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

func newModel(backend session.Backend, activity activity, want device.Target, openApp bool, version, dir string, settings config.Config, problems []string) *model {
	m := &model{
		activity:   activity,
		tracker:    newTracker(backend),
		connection: connection{backend: backend, opensApp: openApp},
		dir:        dir,
		picker:     newPicker(want, 0),
		clock:      wallClock{},
		prompt:     prompt{input: newInput(""), grouped: settings.GroupNamespaces},
		hideData:   !settings.ShowData,
		runs:       newRuns(),
		clipboard:  clipboard.WriteAll,
		header:     header(version),
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
	return tea.Batch(m.activity.listen(), m.tracker.track(m.activity, m.clock), m.picker.input.Focus(), m.tracker.poll(m.clock))
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	return m, tea.Batch(cmd, m.focus())
}

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

func (m *model) picking() bool { return m.picker != nil && m.picker.showing(m.tracker.devices) }

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
	case runEvent:
		m.log.add(m.runs.event(msg, m.hideData))
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
		m.tracker.inspected(msg)
		return tea.Batch(m.checkApp(), m.advance())
	case polled:
		m.tracker.polling = false
		return tea.Batch(m.inspectPicked(), m.tracker.poll(m.clock))
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
		case m.connection.app != nil:
			m.picker = nil
			return m.prompt.input.Focus()
		case !p.quiet:
			p.quiet = true
			p.clear()
		case p.startup && p.input.Value() == "":
			p.startup = false
			p.clear()
		default:
			p.clear()
		}
		return nil
	case "tab":
		return nil
	}
	return p.update(msg, m.tracker.devices)
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
	app, actions, cookbook := m.connection.app, m.prompt.actions, m.connection.cookbook
	header, cmd := m.runs.start(m.activity, &run{runnable: r}, func(ctx context.Context, emit func(step.Event)) (json.RawMessage, error) {
		return session.Run(ctx, app, actions, cookbook, session.Runnable{Name: r.ID, Recipe: r.recipe}, args, emit)
	})
	m.log.add(header)
	return cmd
}

func (m *model) openBox(r runnable) tea.Cmd {
	m.box = newParamBox(r, m.width)
	m.prompt.input.Blur()
	return m.box.init()
}

func (m *model) openApp() tea.Cmd {
	app, activity := m.connection.app, m.activity
	return func() tea.Msg {
		line := statusStyle.Render("showed the app's main screen")
		if _, err := step.Do(context.Background(), app, step.OpenApp{}, nil); err != nil {
			line = problemStyle.Render("open-app: " + err.Error())
		}
		activity.send(logged{line})
		return nil
	}
}

func (m *model) initRecipes() tea.Cmd {
	pkg := m.connection.target.Package
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
	switch mapped, err := recipe.Maps(m.dir, pkg); {
	case err != nil:
		m.log.add(problemStyle.Render("init-recipes: " + err.Error()))
	case !mapped:
		m.log.add(statusStyle.Render("nothing maps " + pkg + " to a recipes file yet: add a line under [recipes] in .quack/quack.toml, such as '" +
			regexp.QuoteMeta(pkg) + "' = \"recipes.ts\""))
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
	if msg.connection != m.connection.generation {
		return
	}
	m.setRecipes(msg.recipes)
	if msg.err != nil {
		m.log.add(problemStyle.Render("reload: " + msg.err.Error()))
		return
	}
	m.connection.hello = msg.hello
	m.prompt.setActions(msg.actions)
	line := fmt.Sprintf("reloaded %d actions", len(msg.actions))
	if msg.recipes.err == nil {
		line += fmt.Sprintf(" and %d recipes", len(msg.recipes.list()))
	}
	m.log.add(statusStyle.Render(line))
}

func (m *model) setRecipes(r recipes) {
	m.connection.cookbook = r.cookbook
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
	if m.connection.app == nil {
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
		p.device = m.connection.target.Device
		p.toStep(packageStep)
	})
}

func (m *model) openPicker(setup func(*picker)) tea.Cmd {
	m.box = nil
	m.prompt.input.Blur()
	m.picker = newPicker(device.Target{}, m.width)
	setup(m.picker)
	return tea.Batch(m.picker.input.Focus(), m.tracker.inspectAll(), m.tracker.poll(m.clock))
}

func (m *model) pick() tea.Cmd {
	p := m.picker
	row, ok := p.selection(m.tracker.devices)
	switch {
	case !ok:
		return nil
	case p.step == packageStep:
		return m.connect(device.Target{Device: row.device, Package: row.pkg})
	}
	p.device, p.back, p.skipPackage = row.device, true, true
	p.toStep(packageStep)
	return m.advance()
}

func (m *model) advance() tea.Cmd {
	p := m.picker
	if p == nil || p.connecting != nil {
		return nil
	}
	if row, ok := p.onlyDevice(m.tracker.devices); p.step == deviceStep && p.auto && ok {
		p.device, p.back = row.device, false
		p.toStep(packageStep)
	}
	if pkgs := p.packages(m.tracker.devices, p.device); p.step == packageStep && p.skipPackage && len(pkgs) == 1 {
		return m.connect(device.Target{Device: p.device, Package: pkgs[0]})
	}
	return nil
}

func (m *model) connect(target device.Target) tea.Cmd {
	m.picker.connecting = &target
	return m.connection.connect(target, m.activity, m.dir)
}

func (m *model) connected(msg connected) tea.Cmd {
	if m.picker == nil || m.picker.connecting == nil || *m.picker.connecting != msg.target {
		return nil
	}
	if msg.err != nil {
		m.picker.connecting, m.picker.skipPackage = nil, false
		m.log.add(problemStyle.Render(msg.target.Package + ": " + msg.err.Error()))
		return m.advance()
	}
	m.connection.open(msg)
	m.picker = nil
	m.prompt.clear()
	m.prompt.connect(true)
	m.prompt.setActions(msg.actions)
	where := string(msg.target.Device)
	if i := m.tracker.index(msg.target.Device); i >= 0 && m.tracker.devices[i].Model != "" {
		where = m.tracker.devices[i].Model + " · " + where
	}
	m.log.add(statusStyle.Render("switched to " + msg.target.Package + " on " + where))
	m.setRecipes(msg.recipes)
	return m.prompt.input.Focus()
}

func (m *model) list(listings []adb.Listing) tea.Cmd {
	cmds := m.tracker.list(listings)
	if c := &m.connection; c.app != nil && m.tracker.index(c.target.Device) < 0 {
		cmds = append(cmds, m.drop(string(c.target.Device)+" disconnected"))
	}
	if p := m.picker; p != nil && p.step == packageStep && m.tracker.index(p.device) < 0 {
		p.toStep(deviceStep)
	}
	return tea.Batch(append(cmds, m.advance())...)
}

func (m *model) inspectPicked() tea.Cmd {
	if m.picker != nil {
		return m.tracker.inspectAll()
	}
	return m.tracker.refresh(m.connection.target.Device)
}

func (m *model) checkApp() tea.Cmd {
	c := &m.connection
	if c.app == nil {
		return nil
	}
	i := m.tracker.index(c.target.Device)
	if i < 0 || !m.tracker.devices[i].inspected || slices.Contains(m.tracker.devices[i].Packages, c.target.Package) {
		return nil
	}
	return m.drop(c.target.Package + " left " + string(c.target.Device))
}

func (m *model) drop(line string) tea.Cmd {
	m.log.add(statusStyle.Render(line))
	m.runs.stop()
	m.connection.drop()
	m.picker = nil
	m.refused.showing = false
	m.prompt.clear()
	m.prompt.connect(false)
	m.prompt.setActions(nil)
	m.setRecipes(recipes{})
	return m.openPicker(func(p *picker) { p.quiet = true })
}

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

func after(c clock, d time.Duration, msg tea.Msg) tea.Cmd {
	ends := c.After(d)
	return func() tea.Msg {
		<-ends
		return msg
	}
}

func commands() []command {
	return []command{
		{"/cancel", "ctrl+c", "Cancel the running action", false, (*model).cancel},
		{"/clear", "ctrl+l", "Clear the log", false, (*model).clear},
		{"/exit", "ctrl+d", "Cancel the run and exit", false, (*model).exit},
		{"/init-recipes", "", "Start the recipes of the app in .quack/ and let editors check them", true, (*model).initRecipes},
		{"/open-app", "ctrl+o", "Show the app's main screen", true, (*model).openApp},
		{"/pick-app", "", "Pick another app on this device", true, (*model).pickApp},
		{"/pick-device", "", "Pick another device", false, (*model).pickDevice},
		{"/reload", "ctrl+r", "Ask the app for its actions and read its recipes again", true, func(m *model) tea.Cmd { return m.connection.reload(m.activity, m.dir) }},
		{"/toggle-data", "", "Show or hide the JSON data of notifications and results", false, (*model).toggleData},
	}
}

var (
	statusStyle  = lipgloss.NewStyle().Faint(true)
	problemStyle = lipgloss.NewStyle().Foreground(lipgloss.Red)
)

const (
	noticeTime       = 2 * time.Second
	refusedDuringRun = "cancel the running action first (ctrl+c)"
	wheelRows        = 3
)
