package core

import (
	"testing"
	"time"
)

func TestNewStateChangedEvent(t *testing.T) {
	timestamp := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	event := NewStateChangedEvent("session-1", SessionStateAuthenticating, SessionStateFetchingResources, timestamp)

	if event.SessionID != "session-1" || event.Type != EventTypeSessionStateChanged {
		t.Fatalf("event identity = %#v", event)
	}
	if event.PreviousState != SessionStateAuthenticating || event.State != SessionStateFetchingResources {
		t.Fatalf("event transition = %#v", event)
	}
	if !event.Timestamp.Equal(timestamp) {
		t.Fatalf("event timestamp = %v", event.Timestamp)
	}
}

func TestRuntimeEventConstructorsCopyPayloads(t *testing.T) {
	timestamp := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	resources := Resources{
		ClientIP:        "10.0.0.2",
		IPResources:     []IPResource{{IPMin: "10.0.0.1"}},
		DomainResources: map[string]DomainResource{"example.edu": {}},
		DNSRecords:      map[string]string{"app.example.edu": "10.0.0.8"},
	}
	resourceEvent := NewResourcesUpdatedEvent("session-1", resources, timestamp)
	if resourceEvent.Resources == nil || resourceEvent.Resources.IPResourceCount != 1 || resourceEvent.Resources.DomainResourceCount != 1 || resourceEvent.Resources.DNSRecordCount != 1 {
		t.Fatalf("resource event = %#v", resourceEvent)
	}

	nodes := map[string]string{"group-1": "192.0.2.1:443"}
	nodeEvent := NewNodeSelectedEvent("session-1", nodes, timestamp)
	nodes["group-1"] = "mutated"
	if nodeEvent.SelectedNodes["group-1"] != "192.0.2.1:443" {
		t.Fatalf("node event payload was mutated: %#v", nodeEvent)
	}

	serviceEvent := NewServiceEvent("session-1", EventTypeServiceStopped, ServiceStatus{Type: ServiceTypeHTTP, Address: "127.0.0.1:1081", Running: true}, timestamp)
	if serviceEvent.Service == nil || serviceEvent.Service.Running {
		t.Fatalf("service event = %#v", serviceEvent)
	}
}

func TestNewResumeStateInvalidatedEvent(t *testing.T) {
	timestamp := time.Date(2026, time.July, 28, 13, 0, 0, 0, time.UTC)
	event := NewResumeStateInvalidatedEvent("session-1", 4, timestamp)
	if event.Type != EventTypeResumeStateInvalidated || event.ResumeStateRevision != 4 || !event.Timestamp.Equal(timestamp) {
		t.Fatalf("event = %#v", event)
	}
}
