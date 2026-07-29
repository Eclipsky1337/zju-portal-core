package core

type Config struct {
	Protocol               string                 `json:"protocol,omitempty"`
	ResumeState            *ResumeState           `json:"-"`
	SessionID              SessionID              `json:"session_id,omitempty"`
	ServerAddress          string                 `json:"server_address"`
	ServerPort             int                    `json:"server_port"`
	Username               string                 `json:"username,omitempty"`
	Password               string                 `json:"password,omitempty"`
	Phone                  string                 `json:"phone,omitempty"`
	LoginDomain            string                 `json:"login_domain,omitempty"`
	AuthType               string                 `json:"auth_type,omitempty"`
	GraphCodeFile          string                 `json:"graph_code_file,omitempty"`
	CASTicket              string                 `json:"cas_ticket,omitempty"`
	OAuth2Code             string                 `json:"oauth2_code,omitempty"`
	SID                    string                 `json:"sid,omitempty"`
	DeviceID               string                 `json:"device_id,omitempty"`
	SignKey                string                 `json:"sign_key,omitempty"`
	ClientDataFile         string                 `json:"client_data_file,omitempty"`
	UpdateBestNodesSeconds int                    `json:"update_best_nodes_seconds,omitempty"`
	BindInterface          string                 `json:"bind_interface,omitempty"`
	AutoDetectInterface    bool                   `json:"auto_detect_interface,omitempty"`
	DisableAutoReconnect   bool                   `json:"disable_auto_reconnect,omitempty"`
	DisableRemoteDNS       bool                   `json:"disable_remote_dns,omitempty"`
	RemoteDNSServer        string                 `json:"remote_dns_server,omitempty"`
	SecondaryDNSServer     string                 `json:"secondary_dns_server,omitempty"`
	DNSTTL                 uint64                 `json:"dns_ttl,omitempty"`
	DNSBind                string                 `json:"dns_bind,omitempty"`
	Hosts                  map[string]string      `json:"hosts,omitempty"`
	SOCKSBind              string                 `json:"socks_bind,omitempty"`
	SOCKSUsername          string                 `json:"socks_username,omitempty"`
	SOCKSPassword          string                 `json:"socks_password,omitempty"`
	HTTPBind               string                 `json:"http_bind,omitempty"`
	TUNEnabled             bool                   `json:"tun_enabled,omitempty"`
	TUNName                string                 `json:"tun_name,omitempty"`
	TUNAddress             string                 `json:"tun_address,omitempty"`
	TUNMTU                 uint32                 `json:"tun_mtu,omitempty"`
	TUNAutoRoute           bool                   `json:"tun_auto_route,omitempty"`
	TUNOutboundInterface   string                 `json:"tun_outbound_interface,omitempty"`
	TUNUDPTimeoutSeconds   int                    `json:"tun_udp_timeout_seconds,omitempty"`
	TUNUDPMaxFlows         int                    `json:"tun_udp_max_flows,omitempty"`
	TUNDNSHijack           bool                   `json:"tun_dns_hijack,omitempty"`
	TUNFakeIP              bool                   `json:"tun_fake_ip,omitempty"`
	TUNFakeIPRange         string                 `json:"tun_fake_ip_range,omitempty"`
	RoutingMode            RoutingMode            `json:"routing_mode,omitempty"`
	InternetOutbound       InternetOutboundConfig `json:"internet_outbound,omitempty"`
}

type InternetOutboundType string

const (
	InternetOutboundDirect InternetOutboundType = "direct"
	InternetOutboundSOCKS5 InternetOutboundType = "socks5"
	InternetOutboundReject InternetOutboundType = "reject"
)

func (outboundType InternetOutboundType) Valid() bool {
	return outboundType == InternetOutboundDirect || outboundType == InternetOutboundSOCKS5 || outboundType == InternetOutboundReject
}

type RoutingMode string

const (
	RoutingModeGlobal RoutingMode = "global"
	RoutingModeRule   RoutingMode = "rule"
	RoutingModeDirect RoutingMode = "direct"
)

func (mode RoutingMode) Valid() bool {
	switch mode {
	case RoutingModeGlobal, RoutingModeRule, RoutingModeDirect:
		return true
	default:
		return false
	}
}

type InternetOutboundConfig struct {
	Type     InternetOutboundType `json:"type,omitempty"`
	Address  string               `json:"address,omitempty"`
	Username string               `json:"username,omitempty"`
	Password string               `json:"password,omitempty"`
}
