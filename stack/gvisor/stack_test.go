package gvisor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func TestStackCloseIsIdempotent(t *testing.T) {
	conn := &closeStub{}
	stack := &Stack{
		endpoint:  &Endpoint{l3Conn: conn},
		closeDone: make(chan struct{}),
	}

	if err := stack.Close(context.Background()); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := stack.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if got := conn.closeCount.Load(); got != 1 {
		t.Fatalf("connection close count = %d, want 1", got)
	}
}

func TestStackCloseHonorsContextTimeout(t *testing.T) {
	release := make(chan struct{})
	conn := &closeStub{release: release}
	stack := &Stack{
		endpoint:  &Endpoint{l3Conn: conn},
		closeDone: make(chan struct{}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := stack.Close(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v", err)
	}

	close(release)
	if err := stack.Close(context.Background()); err != nil {
		t.Fatalf("Close() after release error = %v", err)
	}
	if got := conn.closeCount.Load(); got != 1 {
		t.Fatalf("connection close count = %d, want 1", got)
	}
}

func TestEndpointRejectsConnectionAfterClose(t *testing.T) {
	endpoint := &Endpoint{}
	if err := endpoint.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	conn := &closeStub{}
	if err := endpoint.setConnection(conn); !errors.Is(err, context.Canceled) {
		t.Fatalf("setConnection() error = %v", err)
	}
	if got := conn.closeCount.Load(); got != 1 {
		t.Fatalf("rejected connection close count = %d, want 1", got)
	}
}

func TestEndpointFailureClosesConnectionAndPreservesError(t *testing.T) {
	wantErr := errors.New("server disconnected")
	conn := &closeStub{}
	endpoint := &Endpoint{l3Conn: conn}

	endpoint.fail(wantErr)
	endpoint.fail(errors.New("later error"))

	if !errors.Is(endpoint.terminalError(), wantErr) {
		t.Fatalf("terminal error = %v", endpoint.terminalError())
	}
	if got := conn.closeCount.Load(); got != 1 {
		t.Fatalf("connection close count = %d, want 1", got)
	}
}

func TestEndpointWriteFailureDoesNotTerminateEndpoint(t *testing.T) {
	wantErr := errors.New("network temporarily unavailable")
	conn := &writeStub{err: wantErr}
	endpoint := &Endpoint{l3Conn: conn}
	packet := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData([]byte{1, 2, 3})})
	defer packet.DecRef()
	var packets stack.PacketBufferList
	packets.PushBack(packet)

	written, tcpipErr := endpoint.WritePackets(packets)
	if written != 0 || tcpipErr == nil {
		t.Fatalf("first WritePackets() = %d, %v", written, tcpipErr)
	}
	if err := endpoint.terminalError(); err != nil {
		t.Fatalf("terminal error = %v, want nil", err)
	}
	if got := conn.closeCount.Load(); got != 0 {
		t.Fatalf("connection close count = %d, want 0", got)
	}

	conn.err = nil
	written, tcpipErr = endpoint.WritePackets(packets)
	if written != 1 || tcpipErr != nil {
		t.Fatalf("second WritePackets() = %d, %v", written, tcpipErr)
	}
}

type closeStub struct {
	release    <-chan struct{}
	closeCount atomic.Int32
}

func (*closeStub) Read([]byte) (int, error)       { return 0, nil }
func (*closeStub) Write(data []byte) (int, error) { return len(data), nil }
func (conn *closeStub) Close() error {
	conn.closeCount.Add(1)
	if conn.release != nil {
		<-conn.release
	}
	return nil
}

type writeStub struct {
	err        error
	closeCount atomic.Int32
}

func (*writeStub) Read([]byte) (int, error) { return 0, nil }
func (conn *writeStub) Write(data []byte) (int, error) {
	if conn.err != nil {
		return 0, conn.err
	}
	return len(data), nil
}
func (conn *writeStub) Close() error {
	conn.closeCount.Add(1)
	return nil
}
