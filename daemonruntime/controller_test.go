package daemonruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/daemonconfig"
)

func TestControllerSetConfigStoresDesiredWithoutRestartingSession(t *testing.T) {
	manager := &controllerManagerStub{}
	controller := New(manager, "")
	config := testConfig(true)
	if err := controller.Initialize(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	updated := config.Clone()
	updated.DNS.Remote.Server = "10.0.0.1"
	updated.Routing.Mode = core.RoutingModeGlobal
	snapshot, err := controller.SetConfig(context.Background(), updated)
	if err != nil {
		t.Fatal(err)
	}
	if len(manager.starts) != 1 {
		t.Fatalf("Start() calls = %d, want 1", len(manager.starts))
	}
	if len(manager.routingModes) != 1 || manager.routingModes[0] != core.RoutingModeGlobal {
		t.Fatalf("routing modes = %#v", manager.routingModes)
	}
	if snapshot.Active.DNS.Remote.Server == updated.DNS.Remote.Server {
		t.Fatal("session-scoped DNS config was applied without restart")
	}
	assertPending(t, snapshot.Pending, "dns.remote.server", daemonconfig.ApplyRequirementSessionRestart)
}

func TestControllerApplyConfigRestartsActiveSession(t *testing.T) {
	manager := &controllerManagerStub{}
	controller := New(manager, "")
	config := testConfig(true)
	if err := controller.Initialize(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	updated := config.Clone()
	updated.DNS.Remote.Server = "10.0.0.1"
	if _, err := controller.SetConfig(context.Background(), updated); err != nil {
		t.Fatal(err)
	}

	snapshot, err := controller.ApplyConfig(context.Background(), daemonconfig.ApplyModeRestartSession)
	if err != nil {
		t.Fatal(err)
	}
	if len(manager.starts) != 2 || manager.starts[1].RemoteDNSServer != "10.0.0.1" {
		t.Fatalf("starts = %#v", manager.starts)
	}
	if len(snapshot.Pending) != 0 {
		t.Fatalf("pending = %#v", snapshot.Pending)
	}
}

func TestControllerApplyConfigPreservesCoreRestartChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nsession:\n  auto-start: true\natrust:\n  server: vpn.example.edu\n  username: user\ndns:\n  remote:\n    server: 10.0.0.1\ninbounds:\n  tun:\n    mtu: 1300\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := &controllerManagerStub{}
	controller := New(manager, path)
	config := testConfig(true)
	if err := controller.Initialize(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.ReloadConfig(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := controller.ApplyConfig(context.Background(), daemonconfig.ApplyModeRestartSession)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Active.DNS.Remote.Server != "10.0.0.1" {
		t.Fatal("session config was not activated")
	}
	if snapshot.Active.Inbounds.TUN.MTU != config.Inbounds.TUN.MTU {
		t.Fatal("TUN config was activated without Core restart")
	}
	assertPending(t, snapshot.Pending, "inbounds.tun.mtu", daemonconfig.ApplyRequirementCoreRestart)
}

func TestControllerApplyConfigRollsBackFailedReplacement(t *testing.T) {
	wantErr := errors.New("replacement failed")
	manager := &controllerManagerStub{failures: map[int]error{2: wantErr}}
	controller := New(manager, "")
	config := testConfig(true)
	if err := controller.Initialize(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	updated := config.Clone()
	updated.Inbounds.HTTP.Listen = "127.0.0.1:2081"
	if _, err := controller.SetConfig(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.ApplyConfig(context.Background(), daemonconfig.ApplyModeRestartSession); !errors.Is(err, wantErr) {
		t.Fatalf("ApplyConfig() error = %v", err)
	}
	if len(manager.starts) != 3 || manager.starts[2].HTTPBind != config.Inbounds.HTTP.Listen {
		t.Fatalf("rollback starts = %#v", manager.starts)
	}
}

func TestControllerStartSessionUsesResumePolicies(t *testing.T) {
	resumeState := matchingResumeState(3)
	manager := &controllerManagerStub{}
	controller := New(manager, "")
	controller.SetInitialResumeState(&resumeState)
	if err := controller.Initialize(context.Background(), testConfig(false)); err != nil {
		t.Fatal(err)
	}

	if _, err := controller.StartSession(context.Background(), core.SessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if manager.starts[0].ResumeState == nil || manager.starts[0].ResumeState.Revision != 3 {
		t.Fatalf("automatic resume state = %#v", manager.starts[0].ResumeState)
	}
	if _, err := controller.StartSession(context.Background(), core.SessionStartOptions{}); core.ErrorCodeOf(err) != core.ErrorCodeSessionAlreadyRunning {
		t.Fatalf("second StartSession() error = %v", err)
	}
	if err := controller.Stop(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.StartSession(context.Background(), core.SessionStartOptions{Resume: core.ResumePolicyNone}); err != nil {
		t.Fatal(err)
	}
	if manager.starts[1].ResumeState != nil {
		t.Fatalf("resume none state = %#v", manager.starts[1].ResumeState)
	}
}

func TestControllerStartSessionAcceptsProvidedResumeState(t *testing.T) {
	manager := &controllerManagerStub{}
	controller := New(manager, "")
	if err := controller.Initialize(context.Background(), testConfig(false)); err != nil {
		t.Fatal(err)
	}
	provided := matchingResumeState(9)
	_, err := controller.StartSession(context.Background(), core.SessionStartOptions{
		Resume:      core.ResumePolicyProvided,
		ResumeState: &provided,
	})
	if err != nil {
		t.Fatal(err)
	}
	if manager.starts[0].ResumeState == nil || manager.starts[0].ResumeState.Revision != 9 {
		t.Fatalf("provided resume state = %#v", manager.starts[0].ResumeState)
	}
}

func TestControllerPatchConfigPreservesOmittedFields(t *testing.T) {
	manager := &controllerManagerStub{}
	controller := New(manager, "")
	config := testConfig(false)
	if err := controller.Initialize(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	snapshot, err := controller.PatchConfig(context.Background(), []byte(`{"session":{"auto-reconnect":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Configured.Session.AutoReconnect || snapshot.Configured.ATrust.Server != config.ATrust.Server {
		t.Fatalf("configured = %#v", snapshot.Configured)
	}
}

func TestControllerRejectsCoreChangesFromConfigAPI(t *testing.T) {
	manager := &controllerManagerStub{}
	controller := New(manager, "")
	config := testConfig(false)
	if err := controller.Initialize(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	updated := config.Clone()
	updated.Session.AutoStart = true
	updated.Inbounds.TUN.MTU = 1300
	updated.DNS.Remote.Server = "10.0.0.1"
	snapshot, err := controller.SetConfig(context.Background(), updated)
	if core.ErrorCodeOf(err) != core.ErrorCodeRestartRequired {
		t.Fatalf("SetConfig() error = %v", err)
	}
	if snapshot.Configured.Inbounds.TUN.MTU != config.Inbounds.TUN.MTU || snapshot.Configured.DNS.Remote.Server != config.DNS.Remote.Server {
		t.Fatalf("configuration was partially modified: %#v", snapshot.Configured)
	}
}

func TestControllerReloadAllowsPersistedCoreChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nsession:\n  auto-start: false\ninbounds:\n  tun:\n    mtu: 1300\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := &controllerManagerStub{}
	controller := New(manager, path)
	config := testConfig(false)
	if err := controller.Initialize(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	snapshot, err := controller.ReloadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Configured.Inbounds.TUN.MTU != 1300 || snapshot.Active.Inbounds.TUN.MTU != config.Inbounds.TUN.MTU {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	assertPending(t, snapshot.Pending, "inbounds.tun.mtu", daemonconfig.ApplyRequirementCoreRestart)
}

func TestControllerReloadRequiresPath(t *testing.T) {
	controller := New(&controllerManagerStub{}, "")
	if _, err := controller.ReloadConfig(context.Background()); core.ErrorCodeOf(err) != core.ErrorCodeConfigUnavailable {
		t.Fatalf("ReloadConfig() error = %v", err)
	}
}

func testConfig(autoStart bool) daemonconfig.Config {
	config := daemonconfig.Default()
	config.Session.AutoStart = autoStart
	config.ATrust.Server = "vpn.example.edu"
	config.ATrust.Username = "user"
	config.Inbounds.HTTP.Enabled = true
	return config
}

func matchingResumeState(revision uint64) core.ResumeState {
	return core.ResumeState{
		Format:   core.ResumeStateFormatATrustClientData,
		Version:  core.ResumeStateVersion1,
		Revision: revision,
		Scope: core.ResumeStateScope{
			ServerAddress: "vpn.example.edu",
			ServerPort:    443,
			Username:      "user",
		},
		Data: "resume-data",
	}
}

func assertPending(t *testing.T, changes []daemonconfig.Change, path string, requirement daemonconfig.ApplyRequirement) {
	t.Helper()
	for _, change := range changes {
		if change.Path == path && change.Requires == requirement {
			return
		}
	}
	t.Fatalf("missing pending change %q (%q): %#v", path, requirement, changes)
}

type controllerManagerStub struct {
	core.Manager
	starts       []core.Config
	failures     map[int]error
	routingModes []core.RoutingMode
	resumeState  core.ResumeState
}

func (manager *controllerManagerStub) Start(_ context.Context, config core.Config) (core.SessionID, error) {
	manager.starts = append(manager.starts, config)
	if err := manager.failures[len(manager.starts)]; err != nil {
		return "", err
	}
	return config.SessionID, nil
}

func (*controllerManagerStub) Stop(context.Context, core.SessionID) error { return nil }

func (manager *controllerManagerStub) SetRoutingMode(_ core.SessionID, mode core.RoutingMode) error {
	manager.routingModes = append(manager.routingModes, mode)
	return nil
}

func (manager *controllerManagerStub) ResumeState(core.SessionID) (core.ResumeState, error) {
	if manager.resumeState.Data == "" {
		return core.ResumeState{}, core.WrapError(core.ErrorCodeResumeStateUnavailable, "resume state unavailable", true, nil)
	}
	return manager.resumeState, nil
}
