package networkruntime

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/Eclipsky1337/zju-portal-core/internal/dnsmessage"
	"github.com/miekg/dns"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type tunResolver interface {
	Resolve(context.Context, string) (context.Context, net.IP, error)
}

func (service *tunService) routeDestination(destination M.Socksaddr) string {
	if service.fakeIPs != nil && destination.Addr.IsValid() {
		if domain, ok := service.fakeIPs.Lookup(destination.Addr); ok {
			return net.JoinHostPort(domain, strconv.Itoa(int(destination.Port)))
		}
	}
	return destination.String()
}

func (service *tunService) handleUDPDNS(ctx context.Context, inbound N.PacketConn, destination M.Socksaddr, packet *buf.Buffer) error {
	response, err := service.handleDNSPayload(ctx, packet.Bytes())
	if err != nil {
		return err
	}
	return inbound.WritePacket(buf.As(response), destination)
}

func (service *tunService) handleTCPDNS(ctx context.Context, conn net.Conn) error {
	header := make([]byte, 2)
	for {
		if _, err := io.ReadFull(conn, header); err != nil {
			if isExpectedTUNCloseError(err) {
				return nil
			}
			return err
		}
		length := int(binary.BigEndian.Uint16(header))
		if length == 0 {
			continue
		}
		request := make([]byte, length)
		if _, err := io.ReadFull(conn, request); err != nil {
			return err
		}
		response, err := service.handleDNSPayload(ctx, request)
		if err != nil {
			return err
		}
		if len(response) > 65535 {
			return fmt.Errorf("TUN DNS response is too large")
		}
		binary.BigEndian.PutUint16(header, uint16(len(response)))
		if _, err := conn.Write(header); err != nil {
			return err
		}
		if _, err := conn.Write(response); err != nil {
			return err
		}
	}
}

func (service *tunService) handleDNSPayload(ctx context.Context, payload []byte) ([]byte, error) {
	request := new(dns.Msg)
	if err := request.Unpack(payload); err != nil {
		return nil, fmt.Errorf("unpack TUN DNS query: %w", err)
	}
	handler := dnsmessage.Handler{Resolver: service.config.Resolver}
	if service.fakeIPs != nil {
		handler.FakeIPv4 = service.fakeIPs.Assign
	}
	return handler.Handle(ctx, request).Pack()
}
