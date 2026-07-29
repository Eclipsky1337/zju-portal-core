package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestHTTPServiceStartAndCloseIdempotently(t *testing.T) {
	listener := newListenerStub()
	service := NewHTTPService("127.0.0.1:0", testDialer())
	service.listen = listenStub(listener, nil)
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if service.Addr() == nil {
		t.Fatal("Addr() = nil after start")
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	listener.assertClosed(t)
}

func TestHTTPServiceCloseInterruptsActiveRequests(t *testing.T) {
	service := NewHTTPService("127.0.0.1:0", testDialer())
	requestStarted := make(chan struct{})
	service.server.Handler = http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
	})
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	requestDone := make(chan struct{})
	go func() {
		_, _ = http.Get("http://" + service.Addr().String())
		close(requestDone)
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("HTTP request did not reach proxy")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("active HTTP request was not interrupted")
	}
}

func TestSocks5ServiceStartAndCloseIdempotently(t *testing.T) {
	listener := newListenerStub()
	service := NewSocks5Service("127.0.0.1:0", testDialer(), nil, "", "")
	service.listen = listenStub(listener, nil)
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if service.Addr() == nil {
		t.Fatal("Addr() = nil after start")
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	listener.assertClosed(t)
}

func TestManagedServicesCloseWhenParentContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	httpListener := newListenerStub()
	socksListener := newListenerStub()
	httpService := NewHTTPService("127.0.0.1:0", testDialer())
	httpService.listen = listenStub(httpListener, nil)
	socksService := NewSocks5Service("127.0.0.1:0", testDialer(), nil, "", "")
	socksService.listen = listenStub(socksListener, nil)
	if err := httpService.Start(ctx); err != nil {
		t.Fatalf("HTTP Start() error = %v", err)
	}
	if err := socksService.Start(ctx); err != nil {
		t.Fatalf("SOCKS Start() error = %v", err)
	}
	cancel()

	httpListener.assertClosed(t)
	socksListener.assertClosed(t)
	if err := httpService.Close(context.Background()); err != nil {
		t.Fatalf("HTTP Close() error = %v", err)
	}
	if err := socksService.Close(context.Background()); err != nil {
		t.Fatalf("SOCKS Close() error = %v", err)
	}
}

func TestManagedServicesReportBindFailure(t *testing.T) {
	wantErr := errors.New("address unavailable")
	httpService := NewHTTPService("127.0.0.1:1", testDialer())
	httpService.listen = listenStub(nil, wantErr)
	socksService := NewSocks5Service("127.0.0.1:1", testDialer(), nil, "", "")
	socksService.listen = listenStub(nil, wantErr)

	for name, service := range map[string]interface {
		Start(context.Context) error
	}{"http": httpService, "socks": socksService} {
		if err := service.Start(context.Background()); !errors.Is(err, wantErr) {
			t.Fatalf("%s Start() error = %v", name, err)
		}
	}
}

func TestManagedDNSServiceServesUDPAndTCP(t *testing.T) {
	server := NewManagedDNSService("127.0.0.1:0", resolverStub{address: net.ParseIP("10.0.0.8")})
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer server.Close(context.Background())

	for _, network := range []string{"udp", "tcp"} {
		client := &dns.Client{Net: network}
		request := new(dns.Msg)
		request.SetQuestion("app.example.edu.", dns.TypeA)
		response, _, err := client.Exchange(request, server.Addr().String())
		if err != nil {
			t.Fatalf("%s Exchange() error = %v", network, err)
		}
		if len(response.Answer) != 1 {
			t.Fatalf("%s answer = %v", network, response.Answer)
		}
		answer, ok := response.Answer[0].(*dns.A)
		if !ok || !answer.A.Equal(net.ParseIP("10.0.0.8")) {
			t.Fatalf("%s answer = %v", network, response.Answer)
		}
	}
}

func TestManagedDNSServiceReportsRunError(t *testing.T) {
	service := NewManagedDNSService("127.0.0.1:0", resolverStub{})
	wantErr := errors.New("DNS serve failed")
	service.recordRunError(wantErr)
	select {
	case <-service.Done():
	case <-time.After(time.Second):
		t.Fatal("DNS run error did not close Done()")
	}
	if !errors.Is(service.Err(), wantErr) {
		t.Fatalf("Err() = %v", service.Err())
	}
}

type resolverStub struct {
	address net.IP
}

func (resolver resolverStub) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, resolver.address, nil
}

func listenStub(listener net.Listener, err error) func(string, string) (net.Listener, error) {
	return func(string, string) (net.Listener, error) {
		return listener, err
	}
}

type listenerStub struct {
	closed chan struct{}
	once   sync.Once
}

func newListenerStub() *listenerStub {
	return &listenerStub{closed: make(chan struct{})}
}

func (listener *listenerStub) Accept() (net.Conn, error) {
	<-listener.closed
	return nil, net.ErrClosed
}

func (listener *listenerStub) Close() error {
	listener.once.Do(func() {
		close(listener.closed)
	})
	return nil
}

func (*listenerStub) Addr() net.Addr {
	return addressStub("127.0.0.1:12345")
}

func (listener *listenerStub) assertClosed(t *testing.T) {
	t.Helper()
	select {
	case <-listener.closed:
	case <-time.After(time.Second):
		t.Fatal("listener was not closed")
	}
}

type addressStub string

func (address addressStub) Network() string { return "tcp" }
func (address addressStub) String() string  { return string(address) }

type testContextDialer struct{}

func (testContextDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func testDialer() testContextDialer {
	return testContextDialer{}
}
