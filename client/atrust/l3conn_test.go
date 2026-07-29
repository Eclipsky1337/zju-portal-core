package atrust

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestL3TunnelEvictsActiveTransportFailureWithoutTerminating(t *testing.T) {
	tunnel := newL3TunnelForLifecycleTest()
	transport := newL3TransportForLifecycleTest()
	tunnel.conns["group-main"] = transport

	wantErr := errors.New("transport failed")
	transport.fail(wantErr)
	tunnel.forwardFromConn("group-main", transport)
	if err := connectionContextError(tunnel.ctx); err != nil {
		t.Fatalf("active transport terminated tunnel: %v", err)
	}
	tunnel.connsMu.Lock()
	remaining := tunnel.conns["group-main"]
	tunnel.connsMu.Unlock()
	if remaining != nil {
		t.Fatal("failed active transport was not evicted")
	}
}

func TestL3TunnelIgnoresRetiredTransportFailure(t *testing.T) {
	tunnel := newL3TunnelForLifecycleTest()
	transport := newL3TransportForLifecycleTest()
	tunnel.conns["group-main"] = transport
	if !tunnel.evictConn("group-main", transport) {
		t.Fatal("active transport was not evicted")
	}

	transport.fail(errors.New("retired transport closed"))
	tunnel.forwardFromConn("group-main", transport)
	if err := connectionContextError(tunnel.ctx); err != nil {
		t.Fatalf("retired transport terminated tunnel: %v", err)
	}
}

func TestL3ConnCloseUnblocksRead(t *testing.T) {
	tunnel := newL3TunnelForLifecycleTest()
	logical, err := tunnel.NewL3Conn()
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := logical.Read(make([]byte, 1))
		result <- err
	}()
	if err := logical.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Read() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() did not unblock Read()")
	}
}

func newL3TunnelForLifecycleTest() *L3Tunnel {
	return newL3TunnelWithParentForTest(context.Background())
}

func newL3TunnelWithParentForTest(parent context.Context) *L3Tunnel {
	ctx, cancel := context.WithCancelCause(parent)
	return &L3Tunnel{ctx: ctx, cancel: cancel, conns: make(map[string]*l3TunnelConn), dataChan: make(chan []byte, 1)}
}

func newL3TransportForLifecycleTest() *l3TunnelConn {
	return &l3TunnelConn{closeCh: make(chan struct{}), incoming: make(chan []byte)}
}
