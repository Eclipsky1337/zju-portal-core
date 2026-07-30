package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/daemonconfig"
)

const serviceEventBuffer = 128

type Service struct {
	manager      core.Manager
	coreVersion  string
	capabilities []string

	eventCtx     context.Context
	cancelEvents context.CancelFunc
	eventDone    chan struct{}
	eventMu      sync.Mutex
	nextEventID  uint64
	subscribers  map[uint64]chan core.Event
	pendingAuth  map[string]core.Event

	closeOnce sync.Once
	closeErr  error
}

type configController interface {
	ConfigSnapshot() daemonconfig.Snapshot
	SetConfig(context.Context, daemonconfig.Config) (daemonconfig.Snapshot, error)
	PatchConfig(context.Context, []byte) (daemonconfig.Snapshot, error)
	ApplyConfig(context.Context, daemonconfig.ApplyMode) (daemonconfig.Snapshot, error)
	ReloadConfig(context.Context) (daemonconfig.Snapshot, error)
}

type sessionController interface {
	StartSession(context.Context, core.SessionStartOptions) (core.SessionID, error)
}

func NewService(manager core.Manager, coreVersion string, capabilities []string) *Service {
	eventCtx, cancelEvents := context.WithCancel(context.Background())
	service := &Service{
		manager:      manager,
		coreVersion:  coreVersion,
		capabilities: append([]string(nil), capabilities...),
		eventCtx:     eventCtx,
		cancelEvents: cancelEvents,
		eventDone:    make(chan struct{}),
		subscribers:  make(map[uint64]chan core.Event),
		pendingAuth:  make(map[string]core.Event),
	}
	go service.forwardEvents()
	return service
}

func (service *Service) Call(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case MethodHello:
		var helloParams HelloParams
		if err := decodeParams(params, &helloParams); err != nil {
			return nil, err
		}
		if helloParams.ProtocolVersion != ProtocolVersion {
			return nil, core.WrapError(
				core.ErrorCodeProtocolUnsupported,
				fmt.Sprintf("protocol version %d is unsupported", helloParams.ProtocolVersion),
				false,
				nil,
			)
		}
		return HelloResult{
			CoreVersion:     service.coreVersion,
			ProtocolVersion: ProtocolVersion,
			Capabilities:    append([]string(nil), service.capabilities...),
		}, nil
	case MethodSessionStart:
		controller, ok := service.manager.(sessionController)
		if !ok {
			return nil, core.WrapError(core.ErrorCodeMethodNotFound, "configured session control is unavailable", false, nil)
		}
		var startParams SessionStartParams
		if err := decodeParams(params, &startParams); err != nil {
			return nil, err
		}
		id, err := controller.StartSession(ctx, core.SessionStartOptions{
			SessionID:   startParams.SessionID,
			Resume:      startParams.Resume,
			ResumeState: startParams.ResumeState,
		})
		if err != nil {
			return nil, err
		}
		result := SessionStartResult{SessionID: id}
		if resumeState, stateErr := service.manager.ResumeState(id); stateErr == nil {
			result.ResumeStateRevision = resumeState.Revision
			result.ResumeStateReused = resumeState.Reused
		}
		return result, nil
	case MethodAuthRespond:
		var response core.AuthResponse
		if err := decodeParams(params, &response); err != nil {
			return nil, err
		}
		if err := service.manager.RespondAuth(ctx, response); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	case MethodSessionStop:
		var sessionParams SessionIDParams
		if err := decodeParams(params, &sessionParams); err != nil {
			return nil, err
		}
		if err := service.manager.Stop(ctx, sessionParams.SessionID); err != nil {
			return nil, err
		}
		return SessionStopResult{Stopped: true}, nil
	case MethodSessionStatus:
		var sessionParams SessionIDParams
		if err := decodeParams(params, &sessionParams); err != nil {
			return nil, err
		}
		status := service.manager.Status(sessionParams.SessionID)
		if status.ID == "" {
			return nil, core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
		}
		return status, nil
	case MethodResourcesGet:
		var sessionParams SessionIDParams
		if err := decodeParams(params, &sessionParams); err != nil {
			return nil, err
		}
		return service.manager.Resources(sessionParams.SessionID)
	case MethodResourcesRefresh:
		var sessionParams SessionIDParams
		if err := decodeParams(params, &sessionParams); err != nil {
			return nil, err
		}
		return service.manager.RefreshResources(ctx, sessionParams.SessionID)
	case MethodServicesGet:
		var sessionParams SessionIDParams
		if err := decodeParams(params, &sessionParams); err != nil {
			return nil, err
		}
		return service.manager.Services(sessionParams.SessionID)
	case MethodTrafficGet:
		var sessionParams SessionIDParams
		if err := decodeParams(params, &sessionParams); err != nil {
			return nil, err
		}
		return service.manager.TrafficStats(sessionParams.SessionID)
	case MethodConnectionsList:
		var sessionParams SessionIDParams
		if err := decodeParams(params, &sessionParams); err != nil {
			return nil, err
		}
		return service.manager.Connections(sessionParams.SessionID)
	case MethodConnectionClose:
		var closeParams ConnectionCloseParams
		if err := decodeParams(params, &closeParams); err != nil {
			return nil, err
		}
		if err := service.manager.CloseConnection(closeParams.SessionID, closeParams.ConnectionID); err != nil {
			return nil, err
		}
		return ConnectionCloseResult{Closed: true}, nil
	case MethodTransportConnectionsList:
		var sessionParams SessionIDParams
		if err := decodeParams(params, &sessionParams); err != nil {
			return nil, err
		}
		return service.manager.TransportConnections(sessionParams.SessionID)
	case MethodRoutingModeGet:
		var sessionParams SessionIDParams
		if err := decodeParams(params, &sessionParams); err != nil {
			return nil, err
		}
		mode, err := service.manager.RoutingMode(sessionParams.SessionID)
		if err != nil {
			return nil, err
		}
		return RoutingModeResult{Mode: mode}, nil
	case MethodRoutingModeSet:
		var routingParams RoutingModeSetParams
		if err := decodeParams(params, &routingParams); err != nil {
			return nil, err
		}
		if err := service.manager.SetRoutingMode(routingParams.SessionID, routingParams.Mode); err != nil {
			return nil, err
		}
		return RoutingModeResult{Mode: routingParams.Mode}, nil
	case MethodResumeStateGet:
		var sessionParams SessionIDParams
		if err := decodeParams(params, &sessionParams); err != nil {
			return nil, err
		}
		return service.manager.ResumeState(sessionParams.SessionID)
	case MethodConfigGet:
		controller, ok := service.manager.(configController)
		if !ok {
			return nil, core.WrapError(core.ErrorCodeMethodNotFound, "configuration control is unavailable", false, nil)
		}
		return controller.ConfigSnapshot(), nil
	case MethodConfigSet:
		controller, ok := service.manager.(configController)
		if !ok {
			return nil, core.WrapError(core.ErrorCodeMethodNotFound, "configuration control is unavailable", false, nil)
		}
		var configParams ConfigSetParams
		if err := decodeParams(params, &configParams); err != nil {
			return nil, err
		}
		return controller.SetConfig(ctx, configParams.Config)
	case MethodConfigPatch:
		controller, ok := service.manager.(configController)
		if !ok {
			return nil, core.WrapError(core.ErrorCodeMethodNotFound, "configuration control is unavailable", false, nil)
		}
		var patchParams ConfigPatchParams
		if err := decodeParams(params, &patchParams); err != nil {
			return nil, err
		}
		if len(patchParams.Patch) == 0 {
			return nil, core.WrapError(core.ErrorCodeInvalidRequest, "config patch is required", false, nil)
		}
		return controller.PatchConfig(ctx, patchParams.Patch)
	case MethodConfigApply:
		controller, ok := service.manager.(configController)
		if !ok {
			return nil, core.WrapError(core.ErrorCodeMethodNotFound, "configuration control is unavailable", false, nil)
		}
		var applyParams ConfigApplyParams
		if err := decodeParams(params, &applyParams); err != nil {
			return nil, err
		}
		return controller.ApplyConfig(ctx, applyParams.Mode)
	case MethodConfigReload:
		controller, ok := service.manager.(configController)
		if !ok {
			return nil, core.WrapError(core.ErrorCodeMethodNotFound, "configuration control is unavailable", false, nil)
		}
		return controller.ReloadConfig(ctx)
	default:
		return nil, core.WrapError(core.ErrorCodeMethodNotFound, fmt.Sprintf("method %q is not supported", method), false, nil)
	}
}

func (service *Service) Subscribe(ctx context.Context) <-chan core.Event {
	events := make(chan core.Event, serviceEventBuffer)
	service.eventMu.Lock()
	service.nextEventID++
	id := service.nextEventID
	service.subscribers[id] = events
	for _, event := range service.pendingAuth {
		events <- event
	}
	service.eventMu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			service.removeSubscriber(id)
		case <-service.eventDone:
		}
	}()
	return events
}

func (service *Service) Close(ctx context.Context) error {
	service.closeOnce.Do(func() {
		service.closeErr = service.manager.Close(ctx)
		service.cancelEvents()
		<-service.eventDone
	})
	return service.closeErr
}

func (service *Service) forwardEvents() {
	defer close(service.eventDone)
	managerEvents := service.manager.Events()
	for {
		select {
		case <-service.eventCtx.Done():
			service.drainManagerEvents(managerEvents)
			service.closeSubscribers()
			return
		case event, ok := <-managerEvents:
			if !ok {
				service.closeSubscribers()
				return
			}
			service.broadcast(event)
		}
	}
}

func (service *Service) drainManagerEvents(events <-chan core.Event) {
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			service.broadcast(event)
		default:
			return
		}
	}
}

func (service *Service) broadcast(event core.Event) {
	service.eventMu.Lock()
	defer service.eventMu.Unlock()
	service.updatePendingAuth(event)
	for id, subscriber := range service.subscribers {
		select {
		case subscriber <- event:
		default:
			close(subscriber)
			delete(service.subscribers, id)
		}
	}
}

func (service *Service) updatePendingAuth(event core.Event) {
	switch event.Type {
	case core.EventTypeAuthRequired, core.EventTypeAuthBrowserRequired:
		if event.Auth != nil && event.Auth.ID != "" {
			service.pendingAuth[event.Auth.ID] = event
		}
	case core.EventTypeAuthCompleted:
		if event.Auth != nil && event.Auth.ID != "" {
			delete(service.pendingAuth, event.Auth.ID)
			return
		}
		service.clearPendingAuthForSession(event.SessionID)
	case core.EventTypeSessionError, core.EventTypeShutdownCompleted:
		service.clearPendingAuthForSession(event.SessionID)
	case core.EventTypeSessionStateChanged:
		if event.State == core.SessionStateFailed || event.State == core.SessionStateStopping || event.State == core.SessionStateStopped {
			service.clearPendingAuthForSession(event.SessionID)
		}
	}
}

func (service *Service) clearPendingAuthForSession(sessionID core.SessionID) {
	for id, event := range service.pendingAuth {
		if event.SessionID == sessionID {
			delete(service.pendingAuth, id)
		}
	}
}

func (service *Service) removeSubscriber(id uint64) {
	service.eventMu.Lock()
	defer service.eventMu.Unlock()
	if subscriber := service.subscribers[id]; subscriber != nil {
		close(subscriber)
		delete(service.subscribers, id)
	}
}

func (service *Service) closeSubscribers() {
	service.eventMu.Lock()
	defer service.eventMu.Unlock()
	for id, subscriber := range service.subscribers {
		close(subscriber)
		delete(service.subscribers, id)
	}
}

func decodeParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return core.WrapError(core.ErrorCodeInvalidRequest, "decode request params", false, err)
	}
	return nil
}

func asCoreError(err error) *core.Error {
	var coreError *core.Error
	if errors.As(err, &coreError) {
		return coreError
	}
	return core.WrapError(core.ErrorCodeUnknown, err.Error(), false, err)
}
