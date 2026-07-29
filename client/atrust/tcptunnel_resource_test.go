package atrust

import (
	"context"
	"net"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/Eclipsky1337/zju-portal-core/resolve"
)

func TestTCPResourceForMatchesFourByteAddressAgainstParsedIPv4Resource(t *testing.T) {
	client := &Client{ipResources: []client.IPResource{{
		IPMin:       net.ParseIP("10.10.0.0"),
		IPMax:       net.ParseIP("10.10.255.255"),
		PortMin:     443,
		PortMax:     443,
		Protocol:    "tcp",
		AppID:       "app-1",
		NodeGroupID: "group-1",
	}}}

	appID, nodeGroupID, domain := client.tcpResourceFor(context.Background(), &net.TCPAddr{
		IP:   net.ParseIP("10.10.98.98").To4(),
		Port: 443,
	})

	if appID != "app-1" || nodeGroupID != "group-1" || domain != "" {
		t.Fatalf("tcpResourceFor() = %q, %q, %q", appID, nodeGroupID, domain)
	}
}

func TestTCPResourceForUsesDomainResourceFromContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), resolve.ContextKeyDomainResource, client.DomainResource{
		AppID:       "domain-app",
		NodeGroupID: "domain-group",
	})
	ctx = context.WithValue(ctx, resolve.ContextKeyResolveHost, "app.example.edu")

	appID, nodeGroupID, domain := (&Client{}).tcpResourceFor(ctx, &net.TCPAddr{
		IP:   net.ParseIP("10.10.98.98").To4(),
		Port: 443,
	})

	if appID != "domain-app" || nodeGroupID != "domain-group" || domain != "app.example.edu" {
		t.Fatalf("tcpResourceFor() = %q, %q, %q", appID, nodeGroupID, domain)
	}
}
