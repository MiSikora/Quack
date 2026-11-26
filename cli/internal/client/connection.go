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
	"quack/internal/device"
	"quack/internal/wire"
)

type connection struct {
	conn   net.Conn
	reader *bufio.Reader
	hello  wire.Hello
}

var errNotReady = errors.New("app not ready")

func (a App) connectRetrying(ctx context.Context) (*connection, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c, err := connect(ctx, a.device, a.socketName)
		if !errors.Is(err, errNotReady) {
			return c, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func connect(ctx context.Context, serial adb.Device, socketName string) (*connection, error) {
	conn, err := serial.OpenContext(ctx, "localabstract:"+socketName)
	var refused *adb.ServiceFailure
	if errors.As(err, &refused) {
		// The device answers "closed" when nothing listens on the socket.
		return nil, errNotReady
	}
	if err != nil {
		return nil, err
	}
	defer context.AfterFunc(ctx, func() { conn.Close() })()
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
		return nil, &device.AppError{Message: fmt.Sprintf("expected hello, got %s", first)}
	}
	if hello.Protocol != wire.ProtocolVersion {
		conn.Close()
		return nil, &device.AppError{Message: fmt.Sprintf("app speaks protocol %d, this quack speaks %d. Upgrade quack.", hello.Protocol, wire.ProtocolVersion)}
	}
	conn.SetReadDeadline(time.Time{})
	return &connection{conn, reader, hello}, nil
}

func (c *connection) exchange(request []byte, notify NotificationFunc) (wire.Frame, error) {
	// Never half-close the connection: adb drops the app's reply when the host side shuts down writes.
	if _, err := c.conn.Write(append(request, '\n')); err != nil {
		return nil, &device.AppError{Message: fmt.Sprintf("send: %v", err)}
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
