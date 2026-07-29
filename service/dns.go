package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/internal/dnsmessage"
	"github.com/Eclipsky1337/zju-portal-core/log"
	"github.com/miekg/dns"
)

type DNSResolver interface {
	Resolve(context.Context, string) (context.Context, net.IP, error)
}

type ManagedDNSService struct {
	bindAddr string
	handler  dns.Handler

	mu          sync.Mutex
	packetConn  net.PacketConn
	listener    net.Listener
	udpServer   *dns.Server
	tcpServer   *dns.Server
	stopContext func() bool

	startOnce sync.Once
	startErr  error
	closeOnce sync.Once
	closeErr  error
	runDone   chan struct{}
	runOnce   sync.Once
	runErr    error
}

var _ core.Component = (*ManagedDNSService)(nil)

func NewManagedDNSService(bindAddr string, resolver DNSResolver) *ManagedDNSService {
	handler := dnsmessage.Handler{Resolver: resolver}
	return &ManagedDNSService{
		bindAddr: bindAddr,
		handler: dns.HandlerFunc(func(writer dns.ResponseWriter, request *dns.Msg) {
			_ = writer.WriteMsg(handler.Handle(context.Background(), request))
		}),
		runDone: make(chan struct{}),
	}
}

func (service *ManagedDNSService) Start(ctx context.Context) error {
	service.startOnce.Do(func() {
		packetConn, err := net.ListenPacket("udp", service.bindAddr)
		if err != nil {
			service.startErr = fmt.Errorf("listen DNS UDP: %w", err)
			return
		}
		listener, err := net.Listen("tcp", packetConn.LocalAddr().String())
		if err != nil {
			_ = packetConn.Close()
			service.startErr = fmt.Errorf("listen DNS TCP: %w", err)
			return
		}

		service.mu.Lock()
		service.packetConn = packetConn
		service.listener = listener
		service.udpServer = &dns.Server{PacketConn: packetConn, Handler: service.handler}
		service.tcpServer = &dns.Server{Listener: listener, Handler: service.handler}
		service.stopContext = context.AfterFunc(ctx, func() { _ = service.Close(context.Background()) })
		udpServer := service.udpServer
		tcpServer := service.tcpServer
		service.mu.Unlock()

		go func() {
			if err := udpServer.ActivateAndServe(); err != nil && !errors.Is(err, net.ErrClosed) {
				log.Printf("DNS UDP server stopped: %v", err)
				service.recordRunError(err)
			}
		}()
		go func() {
			if err := tcpServer.ActivateAndServe(); err != nil && !errors.Is(err, net.ErrClosed) {
				log.Printf("DNS TCP server stopped: %v", err)
				service.recordRunError(err)
			}
		}()
		log.Printf("DNS server listening on %s (UDP/TCP)", packetConn.LocalAddr())
	})
	return service.startErr
}

func (service *ManagedDNSService) Addr() net.Addr {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.packetConn == nil {
		return nil
	}
	return service.packetConn.LocalAddr()
}

func (service *ManagedDNSService) Done() <-chan struct{} { return service.runDone }

func (service *ManagedDNSService) Err() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.runErr
}

func (service *ManagedDNSService) recordRunError(err error) {
	service.mu.Lock()
	service.runErr = errors.Join(service.runErr, err)
	service.mu.Unlock()
	service.runOnce.Do(func() { close(service.runDone) })
}

func (service *ManagedDNSService) Close(ctx context.Context) error {
	service.closeOnce.Do(func() {
		service.mu.Lock()
		if service.stopContext != nil {
			service.stopContext()
		}
		udpServer := service.udpServer
		tcpServer := service.tcpServer
		service.mu.Unlock()

		var closeErrors []error
		if udpServer != nil {
			closeErrors = append(closeErrors, udpServer.ShutdownContext(ctx))
		}
		if tcpServer != nil {
			closeErrors = append(closeErrors, tcpServer.ShutdownContext(ctx))
		}
		service.closeErr = errors.Join(closeErrors...)
	})
	return service.closeErr
}
