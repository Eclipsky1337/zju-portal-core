package atrustruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	clientpkg "github.com/Eclipsky1337/zju-portal-core/client"
	atrustclient "github.com/Eclipsky1337/zju-portal-core/client/atrust"
	"github.com/Eclipsky1337/zju-portal-core/core"
)

const sessionEventBuffer = 32

const (
	reconnectInitialDelay = time.Second
	reconnectMaxDelay     = 30 * time.Second
	sessionCleanupTimeout = 5 * time.Second
)

type Session struct {
	id     core.SessionID
	config Config
	deps   dependencies

	mu            sync.RWMutex
	state         core.SessionState
	lastError     *core.Error
	runtime       *Runtime
	network       *networkSession
	cancel        context.CancelFunc
	stageErr      error
	resources     core.Resources
	selectedNodes map[string]string
	resumeState   core.ResumeState

	eventMu sync.Mutex
	events  chan core.Event

	startOnce   sync.Once
	startErr    error
	closeOnce   sync.Once
	closeDone   chan struct{}
	closeReport core.CleanupReport
	closeErr    error
	refreshMu   sync.Mutex
}

func newSession(id core.SessionID, config Config, deps dependencies) *Session {
	return &Session{
		id:        id,
		config:    config,
		deps:      deps,
		state:     core.SessionStateIdle,
		events:    make(chan core.Event, sessionEventBuffer),
		closeDone: make(chan struct{}),
	}
}

func NewSession(id core.SessionID, config Config) *Session {
	return newSession(id, config, defaultDependencies())
}

func (s *Session) ID() core.SessionID {
	return s.id
}

func (s *Session) waitRuntimeClosed(ctx context.Context) error {
	s.mu.RLock()
	runtime := s.runtime
	network := s.network
	s.mu.RUnlock()
	var closeErrors []error
	if network != nil {
		closeErrors = append(closeErrors, network.Close(ctx))
	}
	if runtime != nil {
		closeErrors = append(closeErrors, runtime.CloseContext(ctx))
	}
	return errors.Join(closeErrors...)
}

func (s *Session) WaitClosed(ctx context.Context) error {
	return s.waitRuntimeClosed(ctx)
}

func (s *Session) Start(ctx context.Context) error {
	s.startOnce.Do(func() {
		s.startErr = s.start(ctx)
	})
	return s.startErr
}

func (s *Session) start(ctx context.Context) error {
	sessionCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	config := s.config
	config.NodeSelectionHandler = s.handleNodeSelection
	runtime, err := startWithStageHandler(sessionCtx, config, s.deps, s.handleSetupStage)
	if err == nil {
		s.mu.RLock()
		err = s.stageErr
		state := s.state
		s.mu.RUnlock()
		if state == core.SessionStateStopping || state == core.SessionStateStopped {
			_ = runtime.CloseContext(context.Background())
			if err == nil {
				err = context.Canceled
			}
		}
	}
	if err != nil {
		cancel()
		s.fail(err)
		return err
	}

	network := runtime.DetachOutbound()
	resources, err := s.deps.readResources(runtime.Client())
	if err != nil {
		if network != nil {
			_ = network.Close(context.Background())
		}
		_ = runtime.CloseContext(context.Background())
		cancel()
		resourceErr := core.WrapError(core.ErrorCodeResourcesUnavailable, "read initial resource snapshot", false, err)
		s.failWith(core.ErrorCodeResourcesUnavailable, "read initial resource snapshot", resourceErr)
		return resourceErr
	}
	resumeState, _ := runtime.ResumeState()
	s.mu.Lock()
	s.runtime = runtime
	s.network = network
	s.resources = resources
	s.resumeState = resumeState
	s.mu.Unlock()
	if err := s.transition(core.SessionStateReady); err != nil {
		if network != nil {
			_ = network.Close(context.Background())
		}
		_ = runtime.CloseContext(context.Background())
		cancel()
		s.fail(err)
		return err
	}
	s.emitResourcesUpdated(resources)
	s.emitSelectedNodes()
	s.emitServiceEvents(network, core.EventTypeServiceStarted)
	if resumeState.Data != "" {
		if s.config.ResumeState != nil && !resumeState.Reused {
			s.emit(core.NewResumeStateInvalidatedEvent(s.id, s.config.ResumeState.Revision, time.Now()))
		}
		s.emit(core.NewResumeStateUpdatedEvent(s.id, resumeState.Revision, resumeState.Reused, time.Now()))
	}

	go s.closeWhenCanceled(sessionCtx)
	go s.monitorRuntime(sessionCtx, runtime)
	go s.monitorServiceEvents(sessionCtx, network)
	return nil
}

func (s *Session) monitorRuntime(ctx context.Context, runtime *Runtime) {
	for runtime != nil {
		done := runtime.Done()
		if done == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-done:
		}
		if err := ctx.Err(); err != nil {
			return
		}
		if current := runtime.Done(); current != nil && current != done {
			continue
		}
		runtimeErr := runtime.Err()
		if runtimeErr == nil {
			if current := runtime.Done(); current != nil && current != done {
				continue
			}
			return
		}
		if core.ErrorCodeOf(runtimeErr) == core.ErrorCodeTUNUnavailable {
			s.mu.RLock()
			network := s.network
			s.mu.RUnlock()
			if network != nil {
				_ = network.Close(context.Background())
			}
			_ = runtime.CloseContext(context.Background())
			s.failWith(core.ErrorCodeTUNUnavailable, "TUN service stopped", runtimeErr)
			return
		}
		if s.config.DisableAutoReconnect {
			s.mu.RLock()
			network := s.network
			s.mu.RUnlock()
			if network != nil {
				_ = network.Close(context.Background())
			}
			_ = runtime.CloseContext(context.Background())
			s.failWith(core.ErrorCodeSessionReconnectFailed, "VPN network runtime stopped", runtimeErr)
			return
		}
		runtime = s.reconnect(ctx, runtime, runtimeErr)
	}
}

func (s *Session) monitorServiceEvents(ctx context.Context, network *networkSession) {
	if network == nil || network.ServiceEvents() == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case status, ok := <-network.ServiceEvents():
			if !ok {
				return
			}
			s.emit(core.NewServiceEvent(s.id, core.EventTypeServiceStopped, status, time.Now()))
		}
	}
}

func (s *Session) reconnect(ctx context.Context, failedRuntime *Runtime, runtimeErr error) *Runtime {
	if err := s.transition(core.SessionStateReconnecting); err != nil {
		return nil
	}

	config := s.reconnectConfig(failedRuntime)
	s.mu.Lock()
	if s.runtime == failedRuntime {
		s.runtime = nil
	}
	s.mu.Unlock()
	_ = failedRuntime.CloseContext(context.Background())

	for attempt := 1; ; attempt++ {
		delay := reconnectDelay(attempt)
		s.emit(core.Event{
			SessionID: s.id,
			Type:      core.EventTypeReconnectScheduled,
			Timestamp: time.Now(),
			Error:     asCoreError(core.ErrorCodeSessionReconnectFailed, "VPN network runtime stopped", runtimeErr),
			Reconnect: &core.ReconnectInfo{Attempt: attempt, DelayMillis: delay.Milliseconds()},
		})
		if err := s.deps.wait(ctx, delay); err != nil {
			return nil
		}

		candidate, err := startWithStageHandler(ctx, config, s.deps, nil)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			coreError := asCoreError(core.ErrorCodeSessionReconnectFailed, "reconnect session", err)
			s.mu.Lock()
			s.lastError = coreError
			s.mu.Unlock()
			s.emit(core.Event{
				SessionID: s.id,
				Type:      core.EventTypeReconnectFailed,
				Timestamp: time.Now(),
				Error:     coreError,
				Reconnect: &core.ReconnectInfo{Attempt: attempt},
			})
			if !core.IsRetryable(err) {
				s.mu.RLock()
				network := s.network
				s.mu.RUnlock()
				if network != nil {
					_ = network.Close(context.Background())
				}
				s.failWith(coreError.Code, coreError.Message, coreError)
				return nil
			}
			continue
		}

		if !s.installReconnectedRuntime(ctx, candidate) {
			_ = candidate.CloseContext(context.Background())
			return nil
		}
		if resumeState, err := candidate.ResumeState(); err == nil {
			if config.ResumeState != nil && !resumeState.Reused {
				s.emit(core.NewResumeStateInvalidatedEvent(s.id, config.ResumeState.Revision, time.Now()))
			}
			s.emit(core.NewResumeStateUpdatedEvent(s.id, resumeState.Revision, resumeState.Reused, time.Now()))
		}
		s.emit(core.Event{
			SessionID: s.id,
			Type:      core.EventTypeReconnected,
			Timestamp: time.Now(),
			Reconnect: &core.ReconnectInfo{Attempt: attempt},
		})
		return candidate
	}
}

func (s *Session) reconnectConfig(runtime *Runtime) Config {
	config := s.config
	config.NodeSelectionHandler = s.handleNodeSelection
	s.mu.RLock()
	network := s.network
	s.mu.RUnlock()
	if existing, ok := network.replaceable(); ok {
		config.NetworkRuntime = existing
	}
	if config.ClientDataFile != "" {
		config.ResumeState = nil
		return config
	}
	s.mu.RLock()
	resumeState := s.resumeState
	s.mu.RUnlock()
	if resumeState.Data == "" {
		resumeState, _ = runtime.ResumeState()
	}
	if resumeState.Data != "" {
		config.ResumeState = &resumeState
	}
	return config
}

func (s *Session) installReconnectedRuntime(ctx context.Context, runtime *Runtime) bool {
	network := runtime.DetachOutbound()
	resources, resourceErr := s.deps.readResources(runtime.Client())
	resumeState, _ := runtime.ResumeState()
	s.mu.Lock()
	if ctx.Err() != nil || s.state != core.SessionStateReconnecting {
		s.mu.Unlock()
		if network != nil {
			_ = network.Close(context.Background())
		}
		return false
	}
	previous := s.state
	if err := core.ValidateSessionStateTransition(previous, core.SessionStateReady); err != nil {
		s.mu.Unlock()
		if network != nil {
			_ = network.Close(context.Background())
		}
		return false
	}
	oldNetwork := s.network
	if resourceErr != nil {
		resources = cloneResources(s.resources)
		resources.Stale = true
	}
	s.runtime = runtime
	s.network = network
	s.resources = resources
	if resumeState.Data != "" {
		s.resumeState = resumeState
	}
	s.state = core.SessionStateReady
	s.lastError = nil
	s.mu.Unlock()
	if oldNetwork != nil && !oldNetwork.same(network) {
		_ = oldNetwork.Close(context.Background())
	}

	s.emit(core.NewStateChangedEvent(s.id, previous, core.SessionStateReady, time.Now()))
	if resourceErr == nil {
		s.emitResourcesUpdated(resources)
	}
	s.emitSelectedNodes()
	return true
}

func reconnectDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 0
	}
	delay := reconnectInitialDelay
	for current := 2; current < attempt && delay < reconnectMaxDelay; current++ {
		delay *= 2
		if delay > reconnectMaxDelay {
			delay = reconnectMaxDelay
		}
	}
	return delay
}

func (s *Session) closeWhenCanceled(ctx context.Context) {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.Close(shutdownCtx)
}

func (s *Session) handleSetupStage(stage atrustclient.SetupStage) {
	state, ok := setupStageState(stage)
	if !ok {
		return
	}
	if err := s.transition(state); err != nil {
		s.mu.Lock()
		if s.stageErr == nil {
			s.stageErr = err
		}
		s.mu.Unlock()
	}
}

func setupStageState(stage atrustclient.SetupStage) (core.SessionState, bool) {
	switch stage {
	case atrustclient.SetupStageDiscoveringAuth:
		return core.SessionStateDiscoveringAuth, true
	case atrustclient.SetupStageAuthenticating:
		return core.SessionStateAuthenticating, true
	case atrustclient.SetupStageFetchingResources:
		return core.SessionStateFetchingResources, true
	case atrustclient.SetupStageSelectingNodes:
		return core.SessionStateSelectingNodes, true
	case atrustclient.SetupStageEstablishingTunnel:
		return core.SessionStateEstablishingTunnel, true
	default:
		return "", false
	}
}

func (s *Session) transition(next core.SessionState) error {
	s.mu.Lock()
	previous := s.state
	if err := core.ValidateSessionStateTransition(previous, next); err != nil {
		s.mu.Unlock()
		return err
	}
	s.state = next
	s.mu.Unlock()

	s.emit(core.NewStateChangedEvent(s.id, previous, next, time.Now()))
	return nil
}

func (s *Session) fail(err error) {
	s.failWith(core.ErrorCodeSessionStartFailed, "start session", err)
}

func (s *Session) failWith(code core.ErrorCode, message string, err error) {
	coreError := asCoreError(code, message, err)
	s.mu.Lock()
	s.lastError = coreError
	state := s.state
	s.mu.Unlock()
	if state != core.SessionStateStopping && state != core.SessionStateStopped && state != core.SessionStateFailed {
		_ = s.transition(core.SessionStateFailed)
	}
	s.emit(core.Event{
		SessionID: s.id,
		Type:      core.EventTypeSessionError,
		Timestamp: time.Now(),
		Error:     coreError,
	})
}

func (s *Session) Close(ctx context.Context) (core.CleanupReport, error) {
	s.closeOnce.Do(func() {
		go func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), sessionCleanupTimeout)
			defer cancel()
			s.closeReport, s.closeErr = s.close(cleanupCtx)
			close(s.closeDone)
		}()
	})
	select {
	case <-s.closeDone:
		return s.closeReport, s.closeErr
	case <-ctx.Done():
		return core.CleanupReport{}, core.WrapError(core.ErrorCodeSessionCloseFailed, "wait for session cleanup", false, ctx.Err())
	}
}

func (s *Session) close(ctx context.Context) (core.CleanupReport, error) {
	report := core.CleanupReport{StartedAt: time.Now()}
	s.mu.RLock()
	cancel := s.cancel
	state := s.state
	s.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	if state != core.SessionStateStopping && state != core.SessionStateStopped {
		if err := s.transition(core.SessionStateStopping); err != nil {
			report.Results = append(report.Results, core.CleanupResult{Component: "session.state", Error: err.Error()})
		}
	}
	s.mu.RLock()
	runtime := s.runtime
	network := s.network
	s.mu.RUnlock()
	services := serviceStatuses(network)

	var closeErr error
	result := core.CleanupResult{Component: "atrust.runtime"}
	if network != nil {
		if err := network.Close(ctx); err != nil {
			result.Error = err.Error()
			closeErr = core.WrapError(core.ErrorCodeSessionCloseFailed, "close VPN network runtime", false, err)
		}
	}
	if runtime != nil {
		if err := runtime.CloseContext(ctx); err != nil {
			result.Error = errors.Join(closeErr, err).Error()
			if closeErr == nil {
				closeErr = core.WrapError(core.ErrorCodeSessionCloseFailed, "close aTrust runtime", false, err)
			}
		}
	}
	services = mergeStoppedServiceStatuses(services, serviceStatuses(network))
	for _, status := range services {
		s.emit(core.NewServiceEvent(s.id, core.EventTypeServiceStopped, status, time.Now()))
	}
	report.Results = append(report.Results, result)

	s.mu.RLock()
	state = s.state
	s.mu.RUnlock()
	if state != core.SessionStateStopped {
		if err := s.transition(core.SessionStateStopped); err != nil && closeErr == nil {
			closeErr = core.WrapError(core.ErrorCodeSessionCloseFailed, "mark session stopped", false, err)
		}
	}
	report.CompletedAt = time.Now()
	s.emit(core.Event{
		SessionID: s.id,
		Type:      core.EventTypeShutdownCompleted,
		Timestamp: report.CompletedAt,
		Cleanup:   &report,
	})
	return report, closeErr
}

func (s *Session) Status() core.SessionStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return core.SessionStatus{ID: s.id, State: s.state, LastError: s.lastError}
}

func (s *Session) Events() <-chan core.Event {
	return s.events
}

func (s *Session) emit(event core.Event) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	select {
	case s.events <- event:
		return
	default:
	}
	select {
	case <-s.events:
	default:
	}
	s.events <- event
}

func (s *Session) Client() clientpkg.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.runtime == nil {
		return nil
	}
	return s.runtime.Client()
}

func (s *Session) RefreshResources(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	s.mu.RLock()
	if s.state != core.SessionStateReady || s.runtime == nil {
		s.mu.RUnlock()
		return core.WrapError(core.ErrorCodeSessionNotReady, fmt.Sprintf("session %q is not ready", s.id), true, nil)
	}
	runtime := s.runtime
	network := s.network
	replaceableNetwork, replaceable := network.replaceable()
	s.mu.RUnlock()
	if !replaceable {
		return core.WrapError(core.ErrorCodeOutboundUnavailable, "session network runtime cannot refresh VPN resources", false, nil)
	}

	config := s.reconnectConfig(runtime)
	config.SetupNetwork = false
	config.NetworkRuntime = replaceableNetwork
	var selectedNodes map[string]string
	var selectedNodesMu sync.Mutex
	active := false
	config.NodeSelectionHandler = func(nodes map[string]string) {
		selectedNodesMu.Lock()
		if !active {
			selectedNodes = cloneMap(nodes)
			selectedNodesMu.Unlock()
			return
		}
		selectedNodesMu.Unlock()
		s.handleNodeSelection(nodes)
	}
	candidate, err := startWithStageHandler(ctx, config, s.deps, nil)
	if err != nil {
		return core.WrapError(core.ErrorCodeResourcesUnavailable, "refresh aTrust resources", true, err)
	}
	defer candidate.CloseContext(context.Background())
	resources, err := s.deps.readResources(candidate.Client())
	if err != nil {
		return core.WrapError(core.ErrorCodeResourcesUnavailable, "read refreshed resources", true, err)
	}
	resumeState, _ := candidate.ResumeState()

	s.mu.Lock()
	if s.state != core.SessionStateReady || s.runtime != runtime {
		s.mu.Unlock()
		return core.WrapError(core.ErrorCodeSessionNotReady, fmt.Sprintf("session %q changed while refreshing resources", s.id), true, nil)
	}
	refreshedOutbound, err := s.deps.setupNetwork(ctx, candidate.Client(), config)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if refreshedOutbound == nil {
		s.mu.Unlock()
		return core.WrapError(core.ErrorCodeOutboundUnavailable, "refreshed network runtime is unavailable", true, nil)
	}
	refreshedNetwork := wrapNetwork(refreshedOutbound)
	if !refreshedNetwork.same(network) {
		s.mu.Unlock()
		_ = refreshedNetwork.Close(context.Background())
		return core.WrapError(core.ErrorCodeOutboundUnavailable, "resource refresh replaced the stable network runtime", false, nil)
	}
	closeOldClient := runtime.adoptClient(candidate)
	s.resources = resources
	if resumeState.Data != "" {
		s.resumeState = resumeState
	}
	selectedNodesMu.Lock()
	active = true
	nodes := cloneMap(selectedNodes)
	selectedNodesMu.Unlock()
	if len(nodes) != 0 {
		s.selectedNodes = nodes
	}
	s.mu.Unlock()
	closeOldClient()

	s.emitResourcesUpdated(resources)
	if len(nodes) != 0 {
		s.emit(core.NewNodeSelectedEvent(s.id, nodes, time.Now()))
	}
	if resumeState.Data != "" {
		if config.ResumeState != nil && !resumeState.Reused {
			s.emit(core.NewResumeStateInvalidatedEvent(s.id, config.ResumeState.Revision, time.Now()))
		}
		s.emit(core.NewResumeStateUpdatedEvent(s.id, resumeState.Revision, resumeState.Reused, time.Now()))
	}
	return nil
}

func (s *Session) Outbound() (core.Outbound, error) {
	s.mu.RLock()
	state := s.state
	network := s.network
	s.mu.RUnlock()
	if state == core.SessionStateStopping || state == core.SessionStateStopped || network == nil {
		return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "session outbound is unavailable", true, nil)
	}
	return network.outbound, nil
}

func (s *Session) Services() ([]core.ServiceStatus, error) {
	network, err := s.stableNetwork()
	if err != nil {
		return nil, err
	}
	return network.Services()
}

func (s *Session) handleNodeSelection(nodes map[string]string) {
	nodes = cloneMap(nodes)
	s.mu.Lock()
	changed := !equalStringMaps(s.selectedNodes, nodes)
	s.selectedNodes = nodes
	ready := s.state == core.SessionStateReady
	s.mu.Unlock()
	if changed && ready {
		s.emit(core.NewNodeSelectedEvent(s.id, nodes, time.Now()))
	}
}

func (s *Session) emitResourcesUpdated(resources core.Resources) {
	if resources.IPResources == nil && resources.DomainResources == nil && resources.DNSRecords == nil {
		return
	}
	s.emit(core.NewResourcesUpdatedEvent(s.id, resources, time.Now()))
}

func (s *Session) emitSelectedNodes() {
	s.mu.RLock()
	nodes := cloneMap(s.selectedNodes)
	s.mu.RUnlock()
	if len(nodes) != 0 {
		s.emit(core.NewNodeSelectedEvent(s.id, nodes, time.Now()))
	}
}

func (s *Session) emitServiceEvents(network *networkSession, eventType core.EventType) {
	for _, status := range serviceStatuses(network) {
		if eventType == core.EventTypeServiceStarted && !status.Running {
			continue
		}
		s.emit(core.NewServiceEvent(s.id, eventType, status, time.Now()))
	}
}

func serviceStatuses(network *networkSession) []core.ServiceStatus {
	if network == nil {
		return nil
	}
	services, _ := network.Services()
	return services
}

func mergeStoppedServiceStatuses(before, after []core.ServiceStatus) []core.ServiceStatus {
	byType := make(map[core.ServiceType]core.ServiceStatus, len(after))
	for _, status := range after {
		byType[status.Type] = status
	}
	for index := range before {
		if status, ok := byType[before[index].Type]; ok {
			before[index].Running = status.Running
			before[index].LastError = status.LastError
			if before[index].Address == "" {
				before[index].Address = status.Address
			}
		}
	}
	return before
}

func equalStringMaps(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func asCoreError(code core.ErrorCode, message string, err error) *core.Error {
	var coreError *core.Error
	if errors.As(err, &coreError) {
		return coreError
	}
	return core.WrapError(code, message, false, err)
}

func (s *Session) String() string {
	status := s.Status()
	return fmt.Sprintf("%s (%s)", status.ID, status.State)
}
