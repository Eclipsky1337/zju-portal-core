package service

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestHTTPServiceTracksEachRequestLifecycle(t *testing.T) {
	observer := &connectionObserverStub{}
	service := NewHTTPServiceWithObserver("127.0.0.1:0", testDialer(), observer)
	service.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if _, err := io.ReadAll(request.Body); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("down")),
			Request:    request,
		}, nil
	})

	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "http://example.com/upload", strings.NewReader("up"))
		request.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		service.handle(response, request)
		if response.Code != http.StatusOK || response.Body.String() != "down" {
			t.Fatalf("response = %d %q", response.Code, response.Body.String())
		}
	}

	activities := observer.snapshot()
	if len(activities) != 2 {
		t.Fatalf("activities = %d", len(activities))
	}
	for _, activity := range activities {
		if activity.metadata.Inbound != "http" || activity.metadata.Source != "127.0.0.1:12345" || activity.metadata.Destination != "example.com" {
			t.Fatalf("metadata = %#v", activity.metadata)
		}
		if activity.uploaded != 2 || activity.downloaded != 4 || !activity.closed {
			t.Fatalf("activity = %#v", activity)
		}
	}
}

func TestObservedConnectionCloseWriteFallsBackToClose(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	observer := &connectionObserverStub{}
	activity := observer.OpenConnection(core.ConnectionMetadata{Inbound: "socks5"}, left.Close)
	conn := observeConnection(left, activity)

	writer, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("observed connection does not expose CloseWrite")
	}
	if err := writer.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if !observer.snapshot()[0].closed {
		t.Fatal("connection activity remained open")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type connectionObserverStub struct {
	mu         sync.Mutex
	activities []*connectionActivityStub
}

func (observer *connectionObserverStub) OpenConnection(metadata core.ConnectionMetadata, closeFunc func() error) core.ConnectionActivity {
	activity := &connectionActivityStub{metadata: metadata, closeFunc: closeFunc}
	observer.mu.Lock()
	observer.activities = append(observer.activities, activity)
	observer.mu.Unlock()
	return activity
}

func (observer *connectionObserverStub) snapshot() []*connectionActivityStub {
	observer.mu.Lock()
	activities := append([]*connectionActivityStub(nil), observer.activities...)
	observer.mu.Unlock()
	result := make([]*connectionActivityStub, 0, len(activities))
	for _, activity := range activities {
		activity.mu.Lock()
		result = append(result, &connectionActivityStub{
			metadata:   activity.metadata,
			uploaded:   activity.uploaded,
			downloaded: activity.downloaded,
			closed:     activity.closed,
			closeErr:   activity.closeErr,
		})
		activity.mu.Unlock()
	}
	return result
}

type connectionActivityStub struct {
	mu         sync.Mutex
	metadata   core.ConnectionMetadata
	closeFunc  func() error
	uploaded   uint64
	downloaded uint64
	closed     bool
	closeErr   error
	closeOnce  sync.Once
}

func (activity *connectionActivityStub) RecordUploaded(count uint64) {
	activity.mu.Lock()
	activity.uploaded += count
	activity.mu.Unlock()
}

func (activity *connectionActivityStub) RecordDownloaded(count uint64) {
	activity.mu.Lock()
	activity.downloaded += count
	activity.mu.Unlock()
}

func (activity *connectionActivityStub) Close() error {
	activity.closeOnce.Do(func() {
		var err error
		if activity.closeFunc != nil {
			err = activity.closeFunc()
		}
		activity.mu.Lock()
		activity.closed = true
		activity.closeErr = err
		activity.mu.Unlock()
	})
	activity.mu.Lock()
	defer activity.mu.Unlock()
	return activity.closeErr
}

var _ core.ConnectionObserver = (*connectionObserverStub)(nil)
var _ core.ConnectionActivity = (*connectionActivityStub)(nil)
