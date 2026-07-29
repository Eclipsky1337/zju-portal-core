package gvisor

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/Eclipsky1337/zju-portal-core/log"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

type Stack struct {
	gvisorStack *stack.Stack

	endpoint *Endpoint

	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
}

const NICID tcpip.NICID = 1
const MTU uint32 = 1400

type Endpoint struct {
	client client.Client

	l3Conn io.ReadWriteCloser
	l3Mu   sync.Mutex
	closed bool
	runErr error

	dispatcher stack.NetworkDispatcher
}

func (ep *Endpoint) ParseHeader(*stack.PacketBuffer) bool {
	return true
}

func (ep *Endpoint) MTU() uint32 {
	return MTU
}

func (ep *Endpoint) SetMTU(mtu uint32) {
	log.Printf("don't support change MTU from %d to %d", MTU, mtu)
}

func (ep *Endpoint) MaxHeaderLength() uint16 {
	return 0
}

func (ep *Endpoint) LinkAddress() tcpip.LinkAddress {
	return ""
}

func (ep *Endpoint) SetLinkAddress(addr tcpip.LinkAddress) {}

func (ep *Endpoint) Capabilities() stack.LinkEndpointCapabilities {
	return stack.CapabilityNone
}

func (ep *Endpoint) Attach(dispatcher stack.NetworkDispatcher) {
	ep.dispatcher = dispatcher
}

func (ep *Endpoint) IsAttached() bool {
	return ep.dispatcher != nil
}

func (ep *Endpoint) Wait() {}

func (ep *Endpoint) ARPHardwareType() header.ARPHardwareType {
	return header.ARPHardwareNone
}

func (ep *Endpoint) AddHeader(*stack.PacketBuffer) {}

func (ep *Endpoint) Close() {}

func (ep *Endpoint) SetOnCloseAction(func()) {}

// WritePackets is called when get packets from gVisor stack. Then it sends them to VPN server
func (ep *Endpoint) WritePackets(list stack.PacketBufferList) (int, tcpip.Error) {
	for index, packetBuffer := range list.AsSlice() {
		var buf []byte
		for _, t := range packetBuffer.AsSlices() {
			buf = append(buf, t...)
		}

		conn := ep.connection()
		if conn != nil {
			n, err := conn.Write(buf)
			if err != nil {
				if errors.Is(err, client.ErrResourceNotFound) {
					log.Printf("%v", err)
					continue
				}
				ep.fail(err)
				return index, &tcpip.ErrAborted{}
			}
			log.DebugPrintf("Send: wrote %d bytes", n)
			log.DebugDumpHex(buf[:n])
		}
	}

	return list.Len(), nil
}

func NewStack(client client.Client) (*Stack, error) {
	s := &Stack{closeDone: make(chan struct{})}

	s.gvisorStack = stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
		HandleLocal:        true,
	})

	s.endpoint = &Endpoint{
		client: client,
	}

	tcpipErr := s.gvisorStack.CreateNIC(NICID, s.endpoint)
	if tcpipErr != nil {
		return nil, errors.New(tcpipErr.String())
	}

	ip, err := client.IP()
	if err != nil {
		return nil, err
	}

	addr := tcpip.AddrFromSlice(ip)
	protoAddr := tcpip.ProtocolAddress{
		AddressWithPrefix: tcpip.AddressWithPrefix{
			Address:   addr,
			PrefixLen: 32,
		},
		Protocol: ipv4.ProtocolNumber,
	}

	tcpipErr = s.gvisorStack.AddProtocolAddress(NICID, protoAddr, stack.AddressProperties{})
	if tcpipErr != nil {
		return nil, errors.New(tcpipErr.String())
	}

	sOpt := tcpip.TCPSACKEnabled(true)
	s.gvisorStack.SetTransportProtocolOption(tcp.ProtocolNumber, &sOpt)
	cOpt := tcpip.CongestionControlOption("cubic")
	s.gvisorStack.SetTransportProtocolOption(tcp.ProtocolNumber, &cOpt)
	s.gvisorStack.AddRoute(tcpip.Route{Destination: header.IPv4EmptySubnet, NIC: NICID})

	return s, nil
}

func (s *Stack) Run() {
	if err := s.RunContext(context.Background()); err != nil {
		log.Printf("gVisor stack stopped: %v", err)
	}
}

func (s *Stack) RunContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := s.endpoint.client.NewL3Conn()
	if err != nil {
		return err
	}
	if err := s.endpoint.setConnection(conn); err != nil {
		return err
	}
	stopClose := context.AfterFunc(ctx, func() {
		_ = s.Close(context.Background())
	})
	defer stopClose()
	defer s.Close(context.Background())

	// Read from VPN server and send to gVisor stack
	for {
		buf := make([]byte, MTU)
		n, err := conn.Read(buf)
		if err != nil {
			if runErr := s.endpoint.terminalError(); runErr != nil {
				return runErr
			}
			if ctx.Err() != nil || s.endpoint.isClosed() {
				return nil
			}
			return err
		}
		log.DebugPrintf("Recv: read %d bytes", n)
		log.DebugDumpHex(buf[:n])

		packetBuffer := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(buf),
		})
		s.endpoint.dispatcher.DeliverNetworkPacket(header.IPv4ProtocolNumber, packetBuffer)
		packetBuffer.DecRef()
	}
}

func (s *Stack) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		go func() {
			s.closeErr = s.endpoint.close()
			close(s.closeDone)
		}()
	})
	select {
	case <-s.closeDone:
		return s.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (ep *Endpoint) connection() io.ReadWriteCloser {
	ep.l3Mu.Lock()
	defer ep.l3Mu.Unlock()
	return ep.l3Conn
}

func (ep *Endpoint) setConnection(conn io.ReadWriteCloser) error {
	ep.l3Mu.Lock()
	defer ep.l3Mu.Unlock()
	if ep.closed {
		_ = conn.Close()
		return context.Canceled
	}
	ep.l3Conn = conn
	return nil
}

func (ep *Endpoint) isClosed() bool {
	ep.l3Mu.Lock()
	defer ep.l3Mu.Unlock()
	return ep.closed
}

func (ep *Endpoint) terminalError() error {
	ep.l3Mu.Lock()
	defer ep.l3Mu.Unlock()
	return ep.runErr
}

func (ep *Endpoint) fail(err error) {
	ep.l3Mu.Lock()
	if ep.runErr == nil {
		ep.runErr = err
	}
	conn := ep.l3Conn
	ep.l3Conn = nil
	ep.l3Mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (ep *Endpoint) close() error {
	ep.l3Mu.Lock()
	ep.closed = true
	conn := ep.l3Conn
	ep.l3Conn = nil
	ep.l3Mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}
