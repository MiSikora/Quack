package adb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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

func TestOpenContextCancelsTransportServiceAndFailureDiagnostics(t *testing.T) {
	for _, phase := range []string{"transport", "service", "diagnostic"} {
		t.Run(phase, func(t *testing.T) {
			entered := make(chan struct{})
			serve(t, func(conn net.Conn, request string) {
				if phase == "diagnostic" {
					if strings.HasPrefix(request, "host:transport:") {
						fail(conn, "device disappeared")
						return
					}
				} else if phase == "service" {
					io.WriteString(conn, "OKAY")
					if _, err := readLengthPrefixed(conn); err != nil {
						return
					}
				}
				close(entered)
				io.Copy(io.Discard, conn)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				conn, err := Device("test").OpenContext(ctx, "shell:wait")
				if conn != nil {
					conn.Close()
				}
				done <- err
			}()
			<-entered
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("got %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("OpenContext did not end with its context")
			}
		})
	}
}

func TestOpenContextEndsWhenAdbLauncherLeavesOutputPipesOpen(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	t.Setenv("ANDROID_ADB_SERVER_PORT", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	dir := t.TempDir()
	// The launcher exits, but its child retains stdout and stderr. Signal after
	// spawning the child so cancellation cannot race ahead of process startup.
	script := "#!/bin/sh\n/bin/sleep 2 &\n: > \"$0.ready\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "adb"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := Device("test").OpenContext(ctx, "shell:wait")
		if conn != nil {
			conn.Close()
		}
		done <- err
	}()
	readyTimeout := time.NewTimer(time.Second)
	defer readyTimeout.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "adb.ready")); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("launcher ended before cancellation: %v", err)
		case <-readyTimeout.C:
			t.Fatal("launcher did not spawn its child")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("adb launcher output kept OpenContext blocked after cancellation")
	}
}

func TestOpenContextConnectsWhenSuccessfulAdbLauncherLeavesOutputPipesOpen(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	t.Setenv("ANDROID_ADB_SERVER_PORT", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	dir := t.TempDir()
	launcher := filepath.Join(dir, "adb")
	// Start listening only after the first dial has failed and launched adb.
	// The launcher waits for the server, then exits while its child holds the pipes.
	script := "#!/bin/sh\n/bin/sleep 2 &\n: > \"$0.ready\"\nwhile [ ! -f \"$0.listening\" ]; do /bin/sleep 0.01; done\nexit 0\n"
	if err := os.WriteFile(launcher, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := Device("test").OpenContext(ctx, "shell:ready")
		if conn != nil {
			conn.Close()
		}
		done <- err
	}()
	for {
		if _, err := os.Stat(launcher + ".ready"); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("OpenContext ended before the launcher started: %v", err)
		case <-ctx.Done():
			t.Fatal("adb launcher did not start")
		case <-time.After(time.Millisecond):
		}
	}
	listener, err = net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(time.Second))
		for _, want := range []string{"host:transport:test", "shell:ready"} {
			request, err := readLengthPrefixed(conn)
			if err != nil || request != want {
				return
			}
			io.WriteString(conn, "OKAY")
		}
	}()
	if err := os.WriteFile(launcher+".listening", nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("successful adb launcher did not connect: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("OpenContext did not connect to the launched server")
	}
}

func TestCommandCancelsSetupAndFailureDiagnostics(t *testing.T) {
	for _, phase := range []string{"transport", "service", "transport diagnostics", "service diagnostics", "read diagnostics"} {
		t.Run(phase, func(t *testing.T) {
			entered := make(chan net.Conn, 1)
			serve(t, func(conn net.Conn, request string) {
				if strings.HasSuffix(request, ":get-state") {
					entered <- conn
					io.Copy(io.Discard, conn)
					return
				}
				if phase == "transport diagnostics" {
					fail(conn, "device disappeared")
					return
				}
				if phase != "transport" {
					io.WriteString(conn, "OKAY")
					if _, err := readLengthPrefixed(conn); err != nil {
						return
					}
				}
				switch phase {
				case "service diagnostics":
					fail(conn, "shell refused")
					return
				case "read diagnostics":
					io.WriteString(conn, "OKAY")
					return
				}
				entered <- conn
				io.Copy(io.Discard, conn)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := Device("test").Command(ctx, "wait"); done <- err }()
			var conn net.Conn
			select {
			case conn = <-entered:
			case <-time.After(time.Second):
				t.Fatal("Command did not reach " + phase)
			}
			defer conn.Close()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("got %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				conn.Close()
				t.Fatal("Command did not cancel " + phase)
			}
		})
	}
}

func TestTrackCancelsNegotiation(t *testing.T) {
	entered := make(chan net.Conn, 1)
	serve(t, func(conn net.Conn, request string) {
		entered <- conn
		io.Copy(io.Discard, conn)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Track(ctx, func([]Listing) { t.Error("sent a change before negotiation") }) }()
	conn := <-entered
	defer conn.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("got %v, want nil on cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Track did not cancel negotiation")
	}
}

func TestCanceledCommandAndTrackDoNotStartAdb(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	t.Setenv("ANDROID_ADB_SERVER_PORT", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	dir := t.TempDir()
	launcher := filepath.Join(dir, "adb")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n: > \"$0.started\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Device("test").Command(ctx, "wait"); !errors.Is(err, context.Canceled) {
		t.Errorf("Command got %v, want context.Canceled", err)
	}
	if err := Track(ctx, func([]Listing) { t.Error("sent a change after cancellation") }); err != nil {
		t.Errorf("Track got %v, want nil", err)
	}
	if _, err := os.Stat(launcher + ".started"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("canceled operations started adb: %v", err)
	}
}

func TestTrackCancelsAdbStartup(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	t.Setenv("ANDROID_ADB_SERVER_PORT", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	dir := t.TempDir()
	launcher := filepath.Join(dir, "adb")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n: > \"$0.ready\"\nexec /bin/sleep 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Track(ctx, func([]Listing) { t.Error("sent a change before startup") }) }()
	ready := time.NewTimer(time.Second)
	defer ready.Stop()
	for {
		if _, err := os.Stat(launcher + ".ready"); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Track ended before startup: %v", err)
		case <-ready.C:
			t.Fatal("adb launcher did not start")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("got %v, want nil on cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Track did not cancel adb startup")
	}
}
