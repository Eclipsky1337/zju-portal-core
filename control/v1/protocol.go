package v1

import (
	"encoding/json"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/daemonconfig"
)

const ProtocolVersion = 1

const (
	MethodHello                    = "hello"
	MethodSessionStart             = "session.start"
	MethodAuthRespond              = "auth.respond"
	MethodSessionStop              = "session.stop"
	MethodSessionStatus            = "session.status"
	MethodResourcesGet             = "resources.get"
	MethodResourcesRefresh         = "resources.refresh"
	MethodServicesGet              = "services.get"
	MethodTrafficGet               = "traffic.get"
	MethodConnectionsList          = "connections.list"
	MethodConnectionClose          = "connection.close"
	MethodTransportConnectionsList = "transport_connections.list"
	MethodRoutingModeGet           = "routing.mode.get"
	MethodRoutingModeSet           = "routing.mode.set"
	MethodResumeStateGet           = "resume_state.get"
	MethodConfigGet                = "config.get"
	MethodConfigSet                = "config.set"
	MethodConfigReload             = "config.reload"
)

type Request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result,omitempty"`
	Error  *core.Error     `json:"error,omitempty"`
}

type Event struct {
	Event  core.EventType `json:"event"`
	Params core.Event     `json:"params"`
}

type HelloParams struct {
	ProtocolVersion int `json:"protocol_version"`
}

type HelloResult struct {
	CoreVersion     string   `json:"core_version"`
	ProtocolVersion int      `json:"protocol_version"`
	Capabilities    []string `json:"capabilities"`
}

type SessionStartParams struct {
	Config      core.Config       `json:"config"`
	ResumeState *core.ResumeState `json:"resume_state,omitempty"`
}

type SessionIDParams struct {
	SessionID core.SessionID `json:"session_id"`
}

type SessionStartResult struct {
	SessionID           core.SessionID `json:"session_id"`
	ResumeStateRevision uint64         `json:"resume_state_revision,omitempty"`
	ResumeStateReused   bool           `json:"resume_state_reused,omitempty"`
}

type SessionStopResult struct {
	Stopped bool `json:"stopped"`
}

type ConnectionCloseParams struct {
	SessionID    core.SessionID `json:"session_id"`
	ConnectionID string         `json:"connection_id"`
}

type ConnectionCloseResult struct {
	Closed bool `json:"closed"`
}

type RoutingModeSetParams struct {
	SessionID core.SessionID   `json:"session_id"`
	Mode      core.RoutingMode `json:"mode"`
}

type RoutingModeResult struct {
	Mode core.RoutingMode `json:"mode"`
}

type ConfigSetParams struct {
	Config daemonconfig.Config `json:"config"`
}

type ConfigResult struct {
	Config daemonconfig.Config `json:"config"`
}

type ConfigReloadResult struct {
	Reloaded bool `json:"reloaded"`
}
