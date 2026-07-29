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

const ProtocolATrust = "atrust"

type Session interface {
	ID() core.SessionID
	Start(context.Context) error
	Close(context.Context) (core.CleanupReport, error)
	WaitClosed(context.Context) error
	Status() core.SessionStatus
	Resources() (core.Resources, error)
	RefreshResources(context.Context) error
	Outbound() (core.Outbound, error)
	Services() ([]core.ServiceStatus, error)
	TrafficStats() (core.TrafficStats, error)
	Connections() ([]core.ConnectionInfo, error)
	CloseConnection(string) error
	TransportConnections() ([]core.TransportConnectionInfo, error)
	RoutingMode() (core.RoutingMode, error)
	SetRoutingMode(core.RoutingMode) error
	ResumeState() (core.ResumeState, error)
	Events() <-chan core.Event
}

type SessionFactory func(core.SessionID, core.Config, core.AuthHandler) (Session, error)

type Option func(*Manager)

func WithProtocol(protocol string, factory SessionFactory) Option {
	return func(manager *Manager) {
		if protocol != "" && factory != nil {
			manager.factories[protocol] = factory
		}
	}
}

type sessionCoordinator struct {
	operationMu sync.Mutex
	mu          sync.RWMutex
	active      Session
}

var processSessionCoordinator sessionCoordinator

type Manager struct {
	coordinator *sessionCoordinator
	auth        *authBroker
	events      chan core.Event
	nextID      atomic.Uint64
	factories   map[string]SessionFactory

	mu      sync.RWMutex
	current Session
}

var _ core.Manager = (*Manager)(nil)

func New(options ...Option) *Manager {
	events := make(chan core.Event, managerEventBuffer)
	manager := &Manager{
		coordinator: &processSessionCoordinator,
		auth:        newAuthBroker(events),
		events:      events,
		factories:   make(map[string]SessionFactory),
	}
	manager.factories[ProtocolATrust] = manager.newATrustSession
	for _, option := range options {
		option(manager)
	}
	return manager
}

func (manager *Manager) Start(ctx context.Context, config core.Config) (core.SessionID, error) {
	manager.coordinator.operationMu.Lock()
	defer manager.coordinator.operationMu.Unlock()

	if err := manager.closeActive(ctx); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	id := config.SessionID
	if id == "" {
		id = core.SessionID(fmt.Sprintf("session-%d", manager.nextID.Add(1)))
	}
	protocol := config.Protocol
	if protocol == "" {
		protocol = ProtocolATrust
	}
	factory := manager.factories[protocol]
	if factory == nil {
		return "", core.WrapError(core.ErrorCodeConfigInvalid, fmt.Sprintf("unsupported protocol %q", protocol), false, nil)
	}
	session, err := factory(id, config, manager.auth.handler(id))
	if err != nil {
		return "", err
	}
	if session == nil {
		return "", core.WrapError(core.ErrorCodeSessionStartFailed, fmt.Sprintf("protocol %q returned no session", protocol), false, nil)
	}
	manager.coordinator.setActive(session)
	manager.mu.Lock()
	manager.current = session
	manager.mu.Unlock()
	go manager.forwardSessionEvents(session)
	if err := session.Start(ctx); err != nil {
		_, _ = session.Close(context.Background())
		manager.coordinator.clearActive(session)
		return id, err
	}
	return id, nil
}

func (manager *Manager) RespondAuth(ctx context.Context, response core.AuthResponse) error {
	return manager.auth.respond(ctx, response)
}

func (manager *Manager) Stop(ctx context.Context, id core.SessionID) error {
	manager.coordinator.operationMu.Lock()
	defer manager.coordinator.operationMu.Unlock()
	session := manager.coordinator.Active()
	if session == nil || session.ID() != id {
		return nil
	}
	_, err := session.Close(ctx)
	if err != nil {
		if waitErr := session.WaitClosed(ctx); waitErr != nil {
			return err
		}
	}
	manager.coordinator.clearActive(session)
	return err
}

func (manager *Manager) Close(ctx context.Context) error {
	manager.coordinator.operationMu.Lock()
	defer manager.coordinator.operationMu.Unlock()
	return manager.closeActive(ctx)
}

func (manager *Manager) Status(id core.SessionID) core.SessionStatus {
	session, err := manager.session(id)
	if err != nil {
		return core.SessionStatus{}
	}
	return session.Status()
}

func (manager *Manager) Resources(id core.SessionID) (core.Resources, error) {
	session, err := manager.session(id)
	if err != nil {
		return core.Resources{}, err
	}
	return session.Resources()
}

func (manager *Manager) RefreshResources(ctx context.Context, id core.SessionID) (core.Resources, error) {
	session, err := manager.session(id)
	if err != nil {
		return core.Resources{}, err
	}
	if err = session.RefreshResources(ctx); err != nil {
		return core.Resources{}, err
	}
	return session.Resources()
}

func (manager *Manager) Outbound(id core.SessionID) (core.Outbound, error) {
	session, err := manager.session(id)
	if err != nil {
		return nil, err
	}
	return session.Outbound()
}

func (manager *Manager) Services(id core.SessionID) ([]core.ServiceStatus, error) {
	session, err := manager.session(id)
	if err != nil {
		return nil, err
	}
	return session.Services()
}

func (manager *Manager) TrafficStats(id core.SessionID) (core.TrafficStats, error) {
	session, err := manager.session(id)
	if err != nil {
		return core.TrafficStats{}, err
	}
	return session.TrafficStats()
}

func (manager *Manager) Connections(id core.SessionID) ([]core.ConnectionInfo, error) {
	session, err := manager.session(id)
	if err != nil {
		return nil, err
	}
	return session.Connections()
}

func (manager *Manager) CloseConnection(id core.SessionID, connectionID string) error {
	session, err := manager.session(id)
	if err != nil {
		return err
	}
	return session.CloseConnection(connectionID)
}

func (manager *Manager) TransportConnections(id core.SessionID) ([]core.TransportConnectionInfo, error) {
	session, err := manager.session(id)
	if err != nil {
		return nil, err
	}
	return session.TransportConnections()
}

func (manager *Manager) RoutingMode(id core.SessionID) (core.RoutingMode, error) {
	session, err := manager.session(id)
	if err != nil {
		return "", err
	}
	return session.RoutingMode()
}

func (manager *Manager) SetRoutingMode(id core.SessionID, mode core.RoutingMode) error {
	session, err := manager.session(id)
	if err != nil {
		return err
	}
	return session.SetRoutingMode(mode)
}

func (manager *Manager) ResumeState(id core.SessionID) (core.ResumeState, error) {
	session, err := manager.session(id)
	if err != nil {
		return core.ResumeState{}, err
	}
	return session.ResumeState()
}

func (manager *Manager) Events() <-chan core.Event {
	return manager.events
}

func (manager *Manager) forwardSessionEvents(session Session) {
	for event := range session.Events() {
		manager.events <- event
		if event.Type == core.EventTypeShutdownCompleted {
			return
		}
	}
}

func (manager *Manager) session(id core.SessionID) (Session, error) {
	manager.mu.RLock()
	session := manager.current
	manager.mu.RUnlock()
	if session == nil || session.ID() != id {
		return nil, core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	return session, nil
}

func (manager *Manager) closeActive(ctx context.Context) error {
	active := manager.coordinator.Active()
	if active == nil {
		return nil
	}
	_, err := active.Close(ctx)
	if err != nil {
		if waitErr := active.WaitClosed(ctx); waitErr != nil {
			return err
		}
	}
	manager.coordinator.clearActive(active)
	return nil
}

func (manager *Manager) newATrustSession(id core.SessionID, config core.Config, authHandler core.AuthHandler) (Session, error) {
	runtimeConfig := toRuntimeConfig(config)
	runtimeConfig.SetupNetwork = true
	runtimeConfig.AuthHandler = authHandler
	return atrustruntime.NewSession(id, runtimeConfig), nil
}

func (coordinator *sessionCoordinator) Active() Session {
	coordinator.mu.RLock()
	defer coordinator.mu.RUnlock()
	return coordinator.active
}

func (coordinator *sessionCoordinator) setActive(session Session) {
	coordinator.mu.Lock()
	coordinator.active = session
	coordinator.mu.Unlock()
}

func (coordinator *sessionCoordinator) clearActive(session Session) {
	coordinator.mu.Lock()
	if coordinator.active == session {
		coordinator.active = nil
	}
	coordinator.mu.Unlock()
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
