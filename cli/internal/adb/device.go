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

type Device string

type Listing struct {
	Device Device
	State  string
}

type Error struct {
	Message string
}

func (e *Error) Error() string { return e.Message }

type ServiceFailure struct {
	Reason string
}

func (e *ServiceFailure) Error() string { return e.Reason }

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

// Output comes as it is produced, stdout and stderr together.
func (d Device) ShellStream(cmd string) (io.ReadCloser, error) {
	conn, err := d.Open("shell:" + cmd)
	var refused *ServiceFailure
	if errors.As(err, &refused) {
		// A device always runs a shell for adb, so a refusal means it went away.
		return nil, d.Unreachable(err)
	}
	return conn, err
}

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

type Output struct {
	Stdout string
	Stderr string
	Exit   int
}

// adb's shell protocol frames each packet as an id, a little-endian 32-bit length and data.
const (
	shellStdout = 1
	shellStderr = 2
	shellExit   = 3
)

// adb's shell protocol keeps stdout apart from stderr and reports the exit status. Ending ctx closes the stream,
// which ends cmd, and Command returns ctx's error.
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

// Fails when adb does not know the device, and with an Error when the server does not answer.
func (d Device) State() (string, error) {
	return query("host-serial:" + string(d) + ":get-state")
}

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

// send gets the devices as they are, then again on each change. Returns when ctx ends, or with an Error when the
// adb server goes away.
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

// Asks the adb server itself, not a device.
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

func parseListings(text string) []Listing {
	var listings []Listing
	for _, line := range strings.Split(text, "\n") {
		if serial, state, ok := strings.Cut(line, "\t"); ok {
			listings = append(listings, Listing{Device(serial), state})
		}
	}
	return listings
}

// Starts the server once if it is not running. Reads and writes fail after serverTimeout, so that a server that
// stopped answering cannot hang quack.
func dialServer() (net.Conn, error) {
	port := os.Getenv("ANDROID_ADB_SERVER_PORT")
	if port == "" {
		port = "5037"
	}
	conn, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		if _, err := exec.LookPath("adb"); err != nil {
			return nil, &Error{"the adb server is not running and adb is not on PATH. Install it with brew install --cask android-platform-tools, or add the Android SDK's platform-tools to PATH."}
		}
		if out, err := exec.Command("adb", "start-server").CombinedOutput(); err != nil {
			return nil, &Error{fmt.Sprintf("the adb server is not running and adb start-server failed: %s", strings.TrimSpace(string(out)))}
		}
		if conn, err = net.Dial("tcp", "127.0.0.1:"+port); err != nil {
			return nil, &Error{fmt.Sprintf("connect to the adb server: %v", err)}
		}
	}
	conn.SetDeadline(time.Now().Add(serverTimeout))
	return conn, nil
}

// A 4-hex-digit length, then the text. The server answers OKAY, or FAIL with its reason.
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

func asStuck(err error) (*Error, bool) {
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		return nil, false
	}
	return &Error{fmt.Sprintf("the adb server did not answer within %v. Run adb kill-server, and quack starts it again.", serverTimeout)}, true
}

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

const Ready = "device"

// The server answers in milliseconds, but shell: waits for the device to open it, which takes longer over wireless
// debugging.
const serverTimeout = 10 * time.Second
