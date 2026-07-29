package tcptunnel

import (
	"context"
	"fmt"
	"net"

	"github.com/Eclipsky1337/zju-portal-core/client"
)

type Stack struct {
	client client.Client
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

func (s *Stack) DialTCP(ctx context.Context, addr *net.TCPAddr) (net.Conn, error) {
	if s.client.CanUseTCPTunnel() {
		return s.client.DialTCP(ctx, addr)
	}

	return nil, fmt.Errorf("not implemented")
}

func (s *Stack) DialUDP(ctx context.Context, addr *net.UDPAddr) (net.Conn, error) {
	return nil, fmt.Errorf("not implemented")
}
