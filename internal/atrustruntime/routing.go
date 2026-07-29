package atrustruntime

import (
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

type routingRuntime interface {
	RoutingMode() core.RoutingMode
	SetRoutingMode(core.RoutingMode) (core.RoutingMode, error)
}

func (s *Session) RoutingMode() (core.RoutingMode, error) {
	s.mu.RLock()
	network := s.network
	s.mu.RUnlock()
	provider, ok := network.(routingRuntime)
	if !ok {
		return "", core.WrapError(core.ErrorCodeOutboundUnavailable, "routing mode is unavailable", true, nil)
	}
	return provider.RoutingMode(), nil
}

func (s *Session) SetRoutingMode(mode core.RoutingMode) error {
	if !mode.Valid() {
		return core.WrapError(core.ErrorCodeInvalidRequest, "invalid routing mode", false, nil)
	}
	s.mu.RLock()
	network := s.network
	s.mu.RUnlock()
	provider, ok := network.(routingRuntime)
	if !ok {
		return core.WrapError(core.ErrorCodeOutboundUnavailable, "routing mode is unavailable", true, nil)
	}
	previous, err := provider.SetRoutingMode(mode)
	if err != nil {
		return core.WrapError(core.ErrorCodeInvalidRequest, "set routing mode", false, err)
	}
	if previous == mode {
		return nil
	}
	s.events <- core.Event{
		SessionID:           s.id,
		Type:                core.EventTypeRoutingModeChanged,
		Timestamp:           time.Now(),
		PreviousRoutingMode: previous,
		RoutingMode:         mode,
	}
	return nil
}
