// Package adb reaches Android devices through the adb server. It talks the server's smart-socket protocol directly,
// so no adb process starts and no forward is left behind.
package adb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Device is the target of adb services: a serial as adb devices lists it.
type Device string

// Listing is a device adb knows, and the state adb reports for it: Ready, or another, such as offline or
// unauthorized.
type Listing struct {
	Device Device
	State  string
}

// Error is a device that adb cannot reach, or that went away. Its message says what to do about it.
type Error struct {
	Message string
}

func (e *Error) Error() string { return e.Message }

// ServiceFailure is the device refusing a service, such as localabstract: for a socket nobody listens on.
type ServiceFailure struct {
	Reason string
}

func (e *ServiceFailure) Error() string { return e.Reason }

// Open connects to service on the device and returns the stream to it.
func (d Device) Open(service string) (net.Conn, error) {
	conn, err := dialServer()
	if err != nil {
		return nil, err
	}
	if err := request(conn, "host:transport:"+string(d)); err != nil {
		conn.Close()
		if stuck, ok := asStuck(err); ok {
			return nil, stuck
		}
		return nil, d.Unreachable(err)
	}
	if err := request(conn, service); err != nil {
		conn.Close()
		if stuck, ok := asStuck(err); ok {
			return nil, stuck
		}
		return nil, &ServiceFailure{err.Error()}
	}
	// The stream is the device's now, and a shell command or an app may take longer than the server may.
	conn.SetDeadline(time.Time{})
	return conn, nil
}

// ShellStream runs cmd on the device and returns its output, stdout and stderr together, as it is produced.
func (d Device) ShellStream(cmd string) (io.ReadCloser, error) {
	conn, err := d.Open("shell:" + cmd)
	var refused *ServiceFailure
	if errors.As(err, &refused) {
		// A device always runs a shell for adb, so a refusal means it went away.
		return nil, d.Unreachable(err)
	}
	return conn, err
}

// Shell runs cmd on the device and returns all of its output.
func (d Device) Shell(cmd string) (string, error) {
	conn, err := d.ShellStream(cmd)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	out, err := io.ReadAll(conn)
	if err != nil {
		return "", d.Unreachable(err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Output is what a command printed on the device, and the status it exited with.
type Output struct {
	Stdout string
	Stderr string
	Exit   int
}

// The packet ids of adb's shell protocol, which frames each packet as an id, a little-endian 32-bit length and data.
const (
	shellStdout = 1
	shellStderr = 2
	shellExit   = 3
)

// Command runs cmd on the device through adb's shell protocol, which keeps stdout apart from stderr and tells the
// status cmd exited with. Ending ctx closes the stream, which ends cmd, and Command returns ctx's error.
func (d Device) Command(ctx context.Context, cmd string) (Output, error) {
	conn, err := d.Open("shell,v2,raw:" + cmd)
	var refused *ServiceFailure
	if errors.As(err, &refused) {
		return Output{}, d.Unreachable(err)
	}
	if err != nil {
		return Output{}, err
	}
	defer conn.Close()
	defer context.AfterFunc(ctx, func() { conn.Close() })()
	var stdout, stderr strings.Builder
	header := make([]byte, 5)
	for {
		data, err := readPacket(conn, header)
		switch {
		case ctx.Err() != nil:
			return Output{}, ctx.Err()
		case err != nil:
			return Output{}, d.Unreachable(err)
		}
		switch header[0] {
		case shellStdout:
			stdout.Write(data)
		case shellStderr:
			stderr.Write(data)
		case shellExit:
			exit := 0
			if len(data) > 0 {
				exit = int(data[0])
			}
			return Output{stdout.String(), stderr.String(), exit}, nil
		}
	}
}

// readPacket reads one packet of adb's shell protocol into header and returns its data.
func readPacket(conn net.Conn, header []byte) ([]byte, error) {
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	data := make([]byte, binary.LittleEndian.Uint32(header[1:]))
	if _, err := io.ReadFull(conn, data); err != nil {
		return nil, err
	}
	return data, nil
}

// Unreachable explains a request the device failed, from the state adb reports for it.
func (d Device) Unreachable(cause error) error {
	state, err := d.State()
	var stuck *Error
	if errors.As(err, &stuck) {
		return stuck
	}
	switch {
	case err != nil:
		return &Error{fmt.Sprintf("%s is not connected", d)}
	case state == "offline":
		return &Error{fmt.Sprintf("%s is offline. Wait for adb to see it again, or reconnect it.", d)}
	case state == "unauthorized":
		return &Error{fmt.Sprintf("%s does not trust this computer yet. Allow USB debugging on the device.", d)}
	default:
		return &Error{fmt.Sprintf("lost the connection to %s (adb: %v)", d, cause)}
	}
}

// State returns what adb reports for the device, such as device, offline or unauthorized. It fails when adb does
// not know the device, and with an Error when the server does not answer.
func (d Device) State() (string, error) {
	return query("host-serial:" + string(d) + ":get-state")
}

// Devices lists the devices adb knows.
func Devices() ([]Listing, error) {
	text, err := query("host:devices")
	var stuck *Error
	switch {
	case errors.As(err, &stuck):
		return nil, stuck
	case err != nil:
		return nil, &Error{fmt.Sprintf("list the devices: %v", err)}
	}
	return parseListings(text), nil
}

// Track sends the devices adb knows to send, first as they are and then each time one connects, disconnects or
// changes state. It returns when ctx ends, or with an Error when the adb server goes away.
func Track(ctx context.Context, send func([]Listing)) error {
	conn, err := dialServer()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := request(conn, "host:track-devices"); err != nil {
		if stuck, ok := asStuck(err); ok {
			return stuck
		}
		return &Error{fmt.Sprintf("track the devices: %v", err)}
	}
	// The server answers only when a device changes, which may not happen for hours.
	conn.SetDeadline(time.Time{})
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	for {
		text, err := readLengthPrefixed(conn)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return &Error{fmt.Sprintf("lost track of the devices: %v", err)}
		}
		send(parseListings(text))
	}
}

// query sends a request to the adb server itself, not to a device, and returns its answer.
func query(text string) (string, error) {
	conn, err := dialServer()
	if err != nil {
		return "", err
	}
	defer conn.Close()
	err = request(conn, text)
	answer := ""
	if err == nil {
		answer, err = readLengthPrefixed(conn)
	}
	if stuck, ok := asStuck(err); ok {
		return "", stuck
	}
	return answer, err
}

// parseListings reads the lines of serials and states that host:devices and host:track-devices answer.
func parseListings(text string) []Listing {
	var listings []Listing
	for _, line := range strings.Split(text, "\n") {
		if serial, state, ok := strings.Cut(line, "\t"); ok {
			listings = append(listings, Listing{Device(serial), state})
		}
	}
	return listings
}

// dialServer connects to the adb server, starting it once if it is not running. Reads and writes on the connection
// fail after serverTimeout, so that a server that stopped answering cannot hang quack.
func dialServer() (net.Conn, error) {
	conn, err := dialOrStartServer()
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(serverTimeout))
	return conn, nil
}

func dialOrStartServer() (net.Conn, error) {
	port := os.Getenv("ANDROID_ADB_SERVER_PORT")
	if port == "" {
		port = "5037"
	}
	conn, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err == nil {
		return conn, nil
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return nil, &Error{"the adb server is not running and adb is not on PATH. Install it with brew install --cask android-platform-tools, or add the Android SDK's platform-tools to PATH."}
	}
	if out, err := exec.Command("adb", "start-server").CombinedOutput(); err != nil {
		return nil, &Error{fmt.Sprintf("the adb server is not running and adb start-server failed: %s", strings.TrimSpace(string(out)))}
	}
	conn, err = net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		return nil, &Error{fmt.Sprintf("connect to the adb server: %v", err)}
	}
	return conn, nil
}

// request sends one request, a 4-hex-digit length and the text, and reads OKAY or FAIL with its reason.
func request(conn net.Conn, text string) error {
	if _, err := fmt.Fprintf(conn, "%04x%s", len(text), text); err != nil {
		return err
	}
	status := make([]byte, 4)
	if _, err := io.ReadFull(conn, status); err != nil {
		return err
	}
	if string(status) == "OKAY" {
		return nil
	}
	reason, err := readLengthPrefixed(conn)
	if err != nil {
		return fmt.Errorf("%s", status)
	}
	return fmt.Errorf("%s", reason)
}

// asStuck returns the error that explains a server that missed serverTimeout, when err comes from one.
func asStuck(err error) (*Error, bool) {
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		return nil, false
	}
	return &Error{fmt.Sprintf("the adb server did not answer within %v. Run adb kill-server, and quack starts it again.", serverTimeout)}, true
}

// readLengthPrefixed reads a 4-hex-digit length and that much text, the way the adb server answers.
func readLengthPrefixed(conn net.Conn) (string, error) {
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

// Ready is the state adb reports for a device that it can reach.
const Ready = "device"

// serverTimeout bounds how long the adb server may take to answer one request. It answers in milliseconds, but a
// service such as shell: waits for the device to open it, which takes longer over wireless debugging.
const serverTimeout = 10 * time.Second
