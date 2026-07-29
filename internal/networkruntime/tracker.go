package networkruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

const transportIdleAfter = 5 * time.Second

type trackedOutbound struct {
	delegate core.Outbound
	started  time.Time
	nextID   atomic.Uint64
	uploaded atomic.Uint64
	download atomic.Uint64
	total    atomic.Uint64

	mu          sync.RWMutex
	connections map[string]*trackedConn
}

type trackedConn struct {
	net.Conn
	tracker      *trackedOutbound
	id           string
	network      string
	destination  string
	openedAt     time.Time
	lastActivity atomic.Int64
	uploaded     atomic.Uint64
	downloaded   atomic.Uint64
	closeOnce    sync.Once
	closeErr     error
}

type connectionTracker struct {
	started time.Time
	nextID  atomic.Uint64
	total   atomic.Uint64

	mu          sync.RWMutex
	connections map[string]*logicalConnection
}

type logicalConnection struct {
	tracker      *connectionTracker
	id           string
	metadata     core.ConnectionMetadata
	openedAt     time.Time
	lastActivity atomic.Int64
	uploaded     atomic.Uint64
	downloaded   atomic.Uint64
	closeFunc    func() error
	closeOnce    sync.Once
	closeErr     error
}

type logicalTrackedConn struct {
	net.Conn
	activity  core.ConnectionActivity
	closeOnce sync.Once
	closeErr  error
}

var _ core.ConnectionObserver = (*connectionTracker)(nil)

func newTrackedOutbound(delegate core.Outbound) *trackedOutbound {
	return &trackedOutbound{
		delegate:    delegate,
		started:     time.Now(),
		connections: make(map[string]*trackedConn),
	}
}

func newConnectionTracker() *connectionTracker {
	return &connectionTracker{
		started:     time.Now(),
		connections: make(map[string]*logicalConnection),
	}
}

func (outbound *trackedOutbound) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := outbound.delegate.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tracked := &trackedConn{
		Conn:        conn,
		tracker:     outbound,
		id:          fmt.Sprintf("transport-%d", outbound.nextID.Add(1)),
		network:     network,
		destination: address,
		openedAt:    now,
	}
	tracked.lastActivity.Store(now.UnixNano())
	outbound.mu.Lock()
	outbound.connections[tracked.id] = tracked
	outbound.mu.Unlock()
	outbound.total.Add(1)
	return tracked, nil
}

func (outbound *trackedOutbound) Close(ctx context.Context) error {
	outbound.mu.RLock()
	connections := make([]*trackedConn, 0, len(outbound.connections))
	for _, conn := range outbound.connections {
		connections = append(connections, conn)
	}
	outbound.mu.RUnlock()

	var closeErrors []error
	for _, conn := range connections {
		closeErrors = append(closeErrors, conn.Close())
	}
	closeErrors = append(closeErrors, outbound.delegate.Close(ctx))
	return errors.Join(closeErrors...)
}

func (outbound *trackedOutbound) TrafficStats() core.TrafficStats {
	outbound.mu.RLock()
	open := len(outbound.connections)
	outbound.mu.RUnlock()
	return core.TrafficStats{
		UploadedBytes:             outbound.uploaded.Load(),
		DownloadedBytes:           outbound.download.Load(),
		OpenTransportConnections:  open,
		TotalTransportConnections: outbound.total.Load(),
		StartedAt:                 outbound.started,
		Timestamp:                 time.Now(),
	}
}

func (outbound *trackedOutbound) TransportConnections() []core.TransportConnectionInfo {
	outbound.mu.RLock()
	connections := make([]core.TransportConnectionInfo, 0, len(outbound.connections))
	for _, conn := range outbound.connections {
		connections = append(connections, conn.info())
	}
	outbound.mu.RUnlock()
	sort.Slice(connections, func(left, right int) bool {
		return connections[left].OpenedAt.Before(connections[right].OpenedAt)
	})
	return connections
}

func (outbound *trackedOutbound) remove(id string) {
	outbound.mu.Lock()
	delete(outbound.connections, id)
	outbound.mu.Unlock()
}

func (conn *trackedConn) Read(data []byte) (int, error) {
	count, err := conn.Conn.Read(data)
	if count > 0 {
		conn.downloaded.Add(uint64(count))
		conn.tracker.download.Add(uint64(count))
		conn.touch()
	}
	if isTerminalConnectionError(err) {
		conn.finish()
	}
	return count, err
}

func (conn *trackedConn) Write(data []byte) (int, error) {
	count, err := conn.Conn.Write(data)
	if count > 0 {
		conn.uploaded.Add(uint64(count))
		conn.tracker.uploaded.Add(uint64(count))
		conn.touch()
	}
	if isTerminalConnectionError(err) {
		conn.finish()
	}
	return count, err
}

func (conn *trackedConn) RouteInfo() core.RouteInfo {
	return core.RouteInfoOf(conn.Conn)
}

func (conn *trackedConn) Close() error {
	conn.closeOnce.Do(func() {
		conn.closeErr = conn.Conn.Close()
		conn.tracker.remove(conn.id)
	})
	return conn.closeErr
}

func (conn *trackedConn) CloseWrite() error {
	if writer, ok := conn.Conn.(interface{ CloseWrite() error }); ok {
		return writer.CloseWrite()
	}
	return conn.Close()
}

func (conn *trackedConn) finish() {
	conn.tracker.remove(conn.id)
}

func (conn *trackedConn) touch() {
	conn.lastActivity.Store(time.Now().UnixNano())
}

func (conn *trackedConn) TransportConnectionID() string {
	return conn.id
}

func (conn *trackedConn) info() core.TransportConnectionInfo {
	lastActivity := time.Unix(0, conn.lastActivity.Load())
	state := core.ConnectionStateActive
	if time.Since(lastActivity) >= transportIdleAfter {
		state = core.ConnectionStateIdle
	}
	return core.TransportConnectionInfo{
		ID:              conn.id,
		Outbound:        core.RouteInfoOf(conn.Conn).Outbound,
		RouteReason:     core.RouteInfoOf(conn.Conn).Reason,
		Network:         conn.network,
		Destination:     conn.destination,
		UploadedBytes:   conn.uploaded.Load(),
		DownloadedBytes: conn.downloaded.Load(),
		OpenedAt:        conn.openedAt,
		LastActivityAt:  lastActivity,
		State:           state,
	}
}

func (tracker *connectionTracker) OpenConnection(metadata core.ConnectionMetadata, closeFunc func() error) core.ConnectionActivity {
	now := time.Now()
	connection := &logicalConnection{
		tracker:   tracker,
		id:        fmt.Sprintf("connection-%d", tracker.nextID.Add(1)),
		metadata:  metadata,
		openedAt:  now,
		closeFunc: closeFunc,
	}
	connection.lastActivity.Store(now.UnixNano())
	tracker.mu.Lock()
	tracker.connections[connection.id] = connection
	tracker.mu.Unlock()
	tracker.total.Add(1)
	return connection
}

func (tracker *connectionTracker) TrafficStats() core.TrafficStats {
	tracker.mu.RLock()
	active := len(tracker.connections)
	tracker.mu.RUnlock()
	return core.TrafficStats{
		ActiveConnections: active,
		TotalConnections:  tracker.total.Load(),
		StartedAt:         tracker.started,
		Timestamp:         time.Now(),
	}
}

func (tracker *connectionTracker) Connections() []core.ConnectionInfo {
	tracker.mu.RLock()
	connections := make([]core.ConnectionInfo, 0, len(tracker.connections))
	for _, connection := range tracker.connections {
		connections = append(connections, connection.info())
	}
	tracker.mu.RUnlock()
	sort.Slice(connections, func(left, right int) bool {
		return connections[left].OpenedAt.Before(connections[right].OpenedAt)
	})
	return connections
}

func (tracker *connectionTracker) CloseConnection(id string) error {
	tracker.mu.RLock()
	connection := tracker.connections[id]
	tracker.mu.RUnlock()
	if connection == nil {
		return core.WrapError(core.ErrorCodeConnectionNotFound, fmt.Sprintf("connection %q not found", id), false, nil)
	}
	return connection.Close()
}

func (tracker *connectionTracker) Close() error {
	tracker.mu.RLock()
	connections := make([]*logicalConnection, 0, len(tracker.connections))
	for _, connection := range tracker.connections {
		connections = append(connections, connection)
	}
	tracker.mu.RUnlock()
	var closeErrors []error
	for _, connection := range connections {
		closeErrors = append(closeErrors, connection.Close())
	}
	return errors.Join(closeErrors...)
}

func (tracker *connectionTracker) remove(id string) {
	tracker.mu.Lock()
	delete(tracker.connections, id)
	tracker.mu.Unlock()
}

func (connection *logicalConnection) RecordUploaded(count uint64) {
	if count == 0 {
		return
	}
	connection.uploaded.Add(count)
	connection.touch()
}

func (connection *logicalConnection) RecordDownloaded(count uint64) {
	if count == 0 {
		return
	}
	connection.downloaded.Add(count)
	connection.touch()
}

func (connection *logicalConnection) Close() error {
	connection.closeOnce.Do(func() {
		if connection.closeFunc != nil {
			connection.closeErr = connection.closeFunc()
		}
		connection.tracker.remove(connection.id)
	})
	return connection.closeErr
}

func (connection *logicalConnection) touch() {
	connection.lastActivity.Store(time.Now().UnixNano())
}

func (connection *logicalConnection) info() core.ConnectionInfo {
	return core.ConnectionInfo{
		ID:                    connection.id,
		Inbound:               connection.metadata.Inbound,
		Outbound:              connection.metadata.Outbound,
		RouteReason:           connection.metadata.RouteReason,
		Source:                connection.metadata.Source,
		Network:               connection.metadata.Network,
		Destination:           connection.metadata.Destination,
		UploadedBytes:         connection.uploaded.Load(),
		DownloadedBytes:       connection.downloaded.Load(),
		OpenedAt:              connection.openedAt,
		LastActivityAt:        time.Unix(0, connection.lastActivity.Load()),
		State:                 core.ConnectionStateActive,
		TransportConnectionID: connection.metadata.TransportConnectionID,
	}
}

func transportConnectionID(conn net.Conn) string {
	identified, ok := conn.(interface{ TransportConnectionID() string })
	if !ok {
		return ""
	}
	return identified.TransportConnectionID()
}

func trackLogicalConn(conn net.Conn, activity core.ConnectionActivity) net.Conn {
	return &logicalTrackedConn{Conn: conn, activity: activity}
}

func (conn *logicalTrackedConn) Read(data []byte) (int, error) {
	count, err := conn.Conn.Read(data)
	conn.activity.RecordDownloaded(uint64(count))
	return count, err
}

func (conn *logicalTrackedConn) Write(data []byte) (int, error) {
	count, err := conn.Conn.Write(data)
	conn.activity.RecordUploaded(uint64(count))
	return count, err
}

func (conn *logicalTrackedConn) Close() error {
	conn.closeOnce.Do(func() {
		conn.closeErr = conn.activity.Close()
	})
	return conn.closeErr
}

func isTerminalConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var networkError net.Error
	return !errors.As(err, &networkError) || !networkError.Timeout()
}
