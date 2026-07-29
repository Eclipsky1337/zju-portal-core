package ping

import (
	"context"
	"testing"
	"time"
)

func TestTCPingStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tcping := NewTCPing()
	tcping.SetContext(ctx)
	tcping.SetTarget(&Target{
		Protocol: TCP,
		Host:     "127.0.0.1",
		Port:     1,
		Counter:  1,
		Interval: time.Hour,
		Timeout:  time.Hour,
	})
	done := tcping.Start()
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("TCPing did not stop after context cancellation")
	}
}

func TestTCPingStopIsIdempotent(t *testing.T) {
	tcping := NewTCPing()
	tcping.SetTarget(&Target{
		Protocol: TCP,
		Host:     "127.0.0.1",
		Port:     1,
		Counter:  1,
		Interval: time.Hour,
		Timeout:  time.Hour,
	})
	done := tcping.Start()
	tcping.Stop()
	tcping.Stop()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("TCPing did not stop")
	}
}
