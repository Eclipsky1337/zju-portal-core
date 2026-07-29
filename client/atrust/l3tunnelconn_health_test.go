package atrust

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"
)

func TestL3TunnelHeartbeatExpiration(t *testing.T) {
	now := time.Unix(1000, 0)
	connection := &l3TunnelConn{closeCh: make(chan struct{}), incoming: make(chan []byte)}
	connection.lastHeartbeat.Store(now.Add(-heartbeatTimeout).UnixNano())
	if !connection.heartbeatExpired(now) {
		t.Fatal("heartbeat was not considered expired")
	}
	connection.lastHeartbeat.Store(now.Add(-heartbeatInterval).UnixNano())
	if connection.heartbeatExpired(now) {
		t.Fatal("recent heartbeat was considered expired")
	}
}

func TestL3TunnelHeartbeatWatchdogIsIndependentFromWrites(t *testing.T) {
	now := time.Unix(1000, 0)
	connection := &l3TunnelConn{closeCh: make(chan struct{}), incoming: make(chan []byte)}
	connection.lastHeartbeat.Store(now.Add(-heartbeatTimeout).UnixNano())
	connection.writeMu.Lock()
	defer connection.writeMu.Unlock()

	ticks := make(chan time.Time, 1)
	done := make(chan struct{})
	go func() {
		connection.watchHeartbeats(ticks)
		close(done)
	}()
	ticks <- now

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat watchdog was blocked by an active writer")
	}
	if !errors.Is(connection.Err(), errL3HeartbeatTimeout) {
		t.Fatalf("Err() = %v", connection.Err())
	}
}

func TestL3TunnelFailureReachesPacketReader(t *testing.T) {
	wantErr := errors.New("tunnel failed")
	connection := &l3TunnelConn{closeCh: make(chan struct{}), incoming: make(chan []byte)}
	connection.fail(wantErr)

	if !errors.Is(connection.Err(), wantErr) {
		t.Fatalf("Err() = %v", connection.Err())
	}
	if _, err := connection.ReadPacket(); !errors.Is(err, wantErr) {
		t.Fatalf("ReadPacket() error = %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestL3TunnelConnectionClosePreservesError(t *testing.T) {
	wantErr := errors.New("transport close failed")
	connection := &l3TunnelConn{
		tlsConn:  tls.Client(closeErrorConn{err: wantErr}, &tls.Config{InsecureSkipVerify: true}),
		closeCh:  make(chan struct{}),
		incoming: make(chan []byte),
	}
	if err := connection.Close(); !errors.Is(err, wantErr) {
		t.Fatalf("Close() error = %v", err)
	}
	if err := connection.Close(); !errors.Is(err, wantErr) {
		t.Fatalf("second Close() error = %v", err)
	}
}

type closeErrorConn struct {
	err error
}

func (closeErrorConn) Read([]byte) (int, error)               { return 0, errors.New("not implemented") }
func (closeErrorConn) Write(data []byte) (int, error)         { return len(data), nil }
func (connection closeErrorConn) Close() error                { return connection.err }
func (closeErrorConn) LocalAddr() net.Addr                    { return nil }
func (closeErrorConn) RemoteAddr() net.Addr                   { return nil }
func (closeErrorConn) SetDeadline(time.Time) error            { return nil }
func (closeErrorConn) SetReadDeadline(time.Time) error        { return nil }
func (closeErrorConn) SetWriteDeadline(time.Time) error       { return nil }
func (closeErrorConn) HandshakeContext(context.Context) error { return nil }
