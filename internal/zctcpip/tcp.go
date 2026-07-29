package zctcpip

import (
	"encoding/binary"
)

const TCPHeaderSize = 20

type TCPPacket []byte

func (p TCPPacket) SourcePort() uint16 {
	return binary.BigEndian.Uint16(p)
}

func (p TCPPacket) DestinationPort() uint16 {
	return binary.BigEndian.Uint16(p[2:])
}

func (p TCPPacket) Valid() bool {
	return len(p) >= TCPHeaderSize
}
