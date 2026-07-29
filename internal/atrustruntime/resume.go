package atrustruntime

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	atrustclient "github.com/Eclipsky1337/zju-portal-core/client/atrust"
	"github.com/Eclipsky1337/zju-portal-core/client/atrust/auth"
	"github.com/Eclipsky1337/zju-portal-core/core"
)

func writePrivateFile(path string, data []byte, _ os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

func decodeResumeState(config Config, state core.ResumeState) ([]byte, uint64, error) {
	if state.Format != core.ResumeStateFormatATrustClientData || state.Version != core.ResumeStateVersion1 {
		return nil, 0, core.WrapError(
			core.ErrorCodeResumeStateInvalid,
			fmt.Sprintf("unsupported resume state format %q version %d", state.Format, state.Version),
			false,
			nil,
		)
	}
	if !strings.EqualFold(state.Scope.ServerAddress, config.ServerAddress) || state.Scope.ServerPort != config.ServerPort {
		return nil, 0, core.WrapError(core.ErrorCodeResumeStateScope, "resume state server does not match session config", false, nil)
	}
	if state.Scope.Username != "" && config.Username != "" && state.Scope.Username != config.Username {
		return nil, 0, core.WrapError(core.ErrorCodeResumeStateScope, "resume state username does not match session config", false, nil)
	}
	data, err := base64.StdEncoding.DecodeString(state.Data)
	if err != nil {
		return nil, 0, core.WrapError(core.ErrorCodeResumeStateInvalid, "decode resume state data", false, err)
	}
	var clientData auth.ClientAuthData
	if err := json.Unmarshal(data, &clientData); err != nil {
		return nil, 0, core.WrapError(core.ErrorCodeResumeStateInvalid, "parse resume state client data", false, err)
	}
	if clientData.DeviceID == "" {
		return nil, 0, core.WrapError(core.ErrorCodeResumeStateInvalid, "resume state device ID is empty", false, nil)
	}
	return data, state.Revision, nil
}

func encodeResumeState(config Config, client *atrustclient.Client, data []byte, revision uint64) core.ResumeState {
	if len(data) == 0 {
		return core.ResumeState{}
	}
	username := client.Username
	if username == "" {
		username = config.Username
	}
	return core.ResumeState{
		Format:   core.ResumeStateFormatATrustClientData,
		Version:  core.ResumeStateVersion1,
		Revision: revision,
		Scope: core.ResumeStateScope{
			ServerAddress: config.ServerAddress,
			ServerPort:    config.ServerPort,
			Username:      username,
		},
		UpdatedAt: time.Now(),
		Data:      base64.StdEncoding.EncodeToString(data),
		Reused:    client.ResumeStateReused,
	}
}

func (s *Session) ResumeState() (core.ResumeState, error) {
	s.mu.RLock()
	state := s.resumeState
	s.mu.RUnlock()
	if state.Data == "" {
		return core.ResumeState{}, core.WrapError(core.ErrorCodeResumeStateUnavailable, "resume state is unavailable", true, nil)
	}
	return state, nil
}
