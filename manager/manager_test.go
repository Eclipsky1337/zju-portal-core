package manager

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

type sessionStub struct {
	id        core.SessionID
	started   func() error
	closed    func() error
	state     core.SessionState
	events    chan core.Event
	closeOnce sync.Once
}

func newSessionStub(id core.SessionID) *sessionStub {
	return &sessionStub{id: id, state: core.SessionStateIdle, events: make(chan core.Event)}
}

func (session *sessionStub) ID() core.SessionID { return session.id }
func (session *sessionStub) Start(context.Context) error {
	if session.started != nil {
		if err := session.started(); err != nil {
			return err
		}
	}
	session.state = core.SessionStateReady
	return nil
}
func (session *sessionStub) Close(context.Context) (core.CleanupReport, error) {
	var err error
	session.closeOnce.Do(func() {
		if session.closed != nil {
			err = session.closed()
		}
		session.state = core.SessionStateStopped
		close(session.events)
	})
	return core.CleanupReport{}, err
}
func (*sessionStub) WaitClosed(context.Context) error { return nil }
func (session *sessionStub) Status() core.SessionStatus {
	return core.SessionStatus{ID: session.id, State: session.state}
}
func (*sessionStub) Resources() (core.Resources, error)                            { return core.Resources{}, nil }
func (*sessionStub) RefreshResources(context.Context) error                        { return nil }
func (*sessionStub) Outbound() (core.Outbound, error)                              { return outboundSessionStub{}, nil }
func (*sessionStub) Services() ([]core.ServiceStatus, error)                       { return nil, nil }
func (*sessionStub) TrafficStats() (core.TrafficStats, error)                      { return core.TrafficStats{}, nil }
func (*sessionStub) Connections() ([]core.ConnectionInfo, error)                   { return nil, nil }
func (*sessionStub) CloseConnection(string) error                                  { return nil }
func (*sessionStub) TransportConnections() ([]core.TransportConnectionInfo, error) { return nil, nil }
func (*sessionStub) RoutingMode() (core.RoutingMode, error)                        { return core.RoutingModeRule, nil }
func (*sessionStub) SetRoutingMode(core.RoutingMode) error                         { return nil }
func (*sessionStub) ResumeState() (core.ResumeState, error)                        { return core.ResumeState{}, nil }
func (session *sessionStub) Events() <-chan core.Event                             { return session.events }

type outboundSessionStub struct{}

func (outboundSessionStub) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("not implemented")
}
func (outboundSessionStub) Close(context.Context) error { return nil }

func TestManagerSessionEventForwardingDoesNotBlockWithoutConsumer(t *testing.T) {
	manager := New()
	session := newSessionStub("session-event-backpressure")
	session.events = make(chan core.Event, managerEventBuffer*2)
	for index := 0; index < managerEventBuffer*2-1; index++ {
		session.events <- core.Event{SessionID: session.id, Type: core.EventTypeLog}
	}
	session.events <- core.Event{SessionID: session.id, Type: core.EventTypeShutdownCompleted}

	done := make(chan struct{})
	go func() {
		manager.forwardSessionEvents(session)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session event forwarding blocked on full manager event buffer")
	}
	if len(manager.events) > managerEventBuffer-managerAuthEventReserve {
		t.Fatalf("manager telemetry used authentication reserve: %d events", len(manager.events))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.auth.emit(ctx, core.Event{SessionID: session.id, Type: core.EventTypeAuthRequired}); err != nil {
		t.Fatalf("authentication event emission failed: %v", err)
	}
}

func TestAuthBrokerPublishesChallengeAndAcceptsResponse(t *testing.T) {
	events := make(chan core.Event, 2)
	broker := newAuthBroker(events)
	handler := broker.handler("session-1")
	challenge := core.AuthChallenge{ID: "sms-1", Kind: core.AuthChallengeSMS, Prompt: "SMS code"}
	wantResponse := core.AuthResponse{ChallengeID: challenge.ID, Value: "123456"}

	result := make(chan core.AuthResponse, 1)
	errs := make(chan error, 1)
	go func() {
		response, err := handler.Handle(context.Background(), challenge)
		result <- response
		errs <- err
	}()

	required := <-events
	if required.Type != core.EventTypeAuthRequired || required.SessionID != "session-1" || !reflect.DeepEqual(required.Auth, &challenge) {
		t.Fatalf("auth event = %#v", required)
	}
	if err := broker.respond(context.Background(), wantResponse); err != nil {
		t.Fatalf("respond() error = %v", err)
	}
	if response := <-result; !reflect.DeepEqual(response, wantResponse) {
		t.Fatalf("Handle() response = %#v", response)
	}
	if err := <-errs; err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	completed := <-events
	if completed.Type != core.EventTypeAuthCompleted || completed.SessionID != "session-1" {
		t.Fatalf("completed event = %#v", completed)
	}
}

func TestAuthBrokerUsesBrowserRequiredEventForCallbacks(t *testing.T) {
	events := make(chan core.Event, 2)
	broker := newAuthBroker(events)
	handler := broker.handler("session-browser")
	challenge := core.AuthChallenge{ID: "cas-1", Kind: core.AuthChallengeCASCallback, URL: "https://example.test/login"}

	done := make(chan error, 1)
	go func() {
		_, err := handler.Handle(context.Background(), challenge)
		done <- err
	}()
	if event := <-events; event.Type != core.EventTypeAuthBrowserRequired {
		t.Fatalf("event type = %q", event.Type)
	}
	if err := broker.respond(context.Background(), core.AuthResponse{ChallengeID: challenge.ID, Value: "https://example.test/callback"}); err != nil {
		t.Fatalf("respond() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	<-events
}

func TestAuthBrokerRejectsUnknownAndInvalidResponses(t *testing.T) {
	events := make(chan core.Event, 2)
	broker := newAuthBroker(events)
	if err := broker.respond(context.Background(), core.AuthResponse{ChallengeID: "missing", Value: "123456"}); core.ErrorCodeOf(err) != core.ErrorCodeAuthChallengeNotFound {
		t.Fatalf("unknown challenge error = %v", err)
	}

	handler := broker.handler("session-1")
	challenge := core.AuthChallenge{ID: "sms-1", Kind: core.AuthChallengeSMS}
	done := make(chan error, 1)
	go func() {
		_, err := handler.Handle(context.Background(), challenge)
		done <- err
	}()
	<-events
	if err := broker.respond(context.Background(), core.AuthResponse{ChallengeID: challenge.ID}); core.ErrorCodeOf(err) != core.ErrorCodeAuthResponseInvalid {
		t.Fatalf("invalid response error = %v", err)
	}
	if err := broker.respond(context.Background(), core.AuthResponse{ChallengeID: challenge.ID, Value: "123456"}); err != nil {
		t.Fatalf("valid retry error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	<-events
}

func TestAuthBrokerRemovesCanceledChallenge(t *testing.T) {
	events := make(chan core.Event, 1)
	broker := newAuthBroker(events)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := broker.handler("session-1").Handle(ctx, core.AuthChallenge{ID: "sms-1", Kind: core.AuthChallengeSMS})
		done <- err
	}()
	<-events
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Handle() error = %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err := broker.respond(context.Background(), core.AuthResponse{ChallengeID: "sms-1", Value: "123456"})
		if core.ErrorCodeOf(err) == core.ErrorCodeAuthChallengeNotFound {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("canceled authentication challenge remained pending")
}

func TestRuntimeConfigMapping(t *testing.T) {
	config := core.Config{
		ServerAddress:          "vpn.example.edu",
		ServerPort:             443,
		Username:               "user",
		UpdateBestNodesSeconds: 30,
		AutoDetectInterface:    true,
		DisableAutoReconnect:   true,
		DisableRemoteDNS:       true,
		RemoteDNSServer:        "10.0.0.53",
		SecondaryDNSServer:     "1.1.1.1",
		DNSTTL:                 120,
		DNSBind:                "127.0.0.1:5353",
		Hosts:                  map[string]string{"app.example.edu": "10.0.0.8"},
		SOCKSBind:              "127.0.0.1:1080",
		SOCKSUsername:          "user",
		SOCKSPassword:          "pass",
		HTTPBind:               "127.0.0.1:1081",
		RoutingMode:            core.RoutingModeGlobal,
		InternetOutbound: core.InternetOutboundConfig{
			Type:    core.InternetOutboundSOCKS5,
			Address: "127.0.0.1:7890",
		},
	}
	runtimeConfig := toRuntimeConfig(config)
	if runtimeConfig.ServerAddress != config.ServerAddress || runtimeConfig.ServerPort != config.ServerPort ||
		runtimeConfig.Username != config.Username || runtimeConfig.UpdateBestNodesInterval != 30 ||
		!runtimeConfig.AutoDetectInterface || !runtimeConfig.DisableAutoReconnect || runtimeConfig.SkipTCPTunnelWait || runtimeConfig.TCPTunnelMode ||
		!runtimeConfig.DisableRemoteDNS || runtimeConfig.RemoteDNSServer != "10.0.0.53" ||
		runtimeConfig.SecondaryDNSServer != "1.1.1.1" || runtimeConfig.DNSTTL != 120 || runtimeConfig.DNSBind != "127.0.0.1:5353" {
		t.Fatalf("runtime config = %#v", runtimeConfig)
	}
	if runtimeConfig.SOCKSBind != config.SOCKSBind || runtimeConfig.SOCKSUsername != config.SOCKSUsername ||
		runtimeConfig.SOCKSPassword != config.SOCKSPassword || runtimeConfig.HTTPBind != config.HTTPBind {
		t.Fatalf("runtime service config = %#v", runtimeConfig)
	}
	if runtimeConfig.Hosts["app.example.edu"] != "10.0.0.8" {
		t.Fatalf("runtime hosts = %#v", runtimeConfig.Hosts)
	}
	if runtimeConfig.InternetOutbound != config.InternetOutbound {
		t.Fatalf("runtime internet outbound config = %#v", runtimeConfig.InternetOutbound)
	}
	if runtimeConfig.RoutingMode != core.RoutingModeGlobal {
		t.Fatalf("runtime routing mode = %q", runtimeConfig.RoutingMode)
	}
}

func TestRuntimeConfigProtectsTUNAutoRouteUnderlays(t *testing.T) {
	runtimeConfig := toRuntimeConfig(core.Config{TUNEnabled: true, TUNAutoRoute: true})
	if !runtimeConfig.AutoDetectInterface {
		t.Fatal("TUN auto route did not enable VPN underlay interface detection")
	}

	runtimeConfig = toRuntimeConfig(core.Config{TUNEnabled: true, TUNAutoRoute: true, BindInterface: "en0"})
	if runtimeConfig.TUNOutboundInterface != "en0" {
		t.Fatalf("TUN outbound interface = %q", runtimeConfig.TUNOutboundInterface)
	}
}

func TestOutboundRejectsUnknownSession(t *testing.T) {
	manager := New()
	_, err := manager.Outbound("missing")
	if code := core.ErrorCodeOf(err); code != core.ErrorCodeSessionNotFound {
		t.Fatalf("Outbound() error code = %q, error = %v", code, err)
	}
}

func TestServicesRejectUnknownSession(t *testing.T) {
	manager := New()
	_, err := manager.Services("missing")
	if code := core.ErrorCodeOf(err); code != core.ErrorCodeSessionNotFound {
		t.Fatalf("Services() error code = %q, error = %v", code, err)
	}
}

func TestResourcesRejectsUnknownSession(t *testing.T) {
	manager := New()
	_, err := manager.Resources("missing")
	if code := core.ErrorCodeOf(err); code != core.ErrorCodeSessionNotFound {
		t.Fatalf("Resources() error code = %q, error = %v", code, err)
	}
}

func TestRefreshResourcesRejectsUnknownSession(t *testing.T) {
	manager := New()
	_, err := manager.RefreshResources(context.Background(), "missing")
	if code := core.ErrorCodeOf(err); code != core.ErrorCodeSessionNotFound {
		t.Fatalf("RefreshResources() error code = %q, error = %v", code, err)
	}
}

func TestManagerReplacesTheProcessSessionSerially(t *testing.T) {
	var closeCount atomic.Int32
	var replacementStartedEarly atomic.Bool
	var factoryCalls atomic.Int32
	manager := New(WithProtocol("stub", func(id core.SessionID, _ core.Config, _ core.AuthHandler) (Session, error) {
		session := newSessionStub(id)
		call := factoryCalls.Add(1)
		if call == 1 {
			session.closed = func() error {
				closeCount.Add(1)
				return nil
			}
		} else {
			session.started = func() error {
				if closeCount.Load() != 1 {
					replacementStartedEarly.Store(true)
				}
				return nil
			}
		}
		return session, nil
	}))
	defer manager.Close(context.Background())

	if _, err := manager.Start(context.Background(), core.Config{Protocol: "stub", SessionID: "first"}); err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	if _, err := manager.Start(context.Background(), core.Config{Protocol: "stub", SessionID: "second"}); err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	if replacementStartedEarly.Load() {
		t.Fatal("replacement started before the previous session closed")
	}
	if status := manager.Status("first"); status.ID != "" {
		t.Fatalf("replaced session remains addressable: %#v", status)
	}
	if status := manager.Status("second"); status.State != core.SessionStateReady {
		t.Fatalf("active status = %#v", status)
	}
}

func TestManagerSerializesConcurrentStarts(t *testing.T) {
	var running atomic.Int32
	var concurrent atomic.Bool
	manager := New(WithProtocol("stub", func(id core.SessionID, _ core.Config, _ core.AuthHandler) (Session, error) {
		session := newSessionStub(id)
		session.started = func() error {
			if running.Add(1) != 1 {
				concurrent.Store(true)
			}
			time.Sleep(10 * time.Millisecond)
			running.Add(-1)
			return nil
		}
		return session, nil
	}))
	defer manager.Close(context.Background())

	var waitGroup sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []core.SessionID{"first", "second"} {
		waitGroup.Add(1)
		go func(id core.SessionID) {
			defer waitGroup.Done()
			_, err := manager.Start(context.Background(), core.Config{Protocol: "stub", SessionID: id})
			errs <- err
		}(id)
	}
	waitGroup.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
	}
	if concurrent.Load() {
		t.Fatal("sessions started concurrently")
	}
}

func TestManagerRejectsUnregisteredProtocol(t *testing.T) {
	manager := New()
	_, err := manager.Start(context.Background(), core.Config{Protocol: "missing"})
	if code := core.ErrorCodeOf(err); code != core.ErrorCodeConfigInvalid {
		t.Fatalf("Start() error code = %q, error = %v", code, err)
	}
}
