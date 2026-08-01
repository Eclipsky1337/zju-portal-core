package networkruntime

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/dial"
	"github.com/Eclipsky1337/zju-portal-core/service"
	"github.com/things-go/go-socks5"
)

func TestGvisorRuntimeReportsTerminalRunError(t *testing.T) {
	wantErr := errors.New("l3 tunnel stopped")
	vpnClient := &clientStub{l3Conn: &failingL3Conn{err: wantErr}}
	internet := &outboundStub{}
	runtime, err := New(context.Background(), vpnClient, Config{
		DisableRemoteDNS: true,
		newInternetOutbound: func(core.InternetOutboundConfig) (core.Outbound, error) {
			return internet, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())

	select {
	case <-runtime.Done():
	case <-time.After(time.Second):
		t.Fatal("runtime health signal was not closed")
	}
	if !errors.Is(runtime.Err(), wantErr) {
		t.Fatalf("runtime error = %v", runtime.Err())
	}
	conn, err := runtime.DialContext(context.Background(), "tcp", "192.0.2.1:443")
	if err != nil {
		t.Fatalf("internet dial after VPN failure: %v", err)
	}
	_ = conn.Close()
	if internet.dialCount.Load() != 1 {
		t.Fatalf("internet dial count = %d", internet.dialCount.Load())
	}
}

func TestTCPTunnelRuntimeReportsClientHealthFailure(t *testing.T) {
	wantErr := client.ErrSessionInvalid
	healthDone := make(chan struct{})
	vpnClient := &clientStub{healthDone: healthDone, healthErr: wantErr}
	runtime, err := New(context.Background(), vpnClient, Config{TCPTunnelMode: true, DisableRemoteDNS: true})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())

	close(healthDone)
	select {
	case <-runtime.Done():
	case <-time.After(time.Second):
		t.Fatal("runtime health signal was not closed")
	}
	if !errors.Is(runtime.Err(), wantErr) {
		t.Fatalf("runtime error = %v, want %v", runtime.Err(), wantErr)
	}
}

func TestGvisorRuntimeReportsClientHealthFailure(t *testing.T) {
	wantErr := client.ErrSessionInvalid
	healthDone := make(chan struct{})
	localConn, remoteConn := net.Pipe()
	defer remoteConn.Close()
	vpnClient := &clientStub{
		healthDone: healthDone,
		healthErr:  wantErr,
		l3Conn:     localConn,
	}
	runtime, err := New(context.Background(), vpnClient, Config{DisableRemoteDNS: true})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())

	close(healthDone)
	select {
	case <-runtime.Done():
	case <-time.After(time.Second):
		t.Fatal("runtime health signal was not closed")
	}
	if !errors.Is(runtime.Err(), wantErr) {
		t.Fatalf("runtime error = %v, want %v", runtime.Err(), wantErr)
	}
}

func TestTCPTunnelRuntimeDialsDomainResourceWithoutLocalProxy(t *testing.T) {
	vpnClient := &clientStub{
		domainResources: map[string]client.DomainResource{
			"example.edu": {PortMin: 443, PortMax: 443, Protocol: "tcp"},
		},
		dnsRecords: map[string]net.IP{"app.example.edu": net.ParseIP("10.0.0.8")},
	}
	runtime, err := New(context.Background(), vpnClient, Config{TCPTunnelMode: true, DisableRemoteDNS: true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer runtime.Close(context.Background())

	conn, err := runtime.DialContext(context.Background(), "tcp", "app.example.edu:443")
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	_ = conn.Close()
	if got := vpnClient.dialCount.Load(); got != 1 {
		t.Fatalf("VPN dial count = %d, want 1", got)
	}
	if got := vpnClient.lastAddress; got != "10.0.0.8:443" {
		t.Fatalf("VPN dial address = %q", got)
	}
}

func TestRuntimeRejectsInvalidStaticDNSAddress(t *testing.T) {
	_, err := New(context.Background(), &clientStub{}, Config{
		TCPTunnelMode:    true,
		DisableRemoteDNS: true,
		Hosts:            map[string]string{"app.example.edu": "not-an-ip"},
	})
	if err == nil {
		t.Fatal("invalid static DNS address was accepted")
	}
}

func TestTCPTunnelRuntimeRoutesNonResourcesToInternetOutbound(t *testing.T) {
	vpnClient := &clientStub{
		ipResources: []client.IPResource{{
			IPMin: net.ParseIP("10.0.0.1"), IPMax: net.ParseIP("10.0.0.255"),
			PortMin: 1, PortMax: 65535, Protocol: "all",
		}},
	}
	internet := &outboundStub{}
	runtime, err := New(context.Background(), vpnClient, Config{
		TCPTunnelMode:    true,
		DisableRemoteDNS: true,
		newInternetOutbound: func(core.InternetOutboundConfig) (core.Outbound, error) {
			return internet, nil
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer runtime.Close(context.Background())

	conn, err := runtime.DialContext(context.Background(), "tcp", "192.0.2.1:443")
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	_ = conn.Close()
	if got := vpnClient.dialCount.Load(); got != 0 {
		t.Fatalf("VPN dial count = %d, want 0", got)
	}
	if got := internet.dialCount.Load(); got != 1 {
		t.Fatalf("internet dial count = %d, want 1", got)
	}
}

func TestRuntimeStartsAndClosesConfiguredProxyServices(t *testing.T) {
	tunService := &serviceStub{address: testAddr("ZJU-Portal 172.19.0.1/30")}
	var tunConfig TUNConfig
	socks := &serviceStub{address: testAddr("127.0.0.1:1080")}
	http := &serviceStub{address: testAddr("127.0.0.1:1081")}
	runtime, err := New(context.Background(), &clientStub{domainResources: map[string]client.DomainResource{
		".example.edu": {PortMin: 443, PortMax: 443, Protocol: "tcp"},
	}}, Config{
		TCPTunnelMode:        true,
		DisableRemoteDNS:     true,
		TUNEnabled:           true,
		TUNUDPTimeoutSeconds: 45,
		TUNUDPMaxFlows:       512,
		TUNDNSHijack:         true,
		ControlServerHost:    "vpn.example.edu",
		SOCKSBind:            socks.address.String(),
		HTTPBind:             http.address.String(),
		newTUNService: func(config TUNConfig, _ core.Outbound, _ core.ConnectionObserver) (managedService, error) {
			tunConfig = config
			return tunService, nil
		},
		newSOCKS5Service: func(string, core.Outbound, socks5.NameResolver, string, string) managedService { return socks },
		newHTTPService:   func(string, core.Outbound) managedService { return http },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	statuses := runtime.Services()
	if len(statuses) != 3 {
		t.Fatalf("service statuses = %#v", statuses)
	}
	for _, status := range statuses {
		if !status.Running || status.Address == "" {
			t.Fatalf("service status = %#v", status)
		}
	}
	if tunConfig.UDPTimeout != 45*time.Second || tunConfig.UDPMaxFlows != 512 || !tunConfig.DNSHijack || tunConfig.Resolver == nil || tunConfig.ControlServerHost != "vpn.example.edu" {
		t.Fatalf("TUN config = %#v", tunConfig)
	}
	matcher, ok := tunConfig.Resolver.(tunVPNDomainMatcher)
	if !ok || !matcher.IsVPNDomain("app.example.edu") || matcher.IsVPNDomain("www.example.com") {
		t.Fatalf("TUN VPN domain matcher = %#v, %v", matcher, ok)
	}

	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !tunService.started.Load() || !socks.started.Load() || !http.started.Load() || !tunService.closed.Load() || !socks.closed.Load() || !http.closed.Load() {
		t.Fatalf("service lifecycle: tun=%#v socks=%#v http=%#v", tunService, socks, http)
	}
}

func TestSelectiveTUNKeepsRuleRouterForInternetFallback(t *testing.T) {
	resource := client.IPResource{
		IPMin: net.ParseIP("192.0.2.1"), IPMax: net.ParseIP("192.0.2.255"),
		PortMin: 1, PortMax: 65535, Protocol: "all",
	}
	internet := &outboundStub{}
	service := &serviceStub{address: testAddr("ZJU-Portal 172.19.0.1/30")}
	var tunOutbound core.Outbound
	runtime, err := New(context.Background(), &clientStub{ipResources: []client.IPResource{resource}}, Config{
		TCPTunnelMode:        true,
		DisableRemoteDNS:     true,
		TUNEnabled:           true,
		TUNAutoRoute:         true,
		TUNFakeIP:            true,
		TUNFakeIPRange:       "198.18.0.0/16",
		TUNOutboundInterface: "test0",
		newInternetOutbound: func(core.InternetOutboundConfig) (core.Outbound, error) {
			return internet, nil
		},
		newTUNService: func(config TUNConfig, outbound core.Outbound, _ core.ConnectionObserver) (managedService, error) {
			if !routePrefixesContain(config.RouteAddresses, netip.MustParseAddr("198.18.0.1")) {
				t.Fatalf("selective routes %v do not contain fake IP range", config.RouteAddresses)
			}
			if routePrefixesContain(config.RouteAddresses, netip.MustParseAddr("10.1.2.3")) {
				t.Fatalf("selective routes %v unexpectedly contain unlisted private address", config.RouteAddresses)
			}
			tunOutbound = outbound
			return service, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())

	conn, err := tunOutbound.DialContext(context.Background(), "tcp", "8.8.8.8:443")
	if err != nil {
		t.Fatalf("public internet fallback: %v", err)
	}
	if route := core.RouteInfoOf(conn); route.Outbound != dial.OutboundInternet {
		t.Fatalf("public internet route info = %#v", route)
	}
	_ = conn.Close()

	conn, err = tunOutbound.DialContext(context.Background(), "tcp", "10.1.2.3:443")
	if err != nil {
		t.Fatalf("unlisted private address fallback: %v", err)
	}
	if route := core.RouteInfoOf(conn); route.Outbound != dial.OutboundInternet {
		t.Fatalf("unlisted private address route info = %#v", route)
	}
	_ = conn.Close()
	if got := internet.dialCount.Load(); got != 2 {
		t.Fatalf("internet dial count = %d, want 2", got)
	}
}

func TestRouteAllTUNKeepsRuleRouter(t *testing.T) {
	internet := &outboundStub{}
	service := &serviceStub{address: testAddr("ZJU-Portal 172.19.0.1/30")}
	var tunOutbound core.Outbound
	runtime, err := New(context.Background(), &clientStub{}, Config{
		TCPTunnelMode:        true,
		DisableRemoteDNS:     true,
		TUNEnabled:           true,
		TUNAutoRoute:         true,
		TUNRouteAll:          true,
		TUNOutboundInterface: "test0",
		newInternetOutbound: func(core.InternetOutboundConfig) (core.Outbound, error) {
			return internet, nil
		},
		newTUNService: func(config TUNConfig, outbound core.Outbound, _ core.ConnectionObserver) (managedService, error) {
			if !config.RouteAll {
				t.Fatal("route-all flag was not passed to TUN service")
			}
			if len(config.RouteAddresses) != 0 {
				t.Fatalf("route-all TUN routes = %v, want none", config.RouteAddresses)
			}
			tunOutbound = outbound
			return service, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())

	conn, err := tunOutbound.DialContext(context.Background(), "tcp", "8.8.8.8:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if got := internet.dialCount.Load(); got != 1 {
		t.Fatalf("internet dial count = %d, want 1", got)
	}
}

func TestRuntimeStartsRegisteredInboundFactory(t *testing.T) {
	custom := &serviceStub{address: testAddr("127.0.0.1:19090")}
	config := Config{
		RoutingMode: core.RoutingModeRule,
		InboundFactories: []InboundFactory{{
			Type:    core.ServiceType("custom"),
			Enabled: func(Config) bool { return true },
			New: func(Config, InboundDependencies) (Inbound, error) {
				return custom, nil
			}},
		},
	}
	runtime, err := New(context.Background(), &clientStub{}, config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer runtime.Close(context.Background())

	statuses := runtime.Services()
	if len(statuses) != 1 || statuses[0].Type != core.ServiceType("custom") || !statuses[0].Running {
		t.Fatalf("Services() = %#v", statuses)
	}
}

func TestRuntimeServiceStatusUsesLifecycleStateAndRecordsCloseError(t *testing.T) {
	wantErr := errors.New("close proxy failed")
	http := &serviceStub{closeErr: wantErr}
	runtime, err := New(context.Background(), &clientStub{}, Config{
		TCPTunnelMode:    true,
		DisableRemoteDNS: true,
		HTTPBind:         "127.0.0.1:1081",
		newHTTPService: func(string, core.Outbound) managedService {
			return http
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	statuses := runtime.Services()
	if len(statuses) != 1 || !statuses[0].Running || statuses[0].Address != "" || statuses[0].LastError != "" {
		t.Fatalf("running service status = %#v", statuses)
	}
	if err := runtime.Close(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Close() error = %v, want %v", err, wantErr)
	}
	statuses = runtime.Services()
	if len(statuses) != 1 || statuses[0].Running || statuses[0].LastError != wantErr.Error() {
		t.Fatalf("stopped service status = %#v", statuses)
	}
}

func TestRuntimeReportsUnexpectedServiceExit(t *testing.T) {
	wantErr := errors.New("HTTP serve failed")
	http := &serviceStub{address: testAddr("127.0.0.1:1081"), done: make(chan struct{}), runErr: wantErr}
	runtime, err := New(context.Background(), &clientStub{}, Config{
		TCPTunnelMode:    true,
		DisableRemoteDNS: true,
		HTTPBind:         http.address.String(),
		newHTTPService: func(string, core.Outbound) managedService {
			return http
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	close(http.done)
	select {
	case status := <-runtime.ServiceEvents():
		if status.Type != core.ServiceTypeHTTP || status.Running || status.LastError != wantErr.Error() {
			t.Fatalf("service event = %#v", status)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for service event")
	}
	statuses := runtime.Services()
	if len(statuses) != 1 || statuses[0].Running || statuses[0].LastError != wantErr.Error() {
		t.Fatalf("service status = %#v", statuses)
	}
	select {
	case <-runtime.Done():
		t.Fatal("HTTP service failure stopped network runtime")
	default:
	}
}

func TestRuntimeStopsWhenTUNServiceExits(t *testing.T) {
	wantErr := errors.New("TUN read failed")
	tun := &serviceStub{address: testAddr("ZJU-Portal 172.19.0.1/30"), done: make(chan struct{}), runErr: wantErr}
	runtime, err := New(context.Background(), &clientStub{}, Config{
		TCPTunnelMode:    true,
		DisableRemoteDNS: true,
		TUNEnabled:       true,
		newTUNService: func(TUNConfig, core.Outbound, core.ConnectionObserver) (managedService, error) {
			return tun, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	close(tun.done)
	select {
	case <-runtime.Done():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for network runtime failure")
	}
	if err := runtime.Err(); core.ErrorCodeOf(err) != core.ErrorCodeTUNUnavailable || !errors.Is(err, wantErr) {
		t.Fatalf("runtime error = %v", err)
	}
	if err := runtime.ReplaceVPN(context.Background(), &clientStub{}, Config{TCPTunnelMode: true, DisableRemoteDNS: true}); core.ErrorCodeOf(err) != core.ErrorCodeTUNUnavailable {
		t.Fatalf("ReplaceVPN() error = %v", err)
	}
}

func TestRuntimeRollsBackServicesWhenStartupFails(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		failStage  string
		wantClosed map[string]bool
	}{
		{name: "dns start", failStage: "dns", wantClosed: map[string]bool{"dns": true}},
		{name: "tun factory", failStage: "tun_factory", wantClosed: map[string]bool{"dns": true}},
		{name: "tun start", failStage: "tun", wantClosed: map[string]bool{"dns": true, "tun": true}},
		{name: "socks start", failStage: "socks", wantClosed: map[string]bool{"dns": true, "tun": true, "socks": true}},
		{name: "http start", failStage: "http", wantClosed: map[string]bool{"dns": true, "tun": true, "socks": true, "http": true}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			wantErr := errors.New(testCase.name + " failed")
			services := map[string]*serviceStub{
				"dns":   {address: testAddr("127.0.0.1:5353")},
				"tun":   {address: testAddr("ZJU-Portal 172.19.0.1/30")},
				"socks": {address: testAddr("127.0.0.1:1080")},
				"http":  {address: testAddr("127.0.0.1:1081")},
			}
			if service := services[testCase.failStage]; service != nil {
				service.startErr = wantErr
			}
			internet := &outboundStub{}
			_, err := New(context.Background(), &clientStub{}, Config{
				TCPTunnelMode:    true,
				DisableRemoteDNS: true,
				DNSBind:          services["dns"].address.String(),
				TUNEnabled:       true,
				SOCKSBind:        services["socks"].address.String(),
				HTTPBind:         services["http"].address.String(),
				newInternetOutbound: func(core.InternetOutboundConfig) (core.Outbound, error) {
					return internet, nil
				},
				newDNSService: func(string, service.DNSResolver) managedService {
					return services["dns"]
				},
				newTUNService: func(TUNConfig, core.Outbound, core.ConnectionObserver) (managedService, error) {
					if testCase.failStage == "tun_factory" {
						return nil, wantErr
					}
					return services["tun"], nil
				},
				newSOCKS5Service: func(string, core.Outbound, socks5.NameResolver, string, string) managedService {
					return services["socks"]
				},
				newHTTPService: func(string, core.Outbound) managedService {
					return services["http"]
				},
			})
			if !errors.Is(err, wantErr) {
				t.Fatalf("New() error = %v, want %v", err, wantErr)
			}
			for name, service := range services {
				if got := service.closed.Load(); got != testCase.wantClosed[name] {
					t.Fatalf("%s closed = %v, want %v", name, got, testCase.wantClosed[name])
				}
			}
			if !internet.closed.Load() {
				t.Fatal("internet outbound was not closed")
			}
		})
	}
}

func TestRuntimeReplacesVPNWithoutRestartingProxyServices(t *testing.T) {
	resource := client.IPResource{
		IPMin: net.ParseIP("10.0.0.1"), IPMax: net.ParseIP("10.0.0.255"),
		PortMin: 1, PortMax: 65535, Protocol: "all",
	}
	firstClient := &clientStub{ipResources: []client.IPResource{resource}}
	secondClient := &clientStub{ipResources: []client.IPResource{resource}}
	tunService := &serviceStub{address: testAddr("ZJU-Portal 172.19.0.1/30")}
	socks := &serviceStub{address: testAddr("127.0.0.1:1080")}
	http := &serviceStub{address: testAddr("127.0.0.1:1081")}
	config := Config{
		TCPTunnelMode:    true,
		DisableRemoteDNS: true,
		TUNEnabled:       true,
		SOCKSBind:        socks.address.String(),
		HTTPBind:         http.address.String(),
		newTUNService: func(TUNConfig, core.Outbound, core.ConnectionObserver) (managedService, error) {
			return tunService, nil
		},
		newSOCKS5Service: func(string, core.Outbound, socks5.NameResolver, string, string) managedService { return socks },
		newHTTPService:   func(string, core.Outbound) managedService { return http },
	}
	runtime, err := New(context.Background(), firstClient, config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	if _, err := runtime.SetRoutingMode(core.RoutingModeGlobal); err != nil {
		t.Fatal(err)
	}

	if err := runtime.ReplaceVPN(context.Background(), secondClient, config); err != nil {
		t.Fatal(err)
	}
	if mode := runtime.RoutingMode(); mode != core.RoutingModeGlobal {
		t.Fatalf("routing mode after replacement = %q", mode)
	}
	if tunService.closed.Load() || socks.closed.Load() || http.closed.Load() {
		t.Fatalf("services were closed during VPN replacement: tun=%v socks=%v http=%v", tunService.closed.Load(), socks.closed.Load(), http.closed.Load())
	}
	conn, err := runtime.DialContext(context.Background(), "tcp", "10.0.0.8:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if firstClient.dialCount.Load() != 0 || secondClient.dialCount.Load() != 1 {
		t.Fatalf("dial counts: first=%d second=%d", firstClient.dialCount.Load(), secondClient.dialCount.Load())
	}
}

func TestSelectiveTUNUpdatesResourceRoutesWhenVPNIsReplaced(t *testing.T) {
	firstResource := client.IPResource{
		IPMin: net.ParseIP("192.0.2.1"), IPMax: net.ParseIP("192.0.2.255"),
		PortMin: 1, PortMax: 65535, Protocol: "all",
	}
	secondResource := client.IPResource{
		IPMin: net.ParseIP("198.51.100.1"), IPMax: net.ParseIP("198.51.100.255"),
		PortMin: 1, PortMax: 65535, Protocol: "all",
	}
	firstClient := &clientStub{ipResources: []client.IPResource{firstResource}}
	tunService := &routeUpdatingServiceStub{serviceStub: &serviceStub{address: testAddr("ZJU-Portal 172.19.0.1/30")}}
	config := Config{
		TCPTunnelMode:        true,
		DisableRemoteDNS:     true,
		TUNEnabled:           true,
		TUNAutoRoute:         true,
		TUNOutboundInterface: "test0",
		newInternetOutbound: func(core.InternetOutboundConfig) (core.Outbound, error) {
			return &outboundStub{}, nil
		},
		newTUNService: func(TUNConfig, core.Outbound, core.ConnectionObserver) (managedService, error) {
			return tunService, nil
		},
	}
	runtime, err := New(context.Background(), firstClient, config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())

	secondClient := &clientStub{ipResources: []client.IPResource{secondResource}}
	if err := runtime.ReplaceVPN(context.Background(), secondClient, config); err != nil {
		t.Fatal(err)
	}
	wantRoutes, err := buildResourceRoutePrefixes([]client.IPResource{secondResource}, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tunService.updatedRoutes, wantRoutes) {
		t.Fatalf("updated TUN routes = %v, want %v", tunService.updatedRoutes, wantRoutes)
	}
	conn, err := runtime.DialContext(context.Background(), "tcp", "198.51.100.8:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if firstClient.dialCount.Load() != 0 || secondClient.dialCount.Load() != 1 {
		t.Fatalf("VPN dial counts: first=%d second=%d", firstClient.dialCount.Load(), secondClient.dialCount.Load())
	}
}

func TestClosedRuntimeRejectsVPNReplacement(t *testing.T) {
	runtime, err := New(context.Background(), &clientStub{}, Config{TCPTunnelMode: true, DisableRemoteDNS: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ReplaceVPN(context.Background(), &clientStub{}, Config{TCPTunnelMode: true, DisableRemoteDNS: true}); core.ErrorCodeOf(err) != core.ErrorCodeOutboundUnavailable {
		t.Fatalf("ReplaceVPN() error = %v", err)
	}
}

type clientStub struct {
	ipResources     []client.IPResource
	domainResources map[string]client.DomainResource
	dnsRecords      map[string]net.IP
	dialCount       atomic.Int32
	lastAddress     string
	l3Err           error
	l3Conn          io.ReadWriteCloser
	healthDone      <-chan struct{}
	healthErr       error
}

func (*clientStub) IP() (net.IP, error) { return net.ParseIP("10.0.0.2"), nil }

func (client *clientStub) IPResources() ([]client.IPResource, error) {
	return client.ipResources, nil
}

func (client *clientStub) DomainResources() (map[string]client.DomainResource, error) {
	return client.domainResources, nil
}

func (client *clientStub) DNSResource() (map[string]net.IP, error) {
	return client.dnsRecords, nil
}

func (*clientStub) DNSServer() (string, error) { return "", client.ErrResourceNotFound }

func (*clientStub) CanUseTCPTunnel() bool { return true }

func (client *clientStub) Done() <-chan struct{} { return client.healthDone }

func (client *clientStub) Err() error { return client.healthErr }

func (client *clientStub) DialTCP(_ context.Context, address *net.TCPAddr) (net.Conn, error) {
	client.dialCount.Add(1)
	client.lastAddress = address.String()
	local, remote := net.Pipe()
	_ = remote.Close()
	return local, nil
}

func (client *clientStub) NewL3Conn() (io.ReadWriteCloser, error) {
	if client.l3Err != nil {
		return nil, client.l3Err
	}
	if client.l3Conn != nil {
		return client.l3Conn, nil
	}
	return nil, errors.New("L3 connection must not be opened in TCP tunnel mode")
}

type failingL3Conn struct {
	err error
}

func (conn *failingL3Conn) Read([]byte) (int, error)  { return 0, conn.err }
func (*failingL3Conn) Write(data []byte) (int, error) { return len(data), nil }
func (*failingL3Conn) Close() error                   { return nil }

type serviceStub struct {
	address  testAddr
	startErr error
	closeErr error
	done     chan struct{}
	runErr   error
	started  atomic.Bool
	closed   atomic.Bool
}

type routeUpdatingServiceStub struct {
	*serviceStub
	updatedRoutes []netip.Prefix
	updateErr     error
}

func (service *routeUpdatingServiceStub) UpdateRouteAddresses(routes []netip.Prefix) error {
	service.updatedRoutes = append([]netip.Prefix(nil), routes...)
	return service.updateErr
}

type outboundStub struct {
	dialCount atomic.Int32
	closed    atomic.Bool
}

func (outbound *outboundStub) DialContext(context.Context, string, string) (net.Conn, error) {
	outbound.dialCount.Add(1)
	local, remote := net.Pipe()
	_ = remote.Close()
	return local, nil
}

func (outbound *outboundStub) Close(context.Context) error {
	outbound.closed.Store(true)
	return nil
}

func (service *serviceStub) Start(context.Context) error {
	service.started.Store(true)
	return service.startErr
}

func (service *serviceStub) Close(context.Context) error {
	service.closed.Store(true)
	return service.closeErr
}

func (service *serviceStub) Addr() net.Addr { return service.address }

func (service *serviceStub) Done() <-chan struct{} { return service.done }

func (service *serviceStub) Err() error { return service.runErr }

type testAddr string

func (address testAddr) Network() string { return "tcp" }
func (address testAddr) String() string  { return string(address) }
