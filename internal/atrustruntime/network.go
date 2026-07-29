package atrustruntime

import (
	"context"
	"net"

	clientpkg "github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/internal/networkruntime"
)

type networkSession struct {
	outbound core.Outbound
}

type networkHealth interface {
	Done() <-chan struct{}
	Err() error
}

type networkServices interface {
	Services() []core.ServiceStatus
	ServiceEvents() <-chan core.ServiceStatus
}

type networkStatistics interface {
	TrafficStats() core.TrafficStats
	Connections() []core.ConnectionInfo
	CloseConnection(string) error
	TransportConnections() []core.TransportConnectionInfo
}

type networkRouting interface {
	RoutingMode() core.RoutingMode
	SetRoutingMode(core.RoutingMode) (core.RoutingMode, error)
}

type replaceableNetworkRuntime interface {
	core.Outbound
	ReplaceVPN(context.Context, clientpkg.Client, networkruntime.Config) error
}

func wrapNetwork(outbound core.Outbound) *networkSession {
	if outbound == nil {
		return nil
	}
	return &networkSession{outbound: outbound}
}

func (network *networkSession) DialContext(ctx context.Context, protocol, address string) (net.Conn, error) {
	return network.outbound.DialContext(ctx, protocol, address)
}

func (network *networkSession) Close(ctx context.Context) error {
	return network.outbound.Close(ctx)
}

func (network *networkSession) Done() <-chan struct{} {
	provider, _ := network.outbound.(networkHealth)
	if provider == nil {
		return nil
	}
	return provider.Done()
}

func (network *networkSession) Err() error {
	provider, _ := network.outbound.(networkHealth)
	if provider == nil {
		return nil
	}
	return provider.Err()
}

func (network *networkSession) Services() ([]core.ServiceStatus, error) {
	provider, _ := network.outbound.(networkServices)
	if provider == nil {
		return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "session runtime is unavailable", true, nil)
	}
	return provider.Services(), nil
}

func (network *networkSession) ServiceEvents() <-chan core.ServiceStatus {
	provider, _ := network.outbound.(networkServices)
	if provider == nil {
		return nil
	}
	return provider.ServiceEvents()
}

func (network *networkSession) TrafficStats() (core.TrafficStats, error) {
	provider, _ := network.outbound.(networkStatistics)
	if provider == nil {
		return core.TrafficStats{}, core.WrapError(core.ErrorCodeOutboundUnavailable, "traffic statistics are unavailable", true, nil)
	}
	return provider.TrafficStats(), nil
}

func (network *networkSession) Connections() ([]core.ConnectionInfo, error) {
	provider, _ := network.outbound.(networkStatistics)
	if provider == nil {
		return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "connection statistics are unavailable", true, nil)
	}
	return provider.Connections(), nil
}

func (network *networkSession) CloseConnection(id string) error {
	provider, _ := network.outbound.(networkStatistics)
	if provider == nil {
		return core.WrapError(core.ErrorCodeOutboundUnavailable, "connection control is unavailable", true, nil)
	}
	return provider.CloseConnection(id)
}

func (network *networkSession) TransportConnections() ([]core.TransportConnectionInfo, error) {
	provider, _ := network.outbound.(networkStatistics)
	if provider == nil {
		return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "transport connection statistics are unavailable", true, nil)
	}
	return provider.TransportConnections(), nil
}

func (network *networkSession) RoutingMode() (core.RoutingMode, error) {
	provider, _ := network.outbound.(networkRouting)
	if provider == nil {
		return "", core.WrapError(core.ErrorCodeOutboundUnavailable, "routing mode is unavailable", true, nil)
	}
	return provider.RoutingMode(), nil
}

func (network *networkSession) SetRoutingMode(mode core.RoutingMode) (core.RoutingMode, error) {
	provider, _ := network.outbound.(networkRouting)
	if provider == nil {
		return "", core.WrapError(core.ErrorCodeOutboundUnavailable, "routing mode is unavailable", true, nil)
	}
	return provider.SetRoutingMode(mode)
}

func (network *networkSession) replaceable() (replaceableNetworkRuntime, bool) {
	runtime, ok := network.outbound.(replaceableNetworkRuntime)
	return runtime, ok
}

func (network *networkSession) same(other *networkSession) bool {
	return network != nil && other != nil && network.outbound == other.outbound
}
