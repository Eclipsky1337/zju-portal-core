package atrustruntime

import (
	"errors"
	"fmt"

	clientpkg "github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/Eclipsky1337/zju-portal-core/core"
)

func (s *Session) Resources() (core.Resources, error) {
	s.mu.RLock()
	resources := cloneResources(s.resources)
	state := s.state
	s.mu.RUnlock()
	if state == core.SessionStateStopping || state == core.SessionStateStopped {
		return core.Resources{}, core.WrapError(core.ErrorCodeResourcesUnavailable, fmt.Sprintf("session %q resources are unavailable", s.id), true, nil)
	}
	if resources.IPResources == nil && resources.DomainResources == nil && resources.DNSRecords == nil {
		return core.Resources{}, core.WrapError(core.ErrorCodeResourcesUnavailable, fmt.Sprintf("session %q resources are unavailable", s.id), true, nil)
	}
	resources.Stale = resources.Stale || state != core.SessionStateReady
	return resources, nil
}

func cloneResources(resources core.Resources) core.Resources {
	resources.IPResources = append([]core.IPResource(nil), resources.IPResources...)
	resources.DomainResources = cloneMap(resources.DomainResources)
	resources.DNSRecords = cloneMap(resources.DNSRecords)
	return resources
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	if source == nil {
		return nil
	}
	result := make(map[K]V, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func snapshotResources(client clientpkg.Client) (core.Resources, error) {
	resources := core.Resources{
		IPResources:     []core.IPResource{},
		DomainResources: map[string]core.DomainResource{},
		DNSRecords:      map[string]string{},
	}

	if ip, err := client.IP(); err == nil {
		resources.ClientIP = ip.String()
	} else if !errors.Is(err, clientpkg.ErrResourceNotFound) {
		return core.Resources{}, err
	}
	if entries, err := client.IPResources(); err == nil {
		for _, entry := range entries {
			resources.IPResources = append(resources.IPResources, core.IPResource{
				IPMin:       entry.IPMin.String(),
				IPMax:       entry.IPMax.String(),
				PortMin:     entry.PortMin,
				PortMax:     entry.PortMax,
				Protocol:    entry.Protocol,
				AppID:       entry.AppID,
				NodeGroupID: entry.NodeGroupID,
			})
		}
	} else if !errors.Is(err, clientpkg.ErrResourceNotFound) {
		return core.Resources{}, err
	}
	if entries, err := client.DomainResources(); err == nil {
		for domain, entry := range entries {
			resources.DomainResources[domain] = core.DomainResource{
				PortMin:     entry.PortMin,
				PortMax:     entry.PortMax,
				Protocol:    entry.Protocol,
				AppID:       entry.AppID,
				NodeGroupID: entry.NodeGroupID,
			}
		}
	} else if !errors.Is(err, clientpkg.ErrResourceNotFound) {
		return core.Resources{}, err
	}
	if entries, err := client.DNSResource(); err == nil {
		for domain, ip := range entries {
			resources.DNSRecords[domain] = ip.String()
		}
	} else if !errors.Is(err, clientpkg.ErrResourceNotFound) {
		return core.Resources{}, err
	}
	if server, err := client.DNSServer(); err == nil {
		resources.DNSServer = server
	} else if !errors.Is(err, clientpkg.ErrResourceNotFound) {
		return core.Resources{}, err
	}
	return resources, nil
}
