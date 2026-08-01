package networkruntime

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"

	"github.com/Eclipsky1337/zju-portal-core/internal/dnsmessage"
	"github.com/miekg/dns"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type tunResolver interface {
	Resolve(context.Context, string) (context.Context, net.IP, error)
}

type tunVPNDomainMatcher interface {
	IsVPNDomain(string) bool
}

type tunFakeIPResolver struct {
	resolver          tunResolver
	matcher           tunVPNDomainMatcher
	controlServerHost string
	routeAll          bool
	routes            []netip.Prefix
	fakeIPs           *fakeIPStore
}

type tunDestination struct {
	dial    string
	display string
}

func (service *tunService) routeDestination(destination M.Socksaddr) tunDestination {
	result := tunDestination{dial: destination.String(), display: destination.String()}
	if service.fakeIPs != nil && destination.Addr.IsValid() {
		if domain, resolved, ok := service.fakeIPs.LookupDestination(destination.Addr); ok {
			result.display = net.JoinHostPort(domain, strconv.Itoa(int(destination.Port)))
			if resolved.IsValid() {
				result.dial = net.JoinHostPort(resolved.String(), strconv.Itoa(int(destination.Port)))
			} else {
				result.dial = result.display
			}
		}
	}
	return result
}

func (service *tunService) handleUDPDNS(ctx context.Context, inbound N.PacketConn, destination M.Socksaddr, packet *buf.Buffer) error {
	response, err := service.handleDNSPayload(ctx, packet.Bytes())
	if err != nil {
		return err
	}
	return inbound.WritePacket(buf.As(response), destination)
}

func (service *tunService) handleTCPDNS(ctx context.Context, conn net.Conn) error {
	header := make([]byte, 2)
	for {
		if _, err := io.ReadFull(conn, header); err != nil {
			if isExpectedTUNCloseError(err) {
				return nil
			}
			return err
		}
		length := int(binary.BigEndian.Uint16(header))
		if length == 0 {
			continue
		}
		request := make([]byte, length)
		if _, err := io.ReadFull(conn, request); err != nil {
			return err
		}
		response, err := service.handleDNSPayload(ctx, request)
		if err != nil {
			return err
		}
		if len(response) > 65535 {
			return fmt.Errorf("TUN DNS response is too large")
		}
		binary.BigEndian.PutUint16(header, uint16(len(response)))
		if _, err := conn.Write(header); err != nil {
			return err
		}
		if _, err := conn.Write(response); err != nil {
			return err
		}
	}
}

func (service *tunService) handleDNSPayload(ctx context.Context, payload []byte) ([]byte, error) {
	request := new(dns.Msg)
	if err := request.Unpack(payload); err != nil {
		return nil, fmt.Errorf("unpack TUN DNS query: %w", err)
	}
	handler := dnsmessage.Handler{Resolver: service.config.Resolver}
	if service.fakeIPs != nil {
		if service.config.Resolver == nil {
			handler.FakeIPv4 = service.fakeIPs.Assign
		} else {
			resolver := &tunFakeIPResolver{
				resolver:          service.config.Resolver,
				controlServerHost: normalizeDomain(service.config.ControlServerHost),
				routeAll:          service.config.RouteAll,
				routes:            service.config.RouteAddresses,
				fakeIPs:           service.fakeIPs,
			}
			resolver.matcher, _ = service.config.Resolver.(tunVPNDomainMatcher)
			handler.Resolver = resolver
		}
	}
	return handler.Handle(ctx, request).Pack()
}

func (resolver *tunFakeIPResolver) Resolve(ctx context.Context, host string) (context.Context, net.IP, error) {
	if resolver.controlServerHost != "" && normalizeDomain(host) == resolver.controlServerHost {
		return resolver.resolver.Resolve(ctx, host)
	}
	if resolver.matcher != nil && resolver.matcher.IsVPNDomain(host) {
		return resolver.assign(ctx, host)
	}
	resolvedCtx, address, err := resolver.resolver.Resolve(ctx, host)
	if err != nil {
		return resolvedCtx, nil, err
	}
	parsed, valid := netip.AddrFromSlice(address)
	if valid {
		parsed = parsed.Unmap()
		for _, route := range resolver.routes {
			if route.Contains(parsed) {
				return resolver.assign(resolvedCtx, host)
			}
		}
		if resolver.routeAll && parsed.Is4() {
			return resolver.assignResolved(resolvedCtx, host, parsed)
		}
	}
	return resolvedCtx, address, nil
}

func (resolver *tunFakeIPResolver) assign(ctx context.Context, host string) (context.Context, net.IP, error) {
	address, err := resolver.fakeIPs.Assign(host)
	if err != nil {
		return ctx, nil, err
	}
	return ctx, net.IP(address.AsSlice()), nil
}

func (resolver *tunFakeIPResolver) assignResolved(ctx context.Context, host string, resolved netip.Addr) (context.Context, net.IP, error) {
	address, err := resolver.fakeIPs.AssignResolved(host, resolved)
	if err != nil {
		return ctx, nil, err
	}
	return ctx, net.IP(address.AsSlice()), nil
}
