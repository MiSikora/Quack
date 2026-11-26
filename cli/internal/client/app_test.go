package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"quack/internal/adb"
	"quack/internal/device"
	"quack/internal/wire"
)

func TestCallCancelsStalledStartup(t *testing.T) {
	for _, output := range []string{"", "stopped\n"} {
		t.Run(fmt.Sprintf("output=%q", output), func(t *testing.T) {
			entered := make(chan struct{})
			app := startupServer(t, func(conn net.Conn, service string) {
				io.WriteString(conn, "OKAY"+output)
				close(entered)
				io.Copy(io.Discard, conn)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := app.Call(ctx, wire.Call{Action: "test"}, func(wire.Notification) {}); done <- err }()
			<-entered
			cancel()
			if err := awaitCall(t, done); !errors.Is(err, ErrCanceled) {
				t.Fatalf("got %v, want canceled", err)
			}
		})
	}
}

func TestCallTimesOutStalledStartup(t *testing.T) {
	app := startupServer(t, func(conn net.Conn, service string) {
		io.WriteString(conn, "OKAY")
		io.Copy(io.Discard, conn)
	})
	app.startTimeout = 30 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := app.Call(context.Background(), wire.Call{Action: "test"}, func(wire.Notification) {})
		done <- err
	}()
	err := awaitCall(t, done)
	var appErr *device.AppError
	if !errors.As(err, &appErr) || !strings.Contains(err.Error(), "did not answer within 30ms") {
		t.Fatalf("got %v, want startup timeout", err)
	}
}

func startupServer(t *testing.T, answer func(net.Conn, string)) App {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANDROID_ADB_SERVER_PORT", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				if _, err := readRequest(conn); err != nil {
					return
				}
				io.WriteString(conn, "OKAY")
				service, err := readRequest(conn)
				if err == nil {
					answer(conn, service)
				}
			}()
		}
	}()
	return App{device: adb.Device("test"), pkg: "com.test.app", mode: Wake, socketName: "test.quack", startTimeout: time.Second, status: io.Discard}
}

func readRequest(conn net.Conn) (string, error) {
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return "", err
	}
	n, err := strconv.ParseInt(string(header[:]), 16, 32)
	if err != nil {
		return "", err
	}
	data := make([]byte, n)
	_, err = io.ReadFull(conn, data)
	return string(data), err
}

func awaitCall(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("startup did not end within one second")
		return nil
	}
}

func TestExplicitStartCancelsAndTimesOut(t *testing.T) {
	for _, mode := range []StartMode{Wake, OpenApp} {
		for _, canceled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/canceled=%v", mode, canceled), func(t *testing.T) {
				entered := make(chan struct{})
				app := startupServer(t, func(conn net.Conn, service string) {
					io.WriteString(conn, "OKAYstopped\n")
					close(entered)
					io.Copy(io.Discard, conn)
				})
				app.startTimeout = 100 * time.Millisecond
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- app.Start(ctx, mode) }()
				<-entered
				if canceled {
					cancel()
				}
				err := awaitCall(t, done)
				if canceled {
					if !errors.Is(err, ErrCanceled) {
						t.Fatalf("got %v, want canceled", err)
					}
				} else {
					var appErr *device.AppError
					if !errors.As(err, &appErr) {
						t.Fatalf("got %v, want startup timeout", err)
					}
				}
			})
		}
	}
}

func TestCanceledCallDoesNotStartTheApp(t *testing.T) {
	app := App{startTimeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := app.Call(ctx, wire.Call{Action: "test"}, func(wire.Notification) {})
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("got %v, want canceled", err)
	}
}

func TestCallCancelsStalledConnectionSetup(t *testing.T) {
	for _, handshake := range []bool{true, false} {
		t.Run(fmt.Sprintf("handshake=%v", handshake), func(t *testing.T) {
			entered := make(chan struct{})
			app := startupServer(t, func(conn net.Conn, service string) {
				if strings.HasPrefix(service, "shell:") {
					io.WriteString(conn, "OKAY")
					return
				}
				if !handshake {
					io.WriteString(conn, "OKAY")
				}
				close(entered)
				io.Copy(io.Discard, conn)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := app.Call(ctx, wire.Call{Action: "test"}, func(wire.Notification) {}); done <- err }()
			<-entered
			cancel()
			if err := awaitCall(t, done); !errors.Is(err, ErrCanceled) {
				t.Fatalf("got %v, want canceled", err)
			}
		})
	}
}

func TestCallReportsStartupReadFailure(t *testing.T) {
	app := startupServer(t, func(conn net.Conn, service string) {
		io.WriteString(conn, "OKAYstopped\n")
		conn.(*net.TCPConn).SetLinger(0)
	})
	_, err := app.Call(context.Background(), wire.Call{Action: "test"}, func(wire.Notification) {})
	var netErr net.Error
	if !errors.As(err, &netErr) {
		t.Fatalf("got %v, want startup read failure", err)
	}
}

func TestStartupAndConnectionShareOneTimeout(t *testing.T) {
	refused := make(chan struct{}, 1)
	app := startupServer(t, func(conn net.Conn, service string) {
		if strings.HasPrefix(service, "shell:") {
			io.WriteString(conn, "OKAY")
			time.Sleep(300 * time.Millisecond)
			return
		}
		io.WriteString(conn, "FAIL0006closed")
		select {
		case refused <- struct{}{}:
		default:
		}
	})
	app.startTimeout = 500 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := app.Call(context.Background(), wire.Call{Action: "test"}, func(wire.Notification) {})
		done <- err
	}()
	select {
	case <-refused:
	case <-time.After(time.Second):
		t.Fatal("startup never reached the app socket")
	}
	// Startup used 300ms of the 500ms budget. Giving connection its own 500ms
	// would exceed this guard after the first refused socket.
	select {
	case err := <-done:
		var appErr *device.AppError
		if !errors.As(err, &appErr) || !strings.Contains(err.Error(), "did not answer within 500ms") {
			t.Fatalf("got %v, want startup timeout", err)
		}
	case <-time.After(350 * time.Millisecond):
		t.Fatal("connection received a fresh timeout after startup")
	}
}
