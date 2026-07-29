package atrust

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

func TestIsAuthTimeoutErr(t *testing.T) {
	err := fmt.Errorf("%w for 4:<client-ip>:<client-port>-<dns-server>:53", errL3TunnelAuthTimeout)
	if !isAuthTimeoutErr(err) {
		t.Fatal("expected wrapped l3 tunnel auth timeout to be recognized")
	}

	if isAuthTimeoutErr(nil) {
		t.Fatal("nil error must not be treated as auth timeout")
	}

	if isAuthTimeoutErr(fmt.Errorf("l3-tunnel auth timeout for unrelated text")) {
		t.Fatal("plain text error must not be treated as auth timeout")
	}
}

func TestProcessIPv4RejectsMalformedPackets(t *testing.T) {
	tunnel := newL3TunnelForLifecycleTest()
	tests := []struct {
		name    string
		packet  []byte
		message string
	}{
		{name: "short IPv4", packet: make([]byte, 19), message: "invalid IPv4"},
		{name: "invalid header length", packet: ipv4PacketForTest(4, 20, 0), message: "invalid IPv4"},
		{name: "short TCP", packet: ipv4PacketForTest(5, 20, 6), message: "invalid TCP"},
		{name: "short UDP", packet: ipv4PacketForTest(5, 20, 17), message: "invalid UDP"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := tunnel.processIPV4(test.packet)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("processIPV4() error = %v, want %q", err, test.message)
			}
		})
	}
}

func ipv4PacketForTest(headerWords byte, totalLength uint16, protocol byte) []byte {
	packet := make([]byte, 20)
	packet[0] = 4<<4 | headerWords
	binary.BigEndian.PutUint16(packet[2:], totalLength)
	packet[9] = protocol
	return packet
}
