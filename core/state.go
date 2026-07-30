package core

import "fmt"

type SessionID string

type SessionState string

const (
	SessionStateIdle               SessionState = "idle"
	SessionStateDiscoveringAuth    SessionState = "discovering_auth"
	SessionStateAuthenticating     SessionState = "authenticating"
	SessionStateFetchingResources  SessionState = "fetching_resources"
	SessionStateSelectingNodes     SessionState = "selecting_nodes"
	SessionStateEstablishingTunnel SessionState = "establishing_tunnel"
	SessionStateReady              SessionState = "ready"
	SessionStateReconnecting       SessionState = "reconnecting"
	SessionStateFailed             SessionState = "failed"
	SessionStateStopping           SessionState = "stopping"
	SessionStateStopped            SessionState = "stopped"
)

var sessionStateTransitions = map[SessionState]map[SessionState]struct{}{
	SessionStateIdle: {
		SessionStateDiscoveringAuth: {},
		SessionStateFailed:          {},
		SessionStateStopping:        {},
	},
	SessionStateDiscoveringAuth: {
		SessionStateAuthenticating: {},
		SessionStateFailed:         {},
		SessionStateStopping:       {},
	},
	SessionStateAuthenticating: {
		SessionStateFetchingResources: {},
		SessionStateFailed:            {},
		SessionStateStopping:          {},
	},
	SessionStateFetchingResources: {
		SessionStateSelectingNodes: {},
		SessionStateFailed:         {},
		SessionStateStopping:       {},
	},
	SessionStateSelectingNodes: {
		SessionStateEstablishingTunnel: {},
		SessionStateFailed:             {},
		SessionStateStopping:           {},
	},
	SessionStateEstablishingTunnel: {
		SessionStateReady:    {},
		SessionStateFailed:   {},
		SessionStateStopping: {},
	},
	SessionStateReady: {
		SessionStateReconnecting: {},
		SessionStateFailed:       {},
		SessionStateStopping:     {},
	},
	SessionStateReconnecting: {
		SessionStateReady:    {},
		SessionStateFailed:   {},
		SessionStateStopping: {},
	},
	SessionStateFailed: {
		SessionStateStopping: {},
	},
	SessionStateStopping: {
		SessionStateStopped: {},
	},
	SessionStateStopped: {},
}

func (s SessionState) Valid() bool {
	_, ok := sessionStateTransitions[s]
	return ok
}

func (s SessionState) Terminal() bool {
	return s == SessionStateStopped
}

func CanTransitionSessionState(from, to SessionState) bool {
	transitions, ok := sessionStateTransitions[from]
	if !ok {
		return false
	}
	_, ok = transitions[to]
	return ok
}

func ValidateSessionStateTransition(from, to SessionState) error {
	if CanTransitionSessionState(from, to) {
		return nil
	}
	return WrapError(
		ErrorCodeInvalidStateTransition,
		fmt.Sprintf("cannot transition session state from %q to %q", from, to),
		false,
		nil,
	)
}
