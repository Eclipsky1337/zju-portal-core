package core

import (
	"context"
	"net"
)

type Component interface {
	Start(ctx context.Context) error
	Close(ctx context.Context) error
}

type Outbound interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	Close(ctx context.Context) error
}

type Manager interface {
	Start(ctx context.Context, config Config) (SessionID, error)
	RespondAuth(ctx context.Context, response AuthResponse) error
	Stop(ctx context.Context, id SessionID) error
	Close(ctx context.Context) error
	Status(id SessionID) SessionStatus
	Resources(id SessionID) (Resources, error)
	RefreshResources(ctx context.Context, id SessionID) (Resources, error)
	Outbound(id SessionID) (Outbound, error)
	Services(id SessionID) ([]ServiceStatus, error)
	TrafficStats(id SessionID) (TrafficStats, error)
	Connections(id SessionID) ([]ConnectionInfo, error)
	CloseConnection(id SessionID, connectionID string) error
	TransportConnections(id SessionID) ([]TransportConnectionInfo, error)
	RoutingMode(id SessionID) (RoutingMode, error)
	SetRoutingMode(id SessionID, mode RoutingMode) error
	ResumeState(id SessionID) (ResumeState, error)
	Events() <-chan Event
}
