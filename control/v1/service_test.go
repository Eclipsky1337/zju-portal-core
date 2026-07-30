package v1

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/daemonconfig"
)

func TestServiceSharesControlMethodSemantics(t *testing.T) {
	manager := newManagerStub()
	service := NewService(manager, "test-core", []string{"atrust"})

	result, err := service.Call(context.Background(), MethodHello, json.RawMessage(`{"protocol_version":2}`))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	hello := result.(HelloResult)
	if hello.CoreVersion != "test-core" || hello.ProtocolVersion != ProtocolVersion || len(hello.Capabilities) != 1 {
		t.Fatalf("hello result = %#v", hello)
	}

	_, err = service.Call(context.Background(), "missing.method", nil)
	if core.ErrorCodeOf(err) != core.ErrorCodeMethodNotFound {
		t.Fatalf("missing method error = %v", err)
	}

	result, err = service.Call(context.Background(), MethodResumeStateGet, json.RawMessage(`{"session_id":"default"}`))
	if err != nil {
		t.Fatalf("resume_state.get error = %v", err)
	}
	resumeState := result.(core.ResumeState)
	if resumeState.Revision != 1 || resumeState.Data != "state" {
		t.Fatalf("resume state = %#v", resumeState)
	}

	result, err = service.Call(context.Background(), MethodRoutingModeSet, json.RawMessage(`{"session_id":"default","mode":"global"}`))
	if err != nil {
		t.Fatalf("routing.mode.set error = %v", err)
	}
	if mode := result.(RoutingModeResult).Mode; mode != core.RoutingModeGlobal {
		t.Fatalf("routing set result = %q", mode)
	}
	result, err = service.Call(context.Background(), MethodRoutingModeGet, json.RawMessage(`{"session_id":"default"}`))
	if err != nil {
		t.Fatalf("routing.mode.get error = %v", err)
	}
	if mode := result.(RoutingModeResult).Mode; mode != core.RoutingModeGlobal {
		t.Fatalf("routing get result = %q", mode)
	}

	manager.resources["default"] = core.Resources{ClientIP: "10.0.0.2"}
	result, err = service.Call(context.Background(), MethodResourcesRefresh, json.RawMessage(`{"session_id":"default"}`))
	if err != nil {
		t.Fatalf("resources.refresh error = %v", err)
	}
	if resources := result.(core.Resources); resources.ClientIP != "10.0.1.2" {
		t.Fatalf("refreshed resources = %#v", resources)
	}
}

func TestServiceBroadcastsEventsToSubscribers(t *testing.T) {
	manager := newManagerStub()
	service := NewService(manager, "test", nil)
	defer service.Close(context.Background())

	first := service.Subscribe(context.Background())
	second := service.Subscribe(context.Background())
	want := core.NewStateChangedEvent("default", core.SessionStateIdle, core.SessionStateDiscoveringAuth, time.Unix(1, 0))
	manager.events <- want

	for index, events := range []<-chan core.Event{first, second} {
		select {
		case got := <-events:
			if got.Type != want.Type || got.State != want.State {
				t.Fatalf("subscriber %d event = %#v", index, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d did not receive event", index)
		}
	}
}

func TestServiceReplaysPendingAuthToLateSubscribers(t *testing.T) {
	manager := newManagerStub()
	service := NewService(manager, "test", nil)
	defer service.Close(context.Background())

	challenge := core.AuthChallenge{ID: "graph-1", Kind: core.AuthChallengeGraphClick, Prompt: "captcha", Image: []byte("image")}
	manager.events <- core.Event{
		SessionID: "default",
		Type:      core.EventTypeAuthRequired,
		Timestamp: time.Unix(1, 0),
		Auth:      &challenge,
	}
	waitPendingAuthCount(t, service, 1)

	late := service.Subscribe(context.Background())
	select {
	case event := <-late:
		if event.Type != core.EventTypeAuthRequired || event.Auth == nil || event.Auth.ID != challenge.ID {
			t.Fatalf("replayed event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("late subscriber did not receive pending authentication challenge")
	}

	manager.events <- core.Event{
		SessionID: "default",
		Type:      core.EventTypeAuthCompleted,
		Timestamp: time.Unix(2, 0),
		Auth:      &challenge,
	}
	waitPendingAuthCount(t, service, 0)

	afterCompletion := service.Subscribe(context.Background())
	select {
	case event := <-afterCompletion:
		t.Fatalf("completed challenge was replayed: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func waitPendingAuthCount(t *testing.T, service *Service, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		service.eventMu.Lock()
		count := len(service.pendingAuth)
		service.eventMu.Unlock()
		if count == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	service.eventMu.Lock()
	count := len(service.pendingAuth)
	service.eventMu.Unlock()
	t.Fatalf("pending authentication challenges = %d, want %d", count, want)
}

func TestServicePassesProvidedResumeStateToSessionController(t *testing.T) {
	manager := newManagerStub()
	service := NewService(manager, "test", nil)
	defer service.Close(context.Background())
	manager.responses <- core.AuthResponse{ChallengeID: "sms-1", Value: "123456"}

	result, err := service.Call(context.Background(), MethodSessionStart, json.RawMessage(`{
			"session_id":"default","resume":"provided",
			"resume_state":{"format":"atrust-client-data","version":1,"revision":4,"scope":{"server_address":"vpn.example.edu","server_port":443},"updated_at":"2026-07-28T00:00:00Z","data":"state","reused":false}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	startResult := result.(SessionStartResult)
	if startResult.ResumeStateRevision != 1 {
		t.Fatalf("start result = %#v", startResult)
	}
	manager.mu.Lock()
	resumeState := manager.startConfig.ResumeState
	manager.mu.Unlock()
	if resumeState == nil || resumeState.Revision != 4 || resumeState.Scope.ServerAddress != "vpn.example.edu" {
		t.Fatalf("resume state = %#v", resumeState)
	}
}

func TestServiceControlsDaemonConfiguration(t *testing.T) {
	manager := &configManagerStub{managerStub: newManagerStub(), config: daemonconfig.Default()}
	manager.config.Session.AutoStart = false
	manager.config.ATrust.Password = "secret"
	service := NewService(manager, "test", nil)
	defer service.Close(context.Background())

	result, err := service.Call(context.Background(), MethodConfigGet, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(daemonconfig.Snapshot).Configured.ATrust.Password; got != "secret" {
		t.Fatalf("password = %q", got)
	}

	result, err = service.Call(context.Background(), MethodConfigSet, json.RawMessage(`{"config":{"version":1,"session":{"auto-start":false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(daemonconfig.Snapshot).Configured.ATrust.Port; got != 443 {
		t.Fatalf("default port = %d", got)
	}
	if manager.setCalls != 1 {
		t.Fatalf("set calls = %d", manager.setCalls)
	}

	if _, err := service.Call(context.Background(), MethodConfigPatch, json.RawMessage(`{"patch":{"session":{"auto-reconnect":false}}}`)); err != nil {
		t.Fatal(err)
	}
	if manager.patchCalls != 1 {
		t.Fatalf("patch calls = %d", manager.patchCalls)
	}
	if _, err := service.Call(context.Background(), MethodConfigApply, json.RawMessage(`{"mode":"restart-session"}`)); err != nil {
		t.Fatal(err)
	}
	if manager.applyCalls != 1 {
		t.Fatalf("apply calls = %d", manager.applyCalls)
	}

	if _, err := service.Call(context.Background(), MethodConfigReload, nil); err != nil {
		t.Fatal(err)
	}
	if manager.reloadCalls != 1 {
		t.Fatalf("reload calls = %d", manager.reloadCalls)
	}
}

type configManagerStub struct {
	*managerStub
	config      daemonconfig.Config
	setCalls    int
	patchCalls  int
	applyCalls  int
	reloadCalls int
}

func (manager *configManagerStub) ConfigSnapshot() daemonconfig.Snapshot {
	return daemonconfig.Snapshot{Revision: 1, Configured: manager.config.Clone(), Active: manager.config.Clone(), ActiveRevision: 1}
}
func (manager *configManagerStub) SetConfig(_ context.Context, config daemonconfig.Config) (daemonconfig.Snapshot, error) {
	manager.config = config
	manager.setCalls++
	return manager.ConfigSnapshot(), nil
}
func (manager *configManagerStub) PatchConfig(_ context.Context, patch []byte) (daemonconfig.Snapshot, error) {
	config, err := daemonconfig.MergeJSON(manager.config, patch)
	if err != nil {
		return daemonconfig.Snapshot{}, err
	}
	manager.config = config
	manager.patchCalls++
	return manager.ConfigSnapshot(), nil
}
func (manager *configManagerStub) ApplyConfig(context.Context, daemonconfig.ApplyMode) (daemonconfig.Snapshot, error) {
	manager.applyCalls++
	return manager.ConfigSnapshot(), nil
}
func (manager *configManagerStub) ReloadConfig(context.Context) (daemonconfig.Snapshot, error) {
	manager.reloadCalls++
	return manager.ConfigSnapshot(), nil
}
