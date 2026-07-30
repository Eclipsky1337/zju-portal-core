package tcptunnel

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestStackClassifiesUnsupportedDialOperations(t *testing.T) {
	stack, err := NewStack(tcptunnelClientStub{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.DialTCP(context.Background(), &net.TCPAddr{}); core.ErrorCodeOf(err) != core.ErrorCodeOutboundUnavailable {
		t.Fatalf("DialTCP() error = %v", err)
	}
	if _, err := stack.DialUDP(context.Background(), &net.UDPAddr{}); core.ErrorCodeOf(err) != core.ErrorCodeOutboundUnavailable {
		t.Fatalf("DialUDP() error = %v", err)
	}
}

type tcptunnelClientStub struct{}

func (tcptunnelClientStub) IP() (net.IP, error)                       { return nil, nil }
func (tcptunnelClientStub) IPResources() ([]client.IPResource, error) { return nil, nil }
func (tcptunnelClientStub) DomainResources() (map[string]client.DomainResource, error) {
	return nil, nil
}
func (tcptunnelClientStub) DNSResource() (map[string]net.IP, error) { return nil, nil }
func (tcptunnelClientStub) DNSServer() (string, error)              { return "", nil }
func (tcptunnelClientStub) CanUseTCPTunnel() bool                   { return false }
func (tcptunnelClientStub) DialTCP(context.Context, *net.TCPAddr) (net.Conn, error) {
	return nil, nil
}
func (tcptunnelClientStub) NewL3Conn() (io.ReadWriteCloser, error) { return nil, nil }
