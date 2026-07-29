package networkruntime

import (
	"bytes"
	"net"
	"net/netip"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/client"
)

func TestBuildResourceRoutePrefixesMergesResourcesDNSAndBuiltinRoutes(t *testing.T) {
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

	for _, address := range []string{"10.13.1.1", "192.0.2.8", "172.16.100.1", "198.51.100.1", "198.51.100.6", "203.0.113.9", "198.18.1.1"} {
		if !routePrefixesContain(routes, netip.MustParseAddr(address)) {
			t.Fatalf("routes %v do not contain %s", routes, address)
		}
	}
	if routePrefixesContain(routes, netip.MustParseAddr("8.8.8.8")) {
		t.Fatalf("routes %v unexpectedly contain public internet traffic", routes)
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
	}, nil, []net.IP{net.ParseIP("10.0.0.10"), net.ParseIP("192.0.2.10")}, "")
	if err != nil {
		t.Fatal(err)
	}

	for _, address := range []string{"10.0.0.10", "192.0.2.10"} {
		if routePrefixesContain(routes, netip.MustParseAddr(address)) {
			t.Fatalf("routes %v contain excluded tunnel node %s", routes, address)
		}
	}
	for _, address := range []string{"10.0.0.11", "192.0.2.11"} {
		if !routePrefixesContain(routes, netip.MustParseAddr(address)) {
			t.Fatalf("routes %v do not contain adjacent resource %s", routes, address)
		}
	}
}

func TestAddImplicitRouteResources(t *testing.T) {
	resources := addImplicitRouteResources(nil, map[string]net.IP{
		"static.example.edu": net.ParseIP("203.0.113.9"),
	})
	for _, address := range []net.IP{net.ParseIP("10.1.2.3"), net.ParseIP("203.0.113.9")} {
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
}

func routePrefixesContain(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
