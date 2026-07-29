package service

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

type observedConn struct {
	net.Conn
	activity core.ConnectionActivity
	once     sync.Once
	err      error
}

func observeConnection(conn net.Conn, activity core.ConnectionActivity) net.Conn {
	if activity == nil {
		return conn
	}
	return &observedConn{Conn: conn, activity: activity}
}

func (conn *observedConn) Read(data []byte) (int, error) {
	count, err := conn.Conn.Read(data)
	conn.activity.RecordDownloaded(uint64(count))
	return count, err
}

func (conn *observedConn) Write(data []byte) (int, error) {
	count, err := conn.Conn.Write(data)
	conn.activity.RecordUploaded(uint64(count))
	return count, err
}

func (conn *observedConn) Close() error {
	conn.once.Do(func() {
		conn.err = conn.activity.Close()
	})
	return conn.err
}

func (conn *observedConn) CloseWrite() error {
	if writer, ok := conn.Conn.(interface{ CloseWrite() error }); ok {
		return writer.CloseWrite()
	}
	return conn.Close()
}

func openConnection(observer core.ConnectionObserver, metadata core.ConnectionMetadata, closeFunc func() error) core.ConnectionActivity {
	if observer == nil {
		return nil
	}
	return observer.OpenConnection(metadata, closeFunc)
}

func connectionID(conn net.Conn) string {
	identified, ok := conn.(interface{ TransportConnectionID() string })
	if !ok {
		return ""
	}
	return identified.TransportConnectionID()
}

func relay(left, right net.Conn) error {
	var leftErr, rightErr error
	var waitGroup sync.WaitGroup
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		_, rightErr = io.Copy(right, left)
		_ = right.SetReadDeadline(time.Now().Add(5 * time.Second))
	}()
	_, leftErr = io.Copy(left, right)
	_ = left.SetReadDeadline(time.Now().Add(5 * time.Second))
	waitGroup.Wait()
	if rightErr != nil && !errors.Is(rightErr, os.ErrDeadlineExceeded) {
		return rightErr
	}
	if leftErr != nil && !errors.Is(leftErr, os.ErrDeadlineExceeded) {
		return leftErr
	}
	return nil
}

type countingReadCloser struct {
	reader netReader
	closer netCloser
	record func(uint64)
}

type netReader interface {
	Read([]byte) (int, error)
}

type netCloser interface {
	Close() error
}

func (reader *countingReadCloser) Read(data []byte) (int, error) {
	count, err := reader.reader.Read(data)
	reader.record(uint64(count))
	return count, err
}

func (reader *countingReadCloser) Close() error {
	return reader.closer.Close()
}
