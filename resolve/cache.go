package resolve

import (
	"fmt"
	"net"
	"strings"

	"github.com/patrickmn/go-cache"
)

func (r *Resolver) getDNSCache(host string) (net.IP, bool) {
	host = normalizeDNSName(host)
	if item, found := r.dnsCache.Get(host); found {
		return item.(net.IP), found
	} else {
		return nil, found
	}
}

func (r *Resolver) setDNSCache(host string, ip net.IP) {
	host = normalizeDNSName(host)
	r.dnsCache.Set(host, ip, cache.DefaultExpiration)
}

func (r *Resolver) SetPermanentDNS(host string, ip net.IP) error {
	host = normalizeDNSName(host)
	if host == "" {
		return fmt.Errorf("static DNS host is empty")
	}
	if ip == nil {
		return fmt.Errorf("static DNS address for %q is invalid", host)
	}
	address := append(net.IP(nil), ip...)
	r.permanentDNSLock.Lock()
	r.permanentDNS[host] = address
	r.permanentDNSLock.Unlock()
	r.dnsCache.Set(host, ip, cache.NoExpiration)
	return nil
}

func (r *Resolver) ResolveStatic(host string) (net.IP, bool) {
	host = normalizeDNSName(host)
	r.permanentDNSLock.RLock()
	address, found := r.permanentDNS[host]
	r.permanentDNSLock.RUnlock()
	if found {
		return append(net.IP(nil), address...), true
	}
	address, found = r.dnsResource[host]
	if !found {
		return nil, false
	}
	return append(net.IP(nil), address...), true
}

func normalizeDNSName(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func normalizeDNSRecords(records map[string]net.IP) map[string]net.IP {
	if len(records) == 0 {
		return records
	}
	normalized := make(map[string]net.IP, len(records))
	for host, address := range records {
		normalized[normalizeDNSName(host)] = address
	}
	return normalized
}
