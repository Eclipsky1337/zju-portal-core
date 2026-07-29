package networkruntime

import (
	"errors"
	"syscall"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestWrapServiceStartErrorClassifiesStableCodes(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		serviceType core.ServiceType
		err         error
		want        core.ErrorCode
	}{
		{name: "address in use", serviceType: core.ServiceTypeHTTP, err: syscall.EADDRINUSE, want: core.ErrorCodeAddressInUse},
		{name: "permission", serviceType: core.ServiceTypeTUN, err: syscall.EPERM, want: core.ErrorCodePermissionDenied},
		{name: "route", serviceType: core.ServiceTypeTUN, err: errors.New("create TUN device: add ipv4 route 0.0.0.0/1: failed"), want: core.ErrorCodeRouteSetupFailed},
		{name: "dns", serviceType: core.ServiceTypeDNS, err: errors.New("dns failed"), want: core.ErrorCodeDNSStartFailed},
		{name: "tun", serviceType: core.ServiceTypeTUN, err: errors.New("tun failed"), want: core.ErrorCodeTUNUnavailable},
		{name: "proxy", serviceType: core.ServiceTypeSOCKS5, err: errors.New("proxy failed"), want: core.ErrorCodeNetworkSetupFailed},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if code := core.ErrorCodeOf(wrapServiceStartError(testCase.serviceType, testCase.err)); code != testCase.want {
				t.Fatalf("error code = %q, want %q", code, testCase.want)
			}
		})
	}
}

func TestRouteSetupErrorDetectionIsConservative(t *testing.T) {
	if !isRouteSetupError(errors.New("add rule 1/2: operation failed")) {
		t.Fatal("route rule error was not detected")
	}
	if isRouteSetupError(errors.New("create TUN device: bad tun name")) {
		t.Fatal("device creation error was classified as route setup")
	}
}
