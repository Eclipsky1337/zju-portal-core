package core

import "testing"

func TestSessionStateMainPathTransitions(t *testing.T) {
	states := []SessionState{
		SessionStateIdle,
		SessionStateDiscoveringAuth,
		SessionStateAuthenticating,
		SessionStateFetchingResources,
		SessionStateSelectingNodes,
		SessionStateEstablishingTunnel,
		SessionStateReady,
		SessionStateStopping,
		SessionStateStopped,
	}

	for index, state := range states {
		if !state.Valid() {
			t.Fatalf("state %q is invalid", state)
		}
		if index == len(states)-1 {
			break
		}
		if err := ValidateSessionStateTransition(state, states[index+1]); err != nil {
			t.Fatalf("transition %q -> %q: %v", state, states[index+1], err)
		}
	}
}

func TestSessionStateFailureAndReconnectTransitions(t *testing.T) {
	tests := []struct {
		from SessionState
		to   SessionState
	}{
		{SessionStateReady, SessionStateReconnecting},
		{SessionStateReconnecting, SessionStateEstablishingTunnel},
		{SessionStateReconnecting, SessionStateReady},
		{SessionStateAuthenticating, SessionStateFailed},
		{SessionStateFailed, SessionStateStopping},
	}

	for _, test := range tests {
		if !CanTransitionSessionState(test.from, test.to) {
			t.Fatalf("transition %q -> %q is not allowed", test.from, test.to)
		}
	}
}

func TestSessionStateRejectsInvalidTransitions(t *testing.T) {
	tests := []struct {
		from SessionState
		to   SessionState
	}{
		{SessionStateIdle, SessionStateReady},
		{SessionStateReady, SessionStateAuthenticating},
		{SessionStateStopped, SessionStateIdle},
		{SessionStateReady, SessionStateReady},
		{"unknown", SessionStateStopping},
	}

	for _, test := range tests {
		err := ValidateSessionStateTransition(test.from, test.to)
		if err == nil {
			t.Fatalf("transition %q -> %q succeeded", test.from, test.to)
		}
		if code := ErrorCodeOf(err); code != ErrorCodeInvalidStateTransition {
			t.Fatalf("transition %q -> %q error code = %q", test.from, test.to, code)
		}
	}
}

func TestSessionStateTerminal(t *testing.T) {
	if !SessionStateStopped.Terminal() {
		t.Fatal("stopped state is not terminal")
	}
	if SessionStateFailed.Terminal() {
		t.Fatal("failed state is terminal before cleanup")
	}
}
