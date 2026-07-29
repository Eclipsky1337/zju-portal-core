package dial

import (
	"context"
	"errors"
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

func TestInternetOutboundClassifiesInvalidConfiguration(t *testing.T) {
	for _, config := range []core.InternetOutboundConfig{
		{Type: core.InternetOutboundSOCKS5},
		{Type: "invalid"},
	} {
		if _, err := NewInternetOutbound(config); core.ErrorCodeOf(err) != core.ErrorCodeConfigInvalid {
			t.Fatalf("NewInternetOutbound(%#v) error = %v", config, err)
		}
	}
}

func TestSOCKS5OutboundClassifiesUnsupportedUDP(t *testing.T) {
	outbound := &socks5Outbound{}
	_, err := outbound.DialContext(context.Background(), "udp", "192.0.2.1:53")
	if core.ErrorCodeOf(err) != core.ErrorCodeOutboundUnavailable || !errors.Is(err, ErrSOCKS5UDPUnsupported) {
		t.Fatalf("DialContext() error = %v", err)
	}
}

type contextDialerStub struct{ calls atomic.Int32 }

func (dialer *contextDialerStub) DialContext(context.Context, string, string) (net.Conn, error) {
	dialer.calls.Add(1)
	local, remote := net.Pipe()
	_ = remote.Close()
	return local, nil
}
