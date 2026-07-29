package core

import (
	"errors"
	"testing"
)

func TestWrapErrorPreservesMetadataAndCause(t *testing.T) {
	cause := errors.New("gateway unavailable")
	err := WrapError(ErrorCodeATrustSetupFailed, "start aTrust session", true, cause)

	if got := err.Error(); got != "start aTrust session: gateway unavailable" {
		t.Fatalf("Error() = %q", got)
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped error does not preserve its cause")
	}
	if code := ErrorCodeOf(err); code != ErrorCodeATrustSetupFailed {
		t.Fatalf("ErrorCodeOf() = %q", code)
	}
	if !IsRetryable(err) {
		t.Fatal("IsRetryable() = false")
	}
	if err.Detail != cause.Error() {
		t.Fatalf("Detail = %q", err.Detail)
	}
}

func TestWrapErrorPreservesSensitiveDetailAndCause(t *testing.T) {
	cause := errors.New(`request failed with cookie "secret"`)
	err := WrapError(ErrorCodeATrustSetupFailed, "start aTrust session", false, cause)
	if err.Detail != cause.Error() || err.Error() != "start aTrust session: "+cause.Error() {
		t.Fatalf("error detail = %#v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped error does not preserve its cause")
	}
}

func TestUnknownErrorMetadata(t *testing.T) {
	err := errors.New("plain error")
	if code := ErrorCodeOf(err); code != ErrorCodeUnknown {
		t.Fatalf("ErrorCodeOf() = %q", code)
	}
	if IsRetryable(err) {
		t.Fatal("IsRetryable() = true for plain error")
	}
}
