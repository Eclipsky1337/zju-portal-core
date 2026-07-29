package dial

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestInternetDirectOutboundUsesProvidedDialer(t *testing.T) {
	dialer := &contextDialerStub{}
	outbound, err := NewInternetOutboundWithDialer(core.InternetOutboundConfig{Type: core.InternetOutboundDirect}, dialer)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := outbound.DialContext(context.Background(), "tcp", "192.0.2.1:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if dialer.calls.Load() != 1 {
		t.Fatalf("dial calls = %d", dialer.calls.Load())
	}
}

type contextDialerStub struct{ calls atomic.Int32 }

func (dialer *contextDialerStub) DialContext(context.Context, string, string) (net.Conn, error) {
	dialer.calls.Add(1)
	local, remote := net.Pipe()
	_ = remote.Close()
	return local, nil
}
