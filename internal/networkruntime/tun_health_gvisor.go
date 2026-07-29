//go:build with_gvisor

package networkruntime

import (
	"github.com/metacubex/gvisor/pkg/tcpip/stack"
	tun "github.com/mythologyli/sing-tun"
)

type monitoredGVisorTUN struct {
	*monitoredTUN
	gvisorTUN tun.GVisorTun
}

func (device *monitoredGVisorTUN) NewEndpoint() (stack.LinkEndpoint, error) {
	endpoint, err := device.gvisorTUN.NewEndpoint()
	device.report(err)
	return endpoint, err
}

func wrapTUNDevice(device tun.Tun, onError func(error)) tun.Tun {
	monitored := &monitoredTUN{Tun: device, onError: onError}
	if gvisorTUN, ok := device.(tun.GVisorTun); ok {
		return &monitoredGVisorTUN{monitoredTUN: monitored, gvisorTUN: gvisorTUN}
	}
	if winTUN, ok := device.(tun.WinTun); ok {
		return &monitoredWinTUN{monitoredTUN: monitored, winTUN: winTUN}
	}
	return monitored
}
