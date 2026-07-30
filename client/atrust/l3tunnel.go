package atrust

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/client"
)

type L3Tunnel struct {
	client *Client

	ctx    context.Context
	cancel context.CancelCauseFunc

	ip net.IP

	ipResources []client.IPResource

	conns   map[string]*l3TunnelConn
	connsMu sync.Mutex

	vipMu   sync.Mutex
	vipList []net.IP

	dataChan  chan []byte
	closeOnce sync.Once
}

func NewL3Tunnel(aTrustClient *Client) (*L3Tunnel, error) {
	ctx, cancel := context.WithCancelCause(aTrustClient.lifecycleCtx)
	t := &L3Tunnel{
		client:   aTrustClient,
		ctx:      ctx,
		cancel:   cancel,
		conns:    make(map[string]*l3TunnelConn),
		dataChan: make(chan []byte, 4096),
	}

	ipResources, err := aTrustClient.IPResources()
	if err != nil && !errors.Is(err, client.ErrResourceNotFound) {
		return nil, fmt.Errorf("failed to get IP resources: %w", err)
	}
	if errors.Is(err, client.ErrResourceNotFound) || ipResources == nil {
		ipResources = []client.IPResource{}
	}
	t.ipResources = ipResources

	ip, err := aTrustClient.IP()
	if err != nil {
		return nil, fmt.Errorf("failed to get client IP: %v", err)
	}
	t.ip = ip

	return t, nil
}

func (t *L3Tunnel) updateVIP(ips []net.IP) {
	t.vipMu.Lock()
	defer t.vipMu.Unlock()
	t.vipList = ips
}

func (t *L3Tunnel) Close() {
	t.terminate(io.EOF)
}

func (t *L3Tunnel) terminate(err error) {
	if err == nil {
		err = io.EOF
	}
	t.closeOnce.Do(func() {
		t.connsMu.Lock()
		t.cancel(err)
		conns := make([]*l3TunnelConn, 0, len(t.conns))
		for _, conn := range t.conns {
			conns = append(conns, conn)
		}
		t.conns = make(map[string]*l3TunnelConn)
		t.connsMu.Unlock()

		for _, conn := range conns {
			_ = conn.Close()
		}
	})
}

func (t *L3Tunnel) getConn(nodeGroupID string) (*l3TunnelConn, error) {
	if err := connectionContextError(t.ctx); err != nil {
		return nil, err
	}
	t.connsMu.Lock()
	if conn := t.conns[nodeGroupID]; conn != nil {
		t.connsMu.Unlock()
		return conn, nil
	}
	t.connsMu.Unlock()

	t.client.BestNodesRWMutex.RLock()
	addr := t.client.BestNodes[nodeGroupID]
	if addr == "" {
		addr = t.client.BestNodes[t.client.MajorNodeGroup]
	}
	t.client.BestNodesRWMutex.RUnlock()
	if addr == "" {
		return nil, fmt.Errorf("no available node for group %s", nodeGroupID)
	}

	info := clientInfo{
		sid:          t.client.SID,
		deviceID:     t.client.DeviceID,
		connectionID: t.client.ConnectionID,
		username:     t.client.Username,
	}
	ctx, cancel := context.WithTimeout(t.ctx, 10*time.Second)
	defer cancel()
	conn, err := newL3TunnelConn(ctx, t.client.underlayDialer.DialTLSContext, addr, info, t.client.SignKey, t.updateVIP, t.client.reportHealthError)
	if err != nil {
		return nil, err
	}
	if err := connectionContextError(t.ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}

	t.connsMu.Lock()
	if err := connectionContextError(t.ctx); err != nil {
		t.connsMu.Unlock()
		_ = conn.Close()
		return nil, err
	}
	if existing := t.conns[nodeGroupID]; existing != nil {
		t.connsMu.Unlock()
		_ = conn.Close()
		return existing, nil
	}
	t.conns[nodeGroupID] = conn
	t.connsMu.Unlock()

	go t.forwardFromConn(nodeGroupID, conn)

	return conn, nil
}

func (t *L3Tunnel) evictConn(nodeGroupID string, conn *l3TunnelConn) bool {
	t.connsMu.Lock()
	defer t.connsMu.Unlock()
	if existing := t.conns[nodeGroupID]; existing == conn {
		delete(t.conns, nodeGroupID)
		return true
	}
	return false
}

func (t *L3Tunnel) forwardFromConn(nodeGroupID string, conn *l3TunnelConn) {
	for {
		pkt, err := conn.ReadPacket()
		if err != nil {
			t.evictConn(nodeGroupID, conn)
			return
		}
		logPacket("recv", pkt)
		select {
		case t.dataChan <- pkt:
		case <-t.ctx.Done():
			return
		}
	}
}
