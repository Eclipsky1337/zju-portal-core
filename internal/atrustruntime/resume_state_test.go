package atrustruntime

import (
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestSessionResumeStateRemainsAvailableWithoutActiveRuntime(t *testing.T) {
	want := core.ResumeState{Revision: 4, Data: "cached-state"}
	session := newSession("session-resume-cache", Config{}, defaultDependencies())
	session.state = core.SessionStateReconnecting
	session.resumeState = want

	got, err := session.ResumeState()
	if err != nil || got.Revision != want.Revision || got.Data != want.Data {
		t.Fatalf("ResumeState() while reconnecting = %#v, %v", got, err)
	}
	session.state = core.SessionStateStopped
	got, err = session.ResumeState()
	if err != nil || got.Revision != want.Revision || got.Data != want.Data {
		t.Fatalf("ResumeState() after stop = %#v, %v", got, err)
	}
}

func TestSessionResumeStateReportsUnavailableBeforeFirstUpdate(t *testing.T) {
	session := newSession("session-no-resume", Config{}, defaultDependencies())
	if _, err := session.ResumeState(); core.ErrorCodeOf(err) != core.ErrorCodeResumeStateUnavailable {
		t.Fatalf("ResumeState() error = %v", err)
	}
}
