package networkruntime

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	tun "github.com/mythologyli/sing-tun"
)

func TestNewTUNServiceAppliesDefaults(t *testing.T) {
	created, err := newTUNService(TUNConfig{}, &outboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	if service.config.Name != defaultTUNName || service.config.Address != defaultTUNAddress || service.config.MTU != defaultTUNMTU || service.config.Stack != defaultTUNStack {
		t.Fatalf("TUN defaults = %#v", service.config)
	}
}

func TestNewTUNServiceRejectsInvalidAddress(t *testing.T) {
	if _, err := newTUNService(TUNConfig{Address: "invalid"}, &outboundStub{}, nil); err == nil {
		t.Fatal("expected invalid TUN address error")
	}
}

func TestNewTUNServiceRequiresAutoRouteForFakeIP(t *testing.T) {
	if _, err := newTUNService(TUNConfig{FakeIP: true}, &outboundStub{}, nil); err == nil {
		t.Fatal("expected fake IP auto route error")
	}
}

func TestNewTUNServiceRequiresAutoRouteForDNSHijack(t *testing.T) {
	if _, err := newTUNService(TUNConfig{DNSHijack: true}, &outboundStub{}, nil); err == nil {
		t.Fatal("expected DNS hijack auto route error")
	}
}

func TestTUNDNSServerAddressUsesPeerAddress(t *testing.T) {
	for _, testCase := range []struct {
		prefix string
		want   string
	}{
		{prefix: "172.19.0.1/30", want: "172.19.0.2"},
		{prefix: "172.19.0.2/30", want: "172.19.0.1"},
	} {
		prefix := netip.MustParsePrefix(testCase.prefix)
		address, err := tunDNSServerAddress(prefix)
		if err != nil {
			t.Fatal(err)
		}
		if address.String() != testCase.want {
			t.Fatalf("DNS address for %s = %s", testCase.prefix, address)
		}
	}
	if _, err := tunDNSServerAddress(netip.MustParsePrefix("172.19.0.1/32")); err == nil {
		t.Fatal("expected missing peer address error")
	}
}

func TestTUNServiceControlsSystemDNS(t *testing.T) {
	controller := &systemDNSStub{}
	created, err := newTUNService(TUNConfig{
		DNSHijack: true,
		AutoRoute: true,
		SystemDNS: controller,
	}, &outboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	device := &tunDeviceStub{}
	stack := &tunStackStub{}
	service.newDevice = func(tun.Options) (tun.Tun, error) { return device, nil }
	service.newStack = func(string, tun.StackOptions) (tun.Stack, error) { return stack, nil }
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if controller.applied != "172.19.0.2" {
		t.Fatalf("applied DNS server = %q", controller.applied)
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !controller.restored.Load() {
		t.Fatal("system DNS was not restored")
	}
}

func TestTUNServiceContinuesDNSRestoreAfterCloseContextCancellation(t *testing.T) {
	controller := &systemDNSStub{restoreEntered: make(chan struct{}), restoreRelease: make(chan struct{})}
	created, err := newTUNService(TUNConfig{
		DNSHijack: true,
		AutoRoute: true,
		SystemDNS: controller,
	}, &outboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	service.newDevice = func(tun.Options) (tun.Tun, error) { return &tunDeviceStub{}, nil }
	service.newStack = func(string, tun.StackOptions) (tun.Stack, error) { return &tunStackStub{}, nil }
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() error = %v, want context canceled", err)
	}
	select {
	case <-controller.restoreEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for DNS restore")
	}
	if controller.restoreCanceled.Load() {
		t.Fatal("DNS restore inherited canceled caller context")
	}
	close(controller.restoreRelease)
	if err := service.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := controller.restoreCalls.Load(); calls != 1 {
		t.Fatalf("Restore() calls = %d, want 1", calls)
	}
}

func TestTUNServicePassesSelectiveRouteAddresses(t *testing.T) {
	routes := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("203.0.113.9/32")}
	created, err := newTUNService(TUNConfig{AutoRoute: true, RouteAddresses: routes}, &outboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	device := &tunDeviceStub{}
	stack := &tunStackStub{}
	var options tun.Options
	service.newDevice = func(received tun.Options) (tun.Tun, error) {
		options = received
		return device, nil
	}
	service.newStack = func(string, tun.StackOptions) (tun.Stack, error) { return stack, nil }
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	if !reflect.DeepEqual(options.Inet4RouteAddress, routes) {
		t.Fatalf("route addresses = %v, want %v", options.Inet4RouteAddress, routes)
	}
}

func TestTUNServiceAddsDNSPeerToSelectiveRoutes(t *testing.T) {
	routes := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	controller := &systemDNSStub{}
	created, err := newTUNService(TUNConfig{
		AutoRoute:      true,
		DNSHijack:      true,
		RouteAddresses: routes,
		SystemDNS:      controller,
	}, &outboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	device := &tunDeviceStub{}
	stack := &tunStackStub{}
	var options tun.Options
	service.newDevice = func(received tun.Options) (tun.Tun, error) {
		options = received
		return device, nil
	}
	service.newStack = func(string, tun.StackOptions) (tun.Stack, error) { return stack, nil }
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())

	want := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.19.0.2/32"),
	}
	if !reflect.DeepEqual(options.Inet4RouteAddress, want) {
		t.Fatalf("route addresses = %v, want %v", options.Inet4RouteAddress, want)
	}
}

func TestTUNServiceKeepsEmptyRoutesForRouteAllDNSHijack(t *testing.T) {
	created, err := newTUNService(TUNConfig{
		AutoRoute: true,
		DNSHijack: true,
		SystemDNS: &systemDNSStub{},
	}, &outboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	device := &tunDeviceStub{}
	stack := &tunStackStub{}
	var options tun.Options
	service.newDevice = func(received tun.Options) (tun.Tun, error) {
		options = received
		return device, nil
	}
	service.newStack = func(string, tun.StackOptions) (tun.Stack, error) { return stack, nil }
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())

	if len(options.Inet4RouteAddress) != 0 {
		t.Fatalf("route-all addresses = %v, want none", options.Inet4RouteAddress)
	}
}

func TestTUNServiceRollsBackWhenSystemDNSFails(t *testing.T) {
	wantErr := errors.New("system DNS failed")
	controller := &systemDNSStub{applyErr: wantErr}
	created, err := newTUNService(TUNConfig{
		DNSHijack: true,
		AutoRoute: true,
		SystemDNS: controller,
	}, &outboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	device := &tunDeviceStub{}
	stack := &tunStackStub{}
	service.newDevice = func(tun.Options) (tun.Tun, error) { return device, nil }
	service.newStack = func(string, tun.StackOptions) (tun.Stack, error) { return stack, nil }
	if err := service.Start(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v", err)
	}
	if !device.closed.Load() || !stack.closed.Load() {
		t.Fatalf("resources after DNS failure: device=%v stack=%v", device.closed.Load(), stack.closed.Load())
	}
}

func TestTUNStackNameUsesBuildDefaultForAuto(t *testing.T) {
	if got := tunStackName("auto"); got != "" {
		t.Fatalf("tunStackName(auto) = %q", got)
	}
	if got := tunStackName("system"); got != "system" {
		t.Fatalf("tunStackName(system) = %q", got)
	}
}

func TestTUNServiceRollsBackInitializationFailures(t *testing.T) {
	for _, testCase := range []struct {
		name             string
		failDevice       bool
		failStackFactory bool
		failStackStart   bool
		wantDeviceClosed bool
		wantStackClosed  bool
	}{
		{name: "device creation", failDevice: true},
		{name: "stack creation", failStackFactory: true, wantDeviceClosed: true},
		{name: "stack start", failStackStart: true, wantDeviceClosed: true, wantStackClosed: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			created, err := newTUNService(TUNConfig{}, &outboundStub{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			service := created.(*tunService)
			device := &tunDeviceStub{}
			stack := &tunStackStub{}
			wantErr := errors.New(testCase.name + " failed")
			service.newDevice = func(tun.Options) (tun.Tun, error) {
				if testCase.failDevice {
					return nil, wantErr
				}
				return device, nil
			}
			service.newStack = func(string, tun.StackOptions) (tun.Stack, error) {
				if testCase.failStackFactory {
					return nil, wantErr
				}
				if testCase.failStackStart {
					stack.startErr = wantErr
				}
				return stack, nil
			}

			if err := service.Start(context.Background()); !errors.Is(err, wantErr) {
				t.Fatalf("Start() error = %v, want %v", err, wantErr)
			}
			if got := device.closed.Load(); got != testCase.wantDeviceClosed {
				t.Fatalf("device closed = %v, want %v", got, testCase.wantDeviceClosed)
			}
			if got := stack.closed.Load(); got != testCase.wantStackClosed {
				t.Fatalf("stack closed = %v, want %v", got, testCase.wantStackClosed)
			}
			if service.Addr() != nil {
				t.Fatalf("Addr() = %v after failed start", service.Addr())
			}
		})
	}
}

func TestTUNServiceClosesResourcesWhenClosedDuringStart(t *testing.T) {
	created, err := newTUNService(TUNConfig{}, &outboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	device := &tunDeviceStub{}
	stack := &tunStackStub{startEntered: make(chan struct{}), startRelease: make(chan struct{})}
	service.newDevice = func(tun.Options) (tun.Tun, error) { return device, nil }
	service.newStack = func(string, tun.StackOptions) (tun.Stack, error) { return stack, nil }
	startResult := make(chan error, 1)
	go func() { startResult <- service.Start(context.Background()) }()
	<-stack.startEntered
	if err := service.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(stack.startRelease)
	if err := <-startResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v", err)
	}
	if !device.closed.Load() || !stack.closed.Load() || service.Addr() != nil {
		t.Fatalf("resources after close during start: device=%v stack=%v addr=%v", device.closed.Load(), stack.closed.Load(), service.Addr())
	}
}

func TestTUNServiceClosesResourcesWhenContextIsCanceled(t *testing.T) {
	created, err := newTUNService(TUNConfig{}, &outboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	device := &tunDeviceStub{}
	stack := &tunStackStub{}
	service.newDevice = func(tun.Options) (tun.Tun, error) { return device, nil }
	service.newStack = func(string, tun.StackOptions) (tun.Stack, error) { return stack, nil }
	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && (!device.closed.Load() || !stack.closed.Load()) {
		time.Sleep(time.Millisecond)
	}
	if !device.closed.Load() || !stack.closed.Load() || service.Addr() != nil {
		t.Fatalf("resources after cancellation: device=%v stack=%v addr=%v", device.closed.Load(), stack.closed.Load(), service.Addr())
	}
	select {
	case <-service.Done():
	default:
		t.Fatal("Done() remains open after context cancellation")
	}
	if err := service.Err(); err != nil {
		t.Fatalf("Err() after context cancellation = %v", err)
	}
}

func TestTUNServiceReportsTerminalDeviceFailure(t *testing.T) {
	created, err := newTUNService(TUNConfig{}, &outboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	wantErr := errors.New("TUN device read failed")
	device := &tunDeviceStub{readErr: wantErr}
	stack := &tunStackStub{}
	var stackDevice tun.Tun
	service.newDevice = func(tun.Options) (tun.Tun, error) { return device, nil }
	service.newStack = func(_ string, options tun.StackOptions) (tun.Stack, error) {
		stackDevice = options.Tun
		return stack, nil
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := stackDevice.Read(make([]byte, 1)); !errors.Is(err, wantErr) {
		t.Fatalf("monitored Read() error = %v", err)
	}
	select {
	case <-service.Done():
	case <-time.After(time.Second):
		t.Fatal("terminal device failure did not close Done()")
	}
	if err := service.Err(); !errors.Is(err, wantErr) {
		t.Fatalf("Err() = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && (!device.closed.Load() || !stack.closed.Load()) {
		time.Sleep(time.Millisecond)
	}
	if !device.closed.Load() || !stack.closed.Load() || service.Addr() != nil {
		t.Fatalf("resources after terminal failure: device=%v stack=%v addr=%v", device.closed.Load(), stack.closed.Load(), service.Addr())
	}
}

func TestExpectedTUNCloseErrors(t *testing.T) {
	for _, err := range []error{nil, io.EOF, io.ErrClosedPipe, context.Canceled, context.DeadlineExceeded} {
		if !isExpectedTUNCloseError(err) {
			t.Fatalf("expected close error was not recognized: %v", err)
		}
	}
	if isExpectedTUNCloseError(io.ErrUnexpectedEOF) {
		t.Fatal("unexpected EOF was classified as an expected close error")
	}
}

type tunDeviceStub struct {
	tun.Tun
	closed  atomic.Bool
	readErr error
}

func (device *tunDeviceStub) Read([]byte) (int, error) { return 0, device.readErr }

func (device *tunDeviceStub) Close() error {
	device.closed.Store(true)
	return nil
}

type tunStackStub struct {
	startErr     error
	startEntered chan struct{}
	startRelease chan struct{}
	closed       atomic.Bool
}

type systemDNSStub struct {
	applied         string
	applyErr        error
	restored        atomic.Bool
	restoreCanceled atomic.Bool
	restoreCalls    atomic.Int32
	restoreEntered  chan struct{}
	restoreRelease  chan struct{}
}

func (controller *systemDNSStub) Apply(_ context.Context, address string) error {
	controller.applied = address
	return controller.applyErr
}

func (controller *systemDNSStub) Restore(ctx context.Context) error {
	controller.restoreCalls.Add(1)
	if ctx.Err() != nil {
		controller.restoreCanceled.Store(true)
	}
	if controller.restoreEntered != nil {
		close(controller.restoreEntered)
	}
	if controller.restoreRelease != nil {
		select {
		case <-controller.restoreRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	controller.restored.Store(true)
	return nil
}

func (stack *tunStackStub) Start() error {
	if stack.startEntered != nil {
		close(stack.startEntered)
		<-stack.startRelease
	}
	return stack.startErr
}

func (stack *tunStackStub) Close() error {
	stack.closed.Store(true)
	return nil
}
