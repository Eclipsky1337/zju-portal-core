package networkruntime

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/Eclipsky1337/zju-portal-core/client"
	"inet.af/netaddr"
)

func buildResourceRoutePrefixes(ipResources []client.IPResource, dnsRecords map[string]net.IP, excludedIPs []net.IP, fakeIPRange string) ([]netip.Prefix, error) {
	var builder netaddr.IPSetBuilder

	for _, resource := range ipResources {
		first, firstOK := netaddr.FromStdIP(resource.IPMin)
		last, lastOK := netaddr.FromStdIP(resource.IPMax)
		if !firstOK || !lastOK || !first.Is4() || !last.Is4() {
			continue
		}
		builder.AddRange(netaddr.IPRangeFrom(first, last))
	}
	for _, address := range dnsRecords {
		ip, ok := netaddr.FromStdIP(address)
		if ok && ip.Is4() {
			builder.Add(ip)
		}
	}
	if fakeIPRange != "" {
		prefix, err := netaddr.ParseIPPrefix(fakeIPRange)
		if err != nil || !prefix.IP().Is4() {
			return nil, fmt.Errorf("parse TUN fake IP route %q", fakeIPRange)
		}
		builder.AddPrefix(prefix)
	}
	for _, address := range excludedIPs {
		ip, ok := netaddr.FromStdIP(address)
		if ok && ip.Is4() {
			builder.Remove(ip)
		}
	}

	set, err := builder.IPSet()
	if err != nil {
		return nil, fmt.Errorf("build TUN resource routes: %w", err)
	}
	prefixes := set.Prefixes()
	routes := make([]netip.Prefix, 0, len(prefixes))
	for _, prefix := range prefixes {
		address := netip.AddrFrom4(prefix.IP().As4())
		routes = append(routes, netip.PrefixFrom(address, int(prefix.Bits())))
	}
	return routes, nil
}

func addStaticDNSResources(resources []client.IPResource, dnsRecords map[string]net.IP) []client.IPResource {
	result := append([]client.IPResource(nil), resources...)
	for _, address := range dnsRecords {
		if ipv4 := address.To4(); ipv4 != nil {
			result = append(result, client.IPResource{
				IPMin:    append(net.IP(nil), ipv4...),
				IPMax:    append(net.IP(nil), ipv4...),
				PortMin:  0,
				PortMax:  65535,
				Protocol: "all",
			})
		}
	}
	return result
}

func equalRoutePrefixes(left, right []netip.Prefix) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
