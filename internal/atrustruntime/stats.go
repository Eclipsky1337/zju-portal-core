package atrustruntime

import "github.com/Eclipsky1337/zju-portal-core/core"

func (s *Session) TrafficStats() (core.TrafficStats, error) {
	network, err := s.stableNetwork()
	if err != nil {
		return core.TrafficStats{}, err
	}
	stats, err := network.TrafficStats()
	if err != nil {
		return core.TrafficStats{}, err
	}
	stats.SessionID = s.id
	return stats, err
}

func (s *Session) Connections() ([]core.ConnectionInfo, error) {
	network, err := s.stableNetwork()
	if err != nil {
		return nil, err
	}
	connections, err := network.Connections()
	if err != nil {
		return nil, err
	}
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
	return network.CloseConnection(id)
}

func (s *Session) TransportConnections() ([]core.TransportConnectionInfo, error) {
	network, err := s.stableNetwork()
	if err != nil {
		return nil, err
	}
	connections, err := network.TransportConnections()
	if err != nil {
		return nil, err
	}
	for index := range connections {
		connections[index].SessionID = s.id
	}
	return connections, nil
}

func (s *Session) stableNetwork() (*networkSession, error) {
	s.mu.RLock()
	network := s.network
	state := s.state
	s.mu.RUnlock()
	if network == nil || state == core.SessionStateStopping || state == core.SessionStateStopped {
		return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "session runtime is unavailable", true, nil)
	}
	return network, nil
}
