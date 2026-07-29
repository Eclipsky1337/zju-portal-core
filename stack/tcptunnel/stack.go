package tcptunnel

import (
	"context"
	"fmt"
	"net"

	"github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/Eclipsky1337/zju-portal-core/internal/ippool"
	"github.com/Eclipsky1337/zju-portal-core/internal/zcdns"
)

type Stack struct {
	client  client.Client
	resolve zcdns.LocalServer
	ipPool  *ippool.IPPool[client.DomainResource]
}

func (s *Stack) Run() {}

func (s *Stack) RunContext(ctx context.Context) error {
	return ctx.Err()
}

func (s *Stack) Close(context.Context) error {
	return nil
}

func NewStack(client client.Client) (*Stack, error) {
	s := &Stack{
		client: client,
	}
	return s, nil
}

func (s *Stack) SetupResolve(r zcdns.LocalServer) {
	s.resolve = r
}

func (s *Stack) SetupIPPool(ipPool *ippool.IPPool[client.DomainResource]) {
	s.ipPool = ipPool
}

func (s *Stack) DialTCP(ctx context.Context, addr *net.TCPAddr) (net.Conn, error) {
	if s.client.CanUseTCPTunnel() {
		return s.client.DialTCP(ctx, addr)
	}

	return nil, fmt.Errorf("not implemented")
}

func (s *Stack) DialUDP(ctx context.Context, addr *net.UDPAddr) (net.Conn, error) {
	return nil, fmt.Errorf("not implemented")
}
