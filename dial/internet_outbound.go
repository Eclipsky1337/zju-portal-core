package dial

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"golang.org/x/net/proxy"
)

var (
	ErrRejected             = errors.New("connection rejected by routing policy")
	ErrSOCKS5UDPUnsupported = errors.New("SOCKS5 internet outbound only supports TCP")
)

type directOutbound struct {
	dialer contextDialer
}

func NewDirectOutbound() core.Outbound {
	return &directOutbound{dialer: &net.Dialer{}}
}

func NewDirectOutboundWithDialer(dialer contextDialer) core.Outbound {
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	return &directOutbound{dialer: dialer}
}

func NewInternetOutbound(config core.InternetOutboundConfig) (core.Outbound, error) {
	return NewInternetOutboundWithDialer(config, &net.Dialer{})
}

type contextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

func NewInternetOutboundWithDialer(config core.InternetOutboundConfig, baseDialer contextDialer) (core.Outbound, error) {
	if baseDialer == nil {
		baseDialer = &net.Dialer{}
	}
	switch config.Type {
	case "", core.InternetOutboundDirect:
		return &directOutbound{dialer: baseDialer}, nil
	case core.InternetOutboundSOCKS5:
		if config.Address == "" {
			return nil, core.WrapError(core.ErrorCodeConfigInvalid, "SOCKS5 internet outbound address is required", false, nil)
		}
		var auth *proxy.Auth
		if config.Username != "" || config.Password != "" {
			auth = &proxy.Auth{User: config.Username, Password: config.Password}
		}
		dialer, err := proxy.SOCKS5("tcp", config.Address, auth, proxyDialerAdapter{baseDialer})
		if err != nil {
			return nil, fmt.Errorf("create SOCKS5 internet outbound: %w", err)
		}
		return &socks5Outbound{dialer: dialer}, nil
	case core.InternetOutboundReject:
		return rejectOutbound{}, nil
	default:
		return nil, core.WrapError(core.ErrorCodeConfigInvalid, fmt.Sprintf("unsupported internet outbound type %q", config.Type), false, nil)
	}
}

type proxyDialerAdapter struct{ contextDialer }

func (dialer proxyDialerAdapter) Dial(network, address string) (net.Conn, error) {
	return dialer.DialContext(context.Background(), network, address)
}

func (outbound *directOutbound) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return outbound.dialer.DialContext(ctx, network, address)
}

func (*directOutbound) Close(context.Context) error { return nil }

type socks5Outbound struct {
	dialer proxy.Dialer
}

func (outbound *socks5Outbound) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" {
		return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "SOCKS5 internet outbound only supports TCP", false, fmt.Errorf("%w: %s", ErrSOCKS5UDPUnsupported, network))
	}
	if dialer, ok := outbound.dialer.(proxy.ContextDialer); ok {
		return dialer.DialContext(ctx, "tcp", address)
	}
	return outbound.dialer.Dial("tcp", address)
}

func (*socks5Outbound) Close(context.Context) error { return nil }

type rejectOutbound struct{}

func (rejectOutbound) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	return nil, fmt.Errorf("%w: %s/%s", ErrRejected, address, network)
}

func (rejectOutbound) Close(context.Context) error { return nil }
