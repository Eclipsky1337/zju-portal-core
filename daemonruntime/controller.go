package daemonruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/daemonconfig"
	zlog "github.com/Eclipsky1337/zju-portal-core/log"
)

type Controller struct {
	core.Manager
	configPath  string
	operationMu sync.Mutex

	mu          sync.RWMutex
	config      daemonconfig.Config
	initialized bool
	tunConfig   daemonconfig.TUNConfig
	resumeState *core.ResumeState
}

func New(manager core.Manager, configPath string) *Controller {
	return &Controller{Manager: manager, configPath: configPath}
}

func (controller *Controller) SetInitialResumeState(state *core.ResumeState) {
	controller.mu.Lock()
	controller.resumeState = state
	controller.mu.Unlock()
}

func (controller *Controller) Initialize(ctx context.Context, config daemonconfig.Config) error {
	controller.operationMu.Lock()
	defer controller.operationMu.Unlock()
	if err := config.Validate(); err != nil {
		return core.WrapError(core.ErrorCodeConfigInvalid, "validate configuration", false, err)
	}
	controller.mu.Lock()
	if controller.initialized {
		controller.mu.Unlock()
		return core.WrapError(core.ErrorCodeInvalidStateTransition, "configuration is already initialized", false, nil)
	}
	controller.mu.Unlock()
	if err := controller.apply(ctx, config); err != nil {
		return err
	}
	controller.mu.Lock()
	controller.initialized = true
	controller.tunConfig = config.TUNConfig()
	controller.mu.Unlock()
	return nil
}

func (controller *Controller) Config() daemonconfig.Config {
	controller.mu.RLock()
	config := controller.config
	controller.mu.RUnlock()
	return config.Clone()
}

func (controller *Controller) SetConfig(ctx context.Context, config daemonconfig.Config) error {
	controller.operationMu.Lock()
	defer controller.operationMu.Unlock()
	if err := config.Validate(); err != nil {
		return core.WrapError(core.ErrorCodeConfigInvalid, "validate configuration", false, err)
	}
	controller.mu.RLock()
	initialized := controller.initialized
	tunConfig := controller.tunConfig
	controller.mu.RUnlock()
	if initialized && !reflect.DeepEqual(tunConfig, config.TUNConfig()) {
		return core.WrapError(core.ErrorCodeRestartRequired, "TUN configuration changes require restarting Core", false, nil)
	}
	return controller.apply(ctx, config)
}

func (controller *Controller) ReloadConfig(ctx context.Context) error {
	if controller.configPath == "" {
		return core.WrapError(core.ErrorCodeConfigUnavailable, "no configuration file was specified", false, nil)
	}
	config, err := daemonconfig.Load(controller.configPath)
	if err != nil {
		return core.WrapError(core.ErrorCodeConfigInvalid, "reload configuration", false, err)
	}
	return controller.SetConfig(ctx, config)
}

func (controller *Controller) apply(ctx context.Context, config daemonconfig.Config) error {
	controller.mu.RLock()
	initialized := controller.initialized
	initialResumeState := controller.resumeState
	currentConfig := controller.config
	controller.mu.RUnlock()

	var currentResumeState *core.ResumeState
	if initialized && currentConfig.Session.AutoStart && currentConfig.Session.ID != "" {
		if state, err := controller.Manager.ResumeState(core.SessionID(currentConfig.Session.ID)); err == nil {
			currentResumeState = &state
		}
	}
	if config.Session.AutoStart {
		coreConfig := config.CoreConfig()
		if initialized {
			if resumeStateMatchesConfig(currentResumeState, coreConfig) {
				coreConfig.ResumeState = currentResumeState
			}
		} else if resumeStateMatchesConfig(initialResumeState, coreConfig) {
			coreConfig.ResumeState = initialResumeState
		}
		if _, err := controller.Manager.Start(ctx, coreConfig); err != nil {
			if initialized && currentConfig.Session.AutoStart {
				rollbackConfig := currentConfig.CoreConfig()
				if resumeStateMatchesConfig(currentResumeState, rollbackConfig) {
					rollbackConfig.ResumeState = currentResumeState
				}
				if _, rollbackErr := controller.Manager.Start(context.Background(), rollbackConfig); rollbackErr != nil {
					return core.WrapError(core.ErrorCodeSessionStartFailed, "apply configuration and restore previous session", false, errors.Join(err, rollbackErr))
				}
			}
			return err
		}
	} else {
		if currentConfig.Session.ID != "" {
			status := controller.Manager.Status(core.SessionID(currentConfig.Session.ID))
			if status.ID != "" {
				if err := controller.Manager.Stop(ctx, status.ID); err != nil {
					return err
				}
			}
		}
	}
	controller.mu.Lock()
	controller.config = config
	controller.mu.Unlock()
	for _, warning := range config.SecurityWarnings() {
		zlog.Printf("Security warning: %s", warning)
	}
	return nil
}

func resumeStateMatchesConfig(state *core.ResumeState, config core.Config) bool {
	if state == nil {
		return false
	}
	return state.Scope.ServerAddress == config.ServerAddress &&
		state.Scope.ServerPort == config.ServerPort &&
		(state.Scope.Username == "" || config.Username == "" || state.Scope.Username == config.Username)
}

func (controller *Controller) ConfigPath() string { return controller.configPath }

func (controller *Controller) String() string {
	controller.mu.RLock()
	defer controller.mu.RUnlock()
	return fmt.Sprintf("config(version=%d, session=%s)", controller.config.Version, controller.config.Session.ID)
}
