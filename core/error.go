package core

import "errors"

type ErrorCode string

const (
	ErrorCodeUnknown                ErrorCode = "UNKNOWN"
	ErrorCodeResourceDataReadFailed ErrorCode = "RESOURCE_DATA_READ_FAILED"
	ErrorCodeClientDataWriteFailed  ErrorCode = "CLIENT_DATA_WRITE_FAILED"
	ErrorCodeATrustSetupFailed      ErrorCode = "ATRUST_SETUP_FAILED"
	ErrorCodeInvalidStateTransition ErrorCode = "INVALID_STATE_TRANSITION"
	ErrorCodeAuthChallengeInvalid   ErrorCode = "AUTH_CHALLENGE_INVALID"
	ErrorCodeAuthResponseInvalid    ErrorCode = "AUTH_RESPONSE_INVALID"
	ErrorCodeAuthHandlerUnavailable ErrorCode = "AUTH_HANDLER_UNAVAILABLE"
	ErrorCodeSessionStartFailed     ErrorCode = "SESSION_START_FAILED"
	ErrorCodeSessionReconnectFailed ErrorCode = "SESSION_RECONNECT_FAILED"
	ErrorCodeSessionCloseFailed     ErrorCode = "SESSION_CLOSE_FAILED"
	ErrorCodeInvalidRequest         ErrorCode = "INVALID_REQUEST"
	ErrorCodeMethodNotFound         ErrorCode = "METHOD_NOT_FOUND"
	ErrorCodeProtocolUnsupported    ErrorCode = "PROTOCOL_VERSION_UNSUPPORTED"
	ErrorCodeSessionNotFound        ErrorCode = "SESSION_NOT_FOUND"
	ErrorCodeSessionNotReady        ErrorCode = "SESSION_NOT_READY"
	ErrorCodeResourcesUnavailable   ErrorCode = "RESOURCES_UNAVAILABLE"
	ErrorCodeNetworkSetupFailed     ErrorCode = "NETWORK_SETUP_FAILED"
	ErrorCodeOutboundUnavailable    ErrorCode = "OUTBOUND_UNAVAILABLE"
	ErrorCodeAuthChallengeNotFound  ErrorCode = "AUTH_CHALLENGE_NOT_FOUND"
	ErrorCodeConnectionNotFound     ErrorCode = "CONNECTION_NOT_FOUND"
	ErrorCodeResumeStateInvalid     ErrorCode = "RESUME_STATE_INVALID"
	ErrorCodeResumeStateScope       ErrorCode = "RESUME_STATE_SCOPE_MISMATCH"
	ErrorCodeResumeStateUnavailable ErrorCode = "RESUME_STATE_UNAVAILABLE"
	ErrorCodeConfigInvalid          ErrorCode = "CONFIG_INVALID"
	ErrorCodeConfigUnavailable      ErrorCode = "CONFIG_UNAVAILABLE"
	ErrorCodeRestartRequired        ErrorCode = "RESTART_REQUIRED"
	ErrorCodeAddressInUse           ErrorCode = "ADDRESS_IN_USE"
	ErrorCodePermissionDenied       ErrorCode = "PERMISSION_DENIED"
	ErrorCodeTUNUnavailable         ErrorCode = "TUN_UNAVAILABLE"
	ErrorCodeInterfaceUnavailable   ErrorCode = "INTERFACE_UNAVAILABLE"
	ErrorCodeRouteSetupFailed       ErrorCode = "ROUTE_SETUP_FAILED"
	ErrorCodeDNSStartFailed         ErrorCode = "DNS_START_FAILED"
)

type Error struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Detail    string    `json:"detail,omitempty"`
	Retryable bool      `json:"retryable"`
	Err       error     `json:"-"`
}

func (e *Error) Error() string {
	if e.Err == nil {
		return e.Message
	}
	if e.Message == "" {
		return e.Err.Error()
	}
	if e.Detail == "" {
		return e.Message
	}
	return e.Message + ": " + e.Detail
}

func (e *Error) Unwrap() error {
	return e.Err
}

func WrapError(code ErrorCode, message string, retryable bool, err error) *Error {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	return &Error{
		Code:      code,
		Message:   message,
		Detail:    detail,
		Retryable: retryable,
		Err:       err,
	}
}

func ErrorCodeOf(err error) ErrorCode {
	var coreError *Error
	if errors.As(err, &coreError) {
		return coreError.Code
	}
	return ErrorCodeUnknown
}

func IsRetryable(err error) bool {
	var coreError *Error
	return errors.As(err, &coreError) && coreError.Retryable
}
