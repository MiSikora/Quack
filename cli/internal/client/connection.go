package client

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"quack/internal/adb"
	"quack/internal/wire"
)

// Nothing has been sent on a connection yet, so one that fails before exchange can be retried without running
// anything twice.
type connection struct {
	conn   net.Conn
	reader *bufio.Reader
	hello  wire.Hello
}

var errNotReady = errors.New("app not ready")

func (a App) connectRetrying(ctx context.Context) (*connection, error) {
	deadline := time.Now().Add(a.startTimeout)
	for {
		c, err := connect(a.device, a.socketName)
		if !errors.Is(err, errNotReady) {
			return c, err
		}
		if ctx.Err() != nil {
			return nil, &CallError{"canceled"}
		}
		if time.Now().After(deadline) {
			// adbd refuses a socket of a device that went away the same way as one nobody listens on.
			if state, err := a.device.State(); err != nil || state != "device" {
				return nil, a.device.Unreachable(errors.New("no answer"))
			}
			return nil, &AppError{fmt.Sprintf("%s did not answer within %v. Is it a quack-enabled app?", a.pkg, a.startTimeout)}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func connect(device adb.Device, socketName string) (*connection, error) {
	conn, err := device.Open("localabstract:" + socketName)
	var refused *adb.ServiceFailure
	if errors.As(err, &refused) {
		// The device answers "closed" when nothing listens on the socket.
		return nil, errNotReady
	}
	if err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	first, err := reader.ReadBytes('\n')
	if err != nil {
		// The app closes the connection without hello when the caller lacks DUMP.
		conn.Close()
		return nil, errNotReady
	}
	hello, ok := wire.DecodeFrame(bytes.TrimSuffix(first, []byte("\n"))).(wire.Hello)
	if !ok {
		conn.Close()
		return nil, &AppError{fmt.Sprintf("expected hello, got %s", first)}
	}
	if hello.Protocol != wire.ProtocolVersion {
		conn.Close()
		return nil, &AppError{fmt.Sprintf("app speaks protocol %d, this quack speaks %d. Upgrade quack.", hello.Protocol, wire.ProtocolVersion)}
	}
	conn.SetReadDeadline(time.Time{})
	return &connection{conn, reader, hello}, nil
}

func (c *connection) exchange(request []byte, notify NotificationFunc) (wire.Frame, error) {
	// Never half-close the connection: adb drops the app's reply when the host side shuts down writes.
	if _, err := c.conn.Write(append(request, '\n')); err != nil {
		return nil, &AppError{fmt.Sprintf("send: %v", err)}
	}
	var last wire.Frame
	for {
		line, err := c.reader.ReadBytes('\n')
		if len(line) > 0 {
			last = wire.DecodeFrame(bytes.TrimSuffix(line, []byte("\n")))
			if notification, ok := last.(wire.Notification); ok {
				notify(notification)
			}
		}
		if err != nil {
			break
		}
	}
	if last == nil {
		return nil, &CallError{"the app closed the connection without a reply"}
	}
	return last, nil
}

func (c *connection) close() {
	c.conn.Close()
}
