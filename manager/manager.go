package manager

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/internal/atrustruntime"
)

const managerEventBuffer = 128

type Manager struct {
	runtime *atrustruntime.Manager
	auth    *authBroker
	events  chan core.Event
	nextID  atomic.Uint64

	mu       sync.RWMutex
	sessions map[core.SessionID]*atrustruntime.Session
}

var _ core.Manager = (*Manager)(nil)

func New() *Manager {
	events := make(chan core.Event, managerEventBuffer)
	return &Manager{
		runtime:  atrustruntime.NewManager(),
		auth:     newAuthBroker(events),
		events:   events,
		sessions: make(map[core.SessionID]*atrustruntime.Session),
	}
}

func (manager *Manager) Start(ctx context.Context, config core.Config) (core.SessionID, error) {
	id := config.SessionID
	if id == "" {
		id = core.SessionID(fmt.Sprintf("session-%d", manager.nextID.Add(1)))
	}
	runtimeConfig := toRuntimeConfig(config)
	runtimeConfig.SetupNetwork = true
	runtimeConfig.AuthHandler = manager.auth.handler(id)

	session, err := manager.runtime.StartObserved(ctx, id, runtimeConfig, func(session *atrustruntime.Session) {
		manager.mu.Lock()
		manager.sessions[id] = session
		manager.mu.Unlock()
		go manager.forwardSessionEvents(session)
	})
	if session == nil {
		return "", err
	}
	return id, err
}

func (manager *Manager) RespondAuth(ctx context.Context, response core.AuthResponse) error {
	return manager.auth.respond(ctx, response)
}

func (manager *Manager) Stop(ctx context.Context, id core.SessionID) error {
	_, err := manager.runtime.Stop(ctx, id)
	return err
}

func (manager *Manager) Close(ctx context.Context) error {
	return manager.runtime.Close(ctx)
}

func (manager *Manager) Status(id core.SessionID) core.SessionStatus {
	manager.mu.RLock()
	session := manager.sessions[id]
	manager.mu.RUnlock()
	if session == nil {
		return core.SessionStatus{}
	}
	return session.Status()
}

func (manager *Manager) Resources(id core.SessionID) (core.Resources, error) {
	manager.mu.RLock()
	session := manager.sessions[id]
	manager.mu.RUnlock()
	if session == nil {
		return core.Resources{}, core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	return session.Resources()
}

func (manager *Manager) RefreshResources(ctx context.Context, id core.SessionID) (core.Resources, error) {
	manager.mu.RLock()
	session := manager.sessions[id]
	manager.mu.RUnlock()
	if session == nil {
		return core.Resources{}, core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	if err := session.RefreshResources(ctx); err != nil {
		return core.Resources{}, err
	}
	return session.Resources()
}

func (manager *Manager) Outbound(id core.SessionID) (core.Outbound, error) {
	manager.mu.RLock()
	session := manager.sessions[id]
	manager.mu.RUnlock()
	if session == nil {
		return nil, core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	return session.Outbound()
}

func (manager *Manager) Services(id core.SessionID) ([]core.ServiceStatus, error) {
	manager.mu.RLock()
	session := manager.sessions[id]
	manager.mu.RUnlock()
	if session == nil {
		return nil, core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	return session.Services()
}

func (manager *Manager) TrafficStats(id core.SessionID) (core.TrafficStats, error) {
	manager.mu.RLock()
	session := manager.sessions[id]
	manager.mu.RUnlock()
	if session == nil {
		return core.TrafficStats{}, core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	return session.TrafficStats()
}

func (manager *Manager) Connections(id core.SessionID) ([]core.ConnectionInfo, error) {
	manager.mu.RLock()
	session := manager.sessions[id]
	manager.mu.RUnlock()
	if session == nil {
		return nil, core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	return session.Connections()
}

func (manager *Manager) CloseConnection(id core.SessionID, connectionID string) error {
	manager.mu.RLock()
	session := manager.sessions[id]
	manager.mu.RUnlock()
	if session == nil {
		return core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	return session.CloseConnection(connectionID)
}

func (manager *Manager) TransportConnections(id core.SessionID) ([]core.TransportConnectionInfo, error) {
	manager.mu.RLock()
	session := manager.sessions[id]
	manager.mu.RUnlock()
	if session == nil {
		return nil, core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	return session.TransportConnections()
}

func (manager *Manager) RoutingMode(id core.SessionID) (core.RoutingMode, error) {
	manager.mu.RLock()
	session := manager.sessions[id]
	manager.mu.RUnlock()
	if session == nil {
		return "", core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	return session.RoutingMode()
}

func (manager *Manager) SetRoutingMode(id core.SessionID, mode core.RoutingMode) error {
	manager.mu.RLock()
	session := manager.sessions[id]
	manager.mu.RUnlock()
	if session == nil {
		return core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	return session.SetRoutingMode(mode)
}

func (manager *Manager) ResumeState(id core.SessionID) (core.ResumeState, error) {
	manager.mu.RLock()
	session := manager.sessions[id]
	manager.mu.RUnlock()
	if session == nil {
		return core.ResumeState{}, core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	return session.ResumeState()
}

func (manager *Manager) Events() <-chan core.Event {
	return manager.events
}

func (manager *Manager) forwardSessionEvents(session *atrustruntime.Session) {
	for event := range session.Events() {
		manager.events <- event
		if event.Type == core.EventTypeShutdownCompleted {
			return
		}
	}
}

func toRuntimeConfig(config core.Config) atrustruntime.Config {
	autoDetectInterface := config.AutoDetectInterface
	tunOutboundInterface := config.TUNOutboundInterface
	if config.TUNEnabled && config.TUNAutoRoute {
		if tunOutboundInterface == "" {
			tunOutboundInterface = config.BindInterface
		}
		if config.BindInterface == "" {
			autoDetectInterface = true
		}
	}
	return atrustruntime.Config{
		ResumeState:             config.ResumeState,
		ServerAddress:           config.ServerAddress,
		ServerPort:              config.ServerPort,
		Username:                config.Username,
		Password:                config.Password,
		Phone:                   config.Phone,
		LoginDomain:             config.LoginDomain,
		AuthType:                config.AuthType,
		GraphCodeFile:           config.GraphCodeFile,
		CASTicket:               config.CASTicket,
		OAuth2Code:              config.OAuth2Code,
		SID:                     config.SID,
		DeviceID:                config.DeviceID,
		SignKey:                 config.SignKey,
		ClientDataFile:          config.ClientDataFile,
		UpdateBestNodesInterval: config.UpdateBestNodesSeconds,
		BindInterface:           config.BindInterface,
		AutoDetectInterface:     autoDetectInterface,
		DisableAutoReconnect:    config.DisableAutoReconnect,
		DisableRemoteDNS:        config.DisableRemoteDNS,
		RemoteDNSServer:         config.RemoteDNSServer,
		SecondaryDNSServer:      config.SecondaryDNSServer,
		DNSTTL:                  config.DNSTTL,
		DNSBind:                 config.DNSBind,
		Hosts:                   config.Hosts,
		SOCKSBind:               config.SOCKSBind,
		SOCKSUsername:           config.SOCKSUsername,
		SOCKSPassword:           config.SOCKSPassword,
		HTTPBind:                config.HTTPBind,
		TUNEnabled:              config.TUNEnabled,
		TUNName:                 config.TUNName,
		TUNAddress:              config.TUNAddress,
		TUNMTU:                  config.TUNMTU,
		TUNAutoRoute:            config.TUNAutoRoute,
		TUNOutboundInterface:    tunOutboundInterface,
		TUNUDPTimeoutSeconds:    config.TUNUDPTimeoutSeconds,
		TUNUDPMaxFlows:          config.TUNUDPMaxFlows,
		TUNDNSHijack:            config.TUNDNSHijack,
		TUNFakeIP:               config.TUNFakeIP,
		TUNFakeIPRange:          config.TUNFakeIPRange,
		RoutingMode:             config.RoutingMode,
		InternetOutbound:        config.InternetOutbound,
	}
}

type authBroker struct {
	events chan<- core.Event
	mu     sync.Mutex
	items  map[string]*pendingAuth
}

type pendingAuth struct {
	sessionID core.SessionID
	challenge core.AuthChallenge
	response  chan core.AuthResponse
	responded bool
}

type sessionAuthHandler struct {
	sessionID core.SessionID
	broker    *authBroker
}

func newAuthBroker(events chan<- core.Event) *authBroker {
	return &authBroker{events: events, items: make(map[string]*pendingAuth)}
}

func (broker *authBroker) handler(sessionID core.SessionID) core.AuthHandler {
	return sessionAuthHandler{sessionID: sessionID, broker: broker}
}

func (handler sessionAuthHandler) Handle(ctx context.Context, challenge core.AuthChallenge) (core.AuthResponse, error) {
	if err := challenge.Validate(); err != nil {
		return core.AuthResponse{}, err
	}
	pending := &pendingAuth{
		sessionID: handler.sessionID,
		challenge: challenge,
		response:  make(chan core.AuthResponse, 1),
	}
	handler.broker.mu.Lock()
	if _, exists := handler.broker.items[challenge.ID]; exists {
		handler.broker.mu.Unlock()
		return core.AuthResponse{}, core.WrapError(core.ErrorCodeAuthChallengeInvalid, "authentication challenge ID is already pending", false, nil)
	}
	handler.broker.items[challenge.ID] = pending
	handler.broker.mu.Unlock()
	defer func() {
		handler.broker.mu.Lock()
		delete(handler.broker.items, challenge.ID)
		handler.broker.mu.Unlock()
	}()

	eventType := core.EventTypeAuthRequired
	if challenge.Kind == core.AuthChallengeCASCallback || challenge.Kind == core.AuthChallengeOAuthCallback {
		eventType = core.EventTypeAuthBrowserRequired
	}
	if err := handler.broker.emit(ctx, core.Event{
		SessionID: handler.sessionID,
		Type:      eventType,
		Timestamp: time.Now(),
		Auth:      &challenge,
	}); err != nil {
		return core.AuthResponse{}, err
	}

	select {
	case <-ctx.Done():
		return core.AuthResponse{}, ctx.Err()
	case response := <-pending.response:
		if err := handler.broker.emit(ctx, core.Event{
			SessionID: handler.sessionID,
			Type:      core.EventTypeAuthCompleted,
			Timestamp: time.Now(),
			Auth:      &challenge,
		}); err != nil {
			return core.AuthResponse{}, err
		}
		return response, nil
	}
}

func (broker *authBroker) respond(ctx context.Context, response core.AuthResponse) error {
	broker.mu.Lock()
	pending := broker.items[response.ChallengeID]
	if pending == nil {
		broker.mu.Unlock()
		return core.WrapError(core.ErrorCodeAuthChallengeNotFound, "authentication challenge is not pending", false, nil)
	}
	if pending.responded {
		broker.mu.Unlock()
		return core.WrapError(core.ErrorCodeAuthResponseInvalid, "authentication challenge already has a response", false, nil)
	}
	if err := response.Validate(pending.challenge); err != nil {
		broker.mu.Unlock()
		return err
	}
	pending.responded = true
	broker.mu.Unlock()

	select {
	case <-ctx.Done():
		broker.mu.Lock()
		if broker.items[response.ChallengeID] == pending {
			pending.responded = false
		}
		broker.mu.Unlock()
		return ctx.Err()
	case pending.response <- response:
		return nil
	}
}

func (broker *authBroker) emit(ctx context.Context, event core.Event) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case broker.events <- event:
		return nil
	}
}
