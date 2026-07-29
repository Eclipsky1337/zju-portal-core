package networkruntime

import (
	"io"

	tun "github.com/mythologyli/sing-tun"
	"github.com/sagernet/sing/common/buf"
	N "github.com/sagernet/sing/common/network"
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

func (device *monitoredTUN) CreateVectorisedWriter() N.VectorisedWriter {
	writer := device.Tun.CreateVectorisedWriter()
	if writer == nil {
		return nil
	}
	return &monitoredVectorisedWriter{VectorisedWriter: writer, onError: device.onError}
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

type monitoredVectorisedWriter struct {
	N.VectorisedWriter
	onError func(error)
}

func (writer *monitoredVectorisedWriter) WriteVectorised(buffers []*buf.Buffer) error {
	err := writer.VectorisedWriter.WriteVectorised(buffers)
	if err != nil && writer.onError != nil {
		writer.onError(err)
	}
	return err
}

func wrapStandardTUNDevice(device tun.Tun, onError func(error)) tun.Tun {
	monitored := &monitoredTUN{Tun: device, onError: onError}
	if winTUN, ok := device.(tun.WinTun); ok {
		return &monitoredWinTUN{monitoredTUN: monitored, winTUN: winTUN}
	}
	return monitored
}

var _ io.ReadWriter = (*monitoredTUN)(nil)
