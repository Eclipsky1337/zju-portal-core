package networkruntime

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

func wrapServiceStartError(serviceType core.ServiceType, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return core.WrapError(core.ErrorCodeAddressInUse, fmt.Sprintf("start %s service", serviceType), false, err)
	}
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		return core.WrapError(core.ErrorCodePermissionDenied, fmt.Sprintf("start %s service", serviceType), false, err)
	}
	if serviceType == core.ServiceTypeTUN && isRouteSetupError(err) {
		return core.WrapError(core.ErrorCodeRouteSetupFailed, "install TUN routes", false, err)
	}
	switch serviceType {
	case core.ServiceTypeDNS:
		return core.WrapError(core.ErrorCodeDNSStartFailed, "start DNS service", false, err)
	case core.ServiceTypeTUN:
		return core.WrapError(core.ErrorCodeTUNUnavailable, "start TUN service", false, err)
	default:
		return core.WrapError(core.ErrorCodeNetworkSetupFailed, fmt.Sprintf("start %s service", serviceType), false, err)
	}
}

func isRouteSetupError(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"add route ", "add ipv4 route ", "add ipv6 route ", "add rule ", "cleanup rules"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
