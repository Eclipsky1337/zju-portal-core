package atrustruntime

import "github.com/Eclipsky1337/zju-portal-core/core"

func (s *Session) TrafficStats() (core.TrafficStats, error) {
	network, err := s.stableNetwork()
	if err != nil {
		return core.TrafficStats{}, err
	}
	provider, ok := network.(interface{ TrafficStats() core.TrafficStats })
	if !ok {
		return core.TrafficStats{}, core.WrapError(core.ErrorCodeOutboundUnavailable, "traffic statistics are unavailable", true, nil)
	}
	stats := provider.TrafficStats()
	stats.SessionID = s.id
	return stats, err
}

func (s *Session) Connections() ([]core.ConnectionInfo, error) {
	network, err := s.stableNetwork()
	if err != nil {
		return nil, err
	}
	provider, ok := network.(interface{ Connections() []core.ConnectionInfo })
	if !ok {
		return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "connection statistics are unavailable", true, nil)
	}
	connections := provider.Connections()
	for index := range connections {
		connections[index].SessionID = s.id
	}
	return connections, nil
}

func (s *Session) CloseConnection(id string) error {
	network, err := s.stableNetwork()
	if err != nil {
		return err
	}
	provider, ok := network.(interface{ CloseConnection(string) error })
	if !ok {
		return core.WrapError(core.ErrorCodeOutboundUnavailable, "connection control is unavailable", true, nil)
	}
	return provider.CloseConnection(id)
}

func (s *Session) TransportConnections() ([]core.TransportConnectionInfo, error) {
	network, err := s.stableNetwork()
	if err != nil {
		return nil, err
	}
	provider, ok := network.(interface {
		TransportConnections() []core.TransportConnectionInfo
	})
	if !ok {
		return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "transport connection statistics are unavailable", true, nil)
	}
	connections := provider.TransportConnections()
	for index := range connections {
		connections[index].SessionID = s.id
	}
	return connections, nil
}

func (s *Session) stableNetwork() (core.Outbound, error) {
	s.mu.RLock()
	network := s.network
	state := s.state
	s.mu.RUnlock()
	if network == nil || state == core.SessionStateStopping || state == core.SessionStateStopped {
		return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "session runtime is unavailable", true, nil)
	}
	return network, nil
}
