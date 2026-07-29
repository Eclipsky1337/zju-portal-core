package atrust

import (
	"net"
	"testing"

	clientpkg "github.com/Eclipsky1337/zju-portal-core/client"
)

func TestParseResourcePreservesResourceAndNodeBehavior(t *testing.T) {
	resource := []byte(`{
		"data": {
			"appList": {
				"data": {
					"appInfo": [{
						"apps": [{
							"id": "app-main",
							"nodeGroupID": "group-main",
							"addressList": [
								{"protocol": "tcp", "port": "443", "host": "10.0.0.10"},
								{"protocol": "udp", "port": "1000-2000", "host": "10.1.0.0/16"},
								{"protocol": "all", "port": "80-81", "host": "10.2.0.10-10.2.0.20"},
								{"protocol": "tcp", "port": "8443", "host": "*.example.edu.cn", "ip": ["invalid", "2001:db8::1", "10.3.0.5"]},
								{"protocol": "icmp", "port": "0", "host": "10.4.0.1"},
								{"protocol": "tcp", "port": "invalid", "host": "10.5.0.1"}
							]
						}]
					}],
					"config": {
						"nodeGroupConf": {
							"majorNodeGroup": {"id": "group-main"},
							"nodeGroupList": [
								{"id": "group-main", "addressInfo": [
									{"address": "10.0.0.10", "type": "lan"},
									{"address": "{{sdpcHost}}", "type": "wan"},
									{"address": "node.example.edu.cn:8443", "type": "wan"}
								]}
							]
						}
					}
				}
			},
			"sdpPolicy": {
				"data": {
					"clientOption": {
						"dnsOption": {"firstDNS": "10.10.10.10"},
						"dnsOptionV2": {"firstDNS": "10.20.20.20"}
					}
				}
			}
		}
	}`)

	client := &Client{serverAddress: "vpn.example.edu.cn:443"}
	if err := client.parseResource(resource); err != nil {
		t.Fatalf("parseResource() error = %v", err)
	}

	if len(client.ipResources) != 3 {
		t.Fatalf("len(ipResources) = %d, want 3", len(client.ipResources))
	}
	assertIPResource(t, client.ipResources[0], "10.0.0.10", "10.0.0.10", 443, 443, "tcp")
	assertIPResource(t, client.ipResources[1], "10.1.0.0", "10.1.255.255", 1000, 2000, "udp")
	assertIPResource(t, client.ipResources[2], "10.2.0.10", "10.2.0.20", 80, 81, "all")

	domain, ok := client.domainResources[".example.edu.cn"]
	if !ok {
		t.Fatalf("domainResources does not contain wildcard domain: %#v", client.domainResources)
	}
	if domain.PortMin != 8443 || domain.PortMax != 8443 || domain.Protocol != "tcp" || domain.AppID != "app-main" || domain.NodeGroupID != "group-main" {
		t.Fatalf("domain resource = %#v", domain)
	}

	if got := client.dnsResource["*.example.edu.cn"]; !got.Equal(net.ParseIP("10.3.0.5")) {
		t.Fatalf("DNS resource = %v, want 10.3.0.5", got)
	}
	if client.dnsServer != "10.10.10.10" {
		t.Fatalf("dnsServer = %q, want first legacy DNS option", client.dnsServer)
	}

	if client.MajorNodeGroup != "group-main" {
		t.Fatalf("MajorNodeGroup = %q", client.MajorNodeGroup)
	}
	wantNodes := []string{"10.0.0.10:441", "vpn.example.edu.cn:443", "node.example.edu.cn:8443"}
	gotNodes := client.NodeGroups["group-main"]
	if len(gotNodes) != len(wantNodes) {
		t.Fatalf("NodeGroups[group-main] = %#v", gotNodes)
	}
	for index := range wantNodes {
		if gotNodes[index] != wantNodes[index] {
			t.Fatalf("NodeGroups[group-main][%d] = %q, want %q", index, gotNodes[index], wantNodes[index])
		}
	}

	excluded := client.RouteExcludedIPs()
	if len(excluded) != 1 || !excluded[0].Equal(net.ParseIP("10.0.0.10")) {
		t.Fatalf("RouteExcludedIPs() = %v, want [10.0.0.10]", excluded)
	}
}

func TestParseResourceUsesDNSOptionV2AsFallback(t *testing.T) {
	resource := []byte(`{
		"data": {
			"appList": {"data": {}},
			"sdpPolicy": {"data": {"clientOption": {
				"dnsOptionV2": {"firstDNS": "10.20.20.20"}
			}}}
		}
	}`)

	client := &Client{}
	if err := client.parseResource(resource); err != nil {
		t.Fatalf("parseResource() error = %v", err)
	}
	if client.dnsServer != "10.20.20.20" {
		t.Fatalf("dnsServer = %q, want DNS option v2 fallback", client.dnsServer)
	}
}

func TestParseResourceRejectsMalformedJSON(t *testing.T) {
	if err := (&Client{}).parseResource([]byte(`{"data":`)); err == nil {
		t.Fatal("parseResource() error = nil, want malformed JSON error")
	}
}

func assertIPResource(t *testing.T, resource clientpkg.IPResource, ipMin, ipMax string, portMin, portMax int, protocol string) {
	t.Helper()

	if !resource.IPMin.Equal(net.ParseIP(ipMin)) || !resource.IPMax.Equal(net.ParseIP(ipMax)) || resource.PortMin != portMin || resource.PortMax != portMax || resource.Protocol != protocol || resource.AppID != "app-main" || resource.NodeGroupID != "group-main" {
		t.Fatalf("IP resource = %#v, want %s-%s %d-%d %s", resource, ipMin, ipMax, portMin, portMax, protocol)
	}
}
