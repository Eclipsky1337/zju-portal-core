package dial

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/resolve"
)

var (
	ErrNotInResources     = errors.New("destination is not in VPN resources")
	ErrUnsupportedNetwork = errors.New("VPN outbound only supports TCP and UDP")
)

type vpnStack interface {
	DialTCP(ctx context.Context, addr *net.TCPAddr) (net.Conn, error)
	DialUDP(ctx context.Context, addr *net.UDPAddr) (net.Conn, error)
}

type vpnResolver interface {
	Resolve(ctx context.Context, host string) (context.Context, net.IP, error)
}

type VPNOutbound struct {
	stack       vpnStack
	resolver    vpnResolver
	ipResources []client.IPResource
}

var _ core.Outbound = (*VPNOutbound)(nil)

func NewVPNOutbound(stack vpnStack, resolver vpnResolver, ipResources []client.IPResource) *VPNOutbound {
	return &VPNOutbound{stack: stack, resolver: resolver, ipResources: ipResources}
}

func (outbound *VPNOutbound) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid VPN destination %q: %w", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, fmt.Errorf("invalid VPN destination port %q: %w", portText, err)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		if outbound.resolver == nil {
			return nil, fmt.Errorf("resolve VPN destination %q: resolver unavailable", host)
		}
		ctx, ip, err = outbound.resolver.Resolve(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve VPN destination %q: %w", host, err)
		}
	}
	ip = ip.To4()
	if ip == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotInResources, address)
	}
	if !outbound.matchesResources(ctx, ip, port, network) {
		return nil, fmt.Errorf("%w: %s/%s", ErrNotInResources, address, network)
	}

	switch network {
	case "tcp", "tcp4":
		return outbound.stack.DialTCP(ctx, &net.TCPAddr{IP: ip, Port: port})
	case "udp", "udp4":
		return outbound.stack.DialUDP(ctx, &net.UDPAddr{IP: ip, Port: port})
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedNetwork, network)
	}
}

func (outbound *VPNOutbound) matchesResources(ctx context.Context, ip net.IP, port int, network string) bool {
	resourceNetwork := network
	if network == "tcp4" {
		resourceNetwork = "tcp"
	} else if network == "udp4" {
		resourceNetwork = "udp"
	}
	if resource, ok := ctx.Value(resolve.ContextKeyDomainResource).(client.DomainResource); ok {
		if resourceMatches(resource.PortMin, resource.PortMax, resource.Protocol, port, resourceNetwork) {
			return true
		}
	}
	for _, resource := range outbound.ipResources {
		ipMin := resource.IPMin.To4()
		ipMax := resource.IPMax.To4()
		if ipMin != nil && ipMax != nil && bytes.Compare(ip, ipMin) >= 0 && bytes.Compare(ip, ipMax) <= 0 &&
			resourceMatches(resource.PortMin, resource.PortMax, resource.Protocol, port, resourceNetwork) {
			return true
		}
	}
	return false
}

func resourceMatches(portMin, portMax int, protocol string, port int, network string) bool {
	return portMin <= port && port <= portMax && (protocol == network || protocol == "all")
}

func (outbound *VPNOutbound) Close(context.Context) error {
	return nil
}
