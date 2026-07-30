package tcptunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

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

func TestStackReportsClientHealthFailure(t *testing.T) {
	wantErr := client.ErrSessionInvalid
	healthDone := make(chan struct{})
	stack, err := NewStack(tcptunnelHealthClientStub{
		tcptunnelClientStub: tcptunnelClientStub{},
		done:                healthDone,
		err:                 wantErr,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- stack.RunContext(context.Background())
	}()
	close(healthDone)

	select {
	case err := <-result:
		if !errors.Is(err, wantErr) {
			t.Fatalf("RunContext() error = %v, want %v", err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("RunContext() did not report the client health failure")
	}
}

type tcptunnelClientStub struct{}

type tcptunnelHealthClientStub struct {
	tcptunnelClientStub
	done <-chan struct{}
	err  error
}

func (client tcptunnelHealthClientStub) Done() <-chan struct{} { return client.done }
func (client tcptunnelHealthClientStub) Err() error            { return client.err }

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
