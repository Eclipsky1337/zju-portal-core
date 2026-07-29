package networkruntime

import (
	"context"
	"net"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/service"
	"github.com/things-go/go-socks5"
)

type Inbound interface {
	core.Component
	Addr() net.Addr
}

type InboundResolver interface {
	Resolve(context.Context, string) (context.Context, net.IP, error)
	ResolveStatic(string) (net.IP, bool)
}

type InboundDependencies struct {
	Outbound core.Outbound
	Resolver InboundResolver
	Observer core.ConnectionObserver
}

type InboundFactory struct {
	Type    core.ServiceType
	Enabled func(Config) bool
	New     func(Config, InboundDependencies) (Inbound, error)
}

type managedService = Inbound

func defaultInboundFactories() []InboundFactory {
	return []InboundFactory{
		{
			Type:    core.ServiceTypeDNS,
			Enabled: func(config Config) bool { return config.DNSBind != "" },
			New: func(config Config, dependencies InboundDependencies) (Inbound, error) {
				factory := config.newDNSService
				if factory == nil {
					factory = func(bind string, resolver service.DNSResolver) managedService {
						return service.NewManagedDNSService(bind, resolver)
					}
				}
				return factory(config.DNSBind, dependencies.Resolver), nil
			},
		},
		{
			Type:    core.ServiceTypeTUN,
			Enabled: func(config Config) bool { return config.TUNEnabled },
			New: func(config Config, dependencies InboundDependencies) (Inbound, error) {
				factory := config.newTUNService
				if factory == nil {
					factory = newTUNService
				}
				return factory(TUNConfig{
					Name:              config.TUNName,
					Address:           config.TUNAddress,
					MTU:               config.TUNMTU,
					AutoRoute:         config.TUNAutoRoute,
					StrictRoute:       config.TUNStrictRoute,
					Stack:             config.TUNStack,
					UDPTimeout:        time.Duration(config.TUNUDPTimeoutSeconds) * time.Second,
					UDPMaxFlows:       config.TUNUDPMaxFlows,
					DNSHijack:         config.TUNDNSHijack,
					FakeIP:            config.TUNFakeIP,
					FakeIPRange:       config.TUNFakeIPRange,
					RouteAddresses:    config.TUNRouteAddresses,
					Resolver:          dependencies.Resolver,
					OutboundInterface: config.TUNOutboundInterface,
				}, dependencies.Outbound, dependencies.Observer)
			},
		},
		{
			Type:    core.ServiceTypeSOCKS5,
			Enabled: func(config Config) bool { return config.SOCKSBind != "" },
			New: func(config Config, dependencies InboundDependencies) (Inbound, error) {
				factory := config.newSOCKS5Service
				if factory == nil {
					factory = func(bind string, outbound core.Outbound, resolver socks5.NameResolver, username, password string) managedService {
						return service.NewSocks5ServiceWithObserver(bind, outbound, resolver, username, password, dependencies.Observer)
					}
				}
				return factory(config.SOCKSBind, dependencies.Outbound, dependencies.Resolver, config.SOCKSUsername, config.SOCKSPassword), nil
			},
		},
		{
			Type:    core.ServiceTypeHTTP,
			Enabled: func(config Config) bool { return config.HTTPBind != "" },
			New: func(config Config, dependencies InboundDependencies) (Inbound, error) {
				factory := config.newHTTPService
				if factory == nil {
					factory = func(bind string, outbound core.Outbound) managedService {
						return service.NewHTTPServiceWithObserver(bind, outbound, dependencies.Observer)
					}
				}
				return factory(config.HTTPBind, dependencies.Outbound), nil
			},
		},
	}
}
