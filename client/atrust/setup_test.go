package atrust

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/client/atrust/auth"
)

func TestSetupContextHonorsPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClient("", "", "", "")
	defer client.Close()

	_, err := client.SetupContext(ctx, SetupConfig{}, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SetupContext() error = %v", err)
	}
	if client.underlayDialer != nil {
		t.Fatal("SetupContext() configured underlay after cancellation")
	}
}

func TestAuthenticateWithSavedStateSkipsLogin(t *testing.T) {
	client := NewClient("user", "sid", "device-id", "")
	defer client.Close()
	authData := []byte(`{"cookies":[]}`)
	resourceData := []byte(`{"data":{}}`)

	gotAuthData, gotResourceData, err := client.authenticateAndFetchResources(context.Background(), SetupConfig{}, authData, resourceData)
	if err != nil {
		t.Fatalf("authenticateAndFetchResources() error = %v", err)
	}
	if !bytes.Equal(gotAuthData, authData) || !bytes.Equal(gotResourceData, resourceData) {
		t.Fatalf("returned data = %s, %s", gotAuthData, gotResourceData)
	}
	if client.ConnectionID == "" {
		t.Fatal("ConnectionID is empty")
	}
	if len(client.SignKey) != 64 {
		t.Fatalf("SignKey length = %d", len(client.SignKey))
	}
	if !client.ResumeStateReused {
		t.Fatal("ResumeStateReused = false")
	}
}

func TestAuthenticateRejectsMalformedClientData(t *testing.T) {
	client := NewClient("", "", "", "")
	defer client.Close()

	_, _, err := client.authenticateAndFetchResources(context.Background(), SetupConfig{}, []byte(`{"cookies":`), nil)
	if err == nil {
		t.Fatal("authenticateAndFetchResources() error = nil")
	}
}

func TestBuildLoginMethod(t *testing.T) {
	tests := []struct {
		authType string
		check    func(auth.LoginMethod) bool
	}{
		{authType: "auth/psw", check: func(method auth.LoginMethod) bool { _, ok := method.(auth.PasswordLogin); return ok }},
		{authType: "auth/cas", check: func(method auth.LoginMethod) bool { _, ok := method.(auth.CASLogin); return ok }},
		{authType: "auth/httpsOauth2", check: func(method auth.LoginMethod) bool { _, ok := method.(auth.HTTPSOauth2Login); return ok }},
		{authType: "auth/smsCheckCode", check: func(method auth.LoginMethod) bool { _, ok := method.(auth.SMSLogin); return ok }},
	}
	for _, test := range tests {
		method, err := buildLoginMethod(SetupConfig{AuthType: test.authType})
		if err != nil {
			t.Fatalf("buildLoginMethod(%q) error = %v", test.authType, err)
		}
		if !test.check(method) {
			t.Fatalf("buildLoginMethod(%q) = %T", test.authType, method)
		}
	}

	method, err := buildLoginMethod(SetupConfig{})
	if err != nil || method != nil {
		t.Fatalf("buildLoginMethod(empty) = %T, %v", method, err)
	}
	_, err = buildLoginMethod(SetupConfig{AuthType: "unsupported"})
	if err == nil || !strings.Contains(err.Error(), "unsupported auth type") {
		t.Fatalf("buildLoginMethod(unsupported) error = %v", err)
	}
}

func TestSetupConfigNotifiesStages(t *testing.T) {
	var stages []SetupStage
	config := SetupConfig{StageHandler: func(stage SetupStage) {
		stages = append(stages, stage)
	}}
	for _, stage := range []SetupStage{SetupStageDiscoveringAuth, SetupStageAuthenticating, SetupStageFetchingResources} {
		config.notify(stage)
	}
	want := []SetupStage{SetupStageDiscoveringAuth, SetupStageAuthenticating, SetupStageFetchingResources}
	if len(stages) != len(want) {
		t.Fatalf("stages = %#v", stages)
	}
	for index := range want {
		if stages[index] != want[index] {
			t.Fatalf("stages[%d] = %q, want %q", index, stages[index], want[index])
		}
	}
}
