package atrust

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultConntrackLimit     = 4096
	defaultConntrackIdleAfter = 30 * time.Minute
)

type conntrack struct {
	key          string
	authID       uint64
	connectToken string
	appID        string
	nodeGroupID  string
	authCh       chan struct{}
	authErr      error
	authStarted  uint32
	lastUsed     atomic.Int64
}

type conntrackMgr struct {
	mu         sync.Mutex
	nextAuthID uint64
	byKey      map[string]*conntrack
	byID       map[uint64]*conntrack
	limit      int
	idleAfter  time.Duration
}

func newConntrackMgr() *conntrackMgr {
	return &conntrackMgr{
		byKey:     make(map[string]*conntrack),
		byID:      make(map[uint64]*conntrack),
		limit:     defaultConntrackLimit,
		idleAfter: defaultConntrackIdleAfter,
	}
}

func (m *conntrackMgr) getOrCreate(key, appID, nodeGroupID string) *conntrack {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if ct := m.byKey[key]; ct != nil {
		ct.lastUsed.Store(now.UnixNano())
		return ct
	}
	if len(m.byKey) >= m.limit {
		m.cleanup(now)
	}
	authID := atomic.AddUint64(&m.nextAuthID, 1)
	ct := &conntrack{
		key:         key,
		authID:      authID,
		appID:       appID,
		nodeGroupID: nodeGroupID,
		authCh:      make(chan struct{}),
	}
	ct.lastUsed.Store(now.UnixNano())
	m.byKey[key] = ct
	m.byID[authID] = ct
	return ct
}

func (m *conntrackMgr) markAuth(authID uint64, token string, err error) {
	m.mu.Lock()
	ct := m.byID[authID]
	if ct == nil {
		m.mu.Unlock()
		return
	}
	delete(m.byID, authID)
	if token != "" {
		ct.connectToken = token
	}
	ct.authErr = err
	m.mu.Unlock()
	close(ct.authCh)
}

func (m *conntrackMgr) cleanup(now time.Time) {
	cutoff := now.Add(-m.idleAfter).UnixNano()
	for key, ct := range m.byKey {
		if conntrackCompleted(ct) && ct.lastUsed.Load() <= cutoff {
			delete(m.byKey, key)
			delete(m.byID, ct.authID)
		}
	}
	for len(m.byKey) >= m.limit {
		var oldestKey string
		var oldest *conntrack
		for key, ct := range m.byKey {
			if !conntrackCompleted(ct) || oldest != nil && ct.lastUsed.Load() >= oldest.lastUsed.Load() {
				continue
			}
			oldestKey = key
			oldest = ct
		}
		if oldest == nil {
			return
		}
		delete(m.byKey, oldestKey)
		delete(m.byID, oldest.authID)
	}
}

func conntrackCompleted(ct *conntrack) bool {
	select {
	case <-ct.authCh:
		return true
	default:
		return false
	}
}
