package networkruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/dial"
	"github.com/Eclipsky1337/zju-portal-core/internal/underlay"
	"github.com/Eclipsky1337/zju-portal-core/resolve"
	"github.com/Eclipsky1337/zju-portal-core/service"
	stackpkg "github.com/Eclipsky1337/zju-portal-core/stack"
	"github.com/Eclipsky1337/zju-portal-core/stack/gvisor"
	"github.com/Eclipsky1337/zju-portal-core/stack/tcptunnel"
	"github.com/things-go/go-socks5"
)

type Config struct {
	TCPTunnelMode        bool
	DisableRemoteDNS     bool
	RemoteDNSServer      string
	SecondaryDNSServer   string
	DNSTTL               uint64
	DNSBind              string
	Hosts                map[string]string
	SOCKSBind            string
	SOCKSUsername        string
	SOCKSPassword        string
	HTTPBind             string
	TUNEnabled           bool
	TUNName              string
	TUNAddress           string
	TUNMTU               uint32
	TUNAutoRoute         bool
	TUNRouteAll          bool
	TUNStrictRoute       bool
	TUNStack             string
	TUNOutboundInterface string
	TUNUDPTimeoutSeconds int
	TUNUDPMaxFlows       int
	TUNDNSHijack         bool
	TUNFakeIP            bool
	TUNFakeIPRange       string
	TUNRouteAddresses    []netip.Prefix
	RoutingMode          core.RoutingMode
	InternetOutbound     core.InternetOutboundConfig
	newSOCKS5Service     func(string, core.Outbound, socks5.NameResolver, string, string) managedService
	newHTTPService       func(string, core.Outbound) managedService
	newDNSService        func(string, service.DNSResolver) managedService
	newTUNService        func(TUNConfig, core.Outbound, core.ConnectionObserver) (managedService, error)
	newInternetOutbound  func(core.InternetOutboundConfig) (core.Outbound, error)
	InboundFactories     []InboundFactory
}

type serviceEntry struct {
	typeName  core.ServiceType
	service   managedService
	mu        sync.RWMutex
	running   bool
	lastError string
}

type Runtime struct {
	vpn            *switchableOutbound
	router         *dial.Router
	outbound       *trackedOutbound
	connections    *connectionTracker
	resolver       *switchableResolver
	cancel         context.CancelFunc
	services       []*serviceEntry
	serviceEvents  chan core.ServiceStatus
	backendMu      sync.RWMutex
	backend        *vpnBackend
	resourceRoutes bool
	routeAddresses []netip.Prefix
	closed         bool
	closeOnce      sync.Once
	closeErr       error
}

type vpnBackend struct {
	outbound core.Outbound
	resolver *resolve.Resolver
	stack    stackpkg.Managed
	cancel   context.CancelFunc
	routes   []netip.Prefix

	runMu     sync.RWMutex
	runErr    error
	done      chan struct{}
	doneOnce  sync.Once
	closeOnce sync.Once
	closeErr  error
}

type switchableOutbound struct {
	mu       sync.RWMutex
	delegate core.Outbound
	closed   bool
}

type switchableResolver struct {
	mu       sync.RWMutex
	delegate *resolve.Resolver
}

var _ core.Outbound = (*Runtime)(nil)

func New(ctx context.Context, vpnClient client.Client, config Config) (*Runtime, error) {
	backend, err := newVPNBackend(ctx, vpnClient, config)
	if err != nil {
		return nil, err
	}
	resourceRoutes := config.TUNEnabled && config.TUNAutoRoute && !config.TUNRouteAll
	if resourceRoutes {
		config.TUNRouteAddresses = append([]netip.Prefix(nil), backend.routes...)
	}
	internetFactory := config.newInternetOutbound
	directOutbound := core.Outbound(dial.NewDirectOutbound())
	if config.TUNEnabled && config.TUNAutoRoute && config.TUNOutboundInterface == "" {
		config.TUNOutboundInterface = underlay.New("", underlay.Options{AutoDetect: true}).InterfaceName()
	}
	if config.TUNEnabled && config.TUNAutoRoute && config.TUNOutboundInterface == "" {
		_ = backend.Close(context.Background())
		return nil, core.WrapError(core.ErrorCodeInterfaceUnavailable, "TUN auto route requires a detectable outbound interface", false, nil)
	}
	if internetFactory == nil {
		if config.TUNEnabled && config.TUNAutoRoute {
			interfaceName := config.TUNOutboundInterface
			boundDialer := underlay.New("", underlay.Options{InterfaceName: interfaceName})
			directOutbound = dial.NewDirectOutboundWithDialer(boundDialer)
			internetFactory = func(config core.InternetOutboundConfig) (core.Outbound, error) {
				return dial.NewInternetOutboundWithDialer(config, boundDialer)
			}
		} else {
			internetFactory = dial.NewInternetOutbound
		}
	}
	internetOutbound, err := internetFactory(config.InternetOutbound)
	if err != nil {
		_ = backend.Close(context.Background())
		return nil, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	vpn := &switchableOutbound{delegate: backend.outbound}
	resolver := &switchableResolver{delegate: backend.resolver}
	router, err := dial.NewRouterWithDirect(vpn, internetOutbound, directOutbound, config.RoutingMode)
	if err != nil {
		cancel()
		_ = internetOutbound.Close(context.Background())
		_ = backend.Close(context.Background())
		return nil, err
	}
	runtime := &Runtime{
		vpn:            vpn,
		router:         router,
		outbound:       newTrackedOutbound(router),
		connections:    newConnectionTracker(),
		resolver:       resolver,
		cancel:         cancel,
		backend:        backend,
		resourceRoutes: resourceRoutes,
		routeAddresses: append([]netip.Prefix(nil), backend.routes...),
		serviceEvents:  make(chan core.ServiceStatus, 16),
	}
	if err := runtime.startServices(runCtx, config); err != nil {
		_ = runtime.Close(context.Background())
		return nil, err
	}
	return runtime, nil
}

func newVPNBackend(ctx context.Context, vpnClient client.Client, config Config) (*vpnBackend, error) {
	ipResources, err := optionalIPResources(vpnClient)
	if err != nil {
		return nil, err
	}
	domainResources, err := optionalDomainResources(vpnClient)
	if err != nil {
		return nil, err
	}
	dnsRecords, err := optionalDNSRecords(vpnClient)
	if err != nil {
		return nil, err
	}
	var routes []netip.Prefix
	if config.TUNEnabled && config.TUNAutoRoute && !config.TUNRouteAll {
		fakeIPRange := ""
		if config.TUNFakeIP {
			fakeIPRange = config.TUNFakeIPRange
		}
		routes, err = buildResourceRoutePrefixes(ipResources, dnsRecords, routeExcludedIPs(vpnClient), fakeIPRange)
		if err != nil {
			return nil, err
		}
		ipResources = addImplicitRouteResources(ipResources, dnsRecords)
	}

	var vpnStack stackpkg.Stack
	if config.TCPTunnelMode {
		vpnStack, err = tcptunnel.NewStack(vpnClient)
	} else {
		vpnStack, err = gvisor.NewStack(vpnClient)
	}
	if err != nil {
		return nil, err
	}
	managedStack := vpnStack.(stackpkg.Managed)

	remoteDNSServer := config.RemoteDNSServer
	useRemoteDNS := !config.DisableRemoteDNS
	if useRemoteDNS && (remoteDNSServer == "" || remoteDNSServer == "auto") {
		remoteDNSServer, err = vpnClient.DNSServer()
		if err != nil {
			if !errors.Is(err, client.ErrResourceNotFound) {
				_ = managedStack.Close(context.Background())
				return nil, err
			}
			useRemoteDNS = false
			remoteDNSServer = ""
		}
	}
	ttl := config.DNSTTL
	if ttl == 0 {
		ttl = 3600
	}
	var secondaryDial func(context.Context, string, string) (net.Conn, error)
	if config.TUNEnabled && config.TUNAutoRoute {
		interfaceName := config.TUNOutboundInterface
		if interfaceName == "" {
			interfaceName = underlay.New("", underlay.Options{AutoDetect: true}).InterfaceName()
		}
		if interfaceName != "" {
			secondaryDial = underlay.New("", underlay.Options{InterfaceName: interfaceName}).DialContext
		}
	}
	resolver, err := resolve.NewResolverWithSecondaryDialer(vpnStack, remoteDNSServer, config.SecondaryDNSServer, ttl, domainResources, dnsRecords, useRemoteDNS, secondaryDial)
	if err != nil {
		_ = managedStack.Close(context.Background())
		return nil, err
	}
	for host, addressText := range config.Hosts {
		address := net.ParseIP(addressText)
		if address == nil {
			_ = resolver.CloseContext(context.Background())
			_ = managedStack.Close(context.Background())
			return nil, fmt.Errorf("invalid static DNS address for %q: %q", host, addressText)
		}
		if err := resolver.SetPermanentDNS(host, address); err != nil {
			_ = resolver.CloseContext(context.Background())
			_ = managedStack.Close(context.Background())
			return nil, err
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	backend := &vpnBackend{
		outbound: dial.NewVPNOutbound(vpnStack, resolver, ipResources),
		resolver: resolver,
		stack:    managedStack,
		cancel:   cancel,
		routes:   routes,
		done:     make(chan struct{}),
	}
	if !config.TCPTunnelMode {
		go backend.runStack(runCtx)
	}
	return backend, nil
}

func (runtime *Runtime) ReplaceVPN(ctx context.Context, vpnClient client.Client, config Config) error {
	backend, err := newVPNBackend(ctx, vpnClient, config)
	if err != nil {
		return err
	}

	runtime.backendMu.Lock()
	if runtime.closed {
		runtime.backendMu.Unlock()
		_ = backend.Close(context.Background())
		return core.WrapError(core.ErrorCodeOutboundUnavailable, "network runtime is closed", false, nil)
	}
	if runtime.resourceRoutes && !equalRoutePrefixes(runtime.routeAddresses, backend.routes) {
		runtime.backendMu.Unlock()
		_ = backend.Close(context.Background())
		return core.WrapError(core.ErrorCodeRestartRequired, "VPN resource routes changed; restart Core to update TUN routes", false, nil)
	}
	oldBackend := runtime.backend
	runtime.backend = backend
	runtime.vpn.Replace(backend.outbound)
	runtime.resolver.Replace(backend.resolver)
	runtime.backendMu.Unlock()

	if oldBackend != nil {
		_ = oldBackend.Close(context.Background())
	}
	return nil
}

func (runtime *Runtime) TrafficStats() core.TrafficStats {
	traffic := runtime.outbound.TrafficStats()
	connections := runtime.connections.TrafficStats()
	traffic.ActiveConnections = connections.ActiveConnections
	traffic.TotalConnections = connections.TotalConnections
	return traffic
}

func (runtime *Runtime) Connections() []core.ConnectionInfo { return runtime.connections.Connections() }

func (runtime *Runtime) CloseConnection(id string) error {
	return runtime.connections.CloseConnection(id)
}

func (runtime *Runtime) TransportConnections() []core.TransportConnectionInfo {
	return runtime.outbound.TransportConnections()
}

func (runtime *Runtime) RoutingMode() core.RoutingMode {
	return runtime.router.Mode()
}

func (runtime *Runtime) SetRoutingMode(mode core.RoutingMode) (core.RoutingMode, error) {
	return runtime.router.SetMode(mode)
}

func (runtime *Runtime) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := runtime.outbound.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	route := core.RouteInfoOf(conn)
	activity := runtime.connections.OpenConnection(core.ConnectionMetadata{
		Inbound:               "embedded",
		Outbound:              route.Outbound,
		RouteReason:           route.Reason,
		Network:               network,
		Destination:           address,
		TransportConnectionID: transportConnectionID(conn),
	}, conn.Close)
	return trackLogicalConn(conn, activity), nil
}

func (runtime *Runtime) Done() <-chan struct{} {
	runtime.backendMu.RLock()
	backend := runtime.backend
	runtime.backendMu.RUnlock()
	if backend == nil {
		return nil
	}
	return backend.Done()
}

func (runtime *Runtime) Err() error {
	runtime.backendMu.RLock()
	backend := runtime.backend
	runtime.backendMu.RUnlock()
	if backend == nil {
		return nil
	}
	return backend.Err()
}

func (runtime *Runtime) Close(ctx context.Context) error {
	runtime.closeOnce.Do(func() {
		runtime.backendMu.Lock()
		runtime.closed = true
		runtime.backendMu.Unlock()
		var closeErrors []error
		for index := len(runtime.services) - 1; index >= 0; index-- {
			entry := runtime.services[index]
			err := entry.service.Close(ctx)
			entry.markStopped(err)
			closeErrors = append(closeErrors, err)
		}
		runtime.cancel()
		closeErrors = append(closeErrors, runtime.connections.Close(), runtime.outbound.Close(ctx))
		runtime.backendMu.Lock()
		backend := runtime.backend
		runtime.backend = nil
		runtime.backendMu.Unlock()
		if backend != nil {
			closeErrors = append(closeErrors, backend.Close(ctx))
		}
		runtime.closeErr = errors.Join(closeErrors...)
	})
	return runtime.closeErr
}

func (runtime *Runtime) Services() []core.ServiceStatus {
	statuses := make([]core.ServiceStatus, 0, len(runtime.services))
	for _, entry := range runtime.services {
		statuses = append(statuses, entry.status())
	}
	return statuses
}

func (runtime *Runtime) ServiceEvents() <-chan core.ServiceStatus { return runtime.serviceEvents }

func (runtime *Runtime) startServices(ctx context.Context, config Config) error {
	factories := append(defaultInboundFactories(), config.InboundFactories...)
	dependencies := InboundDependencies{Outbound: runtime.outbound, Resolver: runtime.resolver, Observer: runtime.connections}
	for _, factory := range factories {
		if factory.Enabled == nil || factory.New == nil || !factory.Enabled(config) {
			continue
		}
		server, err := factory.New(config, dependencies)
		if err != nil {
			return wrapServiceStartError(factory.Type, err)
		}
		if server == nil {
			return wrapServiceStartError(factory.Type, errors.New("inbound factory returned no service"))
		}
		entry := runtime.addService(factory.Type, server)
		if err := server.Start(ctx); err != nil {
			entry.markStopped(err)
			return wrapServiceStartError(factory.Type, err)
		}
		entry.markStarted()
		runtime.monitorService(entry)
	}
	return nil
}

func (runtime *Runtime) addService(serviceType core.ServiceType, service managedService) *serviceEntry {
	entry := &serviceEntry{typeName: serviceType, service: service}
	runtime.services = append(runtime.services, entry)
	return entry
}

func (entry *serviceEntry) markStarted() {
	entry.mu.Lock()
	entry.running = true
	entry.lastError = ""
	entry.mu.Unlock()
}

func (entry *serviceEntry) markStopped(err error) {
	entry.mu.Lock()
	entry.running = false
	if err != nil {
		entry.lastError = err.Error()
	}
	entry.mu.Unlock()
}

func (entry *serviceEntry) status() core.ServiceStatus {
	entry.mu.RLock()
	running := entry.running
	lastError := entry.lastError
	entry.mu.RUnlock()
	address := ""
	if addr := entry.service.Addr(); addr != nil {
		address = addr.String()
	}
	return core.ServiceStatus{Type: entry.typeName, Address: address, Running: running, LastError: lastError}
}

func (runtime *Runtime) monitorService(entry *serviceEntry) {
	provider, ok := entry.service.(interface {
		Done() <-chan struct{}
		Err() error
	})
	if !ok || provider.Done() == nil {
		return
	}
	go func() {
		<-provider.Done()
		entry.markStopped(provider.Err())
		runtime.backendMu.RLock()
		closed := runtime.closed
		runtime.backendMu.RUnlock()
		if closed {
			return
		}
		runtime.serviceEvents <- entry.status()
	}()
}

func (backend *vpnBackend) Done() <-chan struct{} { return backend.done }

func (backend *vpnBackend) Err() error {
	backend.runMu.RLock()
	defer backend.runMu.RUnlock()
	return backend.runErr
}

func (backend *vpnBackend) runStack(ctx context.Context) {
	err := backend.stack.RunContext(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		backend.runMu.Lock()
		if backend.runErr == nil {
			backend.runErr = err
		}
		backend.runMu.Unlock()
	}
	backend.signalDone()
}

func (backend *vpnBackend) Close(ctx context.Context) error {
	backend.closeOnce.Do(func() {
		backend.cancel()
		backend.closeErr = errors.Join(backend.resolver.CloseContext(ctx), backend.stack.Close(ctx))
		backend.signalDone()
	})
	return backend.closeErr
}

func (backend *vpnBackend) signalDone() { backend.doneOnce.Do(func() { close(backend.done) }) }

func (outbound *switchableOutbound) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	outbound.mu.RLock()
	delegate := outbound.delegate
	closed := outbound.closed
	outbound.mu.RUnlock()
	if closed || delegate == nil {
		return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "VPN outbound is unavailable", true, nil)
	}
	return delegate.DialContext(ctx, network, address)
}

func (outbound *switchableOutbound) Replace(delegate core.Outbound) {
	outbound.mu.Lock()
	if !outbound.closed {
		outbound.delegate = delegate
	}
	outbound.mu.Unlock()
}

func (outbound *switchableOutbound) Close(context.Context) error {
	outbound.mu.Lock()
	outbound.closed = true
	outbound.delegate = nil
	outbound.mu.Unlock()
	return nil
}

func (resolver *switchableResolver) Resolve(ctx context.Context, host string) (context.Context, net.IP, error) {
	resolver.mu.RLock()
	delegate := resolver.delegate
	resolver.mu.RUnlock()
	if delegate == nil {
		return ctx, nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "VPN resolver is unavailable", true, nil)
	}
	return delegate.Resolve(ctx, host)
}

func (resolver *switchableResolver) ResolveStatic(host string) (net.IP, bool) {
	resolver.mu.RLock()
	delegate := resolver.delegate
	resolver.mu.RUnlock()
	if delegate == nil {
		return nil, false
	}
	return delegate.ResolveStatic(host)
}

func (resolver *switchableResolver) IsVPNDomain(host string) bool {
	resolver.mu.RLock()
	delegate := resolver.delegate
	resolver.mu.RUnlock()
	return delegate != nil && delegate.IsVPNDomain(host)
}

func (resolver *switchableResolver) Replace(delegate *resolve.Resolver) {
	resolver.mu.Lock()
	resolver.delegate = delegate
	resolver.mu.Unlock()
}

func optionalIPResources(vpnClient client.Client) ([]client.IPResource, error) {
	resources, err := vpnClient.IPResources()
	if errors.Is(err, client.ErrResourceNotFound) {
		return nil, nil
	}
	return resources, err
}

type routeExclusionProvider interface {
	RouteExcludedIPs() []net.IP
}

func routeExcludedIPs(vpnClient client.Client) []net.IP {
	provider, ok := vpnClient.(routeExclusionProvider)
	if !ok {
		return nil
	}
	return provider.RouteExcludedIPs()
}

func optionalDomainResources(vpnClient client.Client) (map[string]client.DomainResource, error) {
	resources, err := vpnClient.DomainResources()
	if errors.Is(err, client.ErrResourceNotFound) {
		return nil, nil
	}
	return resources, err
}

func optionalDNSRecords(vpnClient client.Client) (map[string]net.IP, error) {
	resources, err := vpnClient.DNSResource()
	if errors.Is(err, client.ErrResourceNotFound) {
		return nil, nil
	}
	return resources, err
}
