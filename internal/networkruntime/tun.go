package networkruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/internal/systemdns"
	"github.com/Eclipsky1337/zju-portal-core/log"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/control"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

const (
	defaultTUNName               = "ZJU-Portal"
	defaultTUNAddress            = "172.19.0.1/30"
	defaultTUNMTU         uint32 = 1400
	defaultTUNStack              = "auto"
	defaultTUNUDPTimeout         = 60 * time.Second
	defaultTUNUDPMaxFlows        = 512
	tunCleanupTimeout            = 5 * time.Second
)

type TUNConfig struct {
	Name              string
	Address           string
	MTU               uint32
	AutoRoute         bool
	StrictRoute       bool
	Stack             string
	UDPTimeout        time.Duration
	UDPMaxFlows       int
	DNSHijack         bool
	FakeIP            bool
	FakeIPRange       string
	ControlServerHost string
	RouteAddresses    []netip.Prefix
	Resolver          tunResolver
	OutboundInterface string
	SystemDNS         systemdns.Controller
}

type tunService struct {
	config    TUNConfig
	outbound  core.Outbound
	observer  core.ConnectionObserver
	newDevice func(tun.Options) (tun.Tun, error)
	newStack  func(string, tun.StackOptions) (tun.Stack, error)

	mu               sync.RWMutex
	device           tun.Tun
	stack            tun.Stack
	networkMonitor   tun.NetworkUpdateMonitor
	interfaceMonitor tun.DefaultInterfaceMonitor
	addr             net.Addr
	closed           bool
	startOnce        sync.Once
	startErr         error
	closeOnce        sync.Once
	closeErr         error
	closeDone        chan struct{}
	runErr           error
	done             chan struct{}
	doneOnce         sync.Once

	udpFlowMu sync.Mutex
	udpFlows  map[*tunUDPFlow]struct{}
	fakeIPs   *fakeIPStore
	systemDNS systemdns.Controller
	dnsServer string
}

type tunAddr string

func (tunAddr) Network() string     { return "tun" }
func (addr tunAddr) String() string { return string(addr) }

var _ managedService = (*tunService)(nil)
var _ tun.Handler = (*tunService)(nil)

func newTUNService(config TUNConfig, outbound core.Outbound, observer core.ConnectionObserver) (managedService, error) {
	if config.Name == "" {
		config.Name = defaultTUNName
	}
	if config.Address == "" {
		config.Address = defaultTUNAddress
	}
	if config.MTU == 0 {
		config.MTU = defaultTUNMTU
	}
	if config.Stack == "" {
		config.Stack = defaultTUNStack
	}
	if config.UDPTimeout <= 0 {
		config.UDPTimeout = defaultTUNUDPTimeout
	}
	if config.UDPMaxFlows <= 0 {
		config.UDPMaxFlows = defaultTUNUDPMaxFlows
	}
	if config.FakeIP {
		if !config.AutoRoute {
			return nil, fmt.Errorf("TUN fake IP requires auto route")
		}
		config.DNSHijack = true
		if config.FakeIPRange == "" {
			config.FakeIPRange = defaultTUNFakeIPRange
		}
	}
	prefix, err := netip.ParsePrefix(config.Address)
	if err != nil {
		return nil, fmt.Errorf("parse TUN address %q: %w", config.Address, err)
	}
	var dnsServer string
	if config.DNSHijack {
		if !config.AutoRoute {
			return nil, fmt.Errorf("TUN DNS hijack requires auto route")
		}
		dnsAddress, dnsErr := tunDNSServerAddress(prefix)
		if dnsErr != nil {
			return nil, dnsErr
		}
		dnsServer = dnsAddress.String()
		if config.SystemDNS == nil {
			config.SystemDNS = systemdns.New(config.OutboundInterface)
		}
	}
	service := &tunService{
		config:    config,
		outbound:  outbound,
		observer:  observer,
		newStack:  tun.NewStack,
		udpFlows:  make(map[*tunUDPFlow]struct{}),
		done:      make(chan struct{}),
		closeDone: make(chan struct{}),
		systemDNS: config.SystemDNS,
		dnsServer: dnsServer,
	}
	service.newDevice = service.createDevice
	if config.FakeIP {
		fakeIPs, err := newFakeIPStore(config.FakeIPRange)
		if err != nil {
			return nil, err
		}
		service.fakeIPs = fakeIPs
	}
	return service, nil
}

func (service *tunService) createDevice(options tun.Options) (tun.Tun, error) {
	interfaceFinder := control.NewDefaultInterfaceFinder()
	if err := interfaceFinder.Update(); err != nil {
		return nil, fmt.Errorf("initialize TUN interface finder: %w", err)
	}
	networkMonitor, err := tun.NewNetworkUpdateMonitor(logger.NOP())
	if err != nil {
		return nil, fmt.Errorf("create TUN network monitor: %w", err)
	}
	if err := networkMonitor.Start(); err != nil {
		_ = networkMonitor.Close()
		return nil, fmt.Errorf("start TUN network monitor: %w", err)
	}
	interfaceMonitor, err := tun.NewDefaultInterfaceMonitor(networkMonitor, logger.NOP(), tun.DefaultInterfaceMonitorOptions{
		InterfaceFinder:    interfaceFinder,
		OverrideAndroidVPN: true,
	})
	if err != nil {
		_ = networkMonitor.Close()
		return nil, fmt.Errorf("create TUN interface monitor: %w", err)
	}
	if err := interfaceMonitor.Start(); err != nil {
		_ = interfaceMonitor.Close()
		_ = networkMonitor.Close()
		return nil, fmt.Errorf("start TUN interface monitor: %w", err)
	}
	options.InterfaceFinder = interfaceFinder
	options.InterfaceMonitor = interfaceMonitor
	device, err := tun.New(options)
	if err != nil {
		_ = interfaceMonitor.Close()
		_ = networkMonitor.Close()
		return nil, err
	}
	service.mu.Lock()
	service.networkMonitor = networkMonitor
	service.interfaceMonitor = interfaceMonitor
	service.mu.Unlock()
	return device, nil
}

func (service *tunService) closeMonitors() error {
	service.mu.Lock()
	interfaceMonitor := service.interfaceMonitor
	networkMonitor := service.networkMonitor
	service.interfaceMonitor = nil
	service.networkMonitor = nil
	service.mu.Unlock()
	err := errors.Join(
		closeTUNMonitor(interfaceMonitor),
		closeTUNMonitor(networkMonitor),
	)
	return err
}

func closeTUNMonitor(monitor interface{ Close() error }) error {
	if monitor == nil {
		return nil
	}
	return monitor.Close()
}

func tunDNSServerAddress(prefix netip.Prefix) (netip.Addr, error) {
	if !prefix.Addr().Is4() {
		return netip.Addr{}, fmt.Errorf("TUN DNS hijack requires an IPv4 TUN address")
	}
	base := prefix.Masked().Addr()
	candidate := base.Next()
	if candidate == prefix.Addr() {
		candidate = candidate.Next()
	}
	if !candidate.IsValid() || !prefix.Contains(candidate) {
		return netip.Addr{}, fmt.Errorf("TUN address %s has no peer address for system DNS", prefix)
	}
	return candidate, nil
}

func (service *tunService) Start(ctx context.Context) error {
	service.startOnce.Do(func() {
		service.startErr = service.start(ctx)
	})
	return service.startErr
}

func (service *tunService) start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	prefix, _ := netip.ParsePrefix(service.config.Address)
	name := tun.CalculateInterfaceName(service.config.Name)
	routeAddresses := service.routeAddresses()
	options := tun.Options{
		Name:                                  name,
		MTU:                                   service.config.MTU,
		Inet4Address:                          []netip.Prefix{prefix},
		AutoRoute:                             service.config.AutoRoute,
		StrictRoute:                           service.config.StrictRoute,
		Inet4RouteAddress:                     routeAddresses,
		IPRoute2TableIndex:                    1898,
		IPRoute2RuleIndex:                     tun.DefaultIPRoute2RuleIndex,
		IPRoute2AutoRedirectFallbackRuleIndex: tun.DefaultIPRoute2AutoRedirectFallbackRuleIndex,
		EXP_DisableDNSHijack:                  !service.config.DNSHijack,
		Logger:                                logger.NOP(),
	}
	if service.config.DNSHijack {
		dnsAddress, parseErr := netip.ParseAddr(service.dnsServer)
		if parseErr != nil {
			return fmt.Errorf("parse TUN DNS server %q: %w", service.dnsServer, parseErr)
		}
		options.DNSServers = []netip.Addr{dnsAddress}
	}
	device, err := service.newDevice(options)
	if err != nil {
		return fmt.Errorf("create TUN device: %w", err)
	}
	if err := device.Start(); err != nil {
		_ = device.Close()
		_ = service.closeMonitors()
		return fmt.Errorf("start TUN device: %w", err)
	}
	monitoredDevice := wrapTUNDevice(device, service.handleDeviceError)
	stack, err := service.newStack(tunStackName(service.config.Stack), tun.StackOptions{
		Context:    ctx,
		Tun:        monitoredDevice,
		TunOptions: options,
		UDPTimeout: service.config.UDPTimeout,
		Handler:    service,
		Logger:     logger.NOP(),
	})
	if err != nil {
		_ = device.Close()
		_ = service.closeMonitors()
		return fmt.Errorf("create TUN stack: %w", err)
	}
	if err := stack.Start(); err != nil {
		_ = stack.Close()
		_ = device.Close()
		_ = service.closeMonitors()
		return fmt.Errorf("start TUN stack: %w", err)
	}
	if service.systemDNS != nil {
		if err := service.systemDNS.Apply(ctx, service.dnsServer); err != nil {
			_ = stack.Close()
			_ = device.Close()
			_ = service.closeMonitors()
			return fmt.Errorf("configure system DNS: %w", err)
		}
		log.Printf("System DNS on %s now uses %s", service.config.OutboundInterface, service.dnsServer)
	}

	service.mu.Lock()
	if service.closed {
		service.mu.Unlock()
		if service.systemDNS != nil {
			_ = service.systemDNS.Restore(context.Background())
		}
		_ = stack.Close()
		_ = device.Close()
		_ = service.closeMonitors()
		return context.Canceled
	}
	service.device = device
	service.stack = stack
	service.addr = tunAddr(name + " " + prefix.String())
	service.mu.Unlock()
	context.AfterFunc(ctx, func() { _ = service.Close(context.Background()) })
	log.Printf("TUN interface %s listening on %s", name, prefix)
	return nil
}

func (service *tunService) routeAddresses() []netip.Prefix {
	routes := append([]netip.Prefix(nil), service.config.RouteAddresses...)
	if len(routes) == 0 || !service.config.DNSHijack {
		return routes
	}
	dnsAddress, err := netip.ParseAddr(service.dnsServer)
	if err != nil {
		return routes
	}
	for _, route := range routes {
		if route.Contains(dnsAddress) {
			return routes
		}
	}
	return append(routes, netip.PrefixFrom(dnsAddress, dnsAddress.BitLen()))
}

func tunStackName(configured string) string {
	if configured == "" || configured == defaultTUNStack {
		return automaticTUNStackName
	}
	return configured
}

func (service *tunService) Addr() net.Addr {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.addr
}

func (service *tunService) Done() <-chan struct{} { return service.done }

func (service *tunService) Err() error {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.runErr
}

func (service *tunService) Close(ctx context.Context) error {
	service.closeOnce.Do(func() {
		go service.close()
	})
	select {
	case <-service.closeDone:
		return service.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (service *tunService) close() {
	service.mu.Lock()
	service.closed = true
	stack := service.stack
	device := service.device
	service.addr = nil
	service.mu.Unlock()
	var closeErrors []error
	if service.systemDNS != nil {
		ctx, cancel := context.WithTimeout(context.Background(), tunCleanupTimeout)
		restoreErr := service.systemDNS.Restore(ctx)
		cancel()
		closeErrors = append(closeErrors, restoreErr)
		if restoreErr == nil {
			log.Printf("System DNS on %s restored", service.config.OutboundInterface)
		}
	}
	if stack != nil {
		closeErrors = append(closeErrors, stack.Close())
	}
	if device != nil {
		closeErrors = append(closeErrors, device.Close())
	}
	closeErrors = append(closeErrors, service.closeMonitors())
	service.closeErr = errors.Join(closeErrors...)
	service.signalDone()
	close(service.closeDone)
}

func (service *tunService) handleDeviceError(err error) {
	service.mu.Lock()
	if service.closed || isExpectedTUNCloseError(err) {
		service.mu.Unlock()
		return
	}
	if service.runErr != nil {
		service.mu.Unlock()
		return
	}
	service.runErr = err
	service.mu.Unlock()
	log.Printf("TUN stack error: %v", err)
	service.signalDone()
	go func() { _ = service.Close(context.Background()) }()
}

func (service *tunService) signalDone() { service.doneOnce.Do(func() { close(service.done) }) }

func (service *tunService) PrepareConnection(string, M.Socksaddr, M.Socksaddr, tun.DirectRouteContext, time.Duration) (tun.DirectRouteDestination, error) {
	return nil, nil
}

func (service *tunService) NewConnectionEx(ctx context.Context, inbound net.Conn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	err := service.NewConnection(ctx, inbound, M.Metadata{Source: source, Destination: destination})
	if onClose != nil {
		onClose(err)
	}
}

func (service *tunService) NewConnection(ctx context.Context, inbound net.Conn, metadata M.Metadata) error {
	if service.config.DNSHijack && metadata.Destination.Port == 53 {
		defer inbound.Close()
		return service.handleTCPDNS(ctx, inbound)
	}
	destination := service.routeDestination(metadata.Destination)
	remote, err := service.outbound.DialContext(ctx, "tcp", destination)
	if err != nil {
		_ = inbound.Close()
		return err
	}
	activity := service.openActivity("tcp", metadata.Source.String(), destination, remote)
	if activity != nil {
		remote = trackLogicalConn(remote, activity)
	}
	defer inbound.Close()
	defer remote.Close()
	return relayTUN(inbound, remote)
}

func (service *tunService) NewError(ctx context.Context, err error) {
	service.mu.RLock()
	closed := service.closed
	service.mu.RUnlock()
	if closed || isExpectedTUNCloseError(err) {
		return
	}
	log.Printf("TUN stack error: %v", err)
}

func (service *tunService) openActivity(network, source, destination string, remote net.Conn) core.ConnectionActivity {
	if service.observer == nil {
		return nil
	}
	route := core.RouteInfoOf(remote)
	return service.observer.OpenConnection(core.ConnectionMetadata{
		Inbound:               "tun",
		Outbound:              route.Outbound,
		RouteReason:           route.Reason,
		Source:                source,
		Network:               network,
		Destination:           destination,
		TransportConnectionID: transportConnectionID(remote),
	}, remote.Close)
}

func relayTUN(left, right net.Conn) error {
	errCh := make(chan error, 2)
	go func() { _, err := io.Copy(left, right); errCh <- err }()
	go func() { _, err := io.Copy(right, left); errCh <- err }()
	err := <-errCh
	if isExpectedTUNCloseError(err) {
		return nil
	}
	return err
}

func isExpectedTUNCloseError(err error) bool {
	return err == nil || E.IsClosedOrCanceled(err)
}
