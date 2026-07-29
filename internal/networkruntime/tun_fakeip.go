package networkruntime

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"sync"
)

const defaultTUNFakeIPRange = "198.18.0.0/16"

type fakeIPStore struct {
	mu         sync.RWMutex
	prefix     netip.Prefix
	domainToIP map[string]netip.Addr
	ipToDomain map[netip.Addr]string
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
		domainToIP: make(map[string]netip.Addr),
		ipToDomain: make(map[netip.Addr]string),
		next:       base + 2,
		last:       uint32(uint64(base) + count - 2),
	}, nil
}

func (store *fakeIPStore) Assign(domain string) (netip.Addr, error) {
	domain = normalizeDomain(domain)
	store.mu.Lock()
	defer store.mu.Unlock()
	if address, ok := store.domainToIP[domain]; ok {
		return address, nil
	}
	if store.next > store.last {
		return netip.Addr{}, fmt.Errorf("TUN fake IP range %s is exhausted", store.prefix)
	}
	address := uint32ToAddr(store.next)
	store.next++
	store.domainToIP[domain] = address
	store.ipToDomain[address] = domain
	return address, nil
}

func (store *fakeIPStore) Lookup(address netip.Addr) (string, bool) {
	if !address.IsValid() {
		return "", false
	}
	address = address.Unmap()
	store.mu.RLock()
	domain, ok := store.ipToDomain[address]
	store.mu.RUnlock()
	return domain, ok
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
