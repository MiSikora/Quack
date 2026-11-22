// Package tui is quack's interactive mode. It takes the whole terminal, on the alternate screen, and leaves it as it was
// on quit. A panel of constant height sits on the last rows: the title, an input over the list of the actions it
// matches, or of the devices or apps to pick from, or else the box of one action's params, and a footer of key hints or
// the run going on. The rows above it
// show the session's log, which quack draws and scrolls itself, and where a drag of the mouse selects text and copies
// it.
package tui

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/wire"
)

// App is what the TUI needs from a quack app. client.App satisfies it.
type App interface {
	Describe(ctx context.Context) (wire.Hello, []wire.Action, error)
	Call(ctx context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error)
	Start(mode client.StartMode) error
}

// Run shows the TUI until the user quits. It has the user pick a device and an app first, among those that want leaves,
// and starts the first app picked the way mode asks. The app's status lines go into the log with the rest of the
// output, since anything printed around the TUI would break the screen.
func Run(want client.Target, mode client.StartMode, startTimeout time.Duration) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newActivity(ctx)
	_, err := tea.NewProgram(newModel(clientSource{startTimeout}, activity, want, mode)).Run()
	return err
}

// Source is where the TUI finds the devices and the apps on them. clientSource is the real one.
type Source interface {
	// Track sends the devices adb knows each time they change. It returns when ctx ends, or with the error that ended
	// it.
	Track(ctx context.Context, send func([]adb.Listing)) error
	Inspect(listing adb.Listing) (client.Device, error)
	// Connect returns the app of target, which starts the way mode asks and says what it does to status.
	Connect(target client.Target, mode client.StartMode, status io.Writer) (App, error)
}

// model lays out the log and the panel and routes messages: keys go to the device or app picker while one is open, to
// the param box while one is open, and to the action picker otherwise, and the mouse goes to the log.
type model struct {
	source   Source
	activity activity
	// app is the picked app, which runs on target, or nil until the first pick.
	app    App
	target client.Target
	// mode is how the next pick starts its app: the way the flags chose for the first, and Wake after it.
	mode client.StartMode
	// devices are the devices adb lists, in its order, and listed is whether it listed them yet.
	devices []seenDevice
	listed  bool
	// asked numbers the questions to the devices, and pending is the number of the last one asked of each device that
	// is still unanswered. Only the answer to the last question counts, and a poll does not ask a device that has not
	// answered yet.
	asked   int
	pending map[adb.Device]int
	// picker is the open device or app picker, or nil while the actions show. polling is whether a poll is due.
	picker   *picker
	polling  bool
	pollTime time.Duration
	hello    wire.Hello
	prompt   prompt
	// box is the open param box, or nil while the picker shows. The picker keeps its text and selection under it.
	box  *paramBox
	runs runs
	// quit asks for a second ctrl+c to quit, copied says what the mouse copied, and refused why a command did nothing.
	quit    notice
	copied  notice
	refused notice
	log     sessionLog
	mouse   mouse
	// clipboard writes text to the system's clipboard. Tests swap it to read what the model copies.
	clipboard func(text string) error
	width     int
	height    int
}

// notice is a message in the footer for two seconds. shown counts the times it showed, so that the end of an earlier
// one does not close a later one.
type notice struct {
	text    string
	showing bool
	shown   int
}

// noticeEnded is the end of the two seconds of notice's shown-th time.
type noticeEnded struct {
	notice *notice
	shown  int
}

// command is one of the TUI's own commands, which the picker lists for text that starts with '/'. key runs it too.
type command struct {
	name        string
	key         string
	description string
	run         func(*model) tea.Cmd
}

// described is the answer to a reload.
type described struct {
	hello   wire.Hello
	actions []wire.Action
	err     error
}

// opened is the end of showing the app's main screen.
type opened struct {
	err error
}

// listed is the devices adb knows, each time they change, and trackLost the end of following them, which starts again
// after pollTime.
type (
	listed struct {
		listings []adb.Listing
	}
	trackLost struct {
		err error
	}
)

// inspected is what a device answered, and polled the time to ask the devices again.
type (
	inspected struct {
		serial adb.Device
		device client.Device
		asked  int
		err    error
	}
	polled struct{}
)

// connected is the end of starting and describing a picked app.
type connected struct {
	target  client.Target
	app     App
	hello   wire.Hello
	actions []wire.Action
	err     error
}

// clientSource finds devices through adb, and the apps on them through the client.
type clientSource struct {
	startTimeout time.Duration
}

func newModel(source Source, activity activity, want client.Target, mode client.StartMode) *model {
	m := &model{
		source:    source,
		activity:  activity,
		pending:   map[adb.Device]int{},
		mode:      mode,
		picker:    newPicker(want, 0),
		pollTime:  pollTime,
		prompt:    newPrompt(nil),
		runs:      newRuns(),
		clipboard: clipboard.WriteAll,
	}
	m.picker.auto, m.picker.skipPackage = true, true
	m.log.add(duckView())
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.activity.listen(), m.track(), m.picker.focus(), m.poll())
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, m.update(msg)
}

// View is the log over the panel, on the whole of the alternate screen. The mouse reports its clicks, drags and wheel,
// so that the TUI scrolls the log itself.
func (m *model) View() tea.View {
	view := tea.NewView(strings.Join(append(m.log.view(m.logHeight()), m.panel()), "\n"))
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
	case runNotification:
		m.log.add(m.runs.notification(msg))
		return m.activity.listen()
	case runEnded:
		m.log.add(m.runs.end(msg))
		return m.activity.listen()
	case statusLine:
		m.log.add(statusStyle.Render(msg.text))
		return m.activity.listen()
	case described:
		m.describe(msg)
		return m.activity.listen()
	case opened:
		m.log.add(m.openedLine(msg))
		return m.activity.listen()
	case listed:
		return tea.Batch(m.list(msg.listings), m.activity.listen())
	case trackLost:
		m.log.add(problemStyle.Render(msg.err.Error()))
		return m.activity.listen()
	case inspected:
		m.inspected(msg)
		return m.advance()
	case polled:
		m.polling = false
		if m.picker == nil {
			return nil
		}
		return tea.Batch(m.inspectAll(), m.poll())
	case connected:
		cmd := m.connected(msg)
		return tea.Batch(cmd, m.activity.listen())
	case noticeEnded:
		if msg.shown == msg.notice.shown {
			msg.notice.showing = false
		}
		return nil
	default:
		return tea.Batch(m.runs.tick(msg), m.forward(msg))
	}
}

// press handles a key. The keys of the commands and of scrolling work everywhere. End returns to the newest output
// only while the log is scrolled back, and moves to the end of the input otherwise.
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
		m.log.follow()
		return nil
	}
	if m.picker != nil {
		return m.pressPicker(msg)
	}
	for _, c := range commands() {
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
		if m.prompt.value() == "" {
			return m.exit()
		}
	case "enter":
		return m.choose()
	case "tab":
		if c, ok := m.prompt.selection(); ok && c.command == nil && len(c.action.Params) > 0 {
			return m.openBox(c.action)
		}
		return nil
	case "esc":
		m.prompt.clear()
		return nil
	}
	return m.prompt.update(msg)
}

// pressPicker handles a key while the device or app picker is open. Only ctrl+c and ctrl+d work while a picked app
// starts.
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
			return m.prompt.focus()
		default:
			p.clear()
		}
		return nil
	case "tab":
		return nil
	}
	return p.update(msg, m.devices)
}

// pressBox handles a key while the param box is open. Esc closes it, and the picker comes back as it was. ctrl+c
// stops the action that runs, and closes the box when none does. The box runs its action only while none runs.
func (m *model) pressBox(msg tea.KeyPressMsg) tea.Cmd {
	switch key := msg.String(); {
	case key == "ctrl+c" && m.runs.active != nil:
		return m.interrupt()
	case key == "esc", key == "ctrl+c":
		m.box = nil
		return m.prompt.focus()
	}
	run, cmd := m.box.press(msg)
	switch {
	case !run:
		return cmd
	case m.runs.active != nil:
		return tea.Batch(cmd, m.refused.show(refusedDuringRun))
	}
	action, args := m.box.action, m.box.args()
	m.box = nil
	m.prompt.clear()
	return tea.Batch(m.start(action, args), m.prompt.focus())
}

// interrupt is ctrl+c: it stops the action that runs and leaves the text, or else clears the text while none runs, or
// else asks to press it again to quit, and quits when it comes again while that shows. A second ctrl+c while the
// action stops asks too, so that an action that does not stop cannot keep quack open.
func (m *model) interrupt() tea.Cmd {
	switch {
	case m.quit.showing:
		return m.exit()
	case m.runs.stop():
		return nil
	case m.runs.active == nil && m.clearInput():
		return nil
	}
	return m.quit.show("press ctrl+c again to quit")
}

// clearInput empties the text of the open picker, and says whether there was text.
func (m *model) clearInput() bool {
	if m.picker != nil {
		return m.picker.clear()
	}
	return m.prompt.clear()
}

// choose runs the selected command or action. An action with required params opens its box instead, even while
// another runs. Any other action only says to stop the one that runs.
func (m *model) choose() tea.Cmd {
	c, ok := m.prompt.selection()
	switch {
	case !ok:
		return nil
	case c.command != nil:
		m.prompt.clear()
		return c.command.run(m)
	case hasRequired(c.action):
		return m.openBox(c.action)
	case m.runs.active != nil:
		return m.refused.show(refusedDuringRun)
	default:
		m.prompt.clear()
		return m.start(c.action, nil)
	}
}

func (m *model) start(action wire.Action, args map[string]wire.RawArg) tea.Cmd {
	header, work := m.runs.start(m.app, m.activity, action, args)
	m.log.add(header)
	return work
}

func (m *model) openBox(action wire.Action) tea.Cmd {
	m.box = newParamBox(action, m.width)
	m.prompt.blur()
	return m.box.init()
}

// reload asks the app for its actions. The answer goes through activity, after the status lines on the way to it.
func (m *model) reload() tea.Cmd {
	app, activity := m.app, m.activity
	return func() tea.Msg {
		hello, actions, err := app.Describe(activity.ctx)
		activity.send(described{hello, actions, err})
		return nil
	}
}

// open shows the app's main screen. The end goes through activity, after the status lines on the way to it.
func (m *model) open() tea.Cmd {
	app, activity := m.app, m.activity
	return func() tea.Msg {
		activity.send(opened{app.Start(client.Open)})
		return nil
	}
}

// describe takes the actions of a reload, or keeps the ones there are when it failed, and says which.
func (m *model) describe(msg described) {
	if msg.err != nil {
		m.log.add(problemStyle.Render("reload: " + msg.err.Error()))
		return
	}
	m.hello = msg.hello
	m.prompt.setActions(msg.actions)
	m.log.add(statusStyle.Render(fmt.Sprintf("reloaded %d actions", len(msg.actions))))
}

func (m *model) openedLine(msg opened) string {
	if msg.err != nil {
		return problemStyle.Render("open: " + msg.err.Error())
	}
	return statusStyle.Render("showed the app's main screen")
}

func (m *model) stop() tea.Cmd {
	m.runs.stop()
	return nil
}

// exit stops the run and quits.
func (m *model) exit() tea.Cmd {
	m.runs.cancel()
	return tea.Quit
}

// switchDevice opens the device picker, which goes on to the app picker unless the device has one app. It does nothing
// while a run is active.
func (m *model) switchDevice() tea.Cmd {
	if m.runs.active != nil {
		return m.refused.show(refusedDuringRun)
	}
	return m.openPicker(func(p *picker) { p.skipPackage = true })
}

// switchApp opens the app picker for the device of the app. It does nothing while a run is active.
func (m *model) switchApp() tea.Cmd {
	if m.runs.active != nil {
		return m.refused.show(refusedDuringRun)
	}
	return m.openPicker(func(p *picker) {
		p.device = m.target.Device
		p.toStep(packageStep)
	})
}

// openPicker opens a picker that setup prepares, and asks the devices for their apps again.
func (m *model) openPicker(setup func(*picker)) tea.Cmd {
	m.box = nil
	m.prompt.blur()
	m.picker = newPicker(client.Target{}, m.width)
	setup(m.picker)
	return tea.Batch(m.picker.focus(), m.inspectAll(), m.poll())
}

// pick takes the selected device, and goes on to its apps, or the selected app, and starts it.
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

// advance takes the step's only choice when the picker takes it by itself: the only device that can be picked, once
// every device answered, which goes on to its apps, and the only app, which starts.
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

// connect starts the app of target and asks it for its actions. The end goes through activity, after the status lines
// on the way to it.
func (m *model) connect(target client.Target) tea.Cmd {
	m.picker.connecting = &target
	source, activity, mode := m.source, m.activity, m.mode
	return func() tea.Msg {
		app, err := source.Connect(target, mode, activity)
		msg := connected{target: target, app: app, err: err}
		if err == nil {
			msg.hello, msg.actions, msg.err = app.Describe(activity.ctx)
		}
		activity.send(msg)
		return nil
	}
}

// connected hands the panel to the actions of the app that started, unless the picker has gone on to another pick
// since. An app that failed to start leaves the picker on its step, which then no longer takes the only app by itself,
// so that the user picks again.
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
	m.prompt.setActions(msg.actions)
	where := string(msg.target.Device)
	if i := m.deviceIndex(msg.target.Device); i >= 0 && m.devices[i].Model != "" {
		where = m.devices[i].Model + " · " + where
	}
	m.log.add(statusStyle.Render("switched to " + msg.target.Package + " on " + where))
	return m.prompt.focus()
}

// track follows the devices adb knows for the whole session, and follows them again after pollTime when adb fails. It
// says only the first failure of a run of them.
func (m *model) track() tea.Cmd {
	source, activity, wait := m.source, m.activity, m.pollTime
	return func() tea.Msg {
		failing := false
		for activity.ctx.Err() == nil {
			err := source.Track(activity.ctx, func(listings []adb.Listing) {
				failing = false
				activity.send(listed{listings})
			})
			if err != nil && !failing {
				failing = true
				activity.send(trackLost{err})
			}
			select {
			case <-activity.ctx.Done():
			case <-time.After(wait):
			}
		}
		return nil
	}
}

// list takes the devices adb lists now, and asks those that became ready for their apps. When the device of the app
// is gone, it says so and opens the device picker, as at the start.
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
		m.log.add(statusStyle.Render(string(m.target.Device) + " disconnected"))
		m.runs.stop()
		m.app = nil
		m.picker = nil
		m.refused.showing = false
		cmds = append(cmds, m.openPicker(func(p *picker) { p.auto, p.skipPackage = true, true }))
	}
	if p := m.picker; p != nil && p.step == packageStep && m.deviceIndex(p.device) < 0 {
		p.toStep(deviceStep)
	}
	return tea.Batch(append(cmds, m.advance())...)
}

// inspect asks a ready device for its model and apps.
func (m *model) inspect(listing adb.Listing) tea.Cmd {
	if listing.State != adb.Ready {
		return nil
	}
	m.asked++
	asked, source := m.asked, m.source
	m.pending[listing.Device] = asked
	return func() tea.Msg {
		device, err := source.Inspect(listing)
		return inspected{listing.Device, device, asked, err}
	}
}

// inspectAll asks again the ready devices that answered the last question.
func (m *model) inspectAll() tea.Cmd {
	var cmds []tea.Cmd
	for _, d := range m.devices {
		if _, waiting := m.pending[d.Device.Device]; !waiting {
			cmds = append(cmds, m.inspect(d.Listing))
		}
	}
	return tea.Batch(cmds...)
}

// inspected keeps what a device answered to the last question asked of it, while adb still lists it in the state it
// was asked in. A device that did not answer keeps what it answered before, since adb soon lists it as gone when it
// went away.
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

// poll asks the devices for their apps again after pollTime, unless a poll is due already. The answer to it polls again
// while a picker is open.
func (m *model) poll() tea.Cmd {
	if m.polling {
		return nil
	}
	m.polling = true
	return tea.Tick(m.pollTime, func(time.Time) tea.Msg { return polled{} })
}

func (m *model) deviceIndex(device adb.Device) int {
	return slices.IndexFunc(m.devices, func(d seenDevice) bool { return d.Device.Device == device })
}

// logHeight is the number of rows above the panel, where the log shows.
func (m *model) logHeight() int { return m.height - panelHeight(m.height) }

// forward hands a message that is not quack's own, such as a cursor blink, to the inputs that may be waiting for it.
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

// panel is the title, a rule, the picker's input, a rule and its list or else the param box, a rule and the footer,
// fitted to the panel's height and the window's width. Every state goes through it, so that the panel never changes
// height and its rows never move.
func (m *model) panel() string {
	height := panelHeight(m.height)
	if m.width < minWidth || m.height < minHeight {
		return strings.Join(fit([]string{"Make the window larger."}, m.width, height), "\n")
	}
	rows := listRows(m.height)
	rule := ruleStyle.Render(strings.Repeat("─", m.width))
	var middle []string
	switch {
	case m.picker != nil:
		middle = append([]string{m.picker.view(m.devices), rule}, m.picker.list(m.devices, m.listed, rows)...)
	case m.box != nil:
		middle = m.box.view(rows + 2)
	default:
		middle = append([]string{m.prompt.view(), rule}, m.prompt.list(rows)...)
	}
	lines := append([]string{"", m.title(), rule}, fit(middle, m.width, rows+2)...)
	lines = append(lines, rule, footerStyle.Render(" "+m.footer()))
	return strings.Join(fit(lines, m.width, height), "\n")
}

// footer is the quit prompt, or else what the mouse copied, or else why a command did nothing, or else the picker's
// keys, or else the run, or else the keys that work now.
func (m *model) footer() string {
	switch {
	case m.quit.showing:
		return m.quit.text
	case m.copied.showing:
		return m.copied.text
	case m.refused.showing:
		return m.refused.text
	case m.picker != nil:
		return m.picker.keys(m.app != nil)
	case m.runs.active != nil:
		return m.runs.footer()
	case m.box != nil:
		return m.box.keys()
	case m.log.scrolled > 0:
		return "scrolled back · end newest · enter run · tab params · ↑↓ select · ctrl+c stop/quit"
	}
	return "enter run · tab params · ↑↓ select · ctrl+c stop/quit"
}

// title is quack, and the app and its device once one is picked.
func (m *model) title() string {
	if m.app == nil {
		return selectedStyle.Render("quack")
	}
	return selectedStyle.Render("quack") + statusStyle.Render(" · "+m.hello.Package+" "+m.hello.Version+" · "+string(m.target.Device))
}

// panelHeight is the height of the panel in a window height rows high: the list's rows and the rest of the panel, but
// no more than the window.
func panelHeight(height int) int { return max(0, min(height, listRows(height)+panelOverhead)) }

// listRows is maxListRows, or fewer in a window less than twice the panel's height, so that the panel leaves at least
// half the window to the output.
func listRows(height int) int {
	if height < 2*(maxListRows+panelOverhead) {
		return max(1, height/2-panelOverhead)
	}
	return maxListRows
}

// fit cuts each line to width and pads or cuts the lines to height.
func fit(lines []string, width, height int) []string {
	lines = append(lines, make([]string, max(0, height-len(lines)))...)[:height]
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return lines
}

// show shows text for two seconds.
func (n *notice) show(text string) tea.Cmd {
	n.text, n.showing = text, true
	n.shown++
	shown := n.shown
	return tea.Tick(noticeTime, func(time.Time) tea.Msg { return noticeEnded{n, shown} })
}

// commands are the TUI's own commands. ctrl+c and ctrl+d do more than their command, so the picker handles them itself.
func commands() []command {
	return []command{
		{"/reload-actions", "ctrl+r", "Ask the app for its actions again", (*model).reload},
		{"/open-app", "ctrl+o", "Show the app's main screen", (*model).open},
		{"/switch-device", "", "Pick another device", (*model).switchDevice},
		{"/switch-app", "", "Pick another app on this device", (*model).switchApp},
		{"/stop-action", "ctrl+c", "Stop the running action", (*model).stop},
		{"/quit", "ctrl+d", "Stop the run and quit", (*model).exit},
	}
}

func (clientSource) Track(ctx context.Context, send func([]adb.Listing)) error {
	return adb.Track(ctx, send)
}

func (clientSource) Inspect(listing adb.Listing) (client.Device, error) {
	return client.Inspect(listing)
}

func (s clientSource) Connect(target client.Target, mode client.StartMode, status io.Writer) (App, error) {
	app, err := client.Resolve(client.Options{Target: target, Mode: mode, StartTimeout: s.startTimeout, Status: status})
	if err != nil {
		return nil, err
	}
	return app, nil
}

func hasRequired(action wire.Action) bool {
	for _, p := range action.Params {
		if _, ok := p.Presence.(wire.Required); ok {
			return true
		}
	}
	return false
}

var (
	statusStyle  = lipgloss.NewStyle().Faint(true)
	problemStyle = lipgloss.NewStyle().Foreground(lipgloss.Red)
	footerStyle  = lipgloss.NewStyle().Faint(true)
	ruleStyle    = lipgloss.NewStyle().Faint(true)
)

const (
	maxListRows = 5
	// panelOverhead is the panel's rows besides the list: a blank row, the title, three rules, the input and the
	// footer.
	panelOverhead = 7
	minWidth      = 24
	minHeight     = panelOverhead + 1
	noticeTime    = 2 * time.Second
	// pollTime is how often an open picker asks the devices for their apps, so that an app installed while it waits
	// shows by itself.
	pollTime = 5 * time.Second
	// refusedDuringRun is why an action, /switch-device and /switch-app do nothing while an action runs.
	refusedDuringRun = "stop the running action first (ctrl+c)"
	// wheelRows is how far a turn of the mouse wheel scrolls the log.
	wheelRows = 3
)
