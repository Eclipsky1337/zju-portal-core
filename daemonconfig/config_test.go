package daemonconfig

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestDecodeYAMLAppliesDefaultsAndConvertsCoreConfig(t *testing.T) {
	config, err := DecodeYAML([]byte(`
version: 1
session:
  auto-start: true
atrust:
  server: vpn.example.edu
  username: user
  password: secret
underlay:
  auto-detect: false
routing:
  internet-outbound:
    type: socks5
    address: 127.0.0.1:7890
dns:
  secondary:
    server: 1.1.1.1
  hosts:
    APP.EXAMPLE.EDU.: 10.0.0.8
inbounds:
  socks5:
    enabled: true
  tun:
    enabled: true
    auto-route: true
    dns:
      fake-ip: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if config.ATrust.Port != 443 || config.ATrust.AuthType != "auth/psw" || config.DNS.CacheTTL != Duration(time.Hour) {
		t.Fatalf("defaults not applied: %#v", config)
	}
	mapped := config.CoreConfig()
	if mapped.ServerAddress != "vpn.example.edu" || mapped.InternetOutbound.Type != core.InternetOutboundSOCKS5 || mapped.SOCKSBind != "127.0.0.1:1080" || !mapped.TUNEnabled || !mapped.TUNFakeIP {
		t.Fatalf("core config = %#v", mapped)
	}
}

func TestDecodeYAMLRejectsUnknownField(t *testing.T) {
	_, err := DecodeYAML([]byte("version: 1\nunknown: true\n"))
	if err == nil {
		t.Fatal("unknown field was accepted")
	}
}

func TestConfigRequiresPhoneOnlyForDirectSMS(t *testing.T) {
	config := Default()
	config.Session.AutoStart = true
	config.ATrust.Server = "vpn.example.edu"
	config.ATrust.AuthType = "auth/smsCheckCode"
	if err := config.Validate(); err == nil {
		t.Fatal("direct SMS without phone was accepted")
	}
	config.ATrust.Phone = "13800138000"
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	config.ATrust.AuthType = "auth/psw"
	config.ATrust.Phone = ""
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigJSONUsesDefaultsAndRejectsUnknownFields(t *testing.T) {
	var config Config
	if err := json.Unmarshal([]byte(`{"version":1,"session":{"auto-start":false}}`), &config); err != nil {
		t.Fatal(err)
	}
	if config.ATrust.Port != 443 || config.Session.AutoStart || config.Routing.Mode != core.RoutingModeRule {
		t.Fatalf("config = %#v", config)
	}
	if err := json.Unmarshal([]byte(`{"version":1,"unknown":true}`), &config); err == nil {
		t.Fatal("unknown JSON field was accepted")
	}
}

func TestConfigClonePreservesCredentialsAndDoesNotMutateOriginal(t *testing.T) {
	config := Default()
	config.ATrust.Password = "vpn-secret"
	config.Control.REST.Secret = "rest-secret"
	config.Routing.InternetOutbound.Password = "proxy-secret"
	config.Inbounds.SOCKS5.Password = "socks-secret"
	cloned := config.Clone()
	if cloned.ATrust.Password != "vpn-secret" || cloned.Control.REST.Secret != "rest-secret" || cloned.Routing.InternetOutbound.Password != "proxy-secret" || cloned.Inbounds.SOCKS5.Password != "socks-secret" {
		t.Fatalf("cloned = %#v", cloned)
	}
	if config.ATrust.Password != "vpn-secret" || config.Control.REST.Secret != "rest-secret" {
		t.Fatal("Clone mutated original config")
	}
}

func TestTUNConfigComparableForRestartDetection(t *testing.T) {
	first := Default()
	second := Default()
	if !reflect.DeepEqual(first.TUNConfig(), second.TUNConfig()) {
		t.Fatal("equal TUN configs differ")
	}
	second.Inbounds.TUN.MTU++
	if reflect.DeepEqual(first.TUNConfig(), second.TUNConfig()) {
		t.Fatal("changed TUN config was not detected")
	}
}

func TestConfigRejectsOversizedHostsAndFields(t *testing.T) {
	config := Default()
	config.Session.AutoStart = false
	config.DNS.Hosts = make(map[string]string, MaxHostEntries+1)
	for index := 0; index <= MaxHostEntries; index++ {
		config.DNS.Hosts[fmt.Sprintf("host-%d.example", index)] = "192.0.2.1"
	}
	if err := config.Validate(); err == nil {
		t.Fatal("oversized hosts map was accepted")
	}
	config.DNS.Hosts = map[string]string{}
	config.ATrust.Password = strings.Repeat("x", maxValueLength+1)
	if err := config.Validate(); err == nil {
		t.Fatal("oversized password was accepted")
	}
}

func TestConfigCopiesHostsAcrossPublicBoundaries(t *testing.T) {
	config := Default()
	config.DNS.Hosts["app.example.edu"] = "10.0.0.8"
	cloned := config.Clone()
	cloned.DNS.Hosts["app.example.edu"] = "mutated"
	if config.DNS.Hosts["app.example.edu"] != "10.0.0.8" {
		t.Fatal("Clone() exposed the original hosts map")
	}
	mapped := config.CoreConfig()
	mapped.Hosts["app.example.edu"] = "mutated"
	if config.DNS.Hosts["app.example.edu"] != "10.0.0.8" {
		t.Fatal("CoreConfig() exposed the original hosts map")
	}
}

func TestConfigSecurityWarningsForNonLoopbackServices(t *testing.T) {
	config := Default()
	config.Session.AutoStart = false
	config.Inbounds.SOCKS5.Enabled = true
	config.Inbounds.SOCKS5.Listen = "0.0.0.0:1080"
	config.Inbounds.HTTP.Enabled = true
	config.Inbounds.HTTP.Listen = "127.0.0.1:1081"
	config.DNS.Listen = "[::]:5353"
	warnings := config.SecurityWarnings()
	if len(warnings) != 2 || !strings.Contains(warnings[0], "SOCKS5") || !strings.Contains(warnings[1], "DNS") {
		t.Fatalf("security warnings = %#v", warnings)
	}
}

func TestConfigRejectsValuesThatCannotStart(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "log level", mutate: func(config *Config) { config.Log.Level = "trace" }},
		{name: "auth type", mutate: func(config *Config) { config.ATrust.AuthType = "auth/unknown" }},
		{name: "negative node interval", mutate: func(config *Config) { config.ATrust.UpdateBestNodesInterval = -1 }},
		{name: "internet outbound", mutate: func(config *Config) { config.Routing.InternetOutbound.Type = "unknown" }},
		{name: "dns ttl", mutate: func(config *Config) { config.DNS.CacheTTL = 0 }},
		{name: "non-loopback rest", mutate: func(config *Config) { config.Control.REST.Enabled = true; config.Control.REST.Listen = "0.0.0.0:9090" }},
		{name: "missing socks listen", mutate: func(config *Config) { config.Inbounds.SOCKS5.Enabled = true; config.Inbounds.SOCKS5.Listen = "" }},
		{name: "missing http listen", mutate: func(config *Config) { config.Inbounds.HTTP.Enabled = true; config.Inbounds.HTTP.Listen = "" }},
		{name: "tun mtu", mutate: func(config *Config) { config.Inbounds.TUN.Enabled = true; config.Inbounds.TUN.MTU = 0 }},
		{name: "tun udp timeout", mutate: func(config *Config) { config.Inbounds.TUN.Enabled = true; config.Inbounds.TUN.UDP.IdleTimeout = 0 }},
		{name: "tun udp flows", mutate: func(config *Config) { config.Inbounds.TUN.Enabled = true; config.Inbounds.TUN.UDP.MaxFlows = 0 }},
		{name: "dns hijack without auto route", mutate: func(config *Config) {
			config.Inbounds.TUN.Enabled = true
			config.Inbounds.TUN.DNS.Hijack = true
		}},
		{name: "fake ip range", mutate: func(config *Config) {
			config.Inbounds.TUN.Enabled = true
			config.Inbounds.TUN.AutoRoute = true
			config.Inbounds.TUN.DNS.FakeIP = true
			config.Inbounds.TUN.DNS.FakeIPRange = "invalid"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := Default()
			config.Session.AutoStart = false
			test.mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
}
