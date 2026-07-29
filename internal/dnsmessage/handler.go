package dnsmessage

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"

	"github.com/miekg/dns"
)

const DefaultTTL = 60

type Resolver interface {
	Resolve(context.Context, string) (context.Context, net.IP, error)
}

type StaticResolver interface {
	ResolveStatic(string) (net.IP, bool)
}

type Handler struct {
	Resolver Resolver
	FakeIPv4 func(string) (netip.Addr, error)
	TTL      uint32
}

func (handler Handler) Handle(ctx context.Context, request *dns.Msg) *dns.Msg {
	response := new(dns.Msg)
	response.SetReply(request)
	response.Authoritative = false
	response.RecursionAvailable = true
	response.Compress = true
	if request.Opcode != dns.OpcodeQuery {
		response.Rcode = dns.RcodeNotImplemented
		return response
	}

	for _, question := range request.Question {
		if question.Qclass != dns.ClassINET {
			response.Rcode = dns.RcodeNotImplemented
			response.Answer = nil
			return response
		}
		domain := NormalizeDomain(question.Name)
		switch question.Qtype {
		case dns.TypeA:
			address, found, err := handler.resolveIPv4(ctx, domain)
			if err != nil {
				response.Rcode = errorRCode(err)
				response.Answer = nil
				return response
			}
			if found {
				response.Answer = append(response.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: handler.ttl()},
					A:   address.AsSlice(),
				})
			}
		case dns.TypeAAAA:
			address, found, err := handler.resolveIPv6(ctx, domain)
			if err != nil {
				response.Rcode = errorRCode(err)
				response.Answer = nil
				return response
			}
			if found {
				response.Answer = append(response.Answer, &dns.AAAA{
					Hdr:  dns.RR_Header{Name: question.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: handler.ttl()},
					AAAA: address.AsSlice(),
				})
			}
		default:
			response.Rcode = dns.RcodeNotImplemented
			response.Answer = nil
			return response
		}
	}
	return response
}

func (handler Handler) resolveIPv4(ctx context.Context, domain string) (netip.Addr, bool, error) {
	if address, found := handler.resolveStatic(domain); found {
		return address.Unmap(), address.Unmap().Is4(), nil
	}
	if handler.FakeIPv4 != nil {
		address, err := handler.FakeIPv4(domain)
		return address, err == nil, err
	}
	address, err := handler.resolve(ctx, domain)
	if err != nil {
		return netip.Addr{}, false, err
	}
	return address.Unmap(), address.Unmap().Is4(), nil
}

func (handler Handler) resolveIPv6(ctx context.Context, domain string) (netip.Addr, bool, error) {
	if address, found := handler.resolveStatic(domain); found {
		return address, address.Is6(), nil
	}
	if handler.FakeIPv4 != nil {
		return netip.Addr{}, false, nil
	}
	address, err := handler.resolve(ctx, domain)
	if err != nil {
		return netip.Addr{}, false, err
	}
	return address, address.Is6(), nil
}

func (handler Handler) resolveStatic(domain string) (netip.Addr, bool) {
	resolver, ok := handler.Resolver.(StaticResolver)
	if !ok {
		return netip.Addr{}, false
	}
	address, found := resolver.ResolveStatic(domain)
	if !found {
		return netip.Addr{}, false
	}
	parsed, valid := netip.AddrFromSlice(address)
	return parsed, valid
}

func (handler Handler) resolve(ctx context.Context, domain string) (netip.Addr, error) {
	if handler.Resolver == nil {
		return netip.Addr{}, &net.DNSError{Err: "resolver unavailable", Name: domain, IsTemporary: true}
	}
	_, address, err := handler.Resolver.Resolve(ctx, domain)
	if err != nil {
		return netip.Addr{}, err
	}
	parsed, valid := netip.AddrFromSlice(address)
	if !valid {
		return netip.Addr{}, &net.DNSError{Err: "invalid address", Name: domain, IsTemporary: true}
	}
	return parsed, nil
}

func (handler Handler) ttl() uint32 {
	if handler.TTL == 0 {
		return DefaultTTL
	}
	return handler.TTL
}

func errorRCode(err error) int {
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) && dnsError.IsNotFound {
		return dns.RcodeNameError
	}
	return dns.RcodeServerFailure
}

func NormalizeDomain(domain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}
