//go:build !with_gvisor

package networkruntime

import tun "github.com/mythologyli/sing-tun"

func wrapTUNDevice(device tun.Tun, onError func(error)) tun.Tun {
	return wrapStandardTUNDevice(device, onError)
}
