package networkruntime

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	defaultTUNFakeIPRange = "198.18.0.0/16"
	fakeIPReuseAfter      = 30 * time.Minute
)

type fakeIPEntry struct {
	domain   string
	address  netip.Addr
	resolved netip.Addr
	lastUsed time.Time
}

type fakeIPStore struct {
	mu         sync.Mutex
	prefix     netip.Prefix
	domainToIP map[string]*fakeIPEntry
	ipToDomain map[netip.Addr]*fakeIPEntry
	next       uint32
	last       uint32
}

func newFakeIPStore(prefixText string) (*fakeIPStore, error) {
	prefix, err := netip.ParsePrefix(prefixText)
	if err != nil {
		return nil, fmt.Errorf("parse TUN fake IP range %q: %w", prefixText, err)
	}
	prefix = prefix.Masked()
	if !prefix.Addr().Is4() || prefix.Bits() < 8 || prefix.Bits() > 30 {
		return nil, fmt.Errorf("TUN fake IP range must be an IPv4 prefix between /8 and /30")
	}
	base := addrToUint32(prefix.Addr())
	count := uint64(1) << uint64(32-prefix.Bits())
	return &fakeIPStore{
		prefix:     prefix,
		domainToIP: make(map[string]*fakeIPEntry),
		ipToDomain: make(map[netip.Addr]*fakeIPEntry),
		next:       base + 2,
		last:       uint32(uint64(base) + count - 2),
	}, nil
}

func (store *fakeIPStore) Assign(domain string) (netip.Addr, error) {
	return store.assign(domain, netip.Addr{})
}

func (store *fakeIPStore) AssignResolved(domain string, resolved netip.Addr) (netip.Addr, error) {
	resolved = resolved.Unmap()
	if !resolved.Is4() {
		return netip.Addr{}, fmt.Errorf("resolved TUN fake IP destination must be IPv4")
	}
	return store.assign(domain, resolved)
}

func (store *fakeIPStore) assign(domain string, resolved netip.Addr) (netip.Addr, error) {
	domain = normalizeDomain(domain)
	now := time.Now()
	store.mu.Lock()
	defer store.mu.Unlock()
	if entry := store.domainToIP[domain]; entry != nil {
		entry.resolved = resolved
		entry.lastUsed = now
		return entry.address, nil
	}
	if store.next > store.last {
		entry := store.oldestReusable(now)
		if entry == nil {
			return netip.Addr{}, fmt.Errorf("TUN fake IP range %s is exhausted", store.prefix)
		}
		delete(store.domainToIP, entry.domain)
		entry.domain = domain
		entry.resolved = resolved
		entry.lastUsed = now
		store.domainToIP[domain] = entry
		return entry.address, nil
	}
	address := uint32ToAddr(store.next)
	store.next++
	entry := &fakeIPEntry{domain: domain, address: address, resolved: resolved, lastUsed: now}
	store.domainToIP[domain] = entry
	store.ipToDomain[address] = entry
	return address, nil
}

func (store *fakeIPStore) Lookup(address netip.Addr) (string, bool) {
	domain, _, found := store.LookupDestination(address)
	return domain, found
}

func (store *fakeIPStore) LookupDestination(address netip.Addr) (string, netip.Addr, bool) {
	if !address.IsValid() {
		return "", netip.Addr{}, false
	}
	address = address.Unmap()
	store.mu.Lock()
	defer store.mu.Unlock()
	entry := store.ipToDomain[address]
	if entry == nil {
		return "", netip.Addr{}, false
	}
	entry.lastUsed = time.Now()
	return entry.domain, entry.resolved, true
}

func (store *fakeIPStore) oldestReusable(now time.Time) *fakeIPEntry {
	cutoff := now.Add(-fakeIPReuseAfter)
	var oldest *fakeIPEntry
	for _, entry := range store.domainToIP {
		if entry.lastUsed.After(cutoff) || oldest != nil && !entry.lastUsed.Before(oldest.lastUsed) {
			continue
		}
		oldest = entry
	}
	return oldest
}

func normalizeDomain(domain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}

func addrToUint32(address netip.Addr) uint32 {
	value := address.As4()
	return binary.BigEndian.Uint32(value[:])
}

func uint32ToAddr(value uint32) netip.Addr {
	var bytes [4]byte
	binary.BigEndian.PutUint32(bytes[:], value)
	return netip.AddrFrom4(bytes)
}
