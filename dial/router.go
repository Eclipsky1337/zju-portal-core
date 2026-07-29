package dial

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

const (
	OutboundATrust   = "atrust"
	OutboundInternet = "internet"
	OutboundDirect   = "direct"

	RouteReasonVPNResource        = "vpn_resource"
	RouteReasonResourceNotMatched = "resource_not_matched"
	RouteReasonGlobalMode         = "global_mode"
	RouteReasonDirectMode         = "direct_mode"
)

type Router struct {
	vpn      core.Outbound
	internet core.Outbound
	direct   core.Outbound

	modeMu sync.RWMutex
	mode   core.RoutingMode
}

func NewRouter(vpn, internet core.Outbound, mode core.RoutingMode) (*Router, error) {
	return NewRouterWithDirect(vpn, internet, NewDirectOutbound(), mode)
}

func NewRouterWithDirect(vpn, internet, direct core.Outbound, mode core.RoutingMode) (*Router, error) {
	if mode == "" {
		mode = core.RoutingModeRule
	}
	if !mode.Valid() {
		return nil, core.WrapError(core.ErrorCodeConfigInvalid, fmt.Sprintf("invalid routing mode %q", mode), false, nil)
	}
	return &Router{vpn: vpn, internet: internet, direct: direct, mode: mode}, nil
}

func (router *Router) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch router.Mode() {
	case core.RoutingModeGlobal:
		conn, err := router.vpn.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return newRoutedConn(conn, core.RouteInfo{Outbound: OutboundATrust, Reason: RouteReasonGlobalMode}), nil
	case core.RoutingModeDirect:
		conn, err := router.direct.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return newRoutedConn(conn, core.RouteInfo{Outbound: OutboundDirect, Reason: RouteReasonDirectMode}), nil
	default:
		return router.dialRule(ctx, network, address)
	}
}

func (router *Router) dialRule(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := router.vpn.DialContext(ctx, network, address)
	if err == nil {
		return newRoutedConn(conn, core.RouteInfo{Outbound: OutboundATrust, Reason: RouteReasonVPNResource}), nil
	}
	if !errors.Is(err, ErrNotInResources) {
		return nil, err
	}
	conn, err = router.internet.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return newRoutedConn(conn, core.RouteInfo{Outbound: OutboundInternet, Reason: RouteReasonResourceNotMatched}), nil
}

func (router *Router) Mode() core.RoutingMode {
	router.modeMu.RLock()
	defer router.modeMu.RUnlock()
	return router.mode
}

func (router *Router) SetMode(mode core.RoutingMode) (core.RoutingMode, error) {
	if !mode.Valid() {
		return "", core.WrapError(core.ErrorCodeInvalidRequest, fmt.Sprintf("invalid routing mode %q", mode), false, nil)
	}
	router.modeMu.Lock()
	previous := router.mode
	router.mode = mode
	router.modeMu.Unlock()
	return previous, nil
}

func (router *Router) Close(ctx context.Context) error {
	return errors.Join(router.vpn.Close(ctx), router.internet.Close(ctx), router.direct.Close(ctx))
}

type routedConn struct {
	net.Conn
	route     core.RouteInfo
	closeOnce sync.Once
	closeErr  error
}

func newRoutedConn(conn net.Conn, route core.RouteInfo) net.Conn {
	return &routedConn{Conn: conn, route: route}
}

func (conn *routedConn) RouteInfo() core.RouteInfo { return conn.route }

func (conn *routedConn) TransportConnectionID() string {
	identified, ok := conn.Conn.(interface{ TransportConnectionID() string })
	if !ok {
		return ""
	}
	return identified.TransportConnectionID()
}

func (conn *routedConn) Close() error {
	conn.closeOnce.Do(func() { conn.closeErr = conn.Conn.Close() })
	return conn.closeErr
}

func (conn *routedConn) CloseWrite() error {
	if writer, ok := conn.Conn.(interface{ CloseWrite() error }); ok {
		return writer.CloseWrite()
	}
	return conn.Close()
}
