package dnsmessage

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/miekg/dns"
)

func TestHandlerResponseSemantics(t *testing.T) {
	tests := []struct {
		name     string
		question uint16
		resolver Resolver
		rcode    int
		answers  int
	}{
		{name: "success", question: dns.TypeA, resolver: resolverStub{address: net.ParseIP("10.0.0.8")}, rcode: dns.RcodeSuccess, answers: 1},
		{name: "not found", question: dns.TypeA, resolver: resolverStub{err: &net.DNSError{Err: "no such host", IsNotFound: true}}, rcode: dns.RcodeNameError},
		{name: "temporary failure", question: dns.TypeA, resolver: resolverStub{err: &net.DNSError{Err: "timeout", IsTimeout: true, IsTemporary: true}}, rcode: dns.RcodeServerFailure},
		{name: "unsupported type", question: dns.TypeTXT, resolver: resolverStub{}, rcode: dns.RcodeNotImplemented},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := new(dns.Msg)
			request.SetQuestion("app.example.edu.", test.question)
			response := (Handler{Resolver: test.resolver}).Handle(context.Background(), request)
			if response.Rcode != test.rcode || len(response.Answer) != test.answers {
				t.Fatalf("response rcode=%d answers=%v", response.Rcode, response.Answer)
			}
			if response.Authoritative {
				t.Fatal("recursive DNS response marked authoritative")
			}
		})
	}
}

func TestHandlerRejectsUnsupportedOpcode(t *testing.T) {
	request := new(dns.Msg)
	request.SetQuestion("app.example.edu.", dns.TypeA)
	request.Opcode = dns.OpcodeUpdate
	response := (Handler{Resolver: resolverStub{}}).Handle(context.Background(), request)
	if response.Rcode != dns.RcodeNotImplemented {
		t.Fatalf("rcode = %d", response.Rcode)
	}
}

func TestHandlerUsesStaticRecordBeforeFakeIP(t *testing.T) {
	resolver := staticResolverStub{resolverStub: resolverStub{err: errors.New("upstream should not be called")}, address: net.ParseIP("10.0.0.8")}
	request := new(dns.Msg)
	request.SetQuestion("APP.EXAMPLE.EDU.", dns.TypeA)
	response := (Handler{
		Resolver: resolver,
		FakeIPv4: func(string) (address netip.Addr, err error) {
			t.Fatal("fake IP should not be assigned for a static record")
			return address, err
		},
	}).Handle(context.Background(), request)
	if len(response.Answer) != 1 || !response.Answer[0].(*dns.A).A.Equal(net.ParseIP("10.0.0.8")) {
		t.Fatalf("answers = %v", response.Answer)
	}
}

type resolverStub struct {
	address net.IP
	err     error
}

func (resolver resolverStub) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, resolver.address, resolver.err
}

type staticResolverStub struct {
	resolverStub
	address net.IP
}

func (resolver staticResolverStub) ResolveStatic(string) (net.IP, bool) {
	return resolver.address, true
}
