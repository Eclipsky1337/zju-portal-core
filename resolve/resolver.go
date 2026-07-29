package resolve

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/Eclipsky1337/zju-portal-core/internal/ippool"
	"github.com/Eclipsky1337/zju-portal-core/log"
	"github.com/patrickmn/go-cache"
)

type Resolver struct {
	remoteUDPResolver *net.Resolver
	remoteTCPResolver *net.Resolver
	secondaryResolver *net.Resolver
	remoteDNSTimeout  time.Duration
	ttl               uint64
	domainResources   map[string]client.DomainResource
	dnsResource       map[string]net.IP
	permanentDNS      map[string]net.IP
	useRemoteDNS      bool
	permanentDNSLock  sync.RWMutex

	dnsCache *cache.Cache

	IPPool *ippool.IPPool[client.DomainResource]

	timer  *time.Timer
	useTCP bool
	// check to use tcp resolver or udp resolver
	tcpLock sync.RWMutex
	// check to handle concurrent same dns query
	// only the goroutine which get the lock can use remoteResolver
	// MUST handler lock/unlock carefully!
	concurResolveLock sync.Map

	closeOnce sync.Once
}

type contextKey string

var (
	ContextKeyFakeIP         = contextKey("FAKE_IP")
	ContextKeyResolveHost    = contextKey("RESOLVE_HOST")
	ContextKeyDomainResource = contextKey("DOMAIN_RESOURCE")
)

const defaultRemoteDNSLookupTimeout = 1500 * time.Millisecond

// Resolve ip address. If the host could be visited via VPN, this function set a DOMAIN_RESOURCE value in context. If resolve success, this function set a RESOLVE_HOST value in context.
func (r *Resolver) Resolve(ctx context.Context, host string) (resCtx context.Context, resIP net.IP, resErr error) {
	host = normalizeDNSName(host)
	defer func() {
		if resErr == nil {
			resCtx = context.WithValue(resCtx, ContextKeyResolveHost, host)
		}
	}()
	var domainResourceFound = false
	var domainResource client.DomainResource
	if r.domainResources != nil {
		for domain, resource := range r.domainResources {
			if strings.HasSuffix(host, domain) {
				domainResourceFound = true
				domainResource = resource
				ctx = context.WithValue(ctx, ContextKeyDomainResource, resource)
				log.DebugPrintf("Domain resource found: %s", domain)
				break
			}
		}
	}

	if cachedIP, found := r.getDNSCache(host); found {
		log.Printf("%s -> %s", host, cachedIP.String())
		return ctx, cachedIP, nil
	}

	if r.dnsResource != nil {
		if ip, found := r.dnsResource[host]; found {
			log.Printf("%s -> %s", host, ip.String())
			if domainResourceFound {
				err := r.IPPool.SetIPDomain(ip, host, domainResource)
				if err != nil {
					log.DebugPrintf("Set IP err: %s", err)
				}
			}
			return ctx, ip, nil
		}

		if fakeIPValue := ctx.Value(ContextKeyFakeIP); fakeIPValue != nil {
			if domainResourceFound {
				ip := r.IPPool.GenerateIP(host, domainResource)
				log.Printf("%s -> %s (Fake IP)", host, ip.String())
				return ctx, ip, nil
			}
		}
	}

	if r.useRemoteDNS {
		r.tcpLock.RLock()
		useTCP := r.useTCP
		r.tcpLock.RUnlock()
		resolveLockItem, _ := r.concurResolveLock.LoadOrStore(host, new(sync.Mutex))
		resolveLock := resolveLockItem.(*sync.Mutex)
		if resolveLock.TryLock() {
			if !useTCP {
				ips, err := r.lookupRemoteIP(ctx, r.remoteUDPResolver, host)
				if err != nil {
					if ips, err = r.lookupRemoteIP(ctx, r.remoteTCPResolver, host); err != nil {
						log.Printf("Resolve IPv4 addr failed using remote UDP/TCP DNS: %s, using secondary DNS instead", host)
						resolvedCtx, address, resolveErr := r.ResolveWithSecondaryDNS(ctx, host)
						resolveLock.Unlock()
						return resolvedCtx, address, resolveErr
					} else {
						r.tcpLock.Lock()
						r.useTCP = true
						if r.timer == nil {
							r.timer = time.AfterFunc(10*time.Minute, func() {
								r.tcpLock.Lock()
								r.useTCP = false
								r.timer = nil
								r.tcpLock.Unlock()
							})
						}
						r.tcpLock.Unlock()
					}
				}
				// Set DNS cache if tcp or udp DNS success
				r.setDNSCache(host, ips[0])
				resolveLock.Unlock()
				log.Printf("%s -> %s", host, ips[0].String())
				return ctx, ips[0], nil
			} else {
				// Only try tcp and secondary DNS
				if ips, err := r.lookupRemoteIP(ctx, r.remoteTCPResolver, host); err != nil {
					log.Printf("Resolve IPv4 addr failed using remote TCP DNS: %s, using secondary DNS instead", host)
					resolvedCtx, address, resolveErr := r.ResolveWithSecondaryDNS(ctx, host)
					resolveLock.Unlock()
					return resolvedCtx, address, resolveErr
				} else {
					r.setDNSCache(host, ips[0])
					resolveLock.Unlock()
					log.Printf("%s -> %s", host, ips[0].String())
					return ctx, ips[0], nil
				}
			}
		} else {
			// waiting dns query for remoteResolve finish
			resolveLock.Lock()
			resolveLock.Unlock()
			// if host handled by remoteResolver, it must exist in DNSCache
			if cachedIP, found := r.getDNSCache(host); found {
				return ctx, cachedIP, nil
			}
			return r.ResolveWithSecondaryDNS(ctx, host)
		}
	} else {
		return r.ResolveWithSecondaryDNS(ctx, host)
	}
}

func (r *Resolver) lookupRemoteIP(ctx context.Context, resolver *net.Resolver, host string) ([]net.IP, error) {
	timeout := r.remoteDNSTimeout
	if timeout <= 0 {
		timeout = defaultRemoteDNSLookupTimeout
	}
	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return resolver.LookupIP(lookupCtx, "ip4", host)
}

func (r *Resolver) RemoteUDPResolver() (*net.Resolver, error) {
	if r.remoteUDPResolver != nil {
		return r.remoteUDPResolver, nil
	} else {
		return nil, errors.New("remote UDP resolver is nil")
	}
}

func (r *Resolver) RemoteTCPResolver() (*net.Resolver, error) {
	if r.remoteTCPResolver != nil {
		return r.remoteTCPResolver, nil
	} else {
		return nil, errors.New("remote TCP resolver is nil")
	}
}

func (r *Resolver) ResolveWithSecondaryDNS(ctx context.Context, host string) (context.Context, net.IP, error) {
	if targets, err := r.secondaryResolver.LookupIP(ctx, "ip4", host); err != nil {
		log.Printf("Resolve IPv4 addr failed using secondary DNS: %s. Try IPv6 addr", host)

		if targets, err = r.secondaryResolver.LookupIP(ctx, "ip6", host); err != nil {
			log.Printf("Resolve IPv6 addr failed using secondary DNS: %s", host)
			return ctx, nil, err
		} else {
			r.setDNSCache(host, targets[0])
			log.Printf("%s -> %s", host, targets[0].String())
			return ctx, targets[0], nil
		}
	} else {
		r.setDNSCache(host, targets[0])
		log.Printf("%s -> %s", host, targets[0].String())
		return ctx, targets[0], nil
	}
}

func (r *Resolver) Close() {
	r.closeOnce.Do(func() {
		r.tcpLock.Lock()
		if r.timer != nil {
			r.timer.Stop()
			r.timer = nil
		}
		r.tcpLock.Unlock()
	})
}

func (r *Resolver) CloseContext(context.Context) error {
	r.Close()
	return nil
}

type resolverStack interface {
	DialTCP(ctx context.Context, addr *net.TCPAddr) (net.Conn, error)
	DialUDP(ctx context.Context, addr *net.UDPAddr) (net.Conn, error)
}

func NewResolver(stack resolverStack, remoteDNSServer, secondaryDNSServer string, ttl uint64, domainResources map[string]client.DomainResource, dnsResource map[string]net.IP, useRemoteDNS bool) (*Resolver, error) {
	return NewResolverWithSecondaryDialer(stack, remoteDNSServer, secondaryDNSServer, ttl, domainResources, dnsResource, useRemoteDNS, nil)
}

func NewResolverWithSecondaryDialer(stack resolverStack, remoteDNSServer, secondaryDNSServer string, ttl uint64, domainResources map[string]client.DomainResource, dnsResource map[string]net.IP, useRemoteDNS bool, secondaryDial func(context.Context, string, string) (net.Conn, error)) (*Resolver, error) {
	//domainSuffixTree := domainsuffixtrie.NewDomainSuffixTrie[bool]()
	//for domain := range domainResource {
	//	_ = domainSuffixTree.AddDomainSuffix(domain, true)
	//}

	resolver := &Resolver{
		remoteUDPResolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				return stack.DialUDP(ctx, &net.UDPAddr{
					IP:   net.ParseIP(remoteDNSServer),
					Port: 53,
				})
			},
		},
		remoteTCPResolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				return stack.DialTCP(ctx, &net.TCPAddr{
					IP:   net.ParseIP(remoteDNSServer),
					Port: 53,
				})
			},
		},
		ttl:             ttl,
		domainResources: domainResources,
		dnsResource:     normalizeDNSRecords(dnsResource),
		permanentDNS:    make(map[string]net.IP),
		dnsCache:        cache.New(time.Duration(ttl)*time.Second, time.Duration(ttl)*2*time.Second),
		useRemoteDNS:    useRemoteDNS,
	}

	if secondaryDial != nil {
		resolver.secondaryResolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				if secondaryDNSServer != "" {
					address = net.JoinHostPort(secondaryDNSServer, "53")
				}
				return secondaryDial(ctx, network, address)
			},
		}
	} else if secondaryDNSServer != "" {
		resolver.secondaryResolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(secondaryDNSServer, "53"))
			},
		}
	} else {
		resolver.secondaryResolver = &net.Resolver{
			PreferGo: true,
		}
	}
	var err error
	resolver.IPPool, err = ippool.NewIPPool[client.DomainResource]("198.18.0.0/16")
	if err != nil {
		return nil, err
	}

	return resolver, nil
}
