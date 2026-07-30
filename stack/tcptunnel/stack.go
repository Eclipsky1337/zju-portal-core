package tcptunnel

import (
	"context"
	"net"

	"github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/Eclipsky1337/zju-portal-core/core"
)

type Stack struct {
	client client.Client
}

func (s *Stack) Run() {}

func (s *Stack) RunContext(ctx context.Context) error {
	<-ctx.Done()
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

	return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "TCP tunnel is unavailable", false, nil)
}

func (s *Stack) DialUDP(ctx context.Context, addr *net.UDPAddr) (net.Conn, error) {
	return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "TCP tunnel stack does not support UDP", false, nil)
}
