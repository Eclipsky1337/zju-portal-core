package core

import (
	"net"
	"time"
)

type RouteInfo struct {
	Outbound string `json:"outbound"`
	Reason   string `json:"reason"`
}

func RouteInfoOf(conn net.Conn) RouteInfo {
	provider, ok := conn.(interface{ RouteInfo() RouteInfo })
	if !ok {
		return RouteInfo{}
	}
	return provider.RouteInfo()
}

type ConnectionState string

const (
	ConnectionStateActive ConnectionState = "active"
	ConnectionStateIdle   ConnectionState = "idle"
)

type ConnectionMetadata struct {
	Inbound               string
	Outbound              string
	RouteReason           string
	Source                string
	Network               string
	Destination           string
	TransportConnectionID string
}

type ConnectionActivity interface {
	RecordUploaded(uint64)
	RecordDownloaded(uint64)
	Close() error
}

type ConnectionObserver interface {
	OpenConnection(ConnectionMetadata, func() error) ConnectionActivity
}

type TrafficStats struct {
	SessionID                 SessionID `json:"session_id"`
	UploadedBytes             uint64    `json:"uploaded_bytes"`
	DownloadedBytes           uint64    `json:"downloaded_bytes"`
	ActiveConnections         int       `json:"active_connections"`
	TotalConnections          uint64    `json:"total_connections"`
	OpenTransportConnections  int       `json:"open_transport_connections"`
	TotalTransportConnections uint64    `json:"total_transport_connections"`
	StartedAt                 time.Time `json:"started_at"`
	Timestamp                 time.Time `json:"timestamp"`
}

type ConnectionInfo struct {
	ID                    string          `json:"id"`
	SessionID             SessionID       `json:"session_id"`
	Inbound               string          `json:"inbound"`
	Outbound              string          `json:"outbound,omitempty"`
	RouteReason           string          `json:"route_reason,omitempty"`
	Source                string          `json:"source,omitempty"`
	Network               string          `json:"network"`
	Destination           string          `json:"destination"`
	UploadedBytes         uint64          `json:"uploaded_bytes"`
	DownloadedBytes       uint64          `json:"downloaded_bytes"`
	OpenedAt              time.Time       `json:"opened_at"`
	LastActivityAt        time.Time       `json:"last_activity_at"`
	State                 ConnectionState `json:"state"`
	TransportConnectionID string          `json:"transport_connection_id,omitempty"`
}

type TransportConnectionInfo struct {
	ID              string          `json:"id"`
	SessionID       SessionID       `json:"session_id"`
	Outbound        string          `json:"outbound,omitempty"`
	RouteReason     string          `json:"route_reason,omitempty"`
	Network         string          `json:"network"`
	Destination     string          `json:"destination"`
	UploadedBytes   uint64          `json:"uploaded_bytes"`
	DownloadedBytes uint64          `json:"downloaded_bytes"`
	OpenedAt        time.Time       `json:"opened_at"`
	LastActivityAt  time.Time       `json:"last_activity_at"`
	State           ConnectionState `json:"state"`
}
