package daemonruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/daemonconfig"
	coremanager "github.com/Eclipsky1337/zju-portal-core/manager"
)

func TestControllerRejectsTUNChangesAfterInitialization(t *testing.T) {
	controller := New(coremanager.New(), "")
	config := daemonconfig.Default()
	config.Session.AutoStart = false
	if err := controller.Initialize(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	config.Inbounds.TUN.Enabled = true
	if err := controller.SetConfig(context.Background(), config); core.ErrorCodeOf(err) != core.ErrorCodeRestartRequired {
		t.Fatalf("SetConfig() error = %v", err)
	}
}

func TestControllerReturnsCompleteConfig(t *testing.T) {
	controller := New(coremanager.New(), "")
	config := daemonconfig.Default()
	config.Session.AutoStart = false
	config.ATrust.Password = "secret"
	if err := controller.Initialize(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if got := controller.Config().ATrust.Password; got != "secret" {
		t.Fatalf("password = %q", got)
	}
}

func TestControllerStartsConfiguredSessionWithInitialResumeState(t *testing.T) {
	resumeState := core.ResumeState{
		Format:   core.ResumeStateFormatATrustClientData,
		Version:  core.ResumeStateVersion1,
		Revision: 3,
		Scope: core.ResumeStateScope{
			ServerAddress: "vpn.example.edu",
			ServerPort:    443,
			Username:      "user",
		},
		Data: "resume-data",
	}
	manager := &controllerManagerStub{resumeState: resumeState}
	controller := New(manager, "")
	controller.SetInitialResumeState(&resumeState)
	config := daemonconfig.Default()
	config.Session.AutoStart = false
	config.ATrust.Server = "vpn.example.edu"
	config.ATrust.Username = "user"
	config.ATrust.Password = "secret"
	if err := controller.Initialize(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	id, err := controller.Start(context.Background(), core.Config{SessionID: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "default" || len(manager.starts) != 1 {
		t.Fatalf("start result = %q, calls = %d", id, len(manager.starts))
	}
	started := manager.starts[0]
	if started.ServerAddress != "vpn.example.edu" || started.Username != "user" || started.Password != "secret" {
		t.Fatalf("start config = %#v", started)
	}
	if started.ResumeState == nil || started.ResumeState.Revision != resumeState.Revision {
		t.Fatalf("resume state = %#v", started.ResumeState)
	}
}

func TestControllerExplicitStartUsesInitialResumeState(t *testing.T) {
	resumeState := core.ResumeState{
		Format:   core.ResumeStateFormatATrustClientData,
		Version:  core.ResumeStateVersion1,
		Revision: 4,
		Scope:    core.ResumeStateScope{ServerAddress: "vpn.example.edu", ServerPort: 443},
		Data:     "resume-data",
	}
	manager := &controllerManagerStub{resumeState: resumeState}
	controller := New(manager, "")
	controller.SetInitialResumeState(&resumeState)

	_, err := controller.Start(context.Background(), core.Config{
		Protocol:      "atrust",
		SessionID:     "default",
		ServerAddress: "vpn.example.edu",
		ServerPort:    443,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.starts[0].ResumeState; got == nil || got.Revision != resumeState.Revision {
		t.Fatalf("resume state = %#v", got)
	}
}

func TestControllerConfiguredStartPrefersExplicitResumeState(t *testing.T) {
	configuredState := core.ResumeState{
		Format:  core.ResumeStateFormatATrustClientData,
		Version: core.ResumeStateVersion1,
		Scope:   core.ResumeStateScope{ServerAddress: "vpn.example.edu", ServerPort: 443},
		Data:    "configured-state",
	}
	explicitState := configuredState
	explicitState.Revision = 9
	explicitState.Data = "explicit-state"
	manager := &controllerManagerStub{resumeState: explicitState}
	controller := New(manager, "")
	controller.SetInitialResumeState(&configuredState)
	config := daemonconfig.Default()
	config.Session.AutoStart = false
	config.ATrust.Server = "vpn.example.edu"
	if err := controller.Initialize(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	_, err := controller.Start(context.Background(), core.Config{
		SessionID:   "default",
		ResumeState: &explicitState,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := manager.starts[0]
	if started.ServerAddress != "vpn.example.edu" || started.ResumeState == nil || started.ResumeState.Revision != 9 {
		t.Fatalf("start config = %#v", started)
	}
}

func TestControllerStopCachesResumeStateForNextStart(t *testing.T) {
	manager := &controllerManagerStub{}
	controller := New(manager, "")
	config := daemonconfig.Default()
	config.Session.AutoStart = false
	config.ATrust.Server = "vpn.example.edu"
	config.ATrust.Username = "user"
	if err := controller.Initialize(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Start(context.Background(), core.Config{SessionID: "default"}); err != nil {
		t.Fatal(err)
	}
	manager.resumeState = core.ResumeState{
		Format:   core.ResumeStateFormatATrustClientData,
		Version:  core.ResumeStateVersion1,
		Revision: 5,
		Scope: core.ResumeStateScope{
			ServerAddress: "vpn.example.edu",
			ServerPort:    443,
			Username:      "user",
		},
		Data: "updated-resume-data",
	}
	if err := controller.Stop(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Start(context.Background(), core.Config{SessionID: "default"}); err != nil {
		t.Fatal(err)
	}
	if got := manager.starts[1].ResumeState; got == nil || got.Revision != 5 {
		t.Fatalf("resume state = %#v", got)
	}
}

func TestControllerReloadRequiresPath(t *testing.T) {
	controller := New(coremanager.New(), "")
	if err := controller.ReloadConfig(context.Background()); core.ErrorCodeOf(err) != core.ErrorCodeConfigUnavailable {
		t.Fatalf("ReloadConfig() error = %v", err)
	}
}

func TestControllerRestoresPreviousSessionWhenReplacementFails(t *testing.T) {
	wantErr := core.WrapError(core.ErrorCodeNetworkSetupFailed, "start replacement", false, errors.New("address already in use"))
	resumeState := core.ResumeState{
		Format:   core.ResumeStateFormatATrustClientData,
		Version:  core.ResumeStateVersion1,
		Revision: 7,
		Scope: core.ResumeStateScope{
			ServerAddress: "vpn.example.edu",
			ServerPort:    443,
			Username:      "user",
		},
		Data: "resume-data",
	}
	manager := &controllerManagerStub{resumeState: resumeState, failStart: 2, startErr: wantErr}
	controller := New(manager, "")
	oldConfig := daemonconfig.Default()
	oldConfig.Session.AutoStart = true
	oldConfig.ATrust.Server = "vpn.example.edu"
	oldConfig.ATrust.Username = "user"
	oldConfig.Inbounds.HTTP.Enabled = true
	oldConfig.Inbounds.HTTP.Listen = "127.0.0.1:1081"
	if err := controller.Initialize(context.Background(), oldConfig); err != nil {
		t.Fatal(err)
	}
	newConfig := oldConfig
	newConfig.Inbounds.HTTP.Listen = "127.0.0.1:2081"

	if err := controller.SetConfig(context.Background(), newConfig); !errors.Is(err, wantErr) {
		t.Fatalf("SetConfig() error = %v, want %v", err, wantErr)
	}
	if len(manager.starts) != 3 {
		t.Fatalf("Start() calls = %d, want 3", len(manager.starts))
	}
	rollback := manager.starts[2]
	if rollback.HTTPBind != "127.0.0.1:1081" || rollback.ResumeState == nil || rollback.ResumeState.Revision != resumeState.Revision {
		t.Fatalf("rollback config = %#v", rollback)
	}
	controller.mu.RLock()
	current := controller.config
	controller.mu.RUnlock()
	if current.Inbounds.HTTP.Listen != "127.0.0.1:1081" {
		t.Fatalf("current HTTP listen = %q", current.Inbounds.HTTP.Listen)
	}
}

func TestControllerDoesNotReuseResumeStateAcrossServerChanges(t *testing.T) {
	manager := &controllerManagerStub{resumeState: core.ResumeState{
		Scope: core.ResumeStateScope{ServerAddress: "old.example.edu", ServerPort: 443, Username: "user"},
		Data:  "resume-data",
	}}
	controller := New(manager, "")
	oldConfig := daemonconfig.Default()
	oldConfig.Session.AutoStart = true
	oldConfig.ATrust.Server = "old.example.edu"
	oldConfig.ATrust.Username = "user"
	if err := controller.Initialize(context.Background(), oldConfig); err != nil {
		t.Fatal(err)
	}
	newConfig := oldConfig
	newConfig.ATrust.Server = "new.example.edu"
	if err := controller.SetConfig(context.Background(), newConfig); err != nil {
		t.Fatal(err)
	}
	if got := manager.starts[1].ResumeState; got != nil {
		t.Fatalf("replacement resume state = %#v", got)
	}
}

type controllerManagerStub struct {
	core.Manager
	starts      []core.Config
	failStart   int
	startErr    error
	resumeState core.ResumeState
}

func (manager *controllerManagerStub) Start(_ context.Context, config core.Config) (core.SessionID, error) {
	manager.starts = append(manager.starts, config)
	if len(manager.starts) == manager.failStart {
		return "", manager.startErr
	}
	return config.SessionID, nil
}

func (*controllerManagerStub) Stop(context.Context, core.SessionID) error { return nil }

func (manager *controllerManagerStub) ResumeState(core.SessionID) (core.ResumeState, error) {
	if manager.resumeState.Data == "" {
		return core.ResumeState{}, core.WrapError(core.ErrorCodeResumeStateUnavailable, "resume state unavailable", true, nil)
	}
	return manager.resumeState, nil
}
