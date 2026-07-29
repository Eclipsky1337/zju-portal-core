package resolve

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/miekg/dns"
	"github.com/patrickmn/go-cache"
)

func TestResolverMatchesVPNDomain(t *testing.T) {
	resolver := &Resolver{domainResources: map[string]client.DomainResource{
		".example.edu": {PortMin: 443, PortMax: 443, Protocol: "tcp"},
	}}
	if !resolver.IsVPNDomain("APP.EXAMPLE.EDU.") {
		t.Fatal("VPN domain was not matched")
	}
	if resolver.IsVPNDomain("www.example.com") {
		t.Fatal("public domain unexpectedly matched VPN resources")
	}
}

func TestNewResolverPropagatesContextToVPNDNSDial(t *testing.T) {
	const key contextKeyForTest = "request"
	ctx := context.WithValue(context.Background(), key, "dns-1")
	stack := &resolverStackStub{err: errors.New("dial stopped")}
	resolver, err := NewResolver(stack, "10.0.0.53", "", 60, nil, nil, true)
	if err != nil {
		t.Fatalf("NewResolver() error = %v", err)
	}

	_, _ = resolver.remoteUDPResolver.Dial(ctx, "udp", "ignored")
	if got := stack.contextValue; got != "dns-1" {
		t.Fatalf("UDP dial context value = %#v", got)
	}

	stack.contextValue = nil
	_, _ = resolver.remoteTCPResolver.Dial(ctx, "tcp", "ignored")
	if got := stack.contextValue; got != "dns-1" {
		t.Fatalf("TCP dial context value = %#v", got)
	}
}

func TestResolverCloseContextStopsFallbackTimer(t *testing.T) {
	resolver := &Resolver{timer: time.NewTimer(time.Hour)}
	if err := resolver.CloseContext(context.Background()); err != nil {
		t.Fatalf("CloseContext() error = %v", err)
	}
	if resolver.timer != nil {
		t.Fatal("resolver timer was not cleared")
	}
}

func TestResolverStaticDNSNormalizesHostNames(t *testing.T) {
	resolver, err := NewResolver(&resolverStackStub{}, "", "", 60, nil, map[string]net.IP{
		"Server.Example.EDU.": net.ParseIP("10.0.0.7"),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.SetPermanentDNS(" App.Example.EDU. ", net.ParseIP("10.0.0.8")); err != nil {
		t.Fatal(err)
	}

	for host, expected := range map[string]string{
		"app.example.edu":    "10.0.0.8",
		"APP.EXAMPLE.EDU.":   "10.0.0.8",
		"server.example.edu": "10.0.0.7",
	} {
		address, found := resolver.ResolveStatic(host)
		if !found || !address.Equal(net.ParseIP(expected)) {
			t.Fatalf("ResolveStatic(%q) = %v, %v", host, address, found)
		}
	}
}

func TestResolverRejectsInvalidStaticDNS(t *testing.T) {
	resolver, err := NewResolver(&resolverStackStub{}, "", "", 60, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.SetPermanentDNS("", net.ParseIP("10.0.0.8")); err == nil {
		t.Fatal("empty host was accepted")
	}
	if err := resolver.SetPermanentDNS("app.example.edu", nil); err == nil {
		t.Fatal("nil address was accepted")
	}
}

type resolverStackStub struct {
	err          error
	contextValue any
}

func (stack *resolverStackStub) DialTCP(ctx context.Context, _ *net.TCPAddr) (net.Conn, error) {
	stack.contextValue = ctx.Value(contextKeyForTest("request"))
	return nil, stack.err
}

func (stack *resolverStackStub) DialUDP(ctx context.Context, _ *net.UDPAddr) (net.Conn, error) {
	stack.contextValue = ctx.Value(contextKeyForTest("request"))
	return nil, stack.err
}

type contextKeyForTest string

func TestResolverFallsBackFromRemoteUDPToRemoteTCP(t *testing.T) {
	remote, closeRemote := startResolverTestDNS(t, func(network string, request *dns.Msg) *dns.Msg {
		response := new(dns.Msg)
		response.SetReply(request)
		if network == "udp" {
			response.Rcode = dns.RcodeServerFailure
			return response
		}
		response.Answer = append(response.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: request.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("10.0.0.8"),
		})
		return response
	})
	defer closeRemote()

	resolver := newResolverForFallbackTest(remote, remote)
	_, address, err := resolver.Resolve(context.Background(), "app.example.edu")
	if err != nil {
		t.Fatal(err)
	}
	if !address.Equal(net.ParseIP("10.0.0.8")) {
		t.Fatalf("address = %v", address)
	}
	if !resolver.useTCP {
		t.Fatal("resolver did not remember remote TCP preference")
	}
}

func TestResolverFallsBackToSecondaryAfterRemoteFailure(t *testing.T) {
	remote, closeRemote := startResolverTestDNS(t, func(_ string, request *dns.Msg) *dns.Msg {
		response := new(dns.Msg)
		response.SetReply(request)
		response.Rcode = dns.RcodeServerFailure
		return response
	})
	defer closeRemote()
	secondary, closeSecondary := startResolverTestDNS(t, func(_ string, request *dns.Msg) *dns.Msg {
		response := new(dns.Msg)
		response.SetReply(request)
		response.Answer = append(response.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: request.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("192.0.2.8"),
		})
		return response
	})
	defer closeSecondary()

	resolver := newResolverForFallbackTest(remote, secondary)
	_, address, err := resolver.Resolve(context.Background(), "internet.example")
	if err != nil {
		t.Fatal(err)
	}
	if !address.Equal(net.ParseIP("192.0.2.8")) {
		t.Fatalf("address = %v", address)
	}
}

func TestResolverTimesOutRemoteDNSBeforeSecondaryFallback(t *testing.T) {
	releaseRemote := make(chan struct{})
	remote, closeRemote := startResolverTestDNS(t, func(_ string, request *dns.Msg) *dns.Msg {
		<-releaseRemote
		response := new(dns.Msg)
		response.SetReply(request)
		return response
	})
	defer closeRemote()
	defer close(releaseRemote)

	secondary, closeSecondary := startResolverTestDNS(t, func(_ string, request *dns.Msg) *dns.Msg {
		response := new(dns.Msg)
		response.SetReply(request)
		response.Answer = append(response.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: request.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("192.0.2.8"),
		})
		return response
	})
	defer closeSecondary()

	resolver := newResolverForFallbackTest(remote, secondary)
	resolver.remoteDNSTimeout = 50 * time.Millisecond
	startedAt := time.Now()
	_, address, err := resolver.Resolve(context.Background(), "internet.example")
	if err != nil {
		t.Fatal(err)
	}
	if !address.Equal(net.ParseIP("192.0.2.8")) {
		t.Fatalf("address = %v", address)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("secondary fallback took %s", elapsed)
	}
}

func TestResolverSharesAndCachesSecondaryFallback(t *testing.T) {
	remote, closeRemote := startResolverTestDNS(t, func(_ string, request *dns.Msg) *dns.Msg {
		response := new(dns.Msg)
		response.SetReply(request)
		response.Rcode = dns.RcodeServerFailure
		return response
	})
	defer closeRemote()

	var secondaryRequests atomic.Int32
	secondaryEntered := make(chan struct{})
	releaseSecondary := make(chan struct{})
	var enteredOnce sync.Once
	secondary, closeSecondary := startResolverTestDNS(t, func(_ string, request *dns.Msg) *dns.Msg {
		secondaryRequests.Add(1)
		enteredOnce.Do(func() { close(secondaryEntered) })
		<-releaseSecondary
		response := new(dns.Msg)
		response.SetReply(request)
		response.Answer = append(response.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: request.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("192.0.2.8"),
		})
		return response
	})
	defer closeSecondary()

	resolver := newResolverForFallbackTest(remote, secondary)
	const queryCount = 5
	results := make(chan error, queryCount)
	for range queryCount {
		go func() {
			_, address, err := resolver.Resolve(context.Background(), "internet.example")
			if err == nil && !address.Equal(net.ParseIP("192.0.2.8")) {
				err = errors.New("unexpected secondary address")
			}
			results <- err
		}()
	}

	select {
	case <-secondaryEntered:
	case <-time.After(time.Second):
		t.Fatal("secondary DNS was not queried")
	}
	time.Sleep(50 * time.Millisecond)
	if requests := secondaryRequests.Load(); requests != 1 {
		t.Fatalf("concurrent secondary requests = %d, want 1", requests)
	}
	close(releaseSecondary)
	for range queryCount {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}

	if _, _, err := resolver.Resolve(context.Background(), "internet.example"); err != nil {
		t.Fatal(err)
	}
	if requests := secondaryRequests.Load(); requests != 1 {
		t.Fatalf("secondary requests after cached query = %d, want 1", requests)
	}
}

func TestResolverFollowerHonorsContextCancellation(t *testing.T) {
	remoteEntered := make(chan struct{})
	releaseRemote := make(chan struct{})
	var enteredOnce sync.Once
	remote, closeRemote := startResolverTestDNS(t, func(_ string, request *dns.Msg) *dns.Msg {
		enteredOnce.Do(func() { close(remoteEntered) })
		<-releaseRemote
		response := new(dns.Msg)
		response.SetReply(request)
		response.Answer = append(response.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: request.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("10.0.0.8"),
		})
		return response
	})
	defer closeRemote()
	resolver := newResolverForFallbackTest(remote, remote)
	leaderDone := make(chan error, 1)
	go func() {
		_, _, err := resolver.Resolve(context.Background(), "app.example.edu")
		leaderDone <- err
	}()
	select {
	case <-remoteEntered:
	case <-time.After(time.Second):
		t.Fatal("leader DNS query did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	if _, _, err := resolver.Resolve(ctx, "app.example.edu"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("follower Resolve() error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("canceled follower waited %s", elapsed)
	}
	close(releaseRemote)
	if err := <-leaderDone; err != nil {
		t.Fatal(err)
	}
}

func newResolverForFallbackTest(remoteAddress, secondaryAddress string) *Resolver {
	return &Resolver{
		remoteUDPResolver: resolverForAddress(remoteAddress, "udp"),
		remoteTCPResolver: resolverForAddress(remoteAddress, "tcp"),
		secondaryResolver: resolverForAddress(secondaryAddress, ""),
		dnsCache:          cache.New(time.Minute, time.Minute),
		permanentDNS:      make(map[string]net.IP),
		useRemoteDNS:      true,
	}
}

func resolverForAddress(address, forceNetwork string) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			if forceNetwork != "" {
				network = forceNetwork
			}
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}
}

func startResolverTestDNS(t *testing.T, respond func(string, *dns.Msg) *dns.Msg) (string, func()) {
	t.Helper()
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", packetConn.LocalAddr().String())
	if err != nil {
		_ = packetConn.Close()
		t.Fatal(err)
	}
	handler := dns.HandlerFunc(func(writer dns.ResponseWriter, request *dns.Msg) {
		_ = writer.WriteMsg(respond(writer.LocalAddr().Network(), request))
	})
	udpServer := &dns.Server{PacketConn: packetConn, Handler: handler}
	tcpServer := &dns.Server{Listener: listener, Handler: handler}
	go func() { _ = udpServer.ActivateAndServe() }()
	go func() { _ = tcpServer.ActivateAndServe() }()
	return packetConn.LocalAddr().String(), func() {
		_ = udpServer.Shutdown()
		_ = tcpServer.Shutdown()
	}
}
