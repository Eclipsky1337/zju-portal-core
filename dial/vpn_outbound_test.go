package dial

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/client"
)

func TestVPNOutboundRejectsDestinationOutsideResources(t *testing.T) {
	stack := &vpnStackStub{}
	outbound := NewVPNOutbound(stack, nil, []client.IPResource{{
		IPMin:    net.ParseIP("10.0.0.0"),
		IPMax:    net.ParseIP("10.255.255.255"),
		PortMin:  1,
		PortMax:  65535,
		Protocol: "all",
	}})

	_, err := outbound.DialContext(context.Background(), "tcp", "203.0.113.10:443")
	if !errors.Is(err, ErrNotInResources) {
		t.Fatalf("DialContext() error = %v", err)
	}
	if stack.calls != 0 {
		t.Fatalf("VPN stack calls = %d, want 0", stack.calls)
	}
}

func TestVPNOutboundDialsMatchingResource(t *testing.T) {
	stack := &vpnStackStub{conn: &connStub{}}
	outbound := NewVPNOutbound(stack, nil, []client.IPResource{{
		IPMin:    net.ParseIP("10.0.0.0"),
		IPMax:    net.ParseIP("10.255.255.255"),
		PortMin:  443,
		PortMax:  443,
		Protocol: "tcp",
	}})

	conn, err := outbound.DialContext(context.Background(), "tcp", "10.1.2.3:443")
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	if conn != stack.conn || stack.calls != 1 {
		t.Fatalf("DialContext() conn/calls = %#v/%d", conn, stack.calls)
	}
}

func TestVPNOutboundNeverFallsBackForUnsupportedNetwork(t *testing.T) {
	stack := &vpnStackStub{}
	outbound := NewVPNOutbound(stack, nil, []client.IPResource{{
		IPMin:    net.ParseIP("10.0.0.0"),
		IPMax:    net.ParseIP("10.255.255.255"),
		PortMin:  1,
		PortMax:  65535,
		Protocol: "all",
	}})

	_, err := outbound.DialContext(context.Background(), "icmp", "10.1.2.3:7")
	if !errors.Is(err, ErrUnsupportedNetwork) {
		t.Fatalf("DialContext() error = %v", err)
	}
	if stack.calls != 0 {
		t.Fatalf("VPN stack calls = %d, want 0", stack.calls)
	}
}

func TestVPNOutboundNeverFallsBackWhenResolutionFails(t *testing.T) {
	wantErr := errors.New("DNS unavailable")
	stack := &vpnStackStub{}
	outbound := NewVPNOutbound(stack, vpnResolverStub{err: wantErr}, nil)

	_, err := outbound.DialContext(context.Background(), "tcp", "vpn.example.edu:443")
	if !errors.Is(err, wantErr) {
		t.Fatalf("DialContext() error = %v", err)
	}
	if stack.calls != 0 {
		t.Fatalf("VPN stack calls = %d, want 0", stack.calls)
	}
}

type vpnStackStub struct {
	conn  net.Conn
	calls int
}

type vpnResolverStub struct {
	err error
}

func (resolver vpnResolverStub) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, nil, resolver.err
}

func (stack *vpnStackStub) DialTCP(context.Context, *net.TCPAddr) (net.Conn, error) {
	stack.calls++
	return stack.conn, nil
}

func (stack *vpnStackStub) DialUDP(context.Context, *net.UDPAddr) (net.Conn, error) {
	stack.calls++
	return stack.conn, nil
}

type connStub struct{}

func (*connStub) Read([]byte) (int, error)         { return 0, nil }
func (*connStub) Write(data []byte) (int, error)   { return len(data), nil }
func (*connStub) Close() error                     { return nil }
func (*connStub) LocalAddr() net.Addr              { return nil }
func (*connStub) RemoteAddr() net.Addr             { return nil }
func (*connStub) SetDeadline(time.Time) error      { return nil }
func (*connStub) SetReadDeadline(time.Time) error  { return nil }
func (*connStub) SetWriteDeadline(time.Time) error { return nil }
