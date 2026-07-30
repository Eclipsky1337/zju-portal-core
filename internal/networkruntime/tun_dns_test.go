package networkruntime

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/miekg/dns"
	M "github.com/sagernet/sing/common/metadata"
)

func TestTUNFakeIPDNSRestoresDomainDestination(t *testing.T) {
	created, err := newTUNService(TUNConfig{FakeIP: true, AutoRoute: true}, &outboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	query := new(dns.Msg)
	query.SetQuestion("app.example.edu.", dns.TypeA)
	payload, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	responsePayload, err := service.handleDNSPayload(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	response := new(dns.Msg)
	if err := response.Unpack(responsePayload); err != nil {
		t.Fatal(err)
	}
	if len(response.Answer) != 1 {
		t.Fatalf("DNS answers = %#v", response.Answer)
	}
	fakeIP := response.Answer[0].(*dns.A).A
	address, ok := netip.AddrFromSlice(fakeIP)
	if !ok {
		t.Fatalf("fake IP = %v", fakeIP)
	}
	destination := M.SocksaddrFrom(address.Unmap(), 443)
	if got := service.routeDestination(destination); got != "app.example.edu:443" {
		t.Fatalf("routed destination = %q", got)
	}
}

func TestTUNDNSHijackUsesResolverWithoutFakeIP(t *testing.T) {
	resolver := &tunResolverStub{ip: net.ParseIP("10.0.0.8")}
	created, err := newTUNService(TUNConfig{DNSHijack: true, AutoRoute: true, Resolver: resolver}, &outboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	query := new(dns.Msg)
	query.SetQuestion("app.example.edu.", dns.TypeA)
	payload, _ := query.Pack()
	responsePayload, err := service.handleDNSPayload(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	response := new(dns.Msg)
	if err := response.Unpack(responsePayload); err != nil {
		t.Fatal(err)
	}
	if len(response.Answer) != 1 || !response.Answer[0].(*dns.A).A.Equal(net.ParseIP("10.0.0.8")) {
		t.Fatalf("DNS answers = %#v", response.Answer)
	}
	if resolver.host != "app.example.edu" {
		t.Fatalf("resolved host = %q", resolver.host)
	}
}

func TestTUNFakeIPClassifiesStaticDNSRecordByResolvedAddress(t *testing.T) {
	resolver := &tunResolverStub{ip: net.ParseIP("203.0.113.8"), staticIP: net.ParseIP("10.0.0.8")}
	created, err := newTUNService(TUNConfig{
		FakeIP:         true,
		AutoRoute:      true,
		Resolver:       resolver,
		RouteAddresses: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	}, &outboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	query := new(dns.Msg)
	query.SetQuestion("APP.EXAMPLE.EDU.", dns.TypeA)
	payload, _ := query.Pack()
	responsePayload, err := service.handleDNSPayload(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	response := new(dns.Msg)
	if err := response.Unpack(responsePayload); err != nil {
		t.Fatal(err)
	}
	if len(response.Answer) != 1 || response.Answer[0].(*dns.A).A.Equal(net.ParseIP("10.0.0.8")) {
		t.Fatalf("DNS answers = %#v", response.Answer)
	}
	address, ok := netip.AddrFromSlice(response.Answer[0].(*dns.A).A)
	if !ok {
		t.Fatalf("fake IP = %v", response.Answer[0].(*dns.A).A)
	}
	if domain, found := service.fakeIPs.Lookup(address.Unmap()); !found || domain != "app.example.edu" {
		t.Fatalf("static fake IP lookup = %q, %v", domain, found)
	}
}

func TestTUNFakeIPOnlyAppliesToVPNDomains(t *testing.T) {
	resolver := &tunResolverStub{
		vpnDomains: map[string]bool{"app.example.edu": true},
		ips: map[string]net.IP{
			"cc98.org":        net.ParseIP("10.10.98.98"),
			"www.example.com": net.ParseIP("203.0.113.8"),
		},
	}
	created, err := newTUNService(TUNConfig{
		FakeIP:         true,
		AutoRoute:      true,
		Resolver:       resolver,
		RouteAddresses: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	}, &outboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)

	vpnAddress := queryTUNARecord(t, service, "app.example.edu.")
	vpnFakeIP, ok := netip.AddrFromSlice(vpnAddress)
	if !ok {
		t.Fatalf("VPN fake IP = %v", vpnAddress)
	}
	if domain, found := service.fakeIPs.Lookup(vpnFakeIP.Unmap()); !found || domain != "app.example.edu" {
		t.Fatalf("VPN fake IP lookup = %q, %v", domain, found)
	}
	resolvedVPNAddress := queryTUNARecord(t, service, "cc98.org.")
	resolvedVPNFakeIP, ok := netip.AddrFromSlice(resolvedVPNAddress)
	if !ok {
		t.Fatalf("resolved VPN fake IP = %v", resolvedVPNAddress)
	}
	if domain, found := service.fakeIPs.Lookup(resolvedVPNFakeIP.Unmap()); !found || domain != "cc98.org" {
		t.Fatalf("resolved VPN fake IP lookup = %q, %v", domain, found)
	}

	publicAddress := queryTUNARecord(t, service, "www.example.com.")
	if !publicAddress.Equal(net.ParseIP("203.0.113.8")) {
		t.Fatalf("public DNS address = %s", publicAddress)
	}
	if resolver.host != "www.example.com" {
		t.Fatalf("resolved public host = %q", resolver.host)
	}
}

func queryTUNARecord(t *testing.T, service *tunService, domain string) net.IP {
	t.Helper()
	query := new(dns.Msg)
	query.SetQuestion(domain, dns.TypeA)
	payload, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	responsePayload, err := service.handleDNSPayload(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	response := new(dns.Msg)
	if err := response.Unpack(responsePayload); err != nil {
		t.Fatal(err)
	}
	if len(response.Answer) != 1 {
		t.Fatalf("DNS answers = %#v", response.Answer)
	}
	return response.Answer[0].(*dns.A).A
}

func TestTUNFakeIPConnectionTrackingUsesDomain(t *testing.T) {
	tracker := newConnectionTracker()
	localRemote, peerRemote := net.Pipe()
	outbound := &capturingTUNOutbound{conn: localRemote, addresses: make(chan string, 1)}
	created, err := newTUNService(TUNConfig{FakeIP: true, AutoRoute: true}, outbound, tracker)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	fakeIP, err := service.fakeIPs.Assign("app.example.edu")
	if err != nil {
		t.Fatal(err)
	}
	localInbound, peerInbound := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- service.NewConnection(context.Background(), localInbound, M.Metadata{
			Source:      M.SocksaddrFrom(netip.MustParseAddr("172.19.0.1"), 54321),
			Destination: M.SocksaddrFromNet(&net.TCPAddr{IP: net.IP(fakeIP.AsSlice()), Port: 443}),
		})
	}()

	select {
	case address := <-outbound.addresses:
		if address != "app.example.edu:443" {
			t.Fatalf("dialed address = %q", address)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for TUN dial")
	}
	connections := tracker.Connections()
	if len(connections) != 1 || connections[0].Destination != "app.example.edu:443" {
		t.Fatalf("connections = %#v", connections)
	}

	_ = peerInbound.Close()
	_ = peerRemote.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for TUN connection shutdown")
	}
}

func TestFakeIPStoreReturnsStableAddress(t *testing.T) {
	store, err := newFakeIPStore("198.18.0.0/30")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Assign("Example.COM.")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Assign("example.com")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("fake IPs = %s and %s", first, second)
	}
	if domain, ok := store.Lookup(first); !ok || domain != "example.com" {
		t.Fatalf("lookup = %q, %v", domain, ok)
	}
	if _, err := store.Assign("other.example"); err == nil {
		t.Fatal("expected fake IP exhaustion")
	}
}

func TestFakeIPStoreReusesOnlyStaleAddressWhenExhausted(t *testing.T) {
	store, err := newFakeIPStore("198.18.0.0/30")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Assign("old.example")
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.domainToIP["old.example"].lastUsed = time.Now().Add(-fakeIPReuseAfter - time.Minute)
	store.mu.Unlock()

	reused, err := store.Assign("new.example")
	if err != nil {
		t.Fatal(err)
	}
	if reused != first {
		t.Fatalf("reused address = %s, want %s", reused, first)
	}
	if _, found := store.domainToIP["old.example"]; found {
		t.Fatal("stale domain mapping was retained")
	}
	if domain, found := store.Lookup(reused); !found || domain != "new.example" {
		t.Fatalf("reused lookup = %q, %v", domain, found)
	}
}

type tunResolverStub struct {
	ip         net.IP
	ips        map[string]net.IP
	staticIP   net.IP
	host       string
	vpnDomains map[string]bool
}

type capturingTUNOutbound struct {
	conn      net.Conn
	addresses chan string
}

func (outbound *capturingTUNOutbound) DialContext(_ context.Context, _ string, address string) (net.Conn, error) {
	outbound.addresses <- address
	return outbound.conn, nil
}

func (*capturingTUNOutbound) Close(context.Context) error { return nil }

var _ core.Outbound = (*capturingTUNOutbound)(nil)

func (resolver *tunResolverStub) Resolve(ctx context.Context, host string) (context.Context, net.IP, error) {
	resolver.host = host
	if resolver.staticIP != nil {
		return ctx, resolver.staticIP, nil
	}
	if address := resolver.ips[host]; address != nil {
		return ctx, address, nil
	}
	return ctx, resolver.ip, nil
}

func (resolver *tunResolverStub) ResolveStatic(string) (net.IP, bool) {
	return resolver.staticIP, resolver.staticIP != nil
}

func (resolver *tunResolverStub) IsVPNDomain(host string) bool {
	return resolver.vpnDomains[host]
}
