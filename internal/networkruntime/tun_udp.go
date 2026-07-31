package networkruntime

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type tunUDPAssociation struct {
	service *tunService
	inbound N.PacketConn
	source  string

	mu      sync.Mutex
	writeMu sync.Mutex
	flows   map[string]*tunUDPFlow
}

type tunUDPFlow struct {
	association *tunUDPAssociation
	destination M.Socksaddr
	address     string
	remote      net.Conn
	activity    core.ConnectionActivity
	lastActive  atomic.Int64
	closeOnce   sync.Once
	closeErr    error
}

func (service *tunService) NewPacketConnection(ctx context.Context, inbound N.PacketConn, metadata M.Metadata) error {
	association := &tunUDPAssociation{
		service: service,
		inbound: inbound,
		source:  metadata.Source.String(),
		flows:   make(map[string]*tunUDPFlow),
	}
	defer association.Close()

	cleanupDone := make(chan struct{})
	defer close(cleanupDone)
	go association.cleanupIdle(cleanupDone)

	for {
		packet := buf.NewPacket()
		destination, err := inbound.ReadPacket(packet)
		if err != nil {
			packet.Release()
			if isExpectedTUNCloseError(err) {
				return nil
			}
			return err
		}
		if association.service.config.DNSHijack && destination.Port == 53 {
			err = association.service.handleUDPDNS(ctx, inbound, destination, packet)
			packet.Release()
			if err != nil && !isExpectedTUNCloseError(err) {
				return err
			}
			continue
		}
		flow, err := association.flow(ctx, destination)
		if err != nil {
			packet.Release()
			return err
		}
		count, err := flow.remote.Write(packet.Bytes())
		packet.Release()
		if flow.activity != nil {
			flow.activity.RecordUploaded(uint64(count))
		}
		flow.touch()
		if err != nil {
			_ = flow.Close()
			if !isExpectedTUNCloseError(err) {
				return err
			}
		}
	}
}

func (service *tunService) NewPacketConnectionEx(ctx context.Context, inbound N.PacketConn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	err := service.NewPacketConnection(ctx, inbound, M.Metadata{Source: source, Destination: destination})
	if onClose != nil {
		onClose(err)
	}
}

func (association *tunUDPAssociation) flow(ctx context.Context, destination M.Socksaddr) (*tunUDPFlow, error) {
	key := association.service.routeDestination(destination)
	association.mu.Lock()
	flow := association.flows[key]
	association.mu.Unlock()
	if flow != nil {
		return flow, nil
	}

	remote, err := association.service.outbound.DialContext(ctx, "udp", key)
	if err != nil {
		return nil, err
	}
	flow = &tunUDPFlow{
		association: association,
		destination: destination,
		address:     key,
		remote:      remote,
	}
	flow.touch()
	flow.activity = association.service.openActivity("udp", association.source, key, remote)

	association.mu.Lock()
	if existing := association.flows[key]; existing != nil {
		association.mu.Unlock()
		_ = flow.Close()
		return existing, nil
	}
	association.flows[key] = flow
	association.mu.Unlock()
	association.service.registerUDPFlow(flow)
	go flow.readResponses()
	return flow, nil
}

func (association *tunUDPAssociation) cleanupIdle(done <-chan struct{}) {
	interval := association.service.config.UDPTimeout / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case now := <-ticker.C:
			association.mu.Lock()
			flows := make([]*tunUDPFlow, 0, len(association.flows))
			for _, flow := range association.flows {
				flows = append(flows, flow)
			}
			association.mu.Unlock()
			for _, flow := range flows {
				if now.Sub(flow.lastActivity()) >= association.service.config.UDPTimeout {
					_ = flow.Close()
				}
			}
		}
	}
}

func (association *tunUDPAssociation) Close() error {
	association.mu.Lock()
	flows := make([]*tunUDPFlow, 0, len(association.flows))
	for _, flow := range association.flows {
		flows = append(flows, flow)
	}
	association.mu.Unlock()
	var closeErrors []error
	for _, flow := range flows {
		closeErrors = append(closeErrors, flow.Close())
	}
	closeErrors = append(closeErrors, association.inbound.Close())
	return errors.Join(closeErrors...)
}

func (flow *tunUDPFlow) readResponses() {
	data := make([]byte, 64*1024)
	for {
		count, err := flow.remote.Read(data)
		if count > 0 {
			flow.association.writeMu.Lock()
			writeErr := flow.association.inbound.WritePacket(buf.As(data[:count]), flow.destination)
			flow.association.writeMu.Unlock()
			if flow.activity != nil {
				flow.activity.RecordDownloaded(uint64(count))
			}
			flow.touch()
			if writeErr != nil {
				_ = flow.Close()
				return
			}
		}
		if err != nil {
			_ = flow.Close()
			return
		}
	}
}

func (flow *tunUDPFlow) Close() error {
	flow.closeOnce.Do(func() {
		flow.association.mu.Lock()
		if flow.association.flows[flow.address] == flow {
			delete(flow.association.flows, flow.address)
		}
		flow.association.mu.Unlock()
		flow.association.service.unregisterUDPFlow(flow)
		if flow.activity != nil {
			flow.closeErr = flow.activity.Close()
		} else {
			flow.closeErr = flow.remote.Close()
		}
	})
	return flow.closeErr
}

func (flow *tunUDPFlow) touch() { flow.lastActive.Store(time.Now().UnixNano()) }

func (flow *tunUDPFlow) lastActivity() time.Time {
	return time.Unix(0, flow.lastActive.Load())
}

func (service *tunService) registerUDPFlow(flow *tunUDPFlow) {
	service.udpFlowMu.Lock()
	service.udpFlows[flow] = struct{}{}
	var oldest *tunUDPFlow
	if len(service.udpFlows) > service.config.UDPMaxFlows {
		for candidate := range service.udpFlows {
			if candidate != flow && (oldest == nil || candidate.lastActivity().Before(oldest.lastActivity())) {
				oldest = candidate
			}
		}
	}
	service.udpFlowMu.Unlock()
	if oldest != nil {
		_ = oldest.Close()
	}
}

func (service *tunService) unregisterUDPFlow(flow *tunUDPFlow) {
	service.udpFlowMu.Lock()
	delete(service.udpFlows, flow)
	service.udpFlowMu.Unlock()
}
