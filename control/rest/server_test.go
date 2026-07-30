package rest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	controlv1 "github.com/Eclipsky1337/zju-portal-core/control/v1"
	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/daemonconfig"
)

func TestRESTHelloUsesSharedControlService(t *testing.T) {
	service := controlv1.NewService(newManagerStub(), "test-core", []string{"atrust"})
	server := NewServer(service, "secret")
	request := httptest.NewRequest(http.MethodGet, APIBasePath+"/hello", nil)
	request.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()

	server.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Result controlv1.HelloResult `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result.CoreVersion != "test-core" || response.Result.ProtocolVersion != controlv1.ProtocolVersion {
		t.Fatalf("hello result = %#v", response.Result)
	}
}

func TestRESTConfigurationEndpoints(t *testing.T) {
	manager := &restConfigManagerStub{managerStub: newManagerStub(), config: daemonconfig.Default()}
	manager.config.Session.AutoStart = false
	server := NewServer(controlv1.NewService(manager, "test", nil), "secret")

	put := httptest.NewRequest(http.MethodPut, APIBasePath+"/config", strings.NewReader(`{"version":1,"session":{"auto-start":false},"atrust":{"password":"secret"}}`))
	put.Header.Set("Authorization", "Bearer secret")
	putRecorder := httptest.NewRecorder()
	server.ServeHTTP(putRecorder, put)
	if putRecorder.Code != http.StatusOK {
		t.Fatalf("PUT status/body = %d/%s", putRecorder.Code, putRecorder.Body.String())
	}
	if manager.setCalls != 1 {
		t.Fatalf("set calls = %d", manager.setCalls)
	}

	get := httptest.NewRequest(http.MethodGet, APIBasePath+"/config", nil)
	get.Header.Set("Authorization", "Bearer secret")
	getRecorder := httptest.NewRecorder()
	server.ServeHTTP(getRecorder, get)
	if getRecorder.Code != http.StatusOK || !strings.Contains(getRecorder.Body.String(), `"password":"secret"`) {
		t.Fatalf("GET status/body = %d/%s", getRecorder.Code, getRecorder.Body.String())
	}

	reload := httptest.NewRequest(http.MethodPost, APIBasePath+"/config/reload", nil)
	reload.Header.Set("Authorization", "Bearer secret")
	reloadRecorder := httptest.NewRecorder()
	server.ServeHTTP(reloadRecorder, reload)
	if reloadRecorder.Code != http.StatusOK || manager.reloadCalls != 1 {
		t.Fatalf("reload status/calls = %d/%d", reloadRecorder.Code, manager.reloadCalls)
	}
}

type restConfigManagerStub struct {
	*managerStub
	config      daemonconfig.Config
	setCalls    int
	patchCalls  int
	applyCalls  int
	reloadCalls int
}

func (manager *restConfigManagerStub) ConfigSnapshot() daemonconfig.Snapshot {
	return daemonconfig.Snapshot{Revision: 1, Configured: manager.config.Clone(), Active: manager.config.Clone(), ActiveRevision: 1}
}
func (manager *restConfigManagerStub) SetConfig(_ context.Context, config daemonconfig.Config) (daemonconfig.Snapshot, error) {
	manager.config = config
	manager.setCalls++
	return manager.ConfigSnapshot(), nil
}
func (manager *restConfigManagerStub) PatchConfig(_ context.Context, patch []byte) (daemonconfig.Snapshot, error) {
	config, err := daemonconfig.MergeJSON(manager.config, patch)
	if err != nil {
		return daemonconfig.Snapshot{}, err
	}
	manager.config = config
	manager.patchCalls++
	return manager.ConfigSnapshot(), nil
}
func (manager *restConfigManagerStub) ApplyConfig(context.Context, daemonconfig.ApplyMode) (daemonconfig.Snapshot, error) {
	manager.applyCalls++
	return manager.ConfigSnapshot(), nil
}
func (manager *restConfigManagerStub) ReloadConfig(context.Context) (daemonconfig.Snapshot, error) {
	manager.reloadCalls++
	return manager.ConfigSnapshot(), nil
}

func TestRESTRejectsUnauthorizedAndCrossOriginRequests(t *testing.T) {
	server := NewServer(controlv1.NewService(newManagerStub(), "test", nil), "secret")

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, APIBasePath+"/hello", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, APIBasePath+"/hello", nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Origin", "https://evil.example")
	forbidden := httptest.NewRecorder()
	server.ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", forbidden.Code)
	}
}

func TestStatusForStructuredNetworkErrors(t *testing.T) {
	for _, testCase := range []struct {
		code core.ErrorCode
		want int
	}{
		{code: core.ErrorCodeAddressInUse, want: http.StatusConflict},
		{code: core.ErrorCodePermissionDenied, want: http.StatusForbidden},
		{code: core.ErrorCodeTUNUnavailable, want: http.StatusServiceUnavailable},
		{code: core.ErrorCodeDNSStartFailed, want: http.StatusServiceUnavailable},
		{code: core.ErrorCodeOutboundUnavailable, want: http.StatusServiceUnavailable},
	} {
		if got := statusForError(core.WrapError(testCase.code, "test", false, nil)); got != testCase.want {
			t.Fatalf("statusForError(%q) = %d, want %d", testCase.code, got, testCase.want)
		}
	}
}

func TestRESTResumeStateDisablesCaching(t *testing.T) {
	server := NewServer(controlv1.NewService(newManagerStub(), "test", nil), "secret")
	request := httptest.NewRequest(http.MethodGet, APIBasePath+"/sessions/default/resume-state", nil)
	request.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()

	server.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers = %#v", recorder.Header())
	}
}

func TestRESTRoutingModeCanBeReadAndChanged(t *testing.T) {
	manager := newManagerStub()
	server := NewServer(controlv1.NewService(manager, "test", nil), "secret")

	setRequest := httptest.NewRequest(http.MethodPut, APIBasePath+"/sessions/default/routing", strings.NewReader(`{"mode":"global"}`))
	setRequest.Header.Set("Authorization", "Bearer secret")
	setRecorder := httptest.NewRecorder()
	server.ServeHTTP(setRecorder, setRequest)
	if setRecorder.Code != http.StatusOK {
		t.Fatalf("set status = %d, body = %s", setRecorder.Code, setRecorder.Body.String())
	}

	getRequest := httptest.NewRequest(http.MethodGet, APIBasePath+"/sessions/default/routing", nil)
	getRequest.Header.Set("Authorization", "Bearer secret")
	getRecorder := httptest.NewRecorder()
	server.ServeHTTP(getRecorder, getRequest)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRecorder.Code, getRecorder.Body.String())
	}
	var response struct {
		Result controlv1.RoutingModeResult `json:"result"`
	}
	if err := json.Unmarshal(getRecorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result.Mode != core.RoutingModeGlobal {
		t.Fatalf("routing mode = %q", response.Result.Mode)
	}
}

func TestRESTRefreshesSessionResources(t *testing.T) {
	manager := newManagerStub()
	server := NewServer(controlv1.NewService(manager, "test", nil), "secret")
	request := httptest.NewRequest(http.MethodPost, APIBasePath+"/sessions/default/resources/refresh", nil)
	request.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()

	server.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Result core.Resources `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result.ClientIP != "10.0.1.2" || manager.refreshCalls != 1 || manager.refreshSession != "default" {
		t.Fatalf("refresh result/calls/session = %#v/%d/%q", response.Result, manager.refreshCalls, manager.refreshSession)
	}
}

func TestRESTDoesNotServeEmbeddedWebUI(t *testing.T) {
	server := NewServer(controlv1.NewService(newManagerStub(), "test", nil), "secret")
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer secret")
	page := httptest.NewRecorder()
	server.ServeHTTP(page, request)
	if page.Code != http.StatusNotFound {
		t.Fatalf("page status/body = %d/%q", page.Code, page.Body.String())
	}
}

func TestSessionStartUsesServerLifecycleContext(t *testing.T) {
	manager := newManagerStub()
	service := controlv1.NewService(manager, "test", nil)
	lifecycleCtx, cancelLifecycle := context.WithCancel(context.Background())
	defer cancelLifecycle()
	server := NewServerContext(lifecycleCtx, service, "secret")

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, APIBasePath+"/sessions", strings.NewReader(`{"session_id":"default"}`)).WithContext(requestCtx)
	request.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("start status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	cancelRequest()
	manager.mu.Lock()
	startCtx := manager.startCtx
	manager.mu.Unlock()
	select {
	case <-startCtx.Done():
		t.Fatal("session context was canceled with the HTTP request")
	default:
	}

	cancelLifecycle()
	select {
	case <-startCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("session context was not canceled with the REST server lifecycle")
	}
}

func TestRESTStreamsCoreEventsOverSSE(t *testing.T) {
	manager := newManagerStub()
	service := controlv1.NewService(manager, "test", nil)
	server := NewServer(service, "secret")
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, APIBasePath+"/events?access_token=secret", nil).WithContext(ctx)
	writer := newStreamWriter()
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(writer, request)
		close(done)
	}()

	reader := bufio.NewReader(writer)
	if line, err := reader.ReadString('\n'); err != nil || strings.TrimSpace(line) != ": connected" {
		t.Fatalf("SSE preamble = %q, %v", line, err)
	}
	want := core.NewStateChangedEvent("default", core.SessionStateIdle, core.SessionStateDiscoveringAuth, time.Unix(1, 0))
	manager.events <- want
	var eventLine, dataLine string
	for eventLine == "" || dataLine == "" {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "event: ") {
			eventLine = line
		}
		if strings.HasPrefix(line, "data: ") {
			dataLine = line
		}
	}
	if eventLine != "event: "+string(core.EventTypeSessionStateChanged) {
		t.Fatalf("event line = %q", eventLine)
	}
	var event core.Event
	if err := json.Unmarshal([]byte(strings.TrimPrefix(dataLine, "data: ")), &event); err != nil {
		t.Fatal(err)
	}
	if event.State != core.SessionStateDiscoveringAuth {
		t.Fatalf("event = %#v", event)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not stop after cancellation")
	}
}

type streamWriter struct {
	header http.Header
	chunks chan []byte
	buffer []byte
	mu     sync.Mutex
}

func newStreamWriter() *streamWriter {
	return &streamWriter{header: make(http.Header), chunks: make(chan []byte, 16)}
}

func (writer *streamWriter) Header() http.Header { return writer.header }
func (*streamWriter) WriteHeader(int)            {}
func (*streamWriter) Flush()                     {}

func (writer *streamWriter) Write(data []byte) (int, error) {
	copyData := append([]byte(nil), data...)
	writer.chunks <- copyData
	return len(data), nil
}

func (writer *streamWriter) Read(data []byte) (int, error) {
	writer.mu.Lock()
	if len(writer.buffer) > 0 {
		count := copy(data, writer.buffer)
		writer.buffer = writer.buffer[count:]
		writer.mu.Unlock()
		return count, nil
	}
	writer.mu.Unlock()

	chunk := <-writer.chunks
	writer.mu.Lock()
	writer.buffer = chunk
	count := copy(data, writer.buffer)
	writer.buffer = writer.buffer[count:]
	writer.mu.Unlock()
	return count, nil
}

type managerStub struct {
	events         chan core.Event
	mu             sync.Mutex
	status         map[core.SessionID]core.SessionStatus
	startCtx       context.Context
	routingMode    core.RoutingMode
	refreshCalls   int
	refreshSession core.SessionID
}

func newManagerStub() *managerStub {
	return &managerStub{events: make(chan core.Event, 8), status: make(map[core.SessionID]core.SessionStatus), routingMode: core.RoutingModeRule}
}

func (manager *managerStub) Start(ctx context.Context, config core.Config) (core.SessionID, error) {
	id := config.SessionID
	if id == "" {
		id = "default"
	}
	manager.mu.Lock()
	manager.startCtx = ctx
	manager.status[id] = core.SessionStatus{ID: id, State: core.SessionStateReady}
	manager.mu.Unlock()
	return id, nil
}

func (manager *managerStub) StartSession(ctx context.Context, options core.SessionStartOptions) (core.SessionID, error) {
	id := options.SessionID
	if id == "" {
		id = "default"
	}
	return manager.Start(ctx, core.Config{SessionID: id, ResumeState: options.ResumeState})
}

func (*managerStub) RespondAuth(context.Context, core.AuthResponse) error { return nil }
func (*managerStub) Stop(context.Context, core.SessionID) error           { return nil }
func (*managerStub) Close(context.Context) error                          { return nil }
func (manager *managerStub) Status(id core.SessionID) core.SessionStatus {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.status[id]
}
func (*managerStub) Resources(core.SessionID) (core.Resources, error) {
	return core.Resources{}, nil
}
func (manager *managerStub) RefreshResources(_ context.Context, id core.SessionID) (core.Resources, error) {
	manager.mu.Lock()
	manager.refreshCalls++
	manager.refreshSession = id
	manager.mu.Unlock()
	return core.Resources{ClientIP: "10.0.1.2"}, nil
}
func (*managerStub) Outbound(core.SessionID) (core.Outbound, error) {
	return outboundStub{}, nil
}
func (*managerStub) Services(core.SessionID) ([]core.ServiceStatus, error) { return nil, nil }
func (*managerStub) TrafficStats(id core.SessionID) (core.TrafficStats, error) {
	return core.TrafficStats{SessionID: id}, nil
}
func (*managerStub) Connections(core.SessionID) ([]core.ConnectionInfo, error) { return nil, nil }
func (*managerStub) CloseConnection(core.SessionID, string) error              { return nil }
func (*managerStub) TransportConnections(core.SessionID) ([]core.TransportConnectionInfo, error) {
	return nil, nil
}
func (manager *managerStub) RoutingMode(core.SessionID) (core.RoutingMode, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.routingMode, nil
}
func (manager *managerStub) SetRoutingMode(_ core.SessionID, mode core.RoutingMode) error {
	manager.mu.Lock()
	manager.routingMode = mode
	manager.mu.Unlock()
	return nil
}
func (*managerStub) ResumeState(id core.SessionID) (core.ResumeState, error) {
	return core.ResumeState{Revision: 1, Scope: core.ResumeStateScope{Username: string(id)}, Data: "state"}, nil
}
func (manager *managerStub) Events() <-chan core.Event { return manager.events }

type outboundStub struct{}

func (outboundStub) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("not implemented")
}
func (outboundStub) Close(context.Context) error { return nil }
