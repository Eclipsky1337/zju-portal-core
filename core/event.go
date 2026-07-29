package core

import "time"

type EventType string

const (
	EventTypeSessionStateChanged    EventType = "session.state_changed"
	EventTypeAuthRequired           EventType = "auth.required"
	EventTypeAuthBrowserRequired    EventType = "auth.browser_required"
	EventTypeAuthCompleted          EventType = "auth.completed"
	EventTypeResourcesUpdated       EventType = "resources.updated"
	EventTypeNodeSelected           EventType = "node.selected"
	EventTypeServiceStarted         EventType = "service.started"
	EventTypeServiceStopped         EventType = "service.stopped"
	EventTypeSessionError           EventType = "session.error"
	EventTypeReconnectScheduled     EventType = "session.reconnect_scheduled"
	EventTypeReconnectFailed        EventType = "session.reconnect_failed"
	EventTypeReconnected            EventType = "session.reconnected"
	EventTypeRoutingModeChanged     EventType = "routing.mode_changed"
	EventTypeShutdownCompleted      EventType = "shutdown.completed"
	EventTypeResumeStateUpdated     EventType = "session.resume_state_updated"
	EventTypeResumeStateInvalidated EventType = "session.resume_state_invalidated"
	EventTypeLog                    EventType = "log"
)

type Event struct {
	SessionID           SessionID         `json:"session_id"`
	Type                EventType         `json:"type"`
	Timestamp           time.Time         `json:"timestamp"`
	PreviousState       SessionState      `json:"previous_state,omitempty"`
	State               SessionState      `json:"state,omitempty"`
	Error               *Error            `json:"error,omitempty"`
	Cleanup             *CleanupReport    `json:"cleanup,omitempty"`
	Auth                *AuthChallenge    `json:"auth,omitempty"`
	Reconnect           *ReconnectInfo    `json:"reconnect,omitempty"`
	PreviousRoutingMode RoutingMode       `json:"previous_routing_mode,omitempty"`
	RoutingMode         RoutingMode       `json:"routing_mode,omitempty"`
	ResumeStateRevision uint64            `json:"resume_state_revision,omitempty"`
	ResumeStateReused   bool              `json:"resume_state_reused,omitempty"`
	Resources           *ResourceSummary  `json:"resources,omitempty"`
	SelectedNodes       map[string]string `json:"selected_nodes,omitempty"`
	Service             *ServiceStatus    `json:"service,omitempty"`
}

type ResourceSummary struct {
	ClientIP            string `json:"client_ip,omitempty"`
	IPResourceCount     int    `json:"ip_resource_count"`
	DomainResourceCount int    `json:"domain_resource_count"`
	DNSRecordCount      int    `json:"dns_record_count"`
	Stale               bool   `json:"stale,omitempty"`
}

type ReconnectInfo struct {
	Attempt     int   `json:"attempt"`
	DelayMillis int64 `json:"delay_ms,omitempty"`
}

func NewStateChangedEvent(sessionID SessionID, previous, current SessionState, timestamp time.Time) Event {
	return Event{
		SessionID:     sessionID,
		Type:          EventTypeSessionStateChanged,
		Timestamp:     timestamp,
		PreviousState: previous,
		State:         current,
	}
}

func NewResumeStateUpdatedEvent(sessionID SessionID, revision uint64, reused bool, timestamp time.Time) Event {
	return Event{
		SessionID:           sessionID,
		Type:                EventTypeResumeStateUpdated,
		Timestamp:           timestamp,
		ResumeStateRevision: revision,
		ResumeStateReused:   reused,
	}
}

func NewResumeStateInvalidatedEvent(sessionID SessionID, revision uint64, timestamp time.Time) Event {
	return Event{
		SessionID:           sessionID,
		Type:                EventTypeResumeStateInvalidated,
		Timestamp:           timestamp,
		ResumeStateRevision: revision,
	}
}

func NewResourcesUpdatedEvent(sessionID SessionID, resources Resources, timestamp time.Time) Event {
	return Event{
		SessionID: sessionID,
		Type:      EventTypeResourcesUpdated,
		Timestamp: timestamp,
		Resources: &ResourceSummary{
			ClientIP:            resources.ClientIP,
			IPResourceCount:     len(resources.IPResources),
			DomainResourceCount: len(resources.DomainResources),
			DNSRecordCount:      len(resources.DNSRecords),
			Stale:               resources.Stale,
		},
	}
}

func NewNodeSelectedEvent(sessionID SessionID, nodes map[string]string, timestamp time.Time) Event {
	selected := make(map[string]string, len(nodes))
	for group, address := range nodes {
		selected[group] = address
	}
	return Event{SessionID: sessionID, Type: EventTypeNodeSelected, Timestamp: timestamp, SelectedNodes: selected}
}

func NewServiceEvent(sessionID SessionID, eventType EventType, status ServiceStatus, timestamp time.Time) Event {
	status.Running = eventType == EventTypeServiceStarted
	return Event{SessionID: sessionID, Type: eventType, Timestamp: timestamp, Service: &status}
}
