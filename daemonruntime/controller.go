package daemonruntime

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/daemonconfig"
	zlog "github.com/Eclipsky1337/zju-portal-core/log"
)

type Controller struct {
	core.Manager
	configPath  string
	operationMu sync.Mutex

	mu              sync.RWMutex
	configured      daemonconfig.Config
	active          daemonconfig.Config
	revision        uint64
	activeRevision  uint64
	initialized     bool
	activeSessionID core.SessionID
	resumeState     *core.ResumeState
}

func New(manager core.Manager, configPath string) *Controller {
	return &Controller{Manager: manager, configPath: configPath}
}

func (controller *Controller) SetInitialResumeState(state *core.ResumeState) {
	controller.mu.Lock()
	controller.resumeState = cloneResumeState(state)
	controller.mu.Unlock()
}

func (controller *Controller) Initialize(ctx context.Context, config daemonconfig.Config) error {
	controller.operationMu.Lock()
	defer controller.operationMu.Unlock()
	if err := config.Validate(); err != nil {
		return core.WrapError(core.ErrorCodeConfigInvalid, "validate configuration", false, err)
	}
	controller.mu.RLock()
	initialized := controller.initialized
	resumeState := cloneResumeState(controller.resumeState)
	controller.mu.RUnlock()
	if initialized {
		return core.WrapError(core.ErrorCodeInvalidStateTransition, "configuration is already initialized", false, nil)
	}

	var activeSessionID core.SessionID
	if config.Session.AutoStart {
		coreConfig := config.CoreConfig()
		if resumeStateMatchesConfig(resumeState, coreConfig) {
			coreConfig.ResumeState = resumeState
		}
		id, err := controller.Manager.Start(ctx, coreConfig)
		if err != nil {
			return err
		}
		activeSessionID = id
	}

	controller.mu.Lock()
	controller.configured = config.Clone()
	controller.active = config.Clone()
	controller.revision = 1
	controller.activeRevision = 1
	controller.initialized = true
	controller.activeSessionID = activeSessionID
	controller.mu.Unlock()
	zlog.Printf("Configuration initialized (version=%d, session=%s)", config.Version, config.Session.ID)
	return nil
}

func (controller *Controller) ConfigSnapshot() daemonconfig.Snapshot {
	controller.mu.RLock()
	snapshot := daemonconfig.Snapshot{
		Revision:       controller.revision,
		Configured:     controller.configured.Clone(),
		Active:         controller.active.Clone(),
		ActiveRevision: controller.activeRevision,
	}
	controller.mu.RUnlock()
	snapshot.Pending = daemonconfig.Changes(snapshot.Active, snapshot.Configured)
	return snapshot
}

func (controller *Controller) SetConfig(ctx context.Context, config daemonconfig.Config) (daemonconfig.Snapshot, error) {
	controller.operationMu.Lock()
	defer controller.operationMu.Unlock()
	return controller.setConfigLocked(ctx, config, false)
}

func (controller *Controller) PatchConfig(ctx context.Context, patch []byte) (daemonconfig.Snapshot, error) {
	controller.operationMu.Lock()
	defer controller.operationMu.Unlock()
	controller.mu.RLock()
	configured := controller.configured.Clone()
	initialized := controller.initialized
	controller.mu.RUnlock()
	if !initialized {
		return daemonconfig.Snapshot{}, core.WrapError(core.ErrorCodeConfigUnavailable, "configuration is not initialized", true, nil)
	}
	config, err := daemonconfig.MergeJSON(configured, patch)
	if err != nil {
		return daemonconfig.Snapshot{}, core.WrapError(core.ErrorCodeConfigInvalid, "merge configuration patch", false, err)
	}
	return controller.setConfigLocked(ctx, config, false)
}

func (controller *Controller) ReloadConfig(ctx context.Context) (daemonconfig.Snapshot, error) {
	if controller.configPath == "" {
		return daemonconfig.Snapshot{}, core.WrapError(core.ErrorCodeConfigUnavailable, "no configuration file was specified", false, nil)
	}
	config, err := daemonconfig.Load(controller.configPath)
	if err != nil {
		return daemonconfig.Snapshot{}, core.WrapError(core.ErrorCodeConfigInvalid, "reload configuration", false, err)
	}
	controller.operationMu.Lock()
	defer controller.operationMu.Unlock()
	return controller.setConfigLocked(ctx, config, true)
}

func (controller *Controller) ApplyConfig(ctx context.Context, mode daemonconfig.ApplyMode) (daemonconfig.Snapshot, error) {
	controller.operationMu.Lock()
	defer controller.operationMu.Unlock()
	if mode != daemonconfig.ApplyModeRestartSession {
		return daemonconfig.Snapshot{}, core.WrapError(core.ErrorCodeInvalidRequest, fmt.Sprintf("unsupported config apply mode %q", mode), false, nil)
	}

	controller.mu.RLock()
	if !controller.initialized {
		controller.mu.RUnlock()
		return daemonconfig.Snapshot{}, core.WrapError(core.ErrorCodeConfigUnavailable, "configuration is not initialized", true, nil)
	}
	configured := controller.configured.Clone()
	active := controller.active.Clone()
	activeSessionID := controller.activeSessionID
	controller.mu.RUnlock()

	candidate := sessionConfigCandidate(active, configured)
	if activeSessionID == "" {
		controller.setActiveConfig(candidate, "")
		return controller.ConfigSnapshot(), nil
	}

	controller.cacheResumeStateLocked(activeSessionID)
	oldResumeState := controller.resumeStateSnapshot()
	newCoreConfig := candidate.CoreConfig()
	if resumeStateMatchesConfig(oldResumeState, newCoreConfig) {
		newCoreConfig.ResumeState = oldResumeState
	}
	newID, err := controller.Manager.Start(ctx, newCoreConfig)
	if err != nil {
		rollbackConfig := active.CoreConfig()
		if resumeStateMatchesConfig(oldResumeState, rollbackConfig) {
			rollbackConfig.ResumeState = oldResumeState
		}
		rollbackID, rollbackErr := controller.Manager.Start(ctx, rollbackConfig)
		if rollbackErr != nil {
			controller.setActiveSessionID("")
			return controller.ConfigSnapshot(), core.WrapError(core.ErrorCodeSessionStartFailed, "apply configuration and restore previous session", true, fmt.Errorf("apply: %w; rollback: %v", err, rollbackErr))
		}
		controller.setActiveSessionID(rollbackID)
		return controller.ConfigSnapshot(), err
	}
	controller.setActiveConfig(candidate, newID)
	return controller.ConfigSnapshot(), nil
}

func (controller *Controller) StartSession(ctx context.Context, options core.SessionStartOptions) (core.SessionID, error) {
	controller.operationMu.Lock()
	defer controller.operationMu.Unlock()
	controller.mu.RLock()
	if !controller.initialized {
		controller.mu.RUnlock()
		return "", core.WrapError(core.ErrorCodeConfigUnavailable, "configuration is not initialized", true, nil)
	}
	if controller.activeSessionID != "" {
		controller.mu.RUnlock()
		return "", core.WrapError(core.ErrorCodeSessionAlreadyRunning, "session is already running", false, nil)
	}
	candidate := sessionConfigCandidate(controller.active, controller.configured)
	controller.mu.RUnlock()
	if options.SessionID != "" {
		if options.SessionID != core.SessionID(candidate.Session.ID) {
			return "", core.WrapError(core.ErrorCodeInvalidRequest, "session_id does not match configured session", false, nil)
		}
	}

	coreConfig := candidate.CoreConfig()
	resumeState, err := controller.selectResumeState(options, coreConfig)
	if err != nil {
		return "", err
	}
	coreConfig.ResumeState = resumeState
	id, err := controller.Manager.Start(ctx, coreConfig)
	if err != nil {
		return id, err
	}
	if options.Resume == core.ResumePolicyProvided {
		controller.storeResumeState(options.ResumeState)
	}
	controller.setActiveConfig(candidate, id)
	return id, nil
}

func (controller *Controller) Stop(ctx context.Context, id core.SessionID) error {
	controller.operationMu.Lock()
	defer controller.operationMu.Unlock()
	controller.cacheResumeStateLocked(id)
	if err := controller.Manager.Stop(ctx, id); err != nil {
		return err
	}
	controller.mu.Lock()
	if controller.activeSessionID == id {
		controller.activeSessionID = ""
	}
	controller.mu.Unlock()
	return nil
}

func (controller *Controller) SetRoutingMode(id core.SessionID, mode core.RoutingMode) error {
	controller.operationMu.Lock()
	defer controller.operationMu.Unlock()
	if !mode.Valid() {
		return core.WrapError(core.ErrorCodeConfigInvalid, fmt.Sprintf("routing.mode %q is invalid", mode), false, nil)
	}
	if err := controller.Manager.SetRoutingMode(id, mode); err != nil {
		return err
	}
	controller.mu.Lock()
	controller.configured.Routing.Mode = mode
	controller.active.Routing.Mode = mode
	controller.revision++
	controller.activeRevision++
	controller.mu.Unlock()
	return nil
}

func (controller *Controller) setConfigLocked(ctx context.Context, config daemonconfig.Config, allowCoreChanges bool) (daemonconfig.Snapshot, error) {
	if err := config.Validate(); err != nil {
		return daemonconfig.Snapshot{}, core.WrapError(core.ErrorCodeConfigInvalid, "validate configuration", false, err)
	}
	controller.mu.RLock()
	if !controller.initialized {
		controller.mu.RUnlock()
		return daemonconfig.Snapshot{}, core.WrapError(core.ErrorCodeConfigUnavailable, "configuration is not initialized", true, nil)
	}
	configured := controller.configured.Clone()
	active := controller.active.Clone()
	activeSessionID := controller.activeSessionID
	controller.mu.RUnlock()
	if reflect.DeepEqual(config, configured) {
		return controller.ConfigSnapshot(), nil
	}
	if !allowCoreChanges {
		var paths []string
		for _, change := range daemonconfig.Changes(configured, config) {
			if change.Requires == daemonconfig.ApplyRequirementCoreRestart {
				paths = append(paths, change.Path)
			}
		}
		if len(paths) != 0 {
			return controller.ConfigSnapshot(), core.WrapError(
				core.ErrorCodeRestartRequired,
				"Core configuration must be changed in the configuration file and Core restarted",
				false,
				fmt.Errorf("core restart required for %s", strings.Join(paths, ", ")),
			)
		}
	}

	activeChanged := false
	if config.Routing.Mode != active.Routing.Mode {
		if activeSessionID != "" {
			if err := controller.Manager.SetRoutingMode(activeSessionID, config.Routing.Mode); err != nil {
				return controller.ConfigSnapshot(), err
			}
		}
		active.Routing.Mode = config.Routing.Mode
		activeChanged = true
	}
	if activeSessionID == "" {
		candidate := sessionConfigCandidate(active, config)
		activeChanged = activeChanged || !reflect.DeepEqual(candidate, active)
		active = candidate
	}

	controller.mu.Lock()
	controller.configured = config.Clone()
	controller.active = active
	controller.revision++
	if activeChanged {
		controller.activeRevision++
	}
	controller.mu.Unlock()
	zlog.Printf("Configuration updated (revision=%d)", controller.ConfigSnapshot().Revision)
	return controller.ConfigSnapshot(), nil
}

func (controller *Controller) selectResumeState(options core.SessionStartOptions, config core.Config) (*core.ResumeState, error) {
	policy := options.Resume
	if policy == "" {
		policy = core.ResumePolicyAuto
	}
	switch policy {
	case core.ResumePolicyAuto:
		state := controller.resumeStateSnapshot()
		if resumeStateMatchesConfig(state, config) {
			return state, nil
		}
		return nil, nil
	case core.ResumePolicyNone:
		return nil, nil
	case core.ResumePolicyProvided:
		if options.ResumeState == nil {
			return nil, core.WrapError(core.ErrorCodeResumeStateInvalid, "resume_state is required when resume is provided", false, nil)
		}
		if !resumeStateMatchesConfig(options.ResumeState, config) {
			return nil, core.WrapError(core.ErrorCodeResumeStateScope, "resume state does not match session configuration", false, nil)
		}
		return cloneResumeState(options.ResumeState), nil
	default:
		return nil, core.WrapError(core.ErrorCodeInvalidRequest, fmt.Sprintf("unsupported resume policy %q", policy), false, nil)
	}
}

func sessionConfigCandidate(active, configured daemonconfig.Config) daemonconfig.Config {
	candidate := configured.Clone()
	candidate.Log = active.Log
	candidate.Control = active.Control
	candidate.Session.AutoStart = active.Session.AutoStart
	candidate.State = active.State
	candidate.Inbounds.TUN = active.Inbounds.TUN
	return candidate
}

func resumeStateMatchesConfig(state *core.ResumeState, config core.Config) bool {
	return state != nil &&
		state.Scope.ServerAddress == config.ServerAddress &&
		state.Scope.ServerPort == config.ServerPort &&
		(state.Scope.Username == "" || state.Scope.Username == config.Username)
}

func cloneResumeState(state *core.ResumeState) *core.ResumeState {
	if state == nil {
		return nil
	}
	cloned := *state
	return &cloned
}

func (controller *Controller) cacheResumeStateLocked(id core.SessionID) {
	state, err := controller.Manager.ResumeState(id)
	if err == nil {
		controller.storeResumeState(&state)
	}
}

func (controller *Controller) storeResumeState(state *core.ResumeState) {
	controller.mu.Lock()
	controller.resumeState = cloneResumeState(state)
	controller.mu.Unlock()
}

func (controller *Controller) resumeStateSnapshot() *core.ResumeState {
	controller.mu.RLock()
	state := cloneResumeState(controller.resumeState)
	controller.mu.RUnlock()
	return state
}

func (controller *Controller) setActiveConfig(config daemonconfig.Config, id core.SessionID) {
	controller.mu.Lock()
	controller.active = config.Clone()
	controller.activeRevision++
	controller.activeSessionID = id
	controller.mu.Unlock()
}

func (controller *Controller) setActiveSessionID(id core.SessionID) {
	controller.mu.Lock()
	controller.activeSessionID = id
	controller.mu.Unlock()
}
