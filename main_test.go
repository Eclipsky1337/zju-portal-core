package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	controlv1 "github.com/Eclipsky1337/zju-portal-core/control/v1"
	"github.com/Eclipsky1337/zju-portal-core/core"
	zlog "github.com/Eclipsky1337/zju-portal-core/log"
	coremanager "github.com/Eclipsky1337/zju-portal-core/manager"
)

func TestDaemonStdioReservesStdoutForJSONL(t *testing.T) {
	defer zlog.SetOutput(os.Stdout)
	input := strings.NewReader("{\"id\":1,\"method\":\"hello\",\"params\":{\"protocol_version\":2}}\n")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runDaemon(context.Background(), []string{"--stdio"}, input, &stdout, &stderr, func() core.Manager { return coremanager.New() }); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var response struct {
		ID     int `json:"id"`
		Result struct {
			CoreVersion string `json:"core_version"`
		} `json:"result"`
	}
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("decode stdout: %v: %q", err, stdout.String())
	}
	if response.ID != 1 || response.Result.CoreVersion != coreVersionNumber {
		t.Fatalf("response = %#v", response)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("extra stdout = %q", stdout.String())
	}
	zlog.Println("diagnostic output")
	if strings.Contains(stdout.String(), "diagnostic output") || !strings.Contains(stderr.String(), "diagnostic output") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestDaemonRESTBootstrapOnNonLoopback(t *testing.T) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	listenAddress := net.JoinHostPort("0.0.0.0", port)
	requestAddress := net.JoinHostPort("127.0.0.1", port)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr synchronizedBuffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- runDaemon(ctx, []string{"--rest", listenAddress}, strings.NewReader(""), io.Discard, &stderr, func() core.Manager { return coremanager.New() })
	}()

	client := &http.Client{Timeout: 500 * time.Millisecond}
	var response *http.Response
	var requestErr error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		secret := restToken(stderr.String())
		if secret == "" {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		request, requestErr := http.NewRequest(http.MethodGet, "http://"+requestAddress+"/api/v1/hello", nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Header.Set("Authorization", "Bearer "+secret)
		response, requestErr = client.Do(request)
		if requestErr == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if response == nil {
		t.Fatalf("request REST bootstrap: %v; stderr=%q", requestErr, stderr.String())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("REST bootstrap status = %d", response.StatusCode)
	}
	var hello struct {
		Result struct {
			ProtocolVersion int `json:"protocol_version"`
		} `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&hello); err != nil {
		t.Fatal(err)
	}
	if hello.Result.ProtocolVersion != controlv1.ProtocolVersion {
		t.Fatalf("protocol version = %d", hello.Result.ProtocolVersion)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestDaemonRESTShutdownDoesNotWaitForSSETimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var stderr synchronizedBuffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- runDaemon(ctx, []string{"--rest", address}, strings.NewReader(""), io.Discard, &stderr, func() core.Manager { return coremanager.New() })
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	var response *http.Response
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		secret := restToken(stderr.String())
		if secret == "" {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		request, requestErr := http.NewRequest(http.MethodGet, "http://"+address+"/api/v1/events?access_token="+secret, nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		response, err = client.Do(request)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if response == nil {
		cancel()
		t.Fatalf("open REST event stream: %v; stderr=%q", err, stderr.String())
	}
	defer response.Body.Close()

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("REST shutdown waited for the full timeout with an active SSE stream")
	}

	rebound, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("REST address was not released: %v", err)
	}
	_ = rebound.Close()
}

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(data)
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func restToken(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if token, found := strings.CutPrefix(line, "REST control token: "); found {
			return strings.TrimSpace(token)
		}
	}
	return ""
}

func TestDaemonConfigValidationMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nsession:\n  auto-start: false\n"), 0666); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runDaemon(context.Background(), []string{"-t", "-f", path}, strings.NewReader(""), &output, io.Discard, func() core.Manager { t.Fatal("manager created in test mode"); return nil }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "configuration is valid") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestDaemonRejectsLegacySubcommands(t *testing.T) {
	for _, command := range []string{"connect", "test", "web", "v2"} {
		if _, err := parseDaemonOptions([]string{command}, io.Discard); err == nil {
			t.Fatalf("legacy command %q was accepted", command)
		}
	}
}

func TestDaemonRequiresConfigForValidation(t *testing.T) {
	if err := runDaemon(context.Background(), []string{"-t"}, strings.NewReader(""), io.Discard, io.Discard, func() core.Manager { return coremanager.New() }); err == nil {
		t.Fatal("test config without file was accepted")
	}
}

func TestControlCapabilitiesDescribeImplementedAPIs(t *testing.T) {
	want := []string{"config", "resource_snapshots", "resource_refresh", "service_status", "traffic_stats", "connections", "transport_connections", "resume_state", "routing_modes", "stdio", "rest", "sse", "limitation_icmp", "limitation_socks5_udp_associate"}
	available := make(map[string]bool, len(controlCapabilities))
	for _, capability := range controlCapabilities {
		available[capability] = true
	}
	for _, capability := range want {
		if !available[capability] {
			t.Fatalf("capability %q is missing from %#v", capability, controlCapabilities)
		}
	}
}

func TestSaveResumeStateAtomically(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "resume.json")
	if err := os.WriteFile(path, []byte("old"), 0666); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0640); err != nil {
			t.Fatal(err)
		}
	}
	want := core.ResumeState{Format: core.ResumeStateFormatATrustClientData, Version: core.ResumeStateVersion1, Revision: 3, Data: "state"}
	if err := saveResumeState(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadResumeState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("resume state = %#v, want %#v", *got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if gotMode := info.Mode().Perm(); gotMode != 0640 {
			t.Fatalf("resume state mode = %o, want 640", gotMode)
		}
	}
	temporary, err := filepath.Glob(filepath.Join(directory, ".resume.json.tmp-*"))
	if err != nil || len(temporary) != 0 {
		t.Fatalf("temporary files = %v, %v", temporary, err)
	}
}

func TestPersistResumeStateEventsSavesUpdatedRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume.json")
	provider := resumeStateProviderStub{state: core.ResumeState{Revision: 8, Data: "updated"}}
	events := make(chan core.Event, 2)
	events <- core.Event{Type: core.EventTypeResourcesUpdated, SessionID: "default"}
	events <- core.NewResumeStateUpdatedEvent("default", 8, false, time.Now())
	close(events)
	persistResumeStateEvents(context.Background(), events, provider, path)
	state, err := loadResumeState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != 8 || state.Data != "updated" {
		t.Fatalf("persisted state = %#v", state)
	}
}

type resumeStateProviderStub struct {
	state core.ResumeState
}

func (provider resumeStateProviderStub) ResumeState(core.SessionID) (core.ResumeState, error) {
	return provider.state, nil
}
