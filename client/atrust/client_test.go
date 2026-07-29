package atrust

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"
)

func TestClientLifecycleInheritsParentContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := NewClientContext(ctx, "user", "sid", "device", "sign-key")
	localConn, remoteConn := net.Pipe()
	defer remoteConn.Close()
	tunnelConn := &l3TunnelConn{
		tlsConn: tls.Client(localConn, &tls.Config{InsecureSkipVerify: true}),
		closeCh: make(chan struct{}),
	}
	client.l3Tunnel = newL3TunnelWithParentForTest(client.lifecycleCtx)
	client.l3Tunnel.conns["group-main"] = tunnelConn
	cancel()

	select {
	case <-tunnelConn.closeCh:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not close the client tunnel")
	}
}

func TestClientCloseCancelsLifecycleAndClosesTunnel(t *testing.T) {
	client := NewClient("user", "sid", "device", "sign-key")
	localConn, remoteConn := net.Pipe()
	defer remoteConn.Close()

	tunnelConn := &l3TunnelConn{
		tlsConn: tls.Client(localConn, &tls.Config{InsecureSkipVerify: true}),
		closeCh: make(chan struct{}),
	}
	client.l3Tunnel = newL3TunnelWithParentForTest(client.lifecycleCtx)
	client.l3Tunnel.conns["group-main"] = tunnelConn

	client.Close()
	client.Close()

	select {
	case <-client.lifecycleCtx.Done():
	default:
		t.Fatal("Client.Close() did not cancel the lifecycle context")
	}
	select {
	case <-tunnelConn.closeCh:
	default:
		t.Fatal("Client.Close() did not close the L3 tunnel connection")
	}
	if len(client.l3Tunnel.conns) != 0 {
		t.Fatalf("L3 tunnel still owns %d connections after close", len(client.l3Tunnel.conns))
	}
}

func TestClientNewL3ConnBeforeSetup(t *testing.T) {
	client := NewClient("", "", "", "")
	defer client.Close()

	_, err := client.NewL3Conn()
	if err == nil || !strings.Contains(err.Error(), "L3 tunnel not initialized") {
		t.Fatalf("NewL3Conn() error = %v", err)
	}
}
