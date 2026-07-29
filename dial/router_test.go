package dial

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestRouterRuleModeUsesVPNForResource(t *testing.T) {
	vpn := &routerOutboundStub{conn: pipeConn(t)}
	internet := &routerOutboundStub{err: errors.New("internet must not be used")}
	router := newTestRouter(t, vpn, internet, core.RoutingModeRule)

	conn, err := router.DialContext(context.Background(), "tcp", "10.0.0.1:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if vpn.calls != 1 || internet.calls != 0 {
		t.Fatalf("calls: vpn=%d internet=%d", vpn.calls, internet.calls)
	}
	if route := core.RouteInfoOf(conn); route.Outbound != OutboundATrust || route.Reason != RouteReasonVPNResource {
		t.Fatalf("route = %#v", route)
	}
}

func TestRouterRuleModeUsesInternetOnlyWhenResourceDoesNotMatch(t *testing.T) {
	vpn := &routerOutboundStub{err: ErrNotInResources}
	internet := &routerOutboundStub{conn: pipeConn(t)}
	router := newTestRouter(t, vpn, internet, core.RoutingModeRule)

	conn, err := router.DialContext(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if vpn.calls != 1 || internet.calls != 1 {
		t.Fatalf("calls: vpn=%d internet=%d", vpn.calls, internet.calls)
	}
	if route := core.RouteInfoOf(conn); route.Outbound != OutboundInternet {
		t.Fatalf("route = %#v", route)
	}
}

func TestRouterRuleModeDoesNotLeakVPNFailuresToInternet(t *testing.T) {
	wantErr := errors.New("VPN unavailable")
	vpn := &routerOutboundStub{err: wantErr}
	internet := &routerOutboundStub{conn: pipeConn(t)}
	router := newTestRouter(t, vpn, internet, core.RoutingModeRule)

	_, err := router.DialContext(context.Background(), "tcp", "10.0.0.1:443")
	if !errors.Is(err, wantErr) || internet.calls != 0 {
		t.Fatalf("error = %v, internet calls = %d", err, internet.calls)
	}
}

func TestRouterGlobalModeUsesOnlyVPN(t *testing.T) {
	vpn := &routerOutboundStub{err: ErrNotInResources}
	internet := &routerOutboundStub{conn: pipeConn(t)}
	router := newTestRouter(t, vpn, internet, core.RoutingModeGlobal)

	_, err := router.DialContext(context.Background(), "tcp", "example.com:443")
	if !errors.Is(err, ErrNotInResources) || internet.calls != 0 {
		t.Fatalf("error = %v, internet calls = %d", err, internet.calls)
	}
}

func TestRouterDirectModeBypassesVPNAndConfiguredInternet(t *testing.T) {
	vpn := &routerOutboundStub{err: errors.New("VPN must not be used")}
	internet := &routerOutboundStub{err: errors.New("configured internet must not be used")}
	router := newTestRouter(t, vpn, internet, core.RoutingModeDirect)
	direct := &routerOutboundStub{conn: pipeConn(t)}
	router.direct = direct

	conn, err := router.DialContext(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if vpn.calls != 0 || internet.calls != 0 || direct.calls != 1 {
		t.Fatalf("calls: vpn=%d internet=%d direct=%d", vpn.calls, internet.calls, direct.calls)
	}
	if route := core.RouteInfoOf(conn); route.Outbound != OutboundDirect || route.Reason != RouteReasonDirectMode {
		t.Fatalf("route = %#v", route)
	}
}

func TestRouterModeCanBeChanged(t *testing.T) {
	router := newTestRouter(t, &routerOutboundStub{}, &routerOutboundStub{}, core.RoutingModeRule)
	previous, err := router.SetMode(core.RoutingModeGlobal)
	if err != nil || previous != core.RoutingModeRule || router.Mode() != core.RoutingModeGlobal {
		t.Fatalf("SetMode() = %q, %v; mode = %q", previous, err, router.Mode())
	}
	if _, err := router.SetMode("invalid"); err == nil || router.Mode() != core.RoutingModeGlobal {
		t.Fatalf("invalid SetMode() error = %v; mode = %q", err, router.Mode())
	}
}

func TestRoutedConnectionForwardsCloseWrite(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	underlying := &routerCloseWriteConn{Conn: local}
	conn := newRoutedConn(underlying, core.RouteInfo{Outbound: OutboundATrust})

	writer, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("routed connection does not expose CloseWrite")
	}
	if err := writer.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if !underlying.closeWriteCalled {
		t.Fatal("underlying CloseWrite was not called")
	}
}

func TestRejectInternetOutbound(t *testing.T) {
	outbound, err := NewInternetOutbound(core.InternetOutboundConfig{Type: core.InternetOutboundReject})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.DialContext(context.Background(), "tcp", "example.com:443"); !errors.Is(err, ErrRejected) {
		t.Fatalf("error = %v", err)
	}
}

func newTestRouter(t *testing.T, vpn, internet core.Outbound, mode core.RoutingMode) *Router {
	t.Helper()
	router, err := NewRouter(vpn, internet, mode)
	if err != nil {
		t.Fatal(err)
	}
	return router
}

type routerOutboundStub struct {
	conn  net.Conn
	err   error
	calls int
}

type routerCloseWriteConn struct {
	net.Conn
	closeWriteCalled bool
}

func (conn *routerCloseWriteConn) CloseWrite() error {
	conn.closeWriteCalled = true
	return nil
}

func (outbound *routerOutboundStub) DialContext(context.Context, string, string) (net.Conn, error) {
	outbound.calls++
	return outbound.conn, outbound.err
}

func (*routerOutboundStub) Close(context.Context) error { return nil }

func pipeConn(t *testing.T) net.Conn {
	t.Helper()
	local, remote := net.Pipe()
	t.Cleanup(func() { _ = remote.Close() })
	return local
}
