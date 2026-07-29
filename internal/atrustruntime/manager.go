package atrustruntime

import (
	"context"
	"sync"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

type Manager struct {
	coordinator *sessionCoordinator
	deps        dependencies
}

type sessionCoordinator struct {
	operationMu sync.Mutex
	mu          sync.RWMutex
	active      *Session
}

var processSessionCoordinator sessionCoordinator

func NewManager() *Manager {
	return &Manager{
		coordinator: &processSessionCoordinator,
		deps:        defaultDependencies(),
	}
}

func newManager(deps dependencies) *Manager {
	return &Manager{coordinator: &sessionCoordinator{}, deps: deps}
}

func (manager *Manager) Start(ctx context.Context, id core.SessionID, config Config) (*Session, error) {
	return manager.StartObserved(ctx, id, config, nil)
}

func (manager *Manager) StartObserved(ctx context.Context, id core.SessionID, config Config, observe func(*Session)) (*Session, error) {
	manager.coordinator.operationMu.Lock()
	defer manager.coordinator.operationMu.Unlock()

	if err := manager.closeActive(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	session := newSession(id, config, manager.deps)
	manager.setActive(session)
	if observe != nil {
		observe(session)
	}
	if err := session.Start(ctx); err != nil {
		_, _ = session.Close(context.Background())
		manager.clearActive(session)
		return session, err
	}
	return session, nil
}

func (manager *Manager) Stop(ctx context.Context, id core.SessionID) (core.CleanupReport, error) {
	manager.coordinator.operationMu.Lock()
	defer manager.coordinator.operationMu.Unlock()

	session := manager.Active()
	if session == nil || session.id != id {
		return core.CleanupReport{}, nil
	}
	report, err := session.Close(ctx)
	if err != nil {
		if waitErr := session.waitRuntimeClosed(ctx); waitErr != nil {
			return report, err
		}
	}
	manager.clearActive(session)
	return report, err
}

func (manager *Manager) Close(ctx context.Context) error {
	manager.coordinator.operationMu.Lock()
	defer manager.coordinator.operationMu.Unlock()
	return manager.closeActive(ctx)
}

func (manager *Manager) Active() *Session {
	manager.coordinator.mu.RLock()
	defer manager.coordinator.mu.RUnlock()
	return manager.coordinator.active
}

func (manager *Manager) closeActive(ctx context.Context) error {
	active := manager.Active()
	if active == nil {
		return nil
	}
	_, err := active.Close(ctx)
	if err != nil {
		if waitErr := active.waitRuntimeClosed(ctx); waitErr != nil {
			return err
		}
	}
	manager.clearActive(active)
	return nil
}

func (manager *Manager) setActive(session *Session) {
	manager.coordinator.mu.Lock()
	manager.coordinator.active = session
	manager.coordinator.mu.Unlock()
}

func (manager *Manager) clearActive(session *Session) {
	manager.coordinator.mu.Lock()
	if manager.coordinator.active == session {
		manager.coordinator.active = nil
	}
	manager.coordinator.mu.Unlock()
}
