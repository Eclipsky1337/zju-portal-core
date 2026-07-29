package atrustruntime

import (
	"encoding/base64"
	"testing"

	atrustclient "github.com/Eclipsky1337/zju-portal-core/client/atrust"
	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestDecodeResumeStateValidatesScopeAndPayload(t *testing.T) {
	data := []byte(`{"cookies":[],"device_id":"device-1"}`)
	state := core.ResumeState{
		Format:   core.ResumeStateFormatATrustClientData,
		Version:  core.ResumeStateVersion1,
		Revision: 4,
		Scope: core.ResumeStateScope{
			ServerAddress: "vpn.example.edu",
			ServerPort:    443,
			Username:      "user",
		},
		Data: base64.StdEncoding.EncodeToString(data),
	}
	config := Config{ServerAddress: "VPN.EXAMPLE.EDU", ServerPort: 443, Username: "user"}

	decoded, revision, err := decodeResumeState(config, state)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(data) || revision != 4 {
		t.Fatalf("decoded/revision = %s/%d", decoded, revision)
	}

	state.Scope.Username = "other"
	if _, _, err := decodeResumeState(config, state); core.ErrorCodeOf(err) != core.ErrorCodeResumeStateScope {
		t.Fatalf("scope error = %v", err)
	}
}

func TestEncodeResumeStateUsesUpdatedClientIdentity(t *testing.T) {
	client := atrustclient.NewClient("actual-user", "", "", "")
	client.ResumeStateReused = true
	state := encodeResumeState(Config{ServerAddress: "vpn.example.edu", ServerPort: 443, Username: "configured-user"}, client, []byte(`{"device_id":"device-1"}`), 3)

	if state.Format != core.ResumeStateFormatATrustClientData || state.Version != core.ResumeStateVersion1 || state.Revision != 3 {
		t.Fatalf("state = %#v", state)
	}
	if state.Scope.Username != "actual-user" || !state.Reused || state.Data == "" || state.UpdatedAt.IsZero() {
		t.Fatalf("state = %#v", state)
	}
}
