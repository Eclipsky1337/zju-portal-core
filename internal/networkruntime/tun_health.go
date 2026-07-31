package networkruntime

import (
	"io"

	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/buf"
)

type monitoredTUN struct {
	tun.Tun
	onError func(error)
}

func (device *monitoredTUN) Read(buffer []byte) (int, error) {
	count, err := device.Tun.Read(buffer)
	device.report(err)
	return count, err
}

func (device *monitoredTUN) Write(buffer []byte) (int, error) {
	count, err := device.Tun.Write(buffer)
	device.report(err)
	return count, err
}

func (device *monitoredTUN) report(err error) {
	if err != nil && device.onError != nil {
		device.onError(err)
	}
}

type monitoredWinTUN struct {
	*monitoredTUN
	winTUN tun.WinTun
}

func (device *monitoredWinTUN) ReadPacket() ([]byte, func(), error) {
	packet, release, err := device.winTUN.ReadPacket()
	device.report(err)
	return packet, release, err
}

type monitoredDarwinTUN struct {
	*monitoredTUN
	darwinTUN tun.DarwinTUN
}

type monitoredLinuxTUN struct {
	*monitoredTUN
	linuxTUN tun.LinuxTUN
}

func (device *monitoredLinuxTUN) FrontHeadroom() int { return device.linuxTUN.FrontHeadroom() }

func (device *monitoredLinuxTUN) BatchSize() int { return device.linuxTUN.BatchSize() }

func (device *monitoredLinuxTUN) BatchRead(buffers [][]byte, offset int, readN []int) (int, error) {
	count, err := device.linuxTUN.BatchRead(buffers, offset, readN)
	device.report(err)
	return count, err
}

func (device *monitoredLinuxTUN) BatchWrite(buffers [][]byte, offset int) (int, error) {
	count, err := device.linuxTUN.BatchWrite(buffers, offset)
	device.report(err)
	return count, err
}

func (device *monitoredLinuxTUN) TXChecksumOffload() bool {
	return device.linuxTUN.TXChecksumOffload()
}

func (device *monitoredDarwinTUN) BatchRead() ([]*buf.Buffer, error) {
	packets, err := device.darwinTUN.BatchRead()
	device.report(err)
	return packets, err
}

func (device *monitoredDarwinTUN) BatchWrite(buffers []*buf.Buffer) error {
	err := device.darwinTUN.BatchWrite(buffers)
	device.report(err)
	return err
}

func wrapStandardTUNDevice(device tun.Tun, onError func(error)) tun.Tun {
	monitored := &monitoredTUN{Tun: device, onError: onError}
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

var _ io.ReadWriter = (*monitoredTUN)(nil)
