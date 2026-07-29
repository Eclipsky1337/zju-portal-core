package service

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

func TestSocks5ClientCloseReleasesObservedConnection(t *testing.T) {
	targetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetListener.Close()
	targetDone := make(chan struct{})
	go func() {
		defer close(targetDone)
		conn, acceptErr := targetListener.Accept()
		if acceptErr != nil {
			return
		}
		_, _ = io.Copy(io.Discard, conn)
		_ = conn.Close()
	}()

	observer := &connectionObserverStub{}
	dialer := contextDialerFunc(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, targetListener.Addr().String())
	})
	service := NewSocks5ServiceWithObserver("127.0.0.1:0", dialer, nil, "", "", observer)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())

	proxyDialer, err := proxy.SOCKS5("tcp", service.Addr().String(), nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := proxyDialer.Dial("tcp", "10.0.0.1:443")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		activities := observer.snapshot()
		if len(activities) == 1 && activities[0].closed {
			select {
			case <-targetDone:
				return
			case <-time.After(time.Second):
				t.Fatal("target connection did not observe client EOF")
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("observed activities = %#v", observer.snapshot())
}

type contextDialerFunc func(context.Context, string, string) (net.Conn, error)

func (function contextDialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return function(ctx, network, address)
}
