// Package clientip determines the client address of a request. Forwarding
// headers are honoured only when they arrive through configured trusted
// proxies, and are read right to left so a client cannot inject its own
// "original" address in front of the real one.
package clientip

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// ErrInvalidCIDR reports a malformed trusted-proxy prefix.
var ErrInvalidCIDR = errors.New("clientip: invalid CIDR")

// Resolver resolves client addresses. The zero value trusts no proxies.
type Resolver struct {
	trusted []netip.Prefix
}

// New returns a resolver that trusts forwarding headers from cidrs.
func New(cidrs []string) (Resolver, error) {
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			return Resolver{}, fmt.Errorf("%w: %q", ErrInvalidCIDR, c)
		}
		prefixes = append(prefixes, p.Masked())
	}
	return Resolver{trusted: prefixes}, nil
}

// Resolve returns the client address for a request whose TCP peer is
// remoteAddr ("ip:port") and which carried the given X-Forwarded-For header
// values. It walks from the peer leftwards through the forwarded hops while
// each hop is a trusted proxy and returns the first untrusted address. A
// malformed hop stops the walk at the proxy that appended it. The result is
// invalid only if remoteAddr itself cannot be parsed.
func (r Resolver) Resolve(remoteAddr string, xff []string) netip.Addr {
	peer, ok := parseHop(remoteAddr)
	if !ok {
		return netip.Addr{}
	}
	addr := peer
	hops := forwardedHops(xff)
	for i := len(hops) - 1; i >= 0 && r.isTrusted(addr); i-- {
		next, ok := parseHop(hops[i])
		if !ok {
			break
		}
		addr = next
	}
	return addr
}

func (r Resolver) isTrusted(a netip.Addr) bool {
	return slices.ContainsFunc(r.trusted, func(p netip.Prefix) bool { return p.Contains(a) })
}

// forwardedHops flattens header values into hops, oldest first, dropping
// empty entries.
func forwardedHops(values []string) []string {
	var hops []string
	for _, v := range values {
		for h := range strings.SplitSeq(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hops = append(hops, h)
			}
		}
	}
	return hops
}

// parseHop accepts "ip", "ip:port" and "[ipv6]:port", unmapping IPv4-in-IPv6.
func parseHop(s string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	return netip.Addr{}, false
}
