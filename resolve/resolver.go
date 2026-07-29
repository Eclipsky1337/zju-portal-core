package resolve

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/Eclipsky1337/zju-portal-core/log"
	"github.com/patrickmn/go-cache"
	"golang.org/x/sync/singleflight"
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

	timer  *time.Timer
	useTCP bool
	// check to use tcp resolver or udp resolver
	tcpLock      sync.RWMutex
	resolveGroup singleflight.Group

	closeOnce sync.Once
}

type contextKey string

var (
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
	domainResource, domainResourceFound := r.matchDomainResource(host)
	if domainResourceFound {
		ctx = context.WithValue(ctx, ContextKeyDomainResource, domainResource)
	}

	if cachedIP, found := r.getDNSCache(host); found {
		log.Printf("%s -> %s", host, cachedIP.String())
		return ctx, cachedIP, nil
	}

	if r.dnsResource != nil {
		if ip, found := r.dnsResource[host]; found {
			log.Printf("%s -> %s", host, ip.String())
			return ctx, ip, nil
		}
	}

	if r.useRemoteDNS {
		for {
			result := r.resolveGroup.DoChan(host, func() (any, error) {
				return r.resolveRemote(ctx, host)
			})
			select {
			case <-ctx.Done():
				return ctx, nil, ctx.Err()
			case resolved := <-result:
				if resolved.Shared && ctx.Err() == nil && (errors.Is(resolved.Err, context.Canceled) || errors.Is(resolved.Err, context.DeadlineExceeded)) {
					continue
				}
				if resolved.Err != nil {
					return ctx, nil, resolved.Err
				}
				address, ok := resolved.Val.(net.IP)
				if !ok || address == nil {
					return ctx, nil, errors.New("DNS resolver returned an invalid address result")
				}
				return ctx, address, nil
			}
		}
	} else {
		return r.ResolveWithSecondaryDNS(ctx, host)
	}
}

func (r *Resolver) resolveRemote(ctx context.Context, host string) (net.IP, error) {
	r.tcpLock.RLock()
	useTCP := r.useTCP
	r.tcpLock.RUnlock()
	if useTCP {
		ips, err := r.lookupRemoteIP(ctx, r.remoteTCPResolver, host)
		if err != nil {
			log.Printf("Resolve IPv4 addr failed using remote TCP DNS: %s, using secondary DNS instead", host)
			_, address, resolveErr := r.ResolveWithSecondaryDNS(ctx, host)
			return address, resolveErr
		}
		return r.cacheRemoteResult(host, ips)
	}

	ips, err := r.lookupRemoteIP(ctx, r.remoteUDPResolver, host)
	if err != nil {
		ips, err = r.lookupRemoteIP(ctx, r.remoteTCPResolver, host)
		if err != nil {
			log.Printf("Resolve IPv4 addr failed using remote UDP/TCP DNS: %s, using secondary DNS instead", host)
			_, address, resolveErr := r.ResolveWithSecondaryDNS(ctx, host)
			return address, resolveErr
		}
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
	return r.cacheRemoteResult(host, ips)
}

func (r *Resolver) cacheRemoteResult(host string, addresses []net.IP) (net.IP, error) {
	if len(addresses) == 0 {
		return nil, errors.New("remote DNS returned no IPv4 addresses")
	}
	address := addresses[0]
	r.setDNSCache(host, address)
	log.Printf("%s -> %s", host, address.String())
	return address, nil
}

func (r *Resolver) IsVPNDomain(host string) bool {
	_, found := r.matchDomainResource(normalizeDNSName(host))
	return found
}

func (r *Resolver) matchDomainResource(host string) (client.DomainResource, bool) {
	for domain, resource := range r.domainResources {
		if strings.HasSuffix(host, domain) {
			log.DebugPrintf("Domain resource found: %s", domain)
			return resource, true
		}
	}
	return client.DomainResource{}, false
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
	return resolver, nil
}
