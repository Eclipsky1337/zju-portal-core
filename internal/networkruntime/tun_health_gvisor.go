//go:build with_gvisor

package networkruntime

import (
	"github.com/sagernet/gvisor/pkg/tcpip/stack"
	tun "github.com/sagernet/sing-tun"
)

type monitoredGVisorTUN struct {
	*monitoredTUN
	gvisorTUN tun.GVisorTun
}

func (device *monitoredGVisorTUN) NewEndpoint() (stack.LinkEndpoint, stack.NICOptions, error) {
	endpoint, options, err := device.gvisorTUN.NewEndpoint()
	device.report(err)
	return endpoint, options, err
}

func (device *monitoredGVisorTUN) WritePacket(packet *stack.PacketBuffer) (int, error) {
	count, err := device.gvisorTUN.WritePacket(packet)
	device.report(err)
	return count, err
}

func wrapTUNDevice(device tun.Tun, onError func(error)) tun.Tun {
	monitored := &monitoredTUN{Tun: device, onError: onError}
	if gvisorTUN, ok := device.(tun.GVisorTun); ok {
		return &monitoredGVisorTUN{monitoredTUN: monitored, gvisorTUN: gvisorTUN}
	}
	if darwinTUN, ok := device.(tun.DarwinTUN); ok {
		return &monitoredDarwinTUN{monitoredTUN: monitored, darwinTUN: darwinTUN}
	}
	if linuxTUN, ok := device.(tun.LinuxTUN); ok {
		return &monitoredLinuxTUN{monitoredTUN: monitored, linuxTUN: linuxTUN}
	}
	if winTUN, ok := device.(tun.WinTun); ok {
		return &monitoredWinTUN{monitoredTUN: monitored, winTUN: winTUN}
	}
	return monitored
}
