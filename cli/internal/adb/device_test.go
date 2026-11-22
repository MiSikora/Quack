package adb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestDevicesListsEachDeviceWithItsState(t *testing.T) {
	serve(t, func(conn net.Conn, request string) {
		if request != "host:devices" {
			fail(conn, "unknown request "+request)
			return
		}
		okay(conn, "emulator-5554\tdevice\n3A251FDH2000XY\tunauthorized\n")
	})

	listings, err := Devices()

	want := []Listing{{"emulator-5554", Ready}, {"3A251FDH2000XY", "unauthorized"}}
	if err != nil || !reflect.DeepEqual(listings, want) {
		t.Errorf("got %v, %v, want %v", listings, err, want)
	}
}

func TestDevicesSaysWhatTheServerRefused(t *testing.T) {
	serve(t, func(conn net.Conn, request string) { fail(conn, "protocol fault") })

	_, err := Devices()

	var adbErr *Error
	if !errors.As(err, &adbErr) || err.Error() != "list the devices: protocol fault" {
		t.Errorf("got %v, want an Error that says list the devices: protocol fault", err)
	}
}

func TestDevicesSaysHowToInstallAdbWhenNoServerRunsAndAdbIsMissing(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	t.Setenv("ANDROID_ADB_SERVER_PORT", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	t.Setenv("PATH", t.TempDir())

	_, err = Devices()

	want := "the adb server is not running and adb is not on PATH. Install it with brew install --cask android-platform-tools, or add the Android SDK's platform-tools to PATH."
	var adbErr *Error
	if !errors.As(err, &adbErr) || err.Error() != want {
		t.Errorf("got %v, want an Error that says %s", err, want)
	}
}

func TestCommandKeepsStdoutAndStderrApartAndTellsTheExitStatus(t *testing.T) {
	serve(t, func(conn net.Conn, request string) {
		if request != "host:transport:emulator-5554" {
			fail(conn, "unknown request "+request)
			return
		}
		io.WriteString(conn, "OKAY")
		if service, _ := readLengthPrefixed(conn); service != "shell,v2,raw:pm clear app" {
			fail(conn, "unknown service "+service)
			return
		}
		io.WriteString(conn, "OKAY")
		packet(conn, shellStdout, "Suc")
		packet(conn, shellStderr, "warning\n")
		packet(conn, shellStdout, "cess\n")
		packet(conn, shellExit, "\x03")
	})

	out, err := Device("emulator-5554").Command(context.Background(), "pm clear app")

	if want := (Output{"Success\n", "warning\n", 3}); err != nil || out != want {
		t.Errorf("got %v, %v, want %v", out, err, want)
	}
}

func TestCommandEndsWhenTheContextEnds(t *testing.T) {
	serve(t, func(conn net.Conn, request string) {
		io.WriteString(conn, "OKAY")
		readLengthPrefixed(conn)
		io.WriteString(conn, "OKAY")
		io.Copy(io.Discard, conn)
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	_, err := Device("emulator-5554").Command(ctx, "sleep 60")

	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

func TestTrackSendsEachChangeUntilTheServerGoesAway(t *testing.T) {
	serve(t, func(conn net.Conn, request string) {
		if request != "host:track-devices" {
			fail(conn, "unknown request "+request)
			return
		}
		okay(conn, "emulator-5554\tdevice\n")
		writeLengthPrefixed(conn, "")
		writeLengthPrefixed(conn, "emulator-5554\toffline\n")
	})
	var sent [][]Listing

	err := Track(context.Background(), func(listings []Listing) { sent = append(sent, listings) })

	want := [][]Listing{{{"emulator-5554", Ready}}, nil, {{"emulator-5554", "offline"}}}
	if !reflect.DeepEqual(sent, want) {
		t.Errorf("sent %v, want %v", sent, want)
	}
	var adbErr *Error
	if !errors.As(err, &adbErr) || err.Error() != "lost track of the devices: EOF" {
		t.Errorf("got %v, want an Error that says lost track of the devices: EOF", err)
	}
}

func TestTrackWaitsForChangesUntilTheContextEnds(t *testing.T) {
	answered := make(chan struct{})
	serve(t, func(conn net.Conn, request string) {
		okay(conn, "")
		close(answered)
		io.Copy(io.Discard, conn)
	})
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error)

	go func() { ended <- Track(ctx, func([]Listing) {}) }()
	<-answered
	select {
	case err := <-ended:
		t.Fatalf("Track ended with %v while the server was quiet", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()

	select {
	case err := <-ended:
		if err != nil {
			t.Errorf("got %v, want nil once the context ended", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Track did not end with the context")
	}
}

func serve(t *testing.T, answer func(conn net.Conn, request string)) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	t.Setenv("ANDROID_ADB_SERVER_PORT", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				request, err := readLengthPrefixed(conn)
				if err == nil {
					answer(conn, request)
				}
			}()
		}
	}()
}

func okay(conn net.Conn, text string) {
	io.WriteString(conn, "OKAY")
	writeLengthPrefixed(conn, text)
}

func fail(conn net.Conn, reason string) {
	io.WriteString(conn, "FAIL")
	writeLengthPrefixed(conn, reason)
}

func packet(conn net.Conn, id byte, data string) {
	header := []byte{id, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(header[1:], uint32(len(data)))
	conn.Write(append(header, data...))
}

func writeLengthPrefixed(conn net.Conn, text string) {
	fmt.Fprintf(conn, "%04x%s", len(text), text)
}
