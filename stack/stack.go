package stack

import (
	"context"
	"net"
)

type Stack interface {
	Run()
	DialTCP(ctx context.Context, addr *net.TCPAddr) (net.Conn, error)
	DialUDP(ctx context.Context, addr *net.UDPAddr) (net.Conn, error)
}

type Managed interface {
	RunContext(ctx context.Context) error
	Close(ctx context.Context) error
}
