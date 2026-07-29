package dial

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestRouterDirectModeUsesProvidedDirectOutbound(t *testing.T) {
	direct := &routerDirectOutboundStub{}
	router, err := NewRouterWithDirect(rejectOutbound{}, rejectOutbound{}, direct, core.RoutingModeDirect)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := router.DialContext(context.Background(), "tcp", "192.0.2.1:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if direct.calls.Load() != 1 {
		t.Fatalf("direct calls = %d", direct.calls.Load())
	}
}

type routerDirectOutboundStub struct{ calls atomic.Int32 }

func (outbound *routerDirectOutboundStub) DialContext(context.Context, string, string) (net.Conn, error) {
	outbound.calls.Add(1)
	local, remote := net.Pipe()
	_ = remote.Close()
	return local, nil
}

func (*routerDirectOutboundStub) Close(context.Context) error { return nil }
