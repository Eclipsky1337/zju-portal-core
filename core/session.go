package core

import "time"

const (
	ResumeStateFormatATrustClientData = "atrust-client-data"
	ResumeStateVersion1               = 1
)

type ResumeStateScope struct {
	ServerAddress string `json:"server_address"`
	ServerPort    int    `json:"server_port"`
	Username      string `json:"username,omitempty"`
}

type ResumeState struct {
	Format    string           `json:"format"`
	Version   int              `json:"version"`
	Revision  uint64           `json:"revision"`
	Scope     ResumeStateScope `json:"scope"`
	UpdatedAt time.Time        `json:"updated_at"`
	Data      string           `json:"data"`
	Reused    bool             `json:"reused"`
}

type Resources struct {
	Stale           bool                      `json:"stale,omitempty"`
	ClientIP        string                    `json:"client_ip,omitempty"`
	IPResources     []IPResource              `json:"ip_resources"`
	DomainResources map[string]DomainResource `json:"domain_resources"`
	DNSRecords      map[string]string         `json:"dns_records"`
	DNSServer       string                    `json:"dns_server,omitempty"`
}

type IPResource struct {
	IPMin       string `json:"ip_min"`
	IPMax       string `json:"ip_max"`
	PortMin     int    `json:"port_min"`
	PortMax     int    `json:"port_max"`
	Protocol    string `json:"protocol"`
	AppID       string `json:"app_id,omitempty"`
	NodeGroupID string `json:"node_group_id,omitempty"`
}

type DomainResource struct {
	PortMin     int    `json:"port_min"`
	PortMax     int    `json:"port_max"`
	Protocol    string `json:"protocol"`
	AppID       string `json:"app_id,omitempty"`
	NodeGroupID string `json:"node_group_id,omitempty"`
}

type ServiceType string

const (
	ServiceTypeSOCKS5 ServiceType = "socks5"
	ServiceTypeHTTP   ServiceType = "http"
	ServiceTypeTUN    ServiceType = "tun"
	ServiceTypeDNS    ServiceType = "dns"
)

type ServiceStatus struct {
	Type      ServiceType `json:"type"`
	Address   string      `json:"address"`
	Running   bool        `json:"running"`
	LastError string      `json:"last_error,omitempty"`
}

type SessionStatus struct {
	ID        SessionID    `json:"id"`
	State     SessionState `json:"state"`
	LastError *Error       `json:"last_error,omitempty"`
}

type CleanupResult struct {
	Component string `json:"component"`
	Error     string `json:"error,omitempty"`
}

type CleanupReport struct {
	StartedAt   time.Time       `json:"started_at"`
	CompletedAt time.Time       `json:"completed_at"`
	Results     []CleanupResult `json:"results"`
}

func (report CleanupReport) HasErrors() bool {
	for _, result := range report.Results {
		if result.Error != "" {
			return true
		}
	}
	return false
}
