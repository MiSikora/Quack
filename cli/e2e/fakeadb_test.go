package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAdb is an adb server with devices on which quack-enabled apps run. It speaks the adb smart-socket protocol,
// answers the shell commands quack sends, and plays each app's side of the quack protocol. It records what quack asks
// for, so tests can check side effects such as starting the keep-alive service.
type fakeAdb struct {
	listener net.Listener
	devices  []*fakeDevice

	mu  sync.Mutex
	log []string
}

type fakeDevice struct {
	serial string
	// state is what adb reports for the device, such as device, offline or unauthorized. Empty means adb does not know
	// the device.
	state string
	model string
	user  string
	apps  []*fakeApp
	// adbHangs makes the adb server accept connections and never answer them.
	adbHangs bool
}

type fakeApp struct {
	pkg     string
	version string
	// installed is false for a package that the device does not have.
	installed bool
	running   bool
	// deaf apps never bind their socket, so quack times out waiting for them.
	deaf bool
	// noDump apps close every connection without hello, as an app does for a caller without DUMP.
	noDump      bool
	protocol    int
	hasLauncher bool
	// reply writes the frames after hello for one request. It may sleep to simulate a long action.
	reply func(request map[string]any, write func(frame string))
}

func startFakeAdb(t *testing.T, devices []*fakeDevice) *fakeAdb {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	adb := &fakeAdb{listener: listener, devices: devices}
	go adb.serve()
	t.Cleanup(func() { listener.Close() })
	return adb
}

func (a *fakeAdb) port() string {
	return strconv.Itoa(a.listener.Addr().(*net.TCPAddr).Port)
}

func (a *fakeAdb) record(format string, args ...any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.log = append(a.log, fmt.Sprintf(format, args...))
}

func (a *fakeAdb) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.log...)
}

func (a *fakeAdb) serve() {
	for {
		conn, err := a.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			a.handle(conn)
		}()
	}
}

func (a *fakeAdb) handle(conn net.Conn) {
	if a.devices[0].adbHangs {
		io.Copy(io.Discard, conn)
		return
	}
	request, err := readRequest(conn)
	if err != nil {
		return
	}
	switch {
	case strings.HasPrefix(request, "host:transport:"):
		serial := strings.TrimPrefix(request, "host:transport:")
		d := a.device(serial)
		if d == nil {
			fail(conn, "device '"+serial+"' not found")
			return
		}
		if d.state != "device" {
			fail(conn, "device "+d.state)
			return
		}
		okay(conn)
		service, err := readRequest(conn)
		if err != nil {
			return
		}
		a.handleService(conn, d, service)
	case request == "host:devices":
		var out strings.Builder
		for _, d := range a.devices {
			if d.state != "" {
				fmt.Fprintf(&out, "%s\t%s\n", d.serial, d.state)
			}
		}
		okay(conn)
		writeLengthPrefixed(conn, out.String())
	case strings.HasPrefix(request, "host-serial:") && strings.HasSuffix(request, ":get-state"):
		d := a.device(strings.TrimSuffix(strings.TrimPrefix(request, "host-serial:"), ":get-state"))
		if d == nil {
			fail(conn, "device not found")
			return
		}
		okay(conn)
		writeLengthPrefixed(conn, d.state)
	default:
		fail(conn, "unknown request "+request)
	}
}

// device returns the device adb knows by serial, or nil.
func (a *fakeAdb) device(serial string) *fakeDevice {
	for _, d := range a.devices {
		if d.serial == serial && d.state != "" {
			return d
		}
	}
	return nil
}

// handleService runs service on d.
func (a *fakeAdb) handleService(conn net.Conn, d *fakeDevice, service string) {
	if cmd, ok := strings.CutPrefix(service, "shell:"); ok {
		okay(conn)
		io.WriteString(conn, a.shell(d, cmd))
		return
	}
	if socket, ok := strings.CutPrefix(service, "localabstract:"); ok {
		app := a.listening(d, socket)
		if app == nil {
			fail(conn, "closed")
			return
		}
		okay(conn)
		a.converse(conn, app)
		return
	}
	fail(conn, "unknown service "+service)
}

// shell answers the commands quack runs on d, as Android's tools would. It records each command with the device that
// ran it.
func (a *fakeAdb) shell(d *fakeDevice, cmd string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	record := func(entry string) { a.log = append(a.log, "shell "+d.serial+": "+entry) }
	switch {
	case cmd == "getprop ro.product.model":
		record("getprop ro.product.model")
		return d.model + "\n"
	case cmd == "cmd package query-receivers --components -a io.mehow.quack.WAKE":
		record("query-receivers")
		var out strings.Builder
		for _, app := range d.apps {
			if app.installed {
				fmt.Fprintf(&out, "%s/.lib.android.QuackWakeReceiver\n", app.pkg)
			}
		}
		return out.String()
	case cmd == "cmd activity get-current-user":
		record("get-current-user")
		return d.user + "\n"
	case strings.HasPrefix(cmd, "pidof "):
		pkg := strings.Fields(cmd)[1]
		app := d.app(pkg)
		var out strings.Builder
		if app == nil || !app.running {
			if app == nil || !app.installed {
				fmt.Fprintf(&out, "Error: %s is not installed\n", pkg)
			}
			out.WriteString("stopped\n")
		}
		switch {
		case strings.Contains(cmd, "am broadcast -a io.mehow.quack.WAKE -p "+pkg+" --include-stopped-packages"):
			record("wake " + pkg)
			out.WriteString("Broadcasting: Intent { act=io.mehow.quack.WAKE flg=0x400010 pkg=" + pkg + " }\nBroadcast completed: result=0\n")
		case strings.Contains(cmd, "query-activities --brief -a android.intent.action.MAIN -c android.intent.category.LAUNCHER "+pkg):
			record("open " + pkg)
			if app != nil && app.installed && app.hasLauncher {
				out.WriteString("Starting: Intent { cmp=" + pkg + "/.MainActivity }\nStatus: ok\n")
			} else {
				fmt.Fprintf(&out, "Error: %s has no launcher activity\n", pkg)
			}
		default:
			record("unexpected " + cmd)
		}
		if app != nil && app.installed && (app.hasLauncher || !strings.Contains(cmd, "query-activities")) {
			app.running = true
		}
		return out.String()
	case strings.HasPrefix(cmd, "am start-foreground-service -n "):
		component := strings.TrimPrefix(cmd, "am start-foreground-service -n ")
		record("start-foreground-service " + component)
		return "Starting service: Intent { cmp=" + component + " }\n"
	default:
		record("unexpected " + cmd)
		return "/system/bin/sh: unexpected command\n"
	}
}

// listening returns the app on d whose server listens on socket, or nil when nothing does.
func (a *fakeAdb) listening(d *fakeDevice, socket string) *fakeApp {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, app := range d.apps {
		if socket == app.pkg+".quack."+d.user && app.installed && app.running && !app.deaf {
			return app
		}
	}
	return nil
}

func (a *fakeAdb) converse(conn net.Conn, app *fakeApp) {
	if app.noDump {
		return
	}
	hello, _ := json.Marshal(map[string]any{"hello": map[string]any{
		"protocol": app.protocol,
		"app":      map[string]any{"package": app.pkg, "version": app.version},
	}})
	fmt.Fprintf(conn, "%s\n", hello)
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}
	a.record("request: %s", strings.TrimSuffix(line, "\n"))
	var request map[string]any
	if err := json.Unmarshal([]byte(line), &request); err != nil {
		fmt.Fprintln(conn, `{"error":{"code":"bad_frame","message":"malformed request"}}`)
		return
	}
	app.reply(request, func(frame string) { fmt.Fprintln(conn, frame) })
}

func (d *fakeDevice) app(pkg string) *fakeApp {
	for _, app := range d.apps {
		if app.pkg == pkg {
			return app
		}
	}
	return nil
}

func readRequest(conn net.Conn) (string, error) {
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	length := make([]byte, 4)
	if _, err := io.ReadFull(conn, length); err != nil {
		return "", err
	}
	n, err := strconv.ParseUint(string(length), 16, 16)
	if err != nil {
		return "", err
	}
	text := make([]byte, n)
	if _, err := io.ReadFull(conn, text); err != nil {
		return "", err
	}
	return string(text), nil
}

func okay(conn net.Conn) {
	io.WriteString(conn, "OKAY")
}

func fail(conn net.Conn, reason string) {
	io.WriteString(conn, "FAIL")
	writeLengthPrefixed(conn, reason)
}

func writeLengthPrefixed(conn net.Conn, text string) {
	fmt.Fprintf(conn, "%04x%s", len(text), text)
}
