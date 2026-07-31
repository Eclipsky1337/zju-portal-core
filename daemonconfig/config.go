package daemonconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"gopkg.in/yaml.v3"
)

const (
	Version        = 1
	MaxHostEntries = 4096
	maxNameLength  = 256
	maxValueLength = 4096
)

type Duration time.Duration

func (duration *Duration) UnmarshalYAML(node *yaml.Node) error {
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	*duration = Duration(parsed)
	return nil
}
func (duration Duration) MarshalYAML() (any, error) { return time.Duration(duration).String(), nil }
func (duration *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	*duration = Duration(parsed)
	return nil
}
func (duration Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(duration).String())
}

func (config *Config) UnmarshalJSON(data []byte) error {
	type configAlias Config
	defaults := configAlias(Default())
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&defaults); err != nil {
		return err
	}
	*config = Config(defaults)
	return nil
}

type Config struct {
	Version  int            `yaml:"version" json:"version"`
	Log      LogConfig      `yaml:"log" json:"log"`
	Control  ControlConfig  `yaml:"control" json:"control"`
	Session  SessionConfig  `yaml:"session" json:"session"`
	State    StateConfig    `yaml:"state" json:"state"`
	ATrust   ATrustConfig   `yaml:"atrust" json:"atrust"`
	Underlay UnderlayConfig `yaml:"underlay" json:"underlay"`
	Routing  RoutingConfig  `yaml:"routing" json:"routing"`
	DNS      DNSConfig      `yaml:"dns" json:"dns"`
	Inbounds InboundsConfig `yaml:"inbounds" json:"inbounds"`
}

type LogConfig struct {
	Level  string `yaml:"level" json:"level"`
	Output string `yaml:"output" json:"output"`
}
type ControlConfig struct {
	Stdio StdioConfig `yaml:"stdio" json:"stdio"`
	REST  RESTConfig  `yaml:"rest" json:"rest"`
}
type StdioConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}
type RESTConfig struct {
	Enabled    bool   `yaml:"enabled" json:"enabled"`
	Listen     string `yaml:"listen" json:"listen"`
	Secret     string `yaml:"secret" json:"secret,omitempty"`
	SecretFile string `yaml:"secret-file" json:"secret-file,omitempty"`
}
type SessionConfig struct {
	ID            string `yaml:"id" json:"id"`
	AutoStart     bool   `yaml:"auto-start" json:"auto-start"`
	AutoReconnect bool   `yaml:"auto-reconnect" json:"auto-reconnect"`
}
type StateConfig struct {
	ResumeFile string `yaml:"resume-file" json:"resume-file"`
}
type ATrustConfig struct {
	Server                  string   `yaml:"server" json:"server"`
	Port                    int      `yaml:"port" json:"port"`
	Username                string   `yaml:"username" json:"username"`
	Password                string   `yaml:"password" json:"password,omitempty"`
	Phone                   string   `yaml:"phone" json:"phone,omitempty"`
	AuthType                string   `yaml:"auth-type" json:"auth-type"`
	LoginDomain             string   `yaml:"login-domain" json:"login-domain"`
	UpdateBestNodesInterval Duration `yaml:"update-best-nodes-interval" json:"update-best-nodes-interval"`
}
type UnderlayConfig struct {
	Interface  string `yaml:"interface" json:"interface"`
	AutoDetect bool   `yaml:"auto-detect" json:"auto-detect"`
}
type RoutingConfig struct {
	Mode             core.RoutingMode       `yaml:"mode" json:"mode"`
	InternetOutbound InternetOutboundConfig `yaml:"internet-outbound" json:"internet-outbound"`
}
type InternetOutboundConfig struct {
	Type     core.InternetOutboundType `yaml:"type" json:"type"`
	Address  string                    `yaml:"address" json:"address"`
	Username string                    `yaml:"username" json:"username"`
	Password string                    `yaml:"password" json:"password,omitempty"`
}
type DNSConfig struct {
	Remote    RemoteDNSConfig    `yaml:"remote" json:"remote"`
	Secondary SecondaryDNSConfig `yaml:"secondary" json:"secondary"`
	CacheTTL  Duration           `yaml:"cache-ttl" json:"cache-ttl"`
	Listen    string             `yaml:"listen" json:"listen"`
	Hosts     map[string]string  `yaml:"hosts" json:"hosts"`
}
type RemoteDNSConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Server  string `yaml:"server" json:"server"`
}
type SecondaryDNSConfig struct {
	Server string `yaml:"server" json:"server"`
}
type InboundsConfig struct {
	SOCKS5 SOCKS5Config `yaml:"socks5" json:"socks5"`
	HTTP   HTTPConfig   `yaml:"http" json:"http"`
	TUN    TUNConfig    `yaml:"tun" json:"tun"`
}
type SOCKS5Config struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Listen   string `yaml:"listen" json:"listen"`
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password,omitempty"`
}
type HTTPConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Listen  string `yaml:"listen" json:"listen"`
}
type TUNConfig struct {
	Enabled           bool         `yaml:"enabled" json:"enabled"`
	Name              string       `yaml:"name" json:"name"`
	Address           string       `yaml:"address" json:"address"`
	MTU               uint32       `yaml:"mtu" json:"mtu"`
	AutoRoute         bool         `yaml:"auto-route" json:"auto-route"`
	RouteAll          bool         `yaml:"route-all" json:"route-all"`
	OutboundInterface string       `yaml:"outbound-interface" json:"outbound-interface"`
	UDP               TUNUDPConfig `yaml:"udp" json:"udp"`
	DNS               TUNDNSConfig `yaml:"dns" json:"dns"`
}
type TUNUDPConfig struct {
	IdleTimeout Duration `yaml:"idle-timeout" json:"idle-timeout"`
	MaxFlows    int      `yaml:"max-flows" json:"max-flows"`
}
type TUNDNSConfig struct {
	Hijack      bool   `yaml:"hijack" json:"hijack"`
	FakeIP      bool   `yaml:"fake-ip" json:"fake-ip"`
	FakeIPRange string `yaml:"fake-ip-range" json:"fake-ip-range"`
}

func Default() Config {
	return Config{Version: Version, Log: LogConfig{Level: "info", Output: "stderr"}, Control: ControlConfig{REST: RESTConfig{Listen: "127.0.0.1:9090"}}, Session: SessionConfig{ID: "default", AutoStart: true, AutoReconnect: true}, ATrust: ATrustConfig{Port: 443, AuthType: "auth/psw", LoginDomain: "Radius", UpdateBestNodesInterval: Duration(30 * time.Second)}, Underlay: UnderlayConfig{AutoDetect: false}, Routing: RoutingConfig{Mode: core.RoutingModeRule, InternetOutbound: InternetOutboundConfig{Type: core.InternetOutboundDirect}}, DNS: DNSConfig{Remote: RemoteDNSConfig{Enabled: true, Server: "auto"}, CacheTTL: Duration(time.Hour), Hosts: map[string]string{}}, Inbounds: InboundsConfig{SOCKS5: SOCKS5Config{Listen: "127.0.0.1:1080"}, HTTP: HTTPConfig{Listen: "127.0.0.1:1081"}, TUN: TUNConfig{Name: "ZJU-Portal", Address: "172.19.0.1/30", MTU: 1400, UDP: TUNUDPConfig{IdleTimeout: Duration(60 * time.Second), MaxFlows: 512}, DNS: TUNDNSConfig{FakeIPRange: "198.18.0.0/16"}}}}
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return DecodeYAML(data)
}

func DecodeYAML(data []byte) (Config, error) {
	config := Default()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) Validate() error {
	if config.Version != Version {
		return fmt.Errorf("unsupported config version %d", config.Version)
	}
	if config.Session.ID == "" {
		return fmt.Errorf("session.id is required")
	}
	for name, value := range map[string]string{
		"session.id":                         config.Session.ID,
		"log.output":                         config.Log.Output,
		"control.rest.listen":                config.Control.REST.Listen,
		"control.rest.secret":                config.Control.REST.Secret,
		"control.rest.secret-file":           config.Control.REST.SecretFile,
		"atrust.username":                    config.ATrust.Username,
		"atrust.password":                    config.ATrust.Password,
		"atrust.phone":                       config.ATrust.Phone,
		"atrust.auth-type":                   config.ATrust.AuthType,
		"atrust.login-domain":                config.ATrust.LoginDomain,
		"routing.internet-outbound.address":  config.Routing.InternetOutbound.Address,
		"routing.internet-outbound.username": config.Routing.InternetOutbound.Username,
		"routing.internet-outbound.password": config.Routing.InternetOutbound.Password,
		"dns.remote.server":                  config.DNS.Remote.Server,
		"dns.secondary.server":               config.DNS.Secondary.Server,
		"dns.listen":                         config.DNS.Listen,
		"inbounds.socks5.listen":             config.Inbounds.SOCKS5.Listen,
		"inbounds.socks5.username":           config.Inbounds.SOCKS5.Username,
		"inbounds.socks5.password":           config.Inbounds.SOCKS5.Password,
		"inbounds.http.listen":               config.Inbounds.HTTP.Listen,
		"inbounds.tun.address":               config.Inbounds.TUN.Address,
		"inbounds.tun.dns.fake-ip-range":     config.Inbounds.TUN.DNS.FakeIPRange,
	} {
		if len(value) > maxValueLength {
			return fmt.Errorf("%s exceeds %d bytes", name, maxValueLength)
		}
	}
	for name, value := range map[string]string{
		"atrust.server":                   config.ATrust.Server,
		"underlay.interface":              config.Underlay.Interface,
		"inbounds.tun.name":               config.Inbounds.TUN.Name,
		"inbounds.tun.outbound-interface": config.Inbounds.TUN.OutboundInterface,
	} {
		if len(value) > maxNameLength {
			return fmt.Errorf("%s exceeds %d bytes", name, maxNameLength)
		}
	}
	if config.Session.AutoStart && config.ATrust.Server == "" {
		return fmt.Errorf("atrust.server is required when session.auto-start is enabled")
	}
	if config.Log.Level != "info" && config.Log.Level != "debug" {
		return fmt.Errorf("log.level %q is invalid", config.Log.Level)
	}
	if config.ATrust.Port <= 0 || config.ATrust.Port > 65535 {
		return fmt.Errorf("atrust.port is invalid")
	}
	if config.ATrust.UpdateBestNodesInterval < 0 {
		return fmt.Errorf("atrust.update-best-nodes-interval cannot be negative")
	}
	switch config.ATrust.AuthType {
	case "", "auth/psw", "auth/cas", "auth/httpsOauth2", "auth/smsCheckCode":
	default:
		return fmt.Errorf("atrust.auth-type %q is invalid", config.ATrust.AuthType)
	}
	if config.ATrust.AuthType == "auth/smsCheckCode" && config.ATrust.Phone == "" {
		return fmt.Errorf("atrust.phone is required for auth/smsCheckCode")
	}
	if !config.Routing.Mode.Valid() {
		return fmt.Errorf("routing.mode %q is invalid", config.Routing.Mode)
	}
	if !config.Routing.InternetOutbound.Type.Valid() {
		return fmt.Errorf("routing.internet-outbound.type %q is invalid", config.Routing.InternetOutbound.Type)
	}
	if config.Routing.InternetOutbound.Type == core.InternetOutboundSOCKS5 && config.Routing.InternetOutbound.Address == "" {
		return fmt.Errorf("routing.internet-outbound.address is required for socks5")
	}
	if config.DNS.CacheTTL <= 0 {
		return fmt.Errorf("dns.cache-ttl must be positive")
	}
	if config.Control.REST.Secret != "" && config.Control.REST.SecretFile != "" {
		return fmt.Errorf("control.rest.secret and secret-file are mutually exclusive")
	}
	if config.Control.REST.Enabled && config.Control.REST.Listen == "" {
		return fmt.Errorf("control.rest.listen is required when enabled")
	}
	if config.Inbounds.SOCKS5.Enabled && config.Inbounds.SOCKS5.Listen == "" {
		return fmt.Errorf("inbounds.socks5.listen is required when enabled")
	}
	if config.Inbounds.SOCKS5.Enabled && (config.Inbounds.SOCKS5.Username == "") != (config.Inbounds.SOCKS5.Password == "") {
		return fmt.Errorf("inbounds.socks5 username and password must be configured together")
	}
	if config.Inbounds.HTTP.Enabled && config.Inbounds.HTTP.Listen == "" {
		return fmt.Errorf("inbounds.http.listen is required when enabled")
	}
	if len(config.DNS.Hosts) > MaxHostEntries {
		return fmt.Errorf("dns.hosts contains %d entries, maximum is %d", len(config.DNS.Hosts), MaxHostEntries)
	}
	for host, address := range config.DNS.Hosts {
		if len(host) > 253 || len(address) > 64 {
			return fmt.Errorf("dns.hosts contains oversized record")
		}
		if host == "" || net.ParseIP(address) == nil {
			return fmt.Errorf("dns.hosts contains invalid record %q=%q", host, address)
		}
	}
	if config.Inbounds.TUN.Enabled {
		if _, err := netip.ParsePrefix(config.Inbounds.TUN.Address); err != nil {
			return fmt.Errorf("inbounds.tun.address: %w", err)
		}
		if config.Inbounds.TUN.MTU == 0 {
			return fmt.Errorf("inbounds.tun.mtu must be positive")
		}
		if config.Inbounds.TUN.UDP.IdleTimeout <= 0 {
			return fmt.Errorf("inbounds.tun.udp.idle-timeout must be positive")
		}
		if config.Inbounds.TUN.UDP.MaxFlows <= 0 {
			return fmt.Errorf("inbounds.tun.udp.max-flows must be positive")
		}
		if config.Inbounds.TUN.DNS.FakeIP && !config.Inbounds.TUN.AutoRoute {
			return fmt.Errorf("inbounds.tun.dns.fake-ip requires auto-route")
		}
		if config.Inbounds.TUN.DNS.Hijack && !config.Inbounds.TUN.AutoRoute {
			return fmt.Errorf("inbounds.tun.dns.hijack requires auto-route")
		}
		if config.Inbounds.TUN.DNS.FakeIP {
			prefix, err := netip.ParsePrefix(config.Inbounds.TUN.DNS.FakeIPRange)
			if err != nil || !prefix.Addr().Is4() {
				return fmt.Errorf("inbounds.tun.dns.fake-ip-range must be a valid IPv4 prefix")
			}
		}
	}
	return nil
}

func (config Config) CoreConfig() core.Config {
	result := core.Config{Protocol: "atrust", SessionID: core.SessionID(config.Session.ID), ServerAddress: config.ATrust.Server, ServerPort: config.ATrust.Port, Username: config.ATrust.Username, Password: config.ATrust.Password, Phone: config.ATrust.Phone, AuthType: config.ATrust.AuthType, LoginDomain: config.ATrust.LoginDomain, UpdateBestNodesSeconds: int(time.Duration(config.ATrust.UpdateBestNodesInterval) / time.Second), BindInterface: config.Underlay.Interface, AutoDetectInterface: config.Underlay.AutoDetect, DisableAutoReconnect: !config.Session.AutoReconnect, DisableRemoteDNS: !config.DNS.Remote.Enabled, RemoteDNSServer: config.DNS.Remote.Server, SecondaryDNSServer: config.DNS.Secondary.Server, DNSTTL: uint64(time.Duration(config.DNS.CacheTTL) / time.Second), DNSBind: config.DNS.Listen, Hosts: cloneStringMap(config.DNS.Hosts), RoutingMode: config.Routing.Mode, InternetOutbound: core.InternetOutboundConfig{Type: config.Routing.InternetOutbound.Type, Address: config.Routing.InternetOutbound.Address, Username: config.Routing.InternetOutbound.Username, Password: config.Routing.InternetOutbound.Password}}
	if config.Inbounds.SOCKS5.Enabled {
		result.SOCKSBind = config.Inbounds.SOCKS5.Listen
		result.SOCKSUsername = config.Inbounds.SOCKS5.Username
		result.SOCKSPassword = config.Inbounds.SOCKS5.Password
	}
	if config.Inbounds.HTTP.Enabled {
		result.HTTPBind = config.Inbounds.HTTP.Listen
	}
	if config.Inbounds.TUN.Enabled {
		tun := config.Inbounds.TUN
		result.TUNEnabled = true
		result.TUNName = tun.Name
		result.TUNAddress = tun.Address
		result.TUNMTU = tun.MTU
		result.TUNAutoRoute = tun.AutoRoute
		result.TUNRouteAll = tun.RouteAll
		result.TUNOutboundInterface = tun.OutboundInterface
		result.TUNUDPTimeoutSeconds = int(time.Duration(tun.UDP.IdleTimeout) / time.Second)
		result.TUNUDPMaxFlows = tun.UDP.MaxFlows
		result.TUNDNSHijack = tun.DNS.Hijack
		result.TUNFakeIP = tun.DNS.FakeIP
		result.TUNFakeIPRange = tun.DNS.FakeIPRange
	}
	return result
}

func (config Config) Clone() Config {
	config.DNS.Hosts = cloneStringMap(config.DNS.Hosts)
	return config
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (config Config) TUNConfig() TUNConfig {
	return config.Inbounds.TUN
}

func (config Config) SecurityWarnings() []string {
	var warnings []string
	if config.Control.REST.Enabled && !isLoopbackListen(config.Control.REST.Listen) {
		warnings = append(warnings, fmt.Sprintf("REST control %s is exposed over plaintext HTTP outside loopback", config.Control.REST.Listen))
	}
	if config.Inbounds.SOCKS5.Enabled && !isLoopbackListen(config.Inbounds.SOCKS5.Listen) {
		warning := fmt.Sprintf("SOCKS5 inbound %s is not limited to loopback", config.Inbounds.SOCKS5.Listen)
		if config.Inbounds.SOCKS5.Username == "" {
			warning += " and does not require authentication"
		}
		warnings = append(warnings, warning)
	}
	if config.Inbounds.HTTP.Enabled && !isLoopbackListen(config.Inbounds.HTTP.Listen) {
		warnings = append(warnings, fmt.Sprintf("HTTP inbound %s is not limited to loopback and does not require authentication", config.Inbounds.HTTP.Listen))
	}
	if config.DNS.Listen != "" && !isLoopbackListen(config.DNS.Listen) {
		warnings = append(warnings, fmt.Sprintf("DNS service %s is not limited to loopback", config.DNS.Listen))
	}
	return warnings
}

func isLoopbackListen(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
