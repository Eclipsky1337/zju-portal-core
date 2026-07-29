package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestHelloFixture(t *testing.T) {
	request, err := os.ReadFile("testdata/hello.request.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/hello.response.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	manager := newManagerStub()
	server := NewServer(manager, "2.0.0-alpha.1", []string{"atrust", "password", "sms", "cas", "oauth2", "socks5", "http"})
	var output bytes.Buffer
	if err := server.Serve(context.Background(), bytes.NewReader(request), &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if got := output.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("hello response = %s, want %s", got, want)
	}
}

func TestServerProcessesAuthResponseWhileSessionStartWaits(t *testing.T) {
	manager := newManagerStub()
	server := NewServer(manager, "test", []string{"atrust"})
	requests := strings.NewReader(
		"{\"id\":1,\"method\":\"session.start\",\"params\":{\"config\":{\"session_id\":\"session-1\",\"server_address\":\"vpn.example.edu\",\"server_port\":443}}}\n" +
			"{\"id\":2,\"method\":\"auth.respond\",\"params\":{\"challenge_id\":\"sms-1\",\"value\":\"123456\"}}\n",
	)
	input := io.MultiReader(requests, waitForEOF{done: manager.startDone})
	var output bytes.Buffer
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	lines := decodeJSONLines(t, output.Bytes())
	if len(lines) != 3 {
		t.Fatalf("output lines = %d: %s", len(lines), output.String())
	}
	responses := make(map[float64]map[string]any)
	var event map[string]any
	for _, line := range lines {
		if id, ok := line["id"].(float64); ok {
			responses[id] = line
		} else if line["event"] == string(core.EventTypeAuthRequired) {
			event = line
		}
	}
	if event == nil {
		t.Fatalf("auth.required event missing: %s", output.String())
	}
	if responses[1] == nil || responses[2] == nil {
		t.Fatalf("responses = %#v", responses)
	}
	if got := manager.response.ChallengeID; got != "sms-1" {
		t.Fatalf("auth response challenge ID = %q", got)
	}
}

func TestServerReturnsStableMethodError(t *testing.T) {
	server := NewServer(newManagerStub(), "test", nil)
	var output bytes.Buffer
	if err := server.Serve(context.Background(), bytes.NewBufferString("{\"id\":7,\"method\":\"unknown\"}\n"), &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	lines := decodeJSONLines(t, output.Bytes())
	errorValue := lines[0]["error"].(map[string]any)
	if got := errorValue["code"]; got != string(core.ErrorCodeMethodNotFound) {
		t.Fatalf("error code = %#v", got)
	}
}

func decodeJSONLines(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var lines []map[string]any
	for decoder.More() {
		var line map[string]any
		if err := decoder.Decode(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	return lines
}

type managerStub struct {
	events        chan core.Event
	responses     chan core.AuthResponse
	startDone     chan struct{}
	startOnce     sync.Once
	challengeSent chan struct{}
	challengeOnce sync.Once

	mu          sync.Mutex
	response    core.AuthResponse
	statuses    map[core.SessionID]core.SessionStatus
	closeCalls  int
	resources   map[core.SessionID]core.Resources
	services    map[core.SessionID][]core.ServiceStatus
	startConfig core.Config
	routingMode core.RoutingMode
	refreshes   []core.SessionID
}

func newManagerStub() *managerStub {
	return &managerStub{
		events:        make(chan core.Event),
		responses:     make(chan core.AuthResponse, 1),
		startDone:     make(chan struct{}),
		challengeSent: make(chan struct{}),
		statuses:      make(map[core.SessionID]core.SessionStatus),
		resources:     make(map[core.SessionID]core.Resources),
		services:      make(map[core.SessionID][]core.ServiceStatus),
		routingMode:   core.RoutingModeRule,
	}
}

func (manager *managerStub) Start(ctx context.Context, config core.Config) (core.SessionID, error) {
	id := config.SessionID
	manager.mu.Lock()
	manager.startConfig = config
	manager.mu.Unlock()
	manager.events <- core.Event{
		SessionID: id,
		Type:      core.EventTypeAuthRequired,
		Timestamp: time.Unix(0, 0).UTC(),
		Auth: &core.AuthChallenge{
			ID:   "sms-1",
			Kind: core.AuthChallengeSMS,
		},
	}
	manager.challengeOnce.Do(func() { close(manager.challengeSent) })
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case response := <-manager.responses:
		manager.mu.Lock()
		manager.response = response
		manager.statuses[id] = core.SessionStatus{ID: id, State: core.SessionStateReady}
		manager.mu.Unlock()
		manager.startOnce.Do(func() { close(manager.startDone) })
		return id, nil
	}
}

func (manager *managerStub) RespondAuth(ctx context.Context, response core.AuthResponse) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case manager.responses <- response:
		return nil
	}
}

func (manager *managerStub) Stop(_ context.Context, id core.SessionID) error {
	manager.mu.Lock()
	if status, ok := manager.statuses[id]; ok {
		status.State = core.SessionStateStopped
		manager.statuses[id] = status
	}
	manager.mu.Unlock()
	return nil
}

func (manager *managerStub) Close(context.Context) error {
	manager.mu.Lock()
	manager.closeCalls++
	manager.mu.Unlock()
	return nil
}

func (manager *managerStub) Status(id core.SessionID) core.SessionStatus {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.statuses[id]
}

func (manager *managerStub) Resources(id core.SessionID) (core.Resources, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	resources, ok := manager.resources[id]
	if !ok {
		return core.Resources{}, core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	return resources, nil
}

func (manager *managerStub) RefreshResources(_ context.Context, id core.SessionID) (core.Resources, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	resources, ok := manager.resources[id]
	if !ok {
		return core.Resources{}, core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	manager.refreshes = append(manager.refreshes, id)
	resources.ClientIP = "10.0.1.2"
	manager.resources[id] = resources
	return resources, nil
}

func (manager *managerStub) Outbound(core.SessionID) (core.Outbound, error) {
	return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "outbound unavailable", true, nil)
}

func (manager *managerStub) Services(id core.SessionID) ([]core.ServiceStatus, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	services, ok := manager.services[id]
	if !ok {
		return nil, core.WrapError(core.ErrorCodeSessionNotFound, "session not found", false, nil)
	}
	return services, nil
}

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

func TestServerReturnsResourceSnapshot(t *testing.T) {
	manager := newManagerStub()
	manager.resources["session-1"] = core.Resources{
		ClientIP:        "10.0.0.2",
		IPResources:     []core.IPResource{{IPMin: "10.0.0.1", IPMax: "10.0.0.255", PortMin: 1, PortMax: 65535, Protocol: "all"}},
		DomainResources: map[string]core.DomainResource{"example.edu": {PortMin: 443, PortMax: 443, Protocol: "tcp"}},
		DNSRecords:      map[string]string{"app.example.edu": "10.0.0.8"},
		DNSServer:       "10.0.0.53",
	}
	server := NewServer(manager, "test", []string{"atrust"})
	var output bytes.Buffer
	request := bytes.NewBufferString("{\"id\":9,\"method\":\"resources.get\",\"params\":{\"session_id\":\"session-1\"}}\n")
	if err := server.Serve(context.Background(), request, &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	lines := decodeJSONLines(t, output.Bytes())
	result := lines[0]["result"].(map[string]any)
	if result["client_ip"] != "10.0.0.2" || result["dns_server"] != "10.0.0.53" {
		t.Fatalf("resources result = %#v", result)
	}
}

func TestServerReturnsServiceStatuses(t *testing.T) {
	manager := newManagerStub()
	manager.services["session-1"] = []core.ServiceStatus{
		{Type: core.ServiceTypeSOCKS5, Address: "127.0.0.1:1080", Running: true},
		{Type: core.ServiceTypeHTTP, Address: "127.0.0.1:1081", Running: true},
	}
	server := NewServer(manager, "test", []string{"atrust"})
	var output bytes.Buffer
	request := bytes.NewBufferString("{\"id\":10,\"method\":\"services.get\",\"params\":{\"session_id\":\"session-1\"}}\n")
	if err := server.Serve(context.Background(), request, &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	lines := decodeJSONLines(t, output.Bytes())
	result := lines[0]["result"].([]any)
	if len(result) != 2 || result[0].(map[string]any)["type"] != "socks5" || result[1].(map[string]any)["type"] != "http" {
		t.Fatalf("services result = %#v", result)
	}
}

func TestServerFullControlFlow(t *testing.T) {
	manager := newManagerStub()
	manager.resources["session-1"] = core.Resources{
		ClientIP:        "10.0.0.2",
		IPResources:     []core.IPResource{},
		DomainResources: map[string]core.DomainResource{},
		DNSRecords:      map[string]string{},
	}
	manager.services["session-1"] = []core.ServiceStatus{
		{Type: core.ServiceTypeSOCKS5, Address: "127.0.0.1:1080", Running: true},
	}
	server := NewServer(manager, "2.0.0-alpha.1", []string{"atrust", "socks5"})
	reader, writer := io.Pipe()
	var output bytes.Buffer
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(context.Background(), reader, &output)
	}()

	_, _ = io.WriteString(writer, "{\"id\":1,\"method\":\"hello\",\"params\":{\"protocol_version\":1}}\n")
	_, _ = io.WriteString(writer, "{\"id\":2,\"method\":\"session.start\",\"params\":{\"config\":{\"session_id\":\"session-1\"}}}\n")
	<-manager.challengeSent
	_, _ = io.WriteString(writer, "{\"id\":3,\"method\":\"auth.respond\",\"params\":{\"challenge_id\":\"sms-1\",\"value\":\"123456\"}}\n")
	<-manager.startDone
	_, _ = io.WriteString(writer, "{\"id\":4,\"method\":\"session.status\",\"params\":{\"session_id\":\"session-1\"}}\n")
	_, _ = io.WriteString(writer, "{\"id\":5,\"method\":\"resources.get\",\"params\":{\"session_id\":\"session-1\"}}\n")
	_, _ = io.WriteString(writer, "{\"id\":6,\"method\":\"services.get\",\"params\":{\"session_id\":\"session-1\"}}\n")
	_, _ = io.WriteString(writer, "{\"id\":7,\"method\":\"resources.refresh\",\"params\":{\"session_id\":\"session-1\"}}\n")
	_, _ = io.WriteString(writer, "{\"id\":8,\"method\":\"session.stop\",\"params\":{\"session_id\":\"session-1\"}}\n")
	_ = writer.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	lines := decodeJSONLines(t, output.Bytes())
	responses := make(map[float64]map[string]any)
	authRequired := false
	for _, line := range lines {
		if id, ok := line["id"].(float64); ok {
			responses[id] = line
		}
		if line["event"] == string(core.EventTypeAuthRequired) {
			authRequired = true
		}
	}
	if !authRequired {
		t.Fatalf("auth.required event missing: %s", output.String())
	}
	for id := 1; id <= 8; id++ {
		response := responses[float64(id)]
		if response == nil || response["error"] != nil {
			t.Fatalf("response %d = %#v; output = %s", id, response, output.String())
		}
	}
	if got := responses[5]["result"].(map[string]any)["client_ip"]; got != "10.0.0.2" {
		t.Fatalf("resources client IP = %#v", got)
	}
	if got := responses[6]["result"].([]any)[0].(map[string]any)["type"]; got != "socks5" {
		t.Fatalf("service type = %#v", got)
	}
	if got := responses[7]["result"].(map[string]any)["client_ip"]; got != "10.0.1.2" {
		t.Fatalf("refreshed resources client IP = %#v", got)
	}
}

type waitForEOF struct {
	done <-chan struct{}
}

func (reader waitForEOF) Read([]byte) (int, error) {
	<-reader.done
	return 0, io.EOF
}

func TestServerCancelsPendingStartAndClosesManagerOnEOF(t *testing.T) {
	manager := newManagerStub()
	server := NewServer(manager, "test", []string{"atrust"})
	input := bytes.NewBufferString("{\"id\":1,\"method\":\"session.start\",\"params\":{\"config\":{\"session_id\":\"session-1\"}}}\n")
	var output bytes.Buffer

	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	manager.mu.Lock()
	closeCalls := manager.closeCalls
	manager.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("manager close calls = %d, want 1", closeCalls)
	}

	lines := decodeJSONLines(t, output.Bytes())
	var response map[string]any
	for _, line := range lines {
		if line["id"] == float64(1) {
			response = line
		}
	}
	if response == nil {
		t.Fatalf("session.start response missing: %s", output.String())
	}
	if response["error"] == nil {
		t.Fatalf("session.start response has no cancellation error: %s", output.String())
	}
}

func TestProtocolTypesRemainJSONCompatible(t *testing.T) {
	request := Request{ID: json.RawMessage("1"), Method: MethodHello, Params: json.RawMessage(`{"protocol_version":1}`)}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Request
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request, decoded) {
		t.Fatalf("round trip = %#v, want %#v", decoded, request)
	}
}
