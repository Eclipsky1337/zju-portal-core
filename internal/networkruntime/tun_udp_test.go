package networkruntime

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

func TestTUNUDPAssociationCreatesFlowPerDestination(t *testing.T) {
	outbound := &udpFlowOutboundStub{}
	created, err := newTUNService(TUNConfig{}, outbound, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	association := &tunUDPAssociation{service: service, source: "172.19.0.1:12345", flows: make(map[string]*tunUDPFlow)}

	first := M.SocksaddrFromNetIP(netip.MustParseAddrPort("192.0.2.1:3478"))
	second := M.SocksaddrFromNetIP(netip.MustParseAddrPort("198.51.100.1:3478"))
	firstFlow, err := association.flow(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	defer firstFlow.Close()
	secondFlow, err := association.flow(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	defer secondFlow.Close()

	if firstFlow == secondFlow || len(association.flows) != 2 {
		t.Fatalf("UDP flows = %#v", association.flows)
	}
	if addresses := outbound.addresses(); len(addresses) != 2 || addresses[0] != first.String() || addresses[1] != second.String() {
		t.Fatalf("dial addresses = %#v", addresses)
	}
}

func TestTUNUDPFlowLimitEvictsOldest(t *testing.T) {
	outbound := &udpFlowOutboundStub{}
	created, err := newTUNService(TUNConfig{UDPMaxFlows: 1}, outbound, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	association := &tunUDPAssociation{service: service, source: "172.19.0.1:12345", flows: make(map[string]*tunUDPFlow)}

	first, err := association.flow(context.Background(), M.SocksaddrFromNetIP(netip.MustParseAddrPort("192.0.2.1:53")))
	if err != nil {
		t.Fatal(err)
	}
	first.lastActive.Store(time.Now().Add(-time.Minute).UnixNano())
	second, err := association.flow(context.Background(), M.SocksaddrFromNetIP(netip.MustParseAddrPort("198.51.100.1:53")))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	association.mu.Lock()
	flowCount := len(association.flows)
	association.mu.Unlock()
	if flowCount != 1 {
		t.Fatalf("flow count = %d", flowCount)
	}
}

func TestTUNUDPFlowClosePreservesError(t *testing.T) {
	wantErr := errors.New("activity close failed")
	created, err := newTUNService(TUNConfig{}, &udpFlowOutboundStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := created.(*tunService)
	association := &tunUDPAssociation{service: service, flows: make(map[string]*tunUDPFlow)}
	flow := &tunUDPFlow{association: association, address: "192.0.2.1:53", activity: udpFlowActivityStub{closeErr: wantErr}}
	association.flows[flow.address] = flow
	service.registerUDPFlow(flow)

	if err := flow.Close(); !errors.Is(err, wantErr) {
		t.Fatalf("Close() error = %v", err)
	}
	if err := flow.Close(); !errors.Is(err, wantErr) {
		t.Fatalf("second Close() error = %v", err)
	}
}

type udpFlowActivityStub struct {
	closeErr error
}

func (udpFlowActivityStub) RecordUploaded(uint64)   {}
func (udpFlowActivityStub) RecordDownloaded(uint64) {}
func (activity udpFlowActivityStub) Close() error   { return activity.closeErr }

type udpFlowOutboundStub struct {
	mu    sync.Mutex
	dials []string
	peers []net.Conn
}

func (outbound *udpFlowOutboundStub) DialContext(_ context.Context, _ string, address string) (net.Conn, error) {
	local, peer := net.Pipe()
	outbound.mu.Lock()
	outbound.dials = append(outbound.dials, address)
	outbound.peers = append(outbound.peers, peer)
	outbound.mu.Unlock()
	return local, nil
}

func (outbound *udpFlowOutboundStub) Close(context.Context) error {
	outbound.mu.Lock()
	defer outbound.mu.Unlock()
	for _, peer := range outbound.peers {
		_ = peer.Close()
	}
	return nil
}

func (outbound *udpFlowOutboundStub) addresses() []string {
	outbound.mu.Lock()
	defer outbound.mu.Unlock()
	return append([]string(nil), outbound.dials...)
}
