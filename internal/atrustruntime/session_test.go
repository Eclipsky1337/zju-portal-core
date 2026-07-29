package atrustruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clientpkg "github.com/Eclipsky1337/zju-portal-core/client"
	atrustclient "github.com/Eclipsky1337/zju-portal-core/client/atrust"
	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/internal/networkruntime"
)

func TestManagedSessionPublishesLifecycleAndClosesIdempotently(t *testing.T) {
	session := newSession("session-1", Config{}, successfulSessionDependencies())
	if err := session.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if status := session.Status(); status.State != core.SessionStateReady {
		t.Fatalf("status = %#v", status)
	}
	if session.Client() == nil {
		t.Fatal("Client() = nil after successful start")
	}

	wantStates := []core.SessionState{
		core.SessionStateDiscoveringAuth,
		core.SessionStateAuthenticating,
		core.SessionStateFetchingResources,
		core.SessionStateSelectingNodes,
		core.SessionStateEstablishingTunnel,
		core.SessionStateReady,
	}
	for _, want := range wantStates {
		event := <-session.Events()
		if event.Type != core.EventTypeSessionStateChanged || event.State != want {
			t.Fatalf("event = %#v, want state %q", event, want)
		}
	}

	firstReport, firstErr := session.Close(context.Background())
	secondReport, secondErr := session.Close(context.Background())
	if firstErr != nil || secondErr != nil {
		t.Fatalf("Close() errors = %v, %v", firstErr, secondErr)
	}
	if !reflect.DeepEqual(firstReport, secondReport) {
		t.Fatalf("idempotent reports differ: %#v != %#v", firstReport, secondReport)
	}
	if firstReport.HasErrors() {
		t.Fatalf("cleanup report = %#v", firstReport)
	}
	if status := session.Status(); status.State != core.SessionStateStopped {
		t.Fatalf("status after close = %#v", status)
	}
}

func TestManagedSessionPublishesRuntimeSnapshotEvents(t *testing.T) {
	deps := successfulSessionDependencies()
	baseSetup := deps.setup
	var nodeHandler func(map[string]string)
	deps.setup = func(ctx context.Context, client *atrustclient.Client, config Config, clientData, resourceData []byte, stageHandler func(atrustclient.SetupStage)) ([]byte, error) {
		nodeHandler = config.NodeSelectionHandler
		result, err := baseSetup(ctx, client, config, clientData, resourceData, stageHandler)
		if err == nil {
			config.NodeSelectionHandler(map[string]string{"group-1": "192.0.2.1:443"})
		}
		return result, err
	}
	deps.readResources = func(clientpkg.Client) (core.Resources, error) {
		return core.Resources{
			ClientIP:        "10.0.0.2",
			IPResources:     []core.IPResource{{IPMin: "10.0.0.1"}},
			DomainResources: map[string]core.DomainResource{"example.edu": {}},
			DNSRecords:      map[string]string{"app.example.edu": "10.0.0.8"},
		}, nil
	}
	outbound := newHealthOutboundStub()
	outbound.services = []core.ServiceStatus{{Type: core.ServiceTypeHTTP, Address: "127.0.0.1:1081", Running: true}}
	deps.setupNetwork = func(context.Context, clientpkg.Client, Config) (core.Outbound, error) { return outbound, nil }
	session := newSession("session-runtime-events", Config{SetupNetwork: true}, deps)
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	events := collectEventsUntil(t, session.Events(), core.EventTypeServiceStarted)
	resourceEvent := findEvent(events, core.EventTypeResourcesUpdated)
	if resourceEvent == nil || resourceEvent.Resources == nil || resourceEvent.Resources.ClientIP != "10.0.0.2" || resourceEvent.Resources.IPResourceCount != 1 {
		t.Fatalf("resources.updated event = %#v", resourceEvent)
	}
	nodeEvent := findEvent(events, core.EventTypeNodeSelected)
	if nodeEvent == nil || nodeEvent.SelectedNodes["group-1"] != "192.0.2.1:443" {
		t.Fatalf("node.selected event = %#v", nodeEvent)
	}
	serviceEvent := findEvent(events, core.EventTypeServiceStarted)
	if serviceEvent == nil || serviceEvent.Service == nil || serviceEvent.Service.Type != core.ServiceTypeHTTP || !serviceEvent.Service.Running {
		t.Fatalf("service.started event = %#v", serviceEvent)
	}

	nodeHandler(map[string]string{"group-1": "192.0.2.2:443"})
	updated := collectEventsUntil(t, session.Events(), core.EventTypeNodeSelected)
	if event := updated[len(updated)-1]; event.SelectedNodes["group-1"] != "192.0.2.2:443" {
		t.Fatalf("updated node.selected event = %#v", event)
	}
	outbound.serviceEvents <- core.ServiceStatus{Type: core.ServiceTypeHTTP, Address: "127.0.0.1:1081", LastError: "serve failed"}
	stopped := collectEventsUntil(t, session.Events(), core.EventTypeServiceStopped)
	if event := stopped[len(stopped)-1]; event.Service == nil || event.Service.Running || event.Service.LastError != "serve failed" {
		t.Fatalf("unexpected service.stopped event = %#v", event)
	}
	if _, err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed := collectEventsUntil(t, session.Events(), core.EventTypeShutdownCompleted)
	stoppedEvent := findEvent(closed, core.EventTypeServiceStopped)
	if stoppedEvent == nil || stoppedEvent.Service == nil || stoppedEvent.Service.Running {
		t.Fatalf("service.stopped event = %#v", stoppedEvent)
	}
}

func TestManagedSessionInvalidatesRejectedResumeState(t *testing.T) {
	clientData := []byte(`{"device_id":"device-1"}`)
	state := core.ResumeState{
		Format:   core.ResumeStateFormatATrustClientData,
		Version:  core.ResumeStateVersion1,
		Revision: 4,
		Scope:    core.ResumeStateScope{ServerAddress: "vpn.example.edu", ServerPort: 443, Username: "user"},
		Data:     base64.StdEncoding.EncodeToString(clientData),
	}
	deps := successfulSessionDependencies()
	deps.setup = func(_ context.Context, client *atrustclient.Client, _ Config, gotClientData, _ []byte, stageHandler func(atrustclient.SetupStage)) ([]byte, error) {
		if !bytes.Equal(gotClientData, clientData) {
			t.Fatalf("client data = %s", gotClientData)
		}
		client.ResumeStateReused = false
		if stageHandler != nil {
			for _, stage := range []atrustclient.SetupStage{atrustclient.SetupStageDiscoveringAuth, atrustclient.SetupStageAuthenticating, atrustclient.SetupStageFetchingResources, atrustclient.SetupStageSelectingNodes, atrustclient.SetupStageEstablishingTunnel} {
				stageHandler(stage)
			}
		}
		return clientData, nil
	}
	session := newSession("session-resume-invalid", Config{ResumeState: &state, ServerAddress: "vpn.example.edu", ServerPort: 443, Username: "user"}, deps)
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := collectEventsUntil(t, session.Events(), core.EventTypeResumeStateUpdated)
	invalidated := findEvent(events, core.EventTypeResumeStateInvalidated)
	if invalidated == nil || invalidated.ResumeStateRevision != 4 {
		t.Fatalf("resume invalidated event = %#v", invalidated)
	}
	updated := findEvent(events, core.EventTypeResumeStateUpdated)
	if updated == nil || updated.ResumeStateRevision != 5 || updated.ResumeStateReused {
		t.Fatalf("resume updated event = %#v", updated)
	}
	if _, err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSessionExposesOutboundWhileRuntimeIsAvailable(t *testing.T) {
	deps := successfulSessionDependencies()
	outbound := &outboundStub{}
	deps.setupNetwork = func(context.Context, clientpkg.Client, Config) (core.Outbound, error) {
		return outbound, nil
	}
	session := newSession("session-outbound", Config{SetupNetwork: true}, deps)

	if _, err := session.Outbound(); core.ErrorCodeOf(err) != core.ErrorCodeOutboundUnavailable {
		t.Fatalf("Outbound() before start error = %v", err)
	}
	if err := session.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	got, err := session.Outbound()
	if err != nil {
		t.Fatalf("Outbound() error = %v", err)
	}
	if got != core.Outbound(outbound) {
		t.Fatalf("Outbound() = %#v, want %#v", got, outbound)
	}
	if _, err := session.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := session.Outbound(); core.ErrorCodeOf(err) != core.ErrorCodeOutboundUnavailable {
		t.Fatalf("Outbound() after close error = %v", err)
	}
}

func TestManagedSessionPreservesRuntimeQueriesWhileReconnecting(t *testing.T) {
	deps := successfulSessionDependencies()
	var resourceReads atomic.Int32
	deps.readResources = func(clientpkg.Client) (core.Resources, error) {
		if resourceReads.Add(1) == 1 {
			return core.Resources{
				ClientIP:        "10.0.0.2",
				IPResources:     []core.IPResource{{IPMin: "10.0.0.1", IPMax: "10.0.0.255"}},
				DomainResources: map[string]core.DomainResource{"example.edu": {Protocol: "tcp"}},
				DNSRecords:      map[string]string{"app.example.edu": "10.0.0.8"},
			}, nil
		}
		return core.Resources{
			ClientIP:        "10.0.1.2",
			IPResources:     []core.IPResource{},
			DomainResources: map[string]core.DomainResource{},
			DNSRecords:      map[string]string{},
		}, nil
	}
	outbound := newHealthOutboundStub()
	outbound.stats = core.TrafficStats{UploadedBytes: 10, DownloadedBytes: 20}
	outbound.services = []core.ServiceStatus{{Type: core.ServiceTypeSOCKS5, Address: "127.0.0.1:1080", Running: true}}
	outbound.connections = []core.ConnectionInfo{{ID: "connection-1", Network: "tcp", Destination: "example.edu:443"}}
	outbound.transportConnections = []core.TransportConnectionInfo{{ID: "transport-1", Network: "tcp", Destination: "10.0.0.1:443"}}
	var networkCalls atomic.Int32
	deps.setupNetwork = func(ctx context.Context, client clientpkg.Client, config Config) (core.Outbound, error) {
		if networkCalls.Add(1) == 1 {
			return outbound, nil
		}
		if config.NetworkRuntime != outbound {
			t.Fatalf("reconnect network runtime = %#v", config.NetworkRuntime)
		}
		if err := outbound.ReplaceVPN(ctx, client, networkruntime.Config{}); err != nil {
			return nil, err
		}
		return outbound, nil
	}
	reconnect := make(chan struct{})
	deps.wait = func(ctx context.Context, _ time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-reconnect:
			return nil
		}
	}
	session := newSession("session-query-reconnect", Config{SetupNetwork: true}, deps)
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	outbound.fail(errors.New("network stack stopped"))
	collectEventsUntil(t, session.Events(), core.EventTypeReconnectScheduled)
	if state := session.Status().State; state != core.SessionStateReconnecting {
		t.Fatalf("state = %q", state)
	}
	if got, err := session.Outbound(); err != nil || got != core.Outbound(outbound) {
		t.Fatalf("Outbound() = %#v, %v", got, err)
	}
	if services, err := session.Services(); err != nil || !reflect.DeepEqual(services, outbound.services) {
		t.Fatalf("Services() = %#v, %v", services, err)
	}
	if stats, err := session.TrafficStats(); err != nil || stats.SessionID != session.id || stats.UploadedBytes != 10 || stats.DownloadedBytes != 20 {
		t.Fatalf("TrafficStats() = %#v, %v", stats, err)
	}
	if connections, err := session.Connections(); err != nil || len(connections) != 1 || connections[0].SessionID != session.id {
		t.Fatalf("Connections() = %#v, %v", connections, err)
	}
	if connections, err := session.TransportConnections(); err != nil || len(connections) != 1 || connections[0].SessionID != session.id {
		t.Fatalf("TransportConnections() = %#v, %v", connections, err)
	}
	if err := session.CloseConnection("connection-1"); err != nil || outbound.closedConnection != "connection-1" {
		t.Fatalf("CloseConnection() = %v, closed = %q", err, outbound.closedConnection)
	}
	resources, err := session.Resources()
	if err != nil || !resources.Stale {
		t.Fatalf("Resources() = %#v, %v", resources, err)
	}
	resources.IPResources[0].IPMin = "mutated"
	resources.DomainResources["example.edu"] = core.DomainResource{Protocol: "udp"}
	resources.DNSRecords["app.example.edu"] = "mutated"
	resources, err = session.Resources()
	if err != nil || resources.IPResources[0].IPMin != "10.0.0.1" || resources.DomainResources["example.edu"].Protocol != "tcp" || resources.DNSRecords["app.example.edu"] != "10.0.0.8" {
		t.Fatalf("Resources() returned mutable cache: %#v, %v", resources, err)
	}

	close(reconnect)
	collectEventsUntil(t, session.Events(), core.EventTypeReconnected)
	resources, err = session.Resources()
	if err != nil || resources.Stale || resources.ClientIP != "10.0.1.2" {
		t.Fatalf("Resources() after reconnect = %#v, %v", resources, err)
	}
	if _, err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Services(); core.ErrorCodeOf(err) != core.ErrorCodeOutboundUnavailable {
		t.Fatalf("Services() after close error = %v", err)
	}
	if _, err := session.Resources(); core.ErrorCodeOf(err) != core.ErrorCodeResourcesUnavailable {
		t.Fatalf("Resources() after close error = %v", err)
	}
}

func TestInstallReconnectedRuntimePreservesResourcesWhenRefreshFails(t *testing.T) {
	deps := defaultDependencies()
	deps.readResources = func(clientpkg.Client) (core.Resources, error) {
		return core.Resources{}, errors.New("resource refresh failed")
	}
	oldOutbound := newHealthOutboundStub()
	newOutbound := newHealthOutboundStub()
	session := newSession("session-stale-resources", Config{}, deps)
	session.state = core.SessionStateReconnecting
	session.network = wrapNetwork(oldOutbound)
	session.resources = core.Resources{
		ClientIP:        "10.0.0.2",
		IPResources:     []core.IPResource{{IPMin: "10.0.0.1", IPMax: "10.0.0.255"}},
		DomainResources: map[string]core.DomainResource{"example.edu": {Protocol: "tcp"}},
		DNSRecords:      map[string]string{"app.example.edu": "10.0.0.8"},
	}
	candidate := &Runtime{client: &atrustclient.Client{}, outbound: wrapNetwork(newOutbound), ownsOutbound: true}

	if !session.installReconnectedRuntime(context.Background(), candidate) {
		t.Fatal("installReconnectedRuntime() = false")
	}
	resources, err := session.Resources()
	if err != nil {
		t.Fatal(err)
	}
	if !resources.Stale || resources.ClientIP != "10.0.0.2" || len(resources.IPResources) != 1 {
		t.Fatalf("Resources() = %#v", resources)
	}
}

func TestManagedSessionRefreshesResourcesWithoutReplacingStableRuntime(t *testing.T) {
	firstClientData := []byte(`{"device_id":"device-1"}`)
	secondClientData := []byte(`{"device_id":"device-2"}`)
	deps := defaultDependencies()
	var setupCalls atomic.Int32
	deps.setup = func(_ context.Context, _ *atrustclient.Client, _ Config, clientData, _ []byte, stageHandler func(atrustclient.SetupStage)) ([]byte, error) {
		call := setupCalls.Add(1)
		if call == 2 && !bytes.Equal(clientData, firstClientData) {
			t.Fatalf("refresh client data = %s", clientData)
		}
		if stageHandler != nil {
			for _, stage := range []atrustclient.SetupStage{atrustclient.SetupStageDiscoveringAuth, atrustclient.SetupStageAuthenticating, atrustclient.SetupStageFetchingResources, atrustclient.SetupStageSelectingNodes, atrustclient.SetupStageEstablishingTunnel} {
				stageHandler(stage)
			}
		}
		if call == 1 {
			return firstClientData, nil
		}
		return secondClientData, nil
	}
	var resourceReads atomic.Int32
	deps.readResources = func(clientpkg.Client) (core.Resources, error) {
		if resourceReads.Add(1) == 1 {
			return core.Resources{ClientIP: "10.0.0.2", IPResources: []core.IPResource{}, DomainResources: map[string]core.DomainResource{}, DNSRecords: map[string]string{}}, nil
		}
		return core.Resources{ClientIP: "10.0.1.2", IPResources: []core.IPResource{}, DomainResources: map[string]core.DomainResource{}, DNSRecords: map[string]string{}}, nil
	}
	outbound := newHealthOutboundStub()
	var networkCalls atomic.Int32
	deps.setupNetwork = func(ctx context.Context, client clientpkg.Client, config Config) (core.Outbound, error) {
		if networkCalls.Add(1) == 1 {
			return outbound, nil
		}
		if config.NetworkRuntime != outbound {
			t.Fatalf("refresh network runtime = %#v", config.NetworkRuntime)
		}
		if err := outbound.ReplaceVPN(ctx, client, networkruntime.Config{}); err != nil {
			return nil, err
		}
		return outbound, nil
	}
	session := newSession("session-refresh", Config{ServerAddress: "vpn.example.edu", ServerPort: 443, Username: "user", SetupNetwork: true}, deps)
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	initialRuntime := session.runtime
	collectEventsUntil(t, session.Events(), core.EventTypeResumeStateUpdated)
	if err := session.RefreshResources(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := collectEventsUntil(t, session.Events(), core.EventTypeResumeStateUpdated)
	assertEventTypePresent(t, events, core.EventTypeResourcesUpdated)
	resources, err := session.Resources()
	if err != nil || resources.ClientIP != "10.0.1.2" || resources.Stale {
		t.Fatalf("Resources() = %#v, %v", resources, err)
	}
	if session.runtime != initialRuntime || setupCalls.Load() != 2 || networkCalls.Load() != 2 {
		t.Fatalf("refresh replaced runtime or missed setup: runtime=%p/%p setup=%d network=%d", session.runtime, initialRuntime, setupCalls.Load(), networkCalls.Load())
	}
	if _, err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSessionRefreshFailurePreservesResourcesAndNetwork(t *testing.T) {
	wantErr := errors.New("resource refresh login failed")
	deps := successfulSessionDependencies()
	baseSetup := deps.setup
	var setupCalls atomic.Int32
	deps.setup = func(ctx context.Context, client *atrustclient.Client, config Config, clientData, resourceData []byte, stageHandler func(atrustclient.SetupStage)) ([]byte, error) {
		if setupCalls.Add(1) == 2 {
			return nil, wantErr
		}
		return baseSetup(ctx, client, config, clientData, resourceData, stageHandler)
	}
	deps.readResources = func(clientpkg.Client) (core.Resources, error) {
		return core.Resources{ClientIP: "10.0.0.2", IPResources: []core.IPResource{}, DomainResources: map[string]core.DomainResource{}, DNSRecords: map[string]string{}}, nil
	}
	outbound := newHealthOutboundStub()
	var networkCalls atomic.Int32
	deps.setupNetwork = func(context.Context, clientpkg.Client, Config) (core.Outbound, error) {
		networkCalls.Add(1)
		return outbound, nil
	}
	session := newSession("session-refresh-failure", Config{SetupNetwork: true}, deps)
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	initialRuntime := session.runtime
	if err := session.RefreshResources(context.Background()); !errors.Is(err, wantErr) || core.ErrorCodeOf(err) != core.ErrorCodeResourcesUnavailable {
		t.Fatalf("RefreshResources() error = %v", err)
	}
	resources, err := session.Resources()
	if err != nil || resources.ClientIP != "10.0.0.2" {
		t.Fatalf("Resources() after failed refresh = %#v, %v", resources, err)
	}
	if session.runtime != initialRuntime || networkCalls.Load() != 1 {
		t.Fatalf("failed refresh changed runtime: runtime=%p/%p network=%d", session.runtime, initialRuntime, networkCalls.Load())
	}
	if _, err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSessionRefreshResourceReadFailurePreservesResourcesAndNetwork(t *testing.T) {
	wantErr := errors.New("resource snapshot failed")
	deps := successfulSessionDependencies()
	var resourceReads atomic.Int32
	deps.readResources = func(clientpkg.Client) (core.Resources, error) {
		if resourceReads.Add(1) == 1 {
			return core.Resources{ClientIP: "10.0.0.2", IPResources: []core.IPResource{}, DomainResources: map[string]core.DomainResource{}, DNSRecords: map[string]string{}}, nil
		}
		return core.Resources{}, wantErr
	}
	outbound := newHealthOutboundStub()
	var networkCalls atomic.Int32
	deps.setupNetwork = func(context.Context, clientpkg.Client, Config) (core.Outbound, error) {
		networkCalls.Add(1)
		return outbound, nil
	}
	session := newSession("session-refresh-read-failure", Config{SetupNetwork: true}, deps)
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	initialRuntime := session.runtime
	initialClient := initialRuntime.Client()
	if err := session.RefreshResources(context.Background()); !errors.Is(err, wantErr) || core.ErrorCodeOf(err) != core.ErrorCodeResourcesUnavailable {
		t.Fatalf("RefreshResources() error = %v", err)
	}
	resources, err := session.Resources()
	if err != nil || resources.ClientIP != "10.0.0.2" {
		t.Fatalf("Resources() after failed refresh = %#v, %v", resources, err)
	}
	if session.runtime != initialRuntime || initialRuntime.Client() != initialClient || networkCalls.Load() != 1 {
		t.Fatalf("failed refresh changed runtime: runtime=%p/%p client=%p/%p network=%d", session.runtime, initialRuntime, initialRuntime.Client(), initialClient, networkCalls.Load())
	}
	if _, err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSessionRefreshNetworkFailurePreservesResourcesAndClient(t *testing.T) {
	wantErr := errors.New("VPN backend replacement failed")
	deps := successfulSessionDependencies()
	var resourceReads atomic.Int32
	deps.readResources = func(clientpkg.Client) (core.Resources, error) {
		if resourceReads.Add(1) == 1 {
			return core.Resources{ClientIP: "10.0.0.2", IPResources: []core.IPResource{}, DomainResources: map[string]core.DomainResource{}, DNSRecords: map[string]string{}}, nil
		}
		return core.Resources{ClientIP: "10.0.1.2", IPResources: []core.IPResource{}, DomainResources: map[string]core.DomainResource{}, DNSRecords: map[string]string{}}, nil
	}
	outbound := newHealthOutboundStub()
	outbound.replaceErr = wantErr
	deps.setupNetwork = func(ctx context.Context, client clientpkg.Client, config Config) (core.Outbound, error) {
		if config.NetworkRuntime == nil {
			return outbound, nil
		}
		if err := config.NetworkRuntime.ReplaceVPN(ctx, client, networkruntime.Config{}); err != nil {
			return nil, err
		}
		return config.NetworkRuntime, nil
	}
	session := newSession("session-refresh-network-failure", Config{SetupNetwork: true}, deps)
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	initialRuntime := session.runtime
	initialClient := initialRuntime.Client()
	if err := session.RefreshResources(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("RefreshResources() error = %v", err)
	}
	resources, err := session.Resources()
	if err != nil || resources.ClientIP != "10.0.0.2" {
		t.Fatalf("Resources() after failed refresh = %#v, %v", resources, err)
	}
	if session.runtime != initialRuntime || initialRuntime.Client() != initialClient || outbound.replaceCalls.Load() != 1 {
		t.Fatalf("failed refresh changed runtime: runtime=%p/%p client=%p/%p replacements=%d", session.runtime, initialRuntime, initialRuntime.Client(), initialClient, outbound.replaceCalls.Load())
	}
	if _, err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMonitorRuntimeContinuesAfterVPNReplacement(t *testing.T) {
	outbound := newHealthOutboundStub()
	runtime := &Runtime{outbound: wrapNetwork(outbound)}
	session := newSession("session-monitor-replacement", Config{DisableAutoReconnect: true}, defaultDependencies())
	session.state = core.SessionStateReady
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go session.monitorRuntime(ctx, runtime)

	if err := outbound.ReplaceVPN(context.Background(), nil, networkruntime.Config{}); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("replacement backend stopped")
	outbound.fail(wantErr)
	events := collectEventsUntil(t, session.Events(), core.EventTypeSessionError)
	event := findEvent(events, core.EventTypeSessionError)
	if event == nil || event.Error == nil || event.Error.Code != core.ErrorCodeSessionReconnectFailed {
		t.Fatalf("session error event = %#v", event)
	}
}

func TestMonitorRuntimeFailsWithoutReplacingVPNAfterTUNFailure(t *testing.T) {
	outbound := newHealthOutboundStub()
	network := wrapNetwork(outbound)
	runtime := &Runtime{outbound: network}
	session := newSession("session-monitor-tun-failure", Config{}, defaultDependencies())
	session.state = core.SessionStateReady
	session.runtime = runtime
	session.network = network
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go session.monitorRuntime(ctx, runtime)

	wantErr := errors.New("TUN read failed")
	outbound.fail(core.WrapError(core.ErrorCodeTUNUnavailable, "TUN service stopped", false, wantErr))
	events := collectEventsUntil(t, session.Events(), core.EventTypeSessionError)
	event := findEvent(events, core.EventTypeSessionError)
	if event == nil || event.Error == nil || event.Error.Code != core.ErrorCodeTUNUnavailable {
		t.Fatalf("session error event = %#v", event)
	}
	if status := session.Status(); status.State != core.SessionStateFailed || status.LastError == nil || status.LastError.Code != core.ErrorCodeTUNUnavailable {
		t.Fatalf("session status = %#v", status)
	}
	if !outbound.isClosed() {
		t.Fatal("failed TUN network runtime was not closed")
	}
	if calls := outbound.replaceCalls.Load(); calls != 0 {
		t.Fatalf("ReplaceVPN() calls = %d, want 0", calls)
	}
}

func TestManagedSessionReportsStartFailure(t *testing.T) {
	wantErr := errors.New("gateway rejected login")
	deps := defaultDependencies()
	deps.setup = func(_ context.Context, _ *atrustclient.Client, _ Config, _, _ []byte, stageHandler func(atrustclient.SetupStage)) ([]byte, error) {
		stageHandler(atrustclient.SetupStageDiscoveringAuth)
		stageHandler(atrustclient.SetupStageAuthenticating)
		return nil, wantErr
	}
	session := newSession("session-failed", Config{}, deps)

	err := session.Start(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v", err)
	}
	status := session.Status()
	if status.State != core.SessionStateFailed || status.LastError == nil {
		t.Fatalf("status = %#v", status)
	}
	if _, err := session.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestManagedSessionChangesRoutingMode(t *testing.T) {
	deps := successfulSessionDependencies()
	outbound := &routingOutboundStub{mode: core.RoutingModeRule}
	deps.setupNetwork = func(context.Context, clientpkg.Client, Config) (core.Outbound, error) {
		return outbound, nil
	}
	session := newSession("session-routing", Config{SetupNetwork: true}, deps)
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.SetRoutingMode(core.RoutingModeGlobal); err != nil {
		t.Fatal(err)
	}
	mode, err := session.RoutingMode()
	if err != nil || mode != core.RoutingModeGlobal {
		t.Fatalf("RoutingMode() = %q, %v", mode, err)
	}
	events := collectEventsUntil(t, session.Events(), core.EventTypeRoutingModeChanged)
	event := events[len(events)-1]
	if event.PreviousRoutingMode != core.RoutingModeRule || event.RoutingMode != core.RoutingModeGlobal {
		t.Fatalf("routing event = %#v", event)
	}
	if err := session.SetRoutingMode("invalid"); core.ErrorCodeOf(err) != core.ErrorCodeInvalidRequest {
		t.Fatalf("invalid mode error = %v", err)
	}
	if _, err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSessionReconnectsFailedRuntimeWithLatestResumeState(t *testing.T) {
	firstClientData := []byte(`{"device_id":"device-1"}`)
	secondClientData := []byte(`{"device_id":"device-2"}`)
	deps := defaultDependencies()
	var setupCalls atomic.Int32
	deps.setup = func(_ context.Context, _ *atrustclient.Client, _ Config, clientData, _ []byte, stageHandler func(atrustclient.SetupStage)) ([]byte, error) {
		call := setupCalls.Add(1)
		if call == 1 {
			for _, stage := range []atrustclient.SetupStage{
				atrustclient.SetupStageDiscoveringAuth,
				atrustclient.SetupStageAuthenticating,
				atrustclient.SetupStageFetchingResources,
				atrustclient.SetupStageSelectingNodes,
				atrustclient.SetupStageEstablishingTunnel,
			} {
				stageHandler(stage)
			}
			return firstClientData, nil
		}
		if !bytes.Equal(clientData, firstClientData) {
			t.Fatalf("reconnect client data = %s", clientData)
		}
		return secondClientData, nil
	}
	firstOutbound := newHealthOutboundStub()
	var networkCalls atomic.Int32
	deps.setupNetwork = func(ctx context.Context, client clientpkg.Client, config Config) (core.Outbound, error) {
		if networkCalls.Add(1) == 1 {
			return firstOutbound, nil
		}
		if config.NetworkRuntime != firstOutbound {
			t.Fatalf("reconnect network runtime = %#v", config.NetworkRuntime)
		}
		if err := firstOutbound.ReplaceVPN(ctx, client, networkruntime.Config{}); err != nil {
			return nil, err
		}
		return firstOutbound, nil
	}
	deps.wait = func(context.Context, time.Duration) error { return nil }
	session := newSession("session-reconnect", Config{
		ServerAddress: "vpn.example.edu",
		ServerPort:    443,
		Username:      "user",
		SetupNetwork:  true,
	}, deps)
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	firstOutbound.fail(errors.New("network stack stopped"))
	events := collectEventsUntil(t, session.Events(), core.EventTypeReconnected)
	if status := session.Status(); status.State != core.SessionStateReady || status.LastError != nil {
		t.Fatalf("status = %#v", status)
	}
	if setupCalls.Load() != 2 || networkCalls.Load() != 2 {
		t.Fatalf("setup calls = %d, network calls = %d", setupCalls.Load(), networkCalls.Load())
	}
	gotOutbound, err := session.Outbound()
	if err != nil || gotOutbound != core.Outbound(firstOutbound) {
		t.Fatalf("Outbound() = %#v, %v", gotOutbound, err)
	}
	assertEventTypePresent(t, events, core.EventTypeReconnectScheduled)
	assertEventTypePresent(t, events, core.EventTypeResumeStateUpdated)
	if _, err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSessionRetriesReconnectAfterFailure(t *testing.T) {
	clientData := []byte(`{"device_id":"device-1"}`)
	deps := defaultDependencies()
	var setupCalls atomic.Int32
	deps.setup = func(_ context.Context, _ *atrustclient.Client, _ Config, _ []byte, _ []byte, stageHandler func(atrustclient.SetupStage)) ([]byte, error) {
		call := setupCalls.Add(1)
		if call == 1 {
			for _, stage := range []atrustclient.SetupStage{
				atrustclient.SetupStageDiscoveringAuth,
				atrustclient.SetupStageAuthenticating,
				atrustclient.SetupStageFetchingResources,
				atrustclient.SetupStageSelectingNodes,
				atrustclient.SetupStageEstablishingTunnel,
			} {
				stageHandler(stage)
			}
			return clientData, nil
		}
		if call == 2 {
			return nil, errors.New("temporary reconnect failure")
		}
		return clientData, nil
	}
	firstOutbound := newHealthOutboundStub()
	var networkCalls atomic.Int32
	deps.setupNetwork = func(ctx context.Context, client clientpkg.Client, config Config) (core.Outbound, error) {
		if networkCalls.Add(1) == 1 {
			return firstOutbound, nil
		}
		if config.NetworkRuntime != firstOutbound {
			t.Fatalf("reconnect network runtime = %#v", config.NetworkRuntime)
		}
		if err := firstOutbound.ReplaceVPN(ctx, client, networkruntime.Config{}); err != nil {
			return nil, err
		}
		return firstOutbound, nil
	}
	var delaysMu sync.Mutex
	var delays []time.Duration
	deps.wait = func(_ context.Context, delay time.Duration) error {
		delaysMu.Lock()
		delays = append(delays, delay)
		delaysMu.Unlock()
		return nil
	}
	session := newSession("session-reconnect-retry", Config{
		ServerAddress: "vpn.example.edu",
		ServerPort:    443,
		Username:      "user",
		SetupNetwork:  true,
	}, deps)
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	firstOutbound.fail(errors.New("network stack stopped"))
	events := collectEventsUntil(t, session.Events(), core.EventTypeReconnected)
	assertEventTypePresent(t, events, core.EventTypeReconnectFailed)
	delaysMu.Lock()
	gotDelays := append([]time.Duration(nil), delays...)
	delaysMu.Unlock()
	if !reflect.DeepEqual(gotDelays, []time.Duration{0, time.Second}) {
		t.Fatalf("reconnect delays = %v", gotDelays)
	}
	if setupCalls.Load() != 3 || networkCalls.Load() != 2 {
		t.Fatalf("setup calls = %d, network calls = %d", setupCalls.Load(), networkCalls.Load())
	}
	if _, err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSessionStopsWhenParentContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	session := newSession("session-cancel", Config{}, successfulSessionDependencies())
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	cancel()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if session.Status().State == core.SessionStateStopped {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session state = %q", session.Status().State)
}

func TestManagedSessionCanFailBeforeFirstStage(t *testing.T) {
	wantErr := errors.New("resource file missing")
	deps := defaultDependencies()
	deps.readFile = func(string) ([]byte, error) { return nil, wantErr }
	session := newSession("session-early-failure", Config{ResourceFile: "resource.json"}, deps)

	if err := session.Start(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v", err)
	}
	if state := session.Status().State; state != core.SessionStateFailed {
		t.Fatalf("state = %q", state)
	}
}

func TestManagedSessionCloseIsConcurrentAndIdempotent(t *testing.T) {
	deps := successfulSessionDependencies()
	var closeCount atomic.Int32
	deps.closeClient = func(*atrustclient.Client) {
		closeCount.Add(1)
	}
	session := newSession("session-concurrent-close", Config{}, deps)
	if err := session.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	const callers = 8
	reports := make([]core.CleanupReport, callers)
	errs := make([]error, callers)
	var waitGroup sync.WaitGroup
	for index := range callers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			reports[index], errs[index] = session.Close(context.Background())
		}()
	}
	waitGroup.Wait()

	for index := range callers {
		if errs[index] != nil {
			t.Fatalf("Close() error %d = %v", index, errs[index])
		}
		if !reflect.DeepEqual(reports[0], reports[index]) {
			t.Fatalf("Close() report %d differs: %#v != %#v", index, reports[0], reports[index])
		}
	}
	if got := closeCount.Load(); got != 1 {
		t.Fatalf("runtime close count = %d, want 1", got)
	}
}

func TestManagedSessionCleansPartialStartupOnce(t *testing.T) {
	wantErr := errors.New("tunnel setup failed")
	deps := defaultDependencies()
	var closeCount atomic.Int32
	deps.closeClient = func(*atrustclient.Client) {
		closeCount.Add(1)
	}
	deps.setup = func(_ context.Context, _ *atrustclient.Client, _ Config, _, _ []byte, stageHandler func(atrustclient.SetupStage)) ([]byte, error) {
		stageHandler(atrustclient.SetupStageDiscoveringAuth)
		stageHandler(atrustclient.SetupStageAuthenticating)
		stageHandler(atrustclient.SetupStageFetchingResources)
		stageHandler(atrustclient.SetupStageSelectingNodes)
		stageHandler(atrustclient.SetupStageEstablishingTunnel)
		return nil, wantErr
	}
	session := newSession("session-partial-start", Config{}, deps)

	if err := session.Start(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := session.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := closeCount.Load(); got != 1 {
		t.Fatalf("runtime close count = %d, want 1", got)
	}
}

func TestManagedSessionReportsCleanupTimeout(t *testing.T) {
	deps := successfulSessionDependencies()
	release := make(chan struct{})
	deps.closeClient = func(*atrustclient.Client) {
		<-release
	}
	session := newSession("session-close-timeout", Config{}, deps)
	if err := session.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	report, err := session.Close(ctx)
	cancel()
	close(release)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v", err)
	}
	if code := core.ErrorCodeOf(err); code != core.ErrorCodeSessionCloseFailed {
		t.Fatalf("Close() error code = %q", code)
	}
	if !report.HasErrors() {
		t.Fatalf("cleanup report = %#v", report)
	}
	if status := session.Status(); status.State != core.SessionStateStopped {
		t.Fatalf("status after timed out close = %#v", status)
	}
}

func successfulSessionDependencies() dependencies {
	deps := defaultDependencies()
	deps.setup = func(_ context.Context, _ *atrustclient.Client, _ Config, _, _ []byte, stageHandler func(atrustclient.SetupStage)) ([]byte, error) {
		if stageHandler != nil {
			for _, stage := range []atrustclient.SetupStage{
				atrustclient.SetupStageDiscoveringAuth,
				atrustclient.SetupStageAuthenticating,
				atrustclient.SetupStageFetchingResources,
				atrustclient.SetupStageSelectingNodes,
				atrustclient.SetupStageEstablishingTunnel,
			} {
				stageHandler(stage)
			}
		}
		return nil, nil
	}
	return deps
}

func collectEventsUntil(t *testing.T, events <-chan core.Event, eventType core.EventType) []core.Event {
	t.Helper()
	var collected []core.Event
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			collected = append(collected, event)
			if event.Type == eventType {
				return collected
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %q; events = %#v", eventType, collected)
		}
	}
}

func assertEventTypePresent(t *testing.T, events []core.Event, eventType core.EventType) {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType {
			return
		}
	}
	t.Fatalf("event type %q not found in %#v", eventType, events)
}

func findEvent(events []core.Event, eventType core.EventType) *core.Event {
	for index := range events {
		if events[index].Type == eventType {
			return &events[index]
		}
	}
	return nil
}

type healthOutboundStub struct {
	mu                   sync.RWMutex
	done                 chan struct{}
	err                  error
	closed               bool
	stats                core.TrafficStats
	services             []core.ServiceStatus
	connections          []core.ConnectionInfo
	transportConnections []core.TransportConnectionInfo
	closedConnection     string
	serviceEvents        chan core.ServiceStatus
	replaceErr           error
	replaceCalls         atomic.Int32
}

type routingOutboundStub struct {
	mode core.RoutingMode
}

func (*routingOutboundStub) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("not implemented")
}

func (*routingOutboundStub) Close(context.Context) error { return nil }

func (outbound *routingOutboundStub) RoutingMode() core.RoutingMode { return outbound.mode }

func (outbound *routingOutboundStub) SetRoutingMode(mode core.RoutingMode) (core.RoutingMode, error) {
	previous := outbound.mode
	outbound.mode = mode
	return previous, nil
}

func newHealthOutboundStub() *healthOutboundStub {
	return &healthOutboundStub{done: make(chan struct{}), serviceEvents: make(chan core.ServiceStatus, 4)}
}

func (*healthOutboundStub) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("not implemented")
}

func (outbound *healthOutboundStub) Close(context.Context) error {
	outbound.mu.Lock()
	if !outbound.closed {
		close(outbound.done)
		outbound.closed = true
	}
	outbound.mu.Unlock()
	return nil
}

func (outbound *healthOutboundStub) Done() <-chan struct{} {
	outbound.mu.RLock()
	defer outbound.mu.RUnlock()
	return outbound.done
}

func (outbound *healthOutboundStub) Err() error {
	outbound.mu.RLock()
	defer outbound.mu.RUnlock()
	return outbound.err
}

func (outbound *healthOutboundStub) fail(err error) {
	outbound.mu.Lock()
	outbound.err = err
	if !outbound.closed {
		close(outbound.done)
		outbound.closed = true
	}
	outbound.mu.Unlock()
}

func (outbound *healthOutboundStub) isClosed() bool {
	outbound.mu.RLock()
	defer outbound.mu.RUnlock()
	return outbound.closed
}

func (outbound *healthOutboundStub) ReplaceVPN(context.Context, clientpkg.Client, networkruntime.Config) error {
	outbound.replaceCalls.Add(1)
	outbound.mu.Lock()
	if outbound.replaceErr != nil {
		err := outbound.replaceErr
		outbound.mu.Unlock()
		return err
	}
	if !outbound.closed {
		close(outbound.done)
	}
	outbound.done = make(chan struct{})
	outbound.err = nil
	outbound.closed = false
	outbound.mu.Unlock()
	return nil
}

func (outbound *healthOutboundStub) TrafficStats() core.TrafficStats {
	outbound.mu.RLock()
	defer outbound.mu.RUnlock()
	return outbound.stats
}

func (outbound *healthOutboundStub) Services() []core.ServiceStatus {
	outbound.mu.RLock()
	defer outbound.mu.RUnlock()
	return append([]core.ServiceStatus(nil), outbound.services...)
}

func (outbound *healthOutboundStub) Connections() []core.ConnectionInfo {
	outbound.mu.RLock()
	defer outbound.mu.RUnlock()
	return append([]core.ConnectionInfo(nil), outbound.connections...)
}

func (outbound *healthOutboundStub) TransportConnections() []core.TransportConnectionInfo {
	outbound.mu.RLock()
	defer outbound.mu.RUnlock()
	return append([]core.TransportConnectionInfo(nil), outbound.transportConnections...)
}

func (outbound *healthOutboundStub) CloseConnection(id string) error {
	outbound.mu.Lock()
	outbound.closedConnection = id
	outbound.mu.Unlock()
	return nil
}

func (outbound *healthOutboundStub) ServiceEvents() <-chan core.ServiceStatus {
	return outbound.serviceEvents
}
