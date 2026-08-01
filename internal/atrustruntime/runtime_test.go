package atrustruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	clientpkg "github.com/Eclipsky1337/zju-portal-core/client"
	atrustclient "github.com/Eclipsky1337/zju-portal-core/client/atrust"
	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestStartConsumesAndRefreshesResumeState(t *testing.T) {
	initialClientData := []byte(`{"cookies":[],"device_id":"device-1"}`)
	updatedClientData := []byte(`{"cookies":[{"name":"sid","value":"new"}],"device_id":"device-1"}`)
	config := Config{
		ServerAddress: "vpn.example.edu",
		ServerPort:    443,
		Username:      "user",
		ResumeState: &core.ResumeState{
			Format:   core.ResumeStateFormatATrustClientData,
			Version:  core.ResumeStateVersion1,
			Revision: 7,
			Scope: core.ResumeStateScope{
				ServerAddress: "vpn.example.edu",
				ServerPort:    443,
				Username:      "user",
			},
			Data: base64.StdEncoding.EncodeToString(initialClientData),
		},
	}
	deps := defaultDependencies()
	deps.setup = func(_ context.Context, client *atrustclient.Client, _ Config, clientData, _ []byte, _ func(atrustclient.SetupStage)) ([]byte, error) {
		if !bytes.Equal(clientData, initialClientData) {
			t.Fatalf("client data = %s", clientData)
		}
		client.Username = "user"
		client.ResumeStateReused = true
		return updatedClientData, nil
	}

	runtime, err := start(context.Background(), config, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	state, err := runtime.ResumeState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != 8 || !state.Reused {
		t.Fatalf("resume state = %#v", state)
	}
	decoded, err := base64.StdEncoding.DecodeString(state.Data)
	if err != nil || !bytes.Equal(decoded, updatedClientData) {
		t.Fatalf("decoded state = %s, %v", decoded, err)
	}
}

func TestStartLoadsResourceAndPersistsClientData(t *testing.T) {
	tempDir := t.TempDir()
	resourcePath := filepath.Join(tempDir, "resource.json")
	clientDataPath := filepath.Join(tempDir, "client.json")
	resourceData := []byte(`{"resource":true}`)
	initialClientData := []byte(`{"cookies":[]}`)
	updatedClientData := []byte(`{"cookies":[{"name":"sid"}]}`)
	if err := os.WriteFile(resourcePath, resourceData, 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientDataPath, initialClientData, 0666); err != nil {
		t.Fatal(err)
	}

	config := Config{
		ServerAddress:  "vpn.example.edu.cn",
		ServerPort:     443,
		Username:       "user",
		SID:            "sid",
		DeviceID:       "device",
		SignKey:        "sign-key",
		ClientDataFile: clientDataPath,
		ResourceFile:   resourcePath,
	}
	deps := defaultDependencies()
	deps.setup = func(_ context.Context, _ *atrustclient.Client, gotConfig Config, gotClientData, gotResourceData []byte, _ func(atrustclient.SetupStage)) ([]byte, error) {
		if !reflect.DeepEqual(gotConfig, config) {
			t.Fatalf("setup config = %#v, want %#v", gotConfig, config)
		}
		if !bytes.Equal(gotClientData, initialClientData) {
			t.Fatalf("setup client data = %s", gotClientData)
		}
		if !bytes.Equal(gotResourceData, resourceData) {
			t.Fatalf("setup resource data = %s", gotResourceData)
		}
		return updatedClientData, nil
	}

	runtime, err := start(context.Background(), config, deps)
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	defer runtime.Close()
	if runtime.Client() == nil {
		t.Fatal("Runtime.Client() = nil")
	}

	gotClientData, err := os.ReadFile(clientDataPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotClientData, updatedClientData) {
		t.Fatalf("persisted client data = %s", gotClientData)
	}
}

func TestStartCreatesMissingClientDataFile(t *testing.T) {
	clientDataPath := filepath.Join(t.TempDir(), "missing", "client.json")
	config := Config{ClientDataFile: clientDataPath}
	deps := defaultDependencies()
	deps.setup = func(_ context.Context, _ *atrustclient.Client, _ Config, clientData, _ []byte, _ func(atrustclient.SetupStage)) ([]byte, error) {
		if clientData != nil {
			t.Fatalf("setup client data = %s, want nil", clientData)
		}
		if err := os.MkdirAll(filepath.Dir(clientDataPath), 0777); err != nil {
			t.Fatal(err)
		}
		return []byte(`{"device_id":"new-device"}`), nil
	}

	runtime, err := start(context.Background(), config, deps)
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	runtime.Close()

	if _, err := os.Stat(clientDataPath); err != nil {
		t.Fatalf("client data file was not created: %v", err)
	}
}

func TestStartReturnsResourceReadErrorBeforeCreatingClient(t *testing.T) {
	wantErr := errors.New("resource unavailable")
	clientCreated := false
	deps := defaultDependencies()
	deps.readFile = func(string) ([]byte, error) {
		return nil, wantErr
	}
	deps.newClient = func(_ context.Context, username, sid, deviceID, signKey string) *atrustclient.Client {
		clientCreated = true
		return atrustclient.NewClient(username, sid, deviceID, signKey)
	}

	_, err := start(context.Background(), Config{ResourceFile: "resource.json"}, deps)
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "read resource file") {
		t.Fatalf("start() error = %v", err)
	}
	if code := core.ErrorCodeOf(err); code != core.ErrorCodeResourceDataReadFailed {
		t.Fatalf("error code = %q", code)
	}
	if clientCreated {
		t.Fatal("aTrust client was created after resource read failure")
	}
}

func TestStartClassifiesSetupError(t *testing.T) {
	wantErr := errors.New("login failed")
	deps := defaultDependencies()
	deps.setup = func(context.Context, *atrustclient.Client, Config, []byte, []byte, func(atrustclient.SetupStage)) ([]byte, error) {
		return nil, wantErr
	}

	_, err := start(context.Background(), Config{}, deps)
	if !errors.Is(err, wantErr) {
		t.Fatalf("start() error = %v", err)
	}
	if code := core.ErrorCodeOf(err); code != core.ErrorCodeATrustSetupFailed {
		t.Fatalf("error code = %q", code)
	}
	if core.IsRetryable(err) {
		t.Fatal("plain login failure was marked retryable")
	}
}

func TestStartPreservesStructuredSetupError(t *testing.T) {
	wantErr := core.WrapError(core.ErrorCodeAuthHandlerUnavailable, "authentication handler is unavailable", false, nil)
	deps := defaultDependencies()
	deps.setup = func(context.Context, *atrustclient.Client, Config, []byte, []byte, func(atrustclient.SetupStage)) ([]byte, error) {
		return nil, wantErr
	}

	_, err := start(context.Background(), Config{}, deps)
	if core.ErrorCodeOf(err) != core.ErrorCodeAuthHandlerUnavailable || core.IsRetryable(err) {
		t.Fatalf("start() error = %#v", err)
	}
}

func TestStartMarksTransientNetworkSetupErrorRetryable(t *testing.T) {
	wantErr := &net.DNSError{Err: "temporary failure", Name: "vpn.example.edu", IsTimeout: true}
	deps := defaultDependencies()
	deps.setup = func(context.Context, *atrustclient.Client, Config, []byte, []byte, func(atrustclient.SetupStage)) ([]byte, error) {
		return nil, wantErr
	}

	_, err := start(context.Background(), Config{}, deps)
	if !errors.Is(err, wantErr) || core.ErrorCodeOf(err) != core.ErrorCodeATrustSetupFailed || !core.IsRetryable(err) {
		t.Fatalf("start() error = %#v", err)
	}
}

func TestStartClassifiesClientDataWriteError(t *testing.T) {
	wantErr := errors.New("disk full")
	deps := defaultDependencies()
	deps.readFile = func(string) ([]byte, error) {
		return nil, os.ErrNotExist
	}
	deps.setup = func(context.Context, *atrustclient.Client, Config, []byte, []byte, func(atrustclient.SetupStage)) ([]byte, error) {
		return []byte(`{}`), nil
	}
	deps.writeFile = func(string, []byte) error {
		return wantErr
	}

	_, err := start(context.Background(), Config{ClientDataFile: "client.json"}, deps)
	if !errors.Is(err, wantErr) {
		t.Fatalf("start() error = %v", err)
	}
	if code := core.ErrorCodeOf(err); code != core.ErrorCodeClientDataWriteFailed {
		t.Fatalf("error code = %q", code)
	}
}

func TestStartDoesNotPersistClientDataWhenNetworkSetupFails(t *testing.T) {
	wantErr := errors.New("network setup failed")
	deps := defaultDependencies()
	deps.readFile = func(string) ([]byte, error) {
		return []byte(`{"device_id":"old"}`), nil
	}
	deps.setup = func(context.Context, *atrustclient.Client, Config, []byte, []byte, func(atrustclient.SetupStage)) ([]byte, error) {
		return []byte(`{"device_id":"new"}`), nil
	}
	deps.setupNetwork = func(context.Context, clientpkg.Client, Config) (core.Outbound, error) {
		return nil, wantErr
	}
	written := false
	deps.writeFile = func(string, []byte) error {
		written = true
		return nil
	}

	_, err := start(context.Background(), Config{ClientDataFile: "client.json", SetupNetwork: true}, deps)
	if !errors.Is(err, wantErr) {
		t.Fatalf("start() error = %v", err)
	}
	if written {
		t.Fatal("client data was persisted before network setup succeeded")
	}
}

func TestRuntimeCloseIsIdempotent(t *testing.T) {
	runtime := &Runtime{client: atrustclient.NewClient("", "", "", "")}
	runtime.Close()
	runtime.Close()
}

func TestStartContextHonorsPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	clientCreated := false
	deps := defaultDependencies()
	deps.newClient = func(_ context.Context, username, sid, deviceID, signKey string) *atrustclient.Client {
		clientCreated = true
		return atrustclient.NewClient(username, sid, deviceID, signKey)
	}

	_, err := start(ctx, Config{}, deps)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("start() error = %v", err)
	}
	if clientCreated {
		t.Fatal("aTrust client was created after cancellation")
	}
}

func TestStartInjectsAuthenticationHandler(t *testing.T) {
	handler := &authHandlerStub{}
	injected := false
	deps := defaultDependencies()
	deps.setAuthHandler = func(_ *atrustclient.Client, got core.AuthHandler) {
		if got != core.AuthHandler(handler) {
			t.Fatalf("authentication handler = %#v", got)
		}
		injected = true
	}
	deps.setup = func(context.Context, *atrustclient.Client, Config, []byte, []byte, func(atrustclient.SetupStage)) ([]byte, error) {
		return nil, nil
	}

	runtime, err := start(context.Background(), Config{AuthHandler: handler}, deps)
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	runtime.Close()
	if !injected {
		t.Fatal("authentication handler was not injected")
	}
}

func TestStartBuildsAndClosesNetworkBeforeClient(t *testing.T) {
	deps := defaultDependencies()
	deps.setup = func(context.Context, *atrustclient.Client, Config, []byte, []byte, func(atrustclient.SetupStage)) ([]byte, error) {
		return nil, nil
	}
	var closeOrder []string
	outbound := &outboundStub{close: func() { closeOrder = append(closeOrder, "network") }}
	deps.setupNetwork = func(context.Context, clientpkg.Client, Config) (core.Outbound, error) {
		return outbound, nil
	}
	deps.closeClient = func(*atrustclient.Client) {
		closeOrder = append(closeOrder, "client")
	}

	runtime, err := start(context.Background(), Config{SetupNetwork: true}, deps)
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	if runtime.Outbound() != core.Outbound(outbound) {
		t.Fatalf("Runtime.Outbound() = %#v, want %#v", runtime.Outbound(), outbound)
	}
	runtime.Close()
	if got := strings.Join(closeOrder, ","); got != "network,client" {
		t.Fatalf("close order = %q", got)
	}
}

func TestRuntimeCloseReturnsNetworkCloseError(t *testing.T) {
	wantErr := errors.New("network close failed")
	runtime := &Runtime{outbound: wrapNetwork(&outboundStub{closeErr: wantErr}), ownsOutbound: true}
	if err := runtime.CloseContext(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("CloseContext() error = %v", err)
	}
	if err := runtime.CloseContext(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("second CloseContext() error = %v", err)
	}
}

func TestStartClassifiesNetworkSetupFailureAndClosesClient(t *testing.T) {
	wantErr := errors.New("stack setup failed")
	deps := defaultDependencies()
	deps.setup = func(context.Context, *atrustclient.Client, Config, []byte, []byte, func(atrustclient.SetupStage)) ([]byte, error) {
		return nil, nil
	}
	deps.setupNetwork = func(context.Context, clientpkg.Client, Config) (core.Outbound, error) {
		return nil, wantErr
	}
	closed := false
	deps.closeClient = func(*atrustclient.Client) { closed = true }

	_, err := start(context.Background(), Config{SetupNetwork: true}, deps)
	if !errors.Is(err, wantErr) || core.ErrorCodeOf(err) != core.ErrorCodeNetworkSetupFailed {
		t.Fatalf("start() error = %v", err)
	}
	if !closed {
		t.Fatal("aTrust client was not closed after network setup failure")
	}
}

func TestStartPreservesStructuredNetworkSetupFailure(t *testing.T) {
	wantErr := core.WrapError(core.ErrorCodeAddressInUse, "start HTTP service", false, syscall.EADDRINUSE)
	deps := defaultDependencies()
	deps.setup = func(context.Context, *atrustclient.Client, Config, []byte, []byte, func(atrustclient.SetupStage)) ([]byte, error) {
		return nil, nil
	}
	deps.setupNetwork = func(context.Context, clientpkg.Client, Config) (core.Outbound, error) {
		return nil, wantErr
	}

	_, err := start(context.Background(), Config{SetupNetwork: true}, deps)
	if !errors.Is(err, syscall.EADDRINUSE) || core.ErrorCodeOf(err) != core.ErrorCodeAddressInUse {
		t.Fatalf("start() error = %v", err)
	}
}

type authHandlerStub struct{}

func (authHandlerStub) Handle(context.Context, core.AuthChallenge) (core.AuthResponse, error) {
	return core.AuthResponse{}, nil
}

type outboundStub struct {
	close    func()
	closeErr error
}

func (*outboundStub) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("not implemented")
}

func (outbound *outboundStub) Close(context.Context) error {
	if outbound.close != nil {
		outbound.close()
	}
	return outbound.closeErr
}
