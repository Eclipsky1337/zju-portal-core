package networkruntime

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestTrackedOutboundCountsTrafficAndTransportConnections(t *testing.T) {
	delegate := &trackingDelegate{}
	outbound := newTrackedOutbound(delegate)
	conn, err := outbound.DialContext(context.Background(), "tcp", "10.0.0.8:443")
	if err != nil {
		t.Fatal(err)
	}
	peer := delegate.peer

	writeDone := make(chan error, 1)
	go func() {
		_, err := peer.Write([]byte("down"))
		writeDone <- err
	}()
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}

	readDone := make(chan error, 1)
	go func() {
		data := make([]byte, 2)
		_, err := io.ReadFull(peer, data)
		readDone <- err
	}()
	if _, err := conn.Write([]byte("up")); err != nil {
		t.Fatal(err)
	}
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}

	stats := outbound.TrafficStats()
	if stats.UploadedBytes != 2 || stats.DownloadedBytes != 4 || stats.OpenTransportConnections != 1 || stats.TotalTransportConnections != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	connections := outbound.TransportConnections()
	if len(connections) != 1 || connections[0].Destination != "10.0.0.8:443" || connections[0].UploadedBytes != 2 || connections[0].DownloadedBytes != 4 {
		t.Fatalf("connections = %#v", connections)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if got := outbound.TrafficStats().OpenTransportConnections; got != 0 {
		t.Fatalf("open transport connections = %d", got)
	}
}

func TestTrackedOutboundRemovesTransportConnectionOnEOF(t *testing.T) {
	delegate := &trackingDelegate{}
	outbound := newTrackedOutbound(delegate)
	conn, err := outbound.DialContext(context.Background(), "tcp", "10.0.0.8:443")
	if err != nil {
		t.Fatal(err)
	}
	if err := delegate.peer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("read unexpectedly succeeded")
	}
	if got := outbound.TransportConnections(); len(got) != 0 {
		t.Fatalf("transport connections = %#v", got)
	}
}

func TestTrackedConnectionForwardsCloseWrite(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	underlying := &trackerCloseWriteConn{Conn: local}
	outbound := newTrackedOutbound(&fixedConnectionDelegate{conn: underlying})
	conn, err := outbound.DialContext(context.Background(), "tcp", "10.0.0.8:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	writer, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("tracked connection does not expose CloseWrite")
	}
	if err := writer.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if !underlying.closeWriteCalled {
		t.Fatal("underlying CloseWrite was not called")
	}
}

func TestConnectionTrackerTracksLogicalLifecycle(t *testing.T) {
	tracker := newConnectionTracker()
	closed := false
	activity := tracker.OpenConnection(coreMetadata("http", "127.0.0.1:1234", "example.com:443", "transport-1"), func() error {
		closed = true
		return nil
	})
	activity.RecordUploaded(3)
	activity.RecordDownloaded(5)

	stats := tracker.TrafficStats()
	if stats.ActiveConnections != 1 || stats.TotalConnections != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	connections := tracker.Connections()
	if len(connections) != 1 || connections[0].Inbound != "http" || connections[0].Source != "127.0.0.1:1234" || connections[0].TransportConnectionID != "transport-1" || connections[0].UploadedBytes != 3 || connections[0].DownloadedBytes != 5 {
		t.Fatalf("connections = %#v", connections)
	}
	if err := tracker.CloseConnection(connections[0].ID); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("connection close function was not called")
	}
	if got := tracker.Connections(); len(got) != 0 {
		t.Fatalf("connections = %#v", got)
	}
}

func coreMetadata(inbound, source, destination, transportID string) core.ConnectionMetadata {
	return core.ConnectionMetadata{
		Inbound:               inbound,
		Source:                source,
		Network:               "tcp",
		Destination:           destination,
		TransportConnectionID: transportID,
	}
}

type trackingDelegate struct {
	peer net.Conn
}

type fixedConnectionDelegate struct{ conn net.Conn }

func (delegate *fixedConnectionDelegate) DialContext(context.Context, string, string) (net.Conn, error) {
	return delegate.conn, nil
}

func (*fixedConnectionDelegate) Close(context.Context) error { return nil }

type trackerCloseWriteConn struct {
	net.Conn
	closeWriteCalled bool
}

func (conn *trackerCloseWriteConn) CloseWrite() error {
	conn.closeWriteCalled = true
	return nil
}

func (delegate *trackingDelegate) DialContext(context.Context, string, string) (net.Conn, error) {
	local, peer := net.Pipe()
	delegate.peer = peer
	return local, nil
}

func (delegate *trackingDelegate) Close(context.Context) error {
	if delegate.peer != nil {
		return delegate.peer.Close()
	}
	return nil
}
