package networkruntime

import (
	"bytes"
	"net"
	"net/netip"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/client"
)

func TestBuildResourceRoutePrefixesMergesResourcesDNSAndFakeIPRoutes(t *testing.T) {
	routes, err := buildResourceRoutePrefixes([]client.IPResource{
		{IPMin: net.ParseIP("192.0.2.8"), IPMax: net.ParseIP("192.0.2.8")},
		{IPMin: net.ParseIP("172.16.0.0"), IPMax: net.ParseIP("172.16.255.255")},
		{IPMin: net.ParseIP("198.51.100.1"), IPMax: net.ParseIP("198.51.100.6")},
	}, map[string]net.IP{
		"static.example.edu": net.ParseIP("203.0.113.9"),
		"ipv6.example.edu":   net.ParseIP("2001:db8::1"),
	}, nil, "198.18.0.0/16")
	if err != nil {
		t.Fatal(err)
	}

	for _, address := range []string{"192.0.2.8", "172.16.100.1", "198.51.100.1", "198.51.100.6", "203.0.113.9", "198.18.1.1"} {
		if !routePrefixesContain(routes, netip.MustParseAddr(address)) {
			t.Fatalf("routes %v do not contain %s", routes, address)
		}
	}
	for _, address := range []string{"10.13.1.1", "8.8.8.8"} {
		if routePrefixesContain(routes, netip.MustParseAddr(address)) {
			t.Fatalf("routes %v unexpectedly contain unlisted address %s", routes, address)
		}
	}
}

func TestBuildResourceRoutePrefixesRejectsInvalidFakeIPRange(t *testing.T) {
	if _, err := buildResourceRoutePrefixes(nil, nil, nil, "invalid"); err == nil {
		t.Fatal("invalid fake IP range was accepted")
	}
}

func TestBuildResourceRoutePrefixesExcludesTunnelNodes(t *testing.T) {
	routes, err := buildResourceRoutePrefixes([]client.IPResource{
		{IPMin: net.ParseIP("192.0.2.0"), IPMax: net.ParseIP("192.0.2.255")},
	}, nil, []net.IP{net.ParseIP("192.0.2.10")}, "")
	if err != nil {
		t.Fatal(err)
	}

	if routePrefixesContain(routes, netip.MustParseAddr("192.0.2.10")) {
		t.Fatalf("routes %v contain excluded tunnel node", routes)
	}
	if !routePrefixesContain(routes, netip.MustParseAddr("192.0.2.11")) {
		t.Fatalf("routes %v do not contain adjacent resource", routes)
	}
}

func TestAddStaticDNSResources(t *testing.T) {
	resources := addStaticDNSResources(nil, map[string]net.IP{
		"static.example.edu": net.ParseIP("203.0.113.9"),
	})
	for _, address := range []net.IP{net.ParseIP("203.0.113.9")} {
		matched := false
		for _, resource := range resources {
			if bytes.Compare(address.To4(), resource.IPMin.To4()) >= 0 && bytes.Compare(address.To4(), resource.IPMax.To4()) <= 0 {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("implicit resources %#v do not contain %s", resources, address)
		}
	}
	for _, resource := range resources {
		address := net.ParseIP("10.1.2.3").To4()
		if bytes.Compare(address, resource.IPMin.To4()) >= 0 && bytes.Compare(address, resource.IPMax.To4()) <= 0 {
			t.Fatalf("static DNS resources %#v unexpectedly contain 10.1.2.3", resources)
		}
	}
}

func routePrefixesContain(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
