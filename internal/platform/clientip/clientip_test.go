package clientip_test

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"testing/quick"

	"github.com/yahya31415/tuorps/internal/platform/clientip"
)

func resolver(t *testing.T, cidrs ...string) clientip.Resolver {
	t.Helper()
	r, err := clientip.New(cidrs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestResolve(t *testing.T) {
	r := resolver(t, "10.0.0.0/8", "fd00::/8")
	cases := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"direct client, no header", "203.0.113.7:51000", nil, "203.0.113.7"},
		{"untrusted peer: header ignored (spoofing)", "203.0.113.7:51000", []string{"1.2.3.4"}, "203.0.113.7"},
		{"trusted proxy forwards client", "10.0.0.2:443", []string{"198.51.100.9"}, "198.51.100.9"},
		{"rightmost untrusted wins over forged left entries", "10.0.0.2:443", []string{"6.6.6.6, 198.51.100.9"}, "198.51.100.9"},
		{"chain of trusted proxies", "10.0.0.2:443", []string{"198.51.100.9, 10.1.1.1", "10.2.2.2"}, "198.51.100.9"},
		{"all hops trusted: leftmost", "10.0.0.2:443", []string{"10.9.9.9, 10.8.8.8"}, "10.9.9.9"},
		{"garbage hop stops the walk at the proxy that added it", "10.0.0.2:443", []string{"198.51.100.9, not-an-ip"}, "10.0.0.2"},
		{"empty entries skipped", "10.0.0.2:443", []string{" , 198.51.100.9 ,"}, "198.51.100.9"},
		{"trusted peer without header", "10.0.0.2:443", nil, "10.0.0.2"},
		{"ipv6 peer", "[2001:db8::1]:443", nil, "2001:db8::1"},
		{"ipv6 trusted proxy", "[fd00::5]:443", []string{"2001:db8::7"}, "2001:db8::7"},
		{"ipv4-mapped peer is unmapped", "[::ffff:203.0.113.7]:1", nil, "203.0.113.7"},
		{"bracketed v6 hop with port", "10.0.0.2:443", []string{"[2001:db8::9]:8080"}, "2001:db8::9"},
		{"v4 hop with port", "10.0.0.2:443", []string{"198.51.100.9:8080"}, "198.51.100.9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Resolve(tc.remote, tc.xff)
			if got.String() != tc.want {
				t.Fatalf("Resolve = %v, want %s", got, tc.want)
			}
		})
	}
}

func TestResolveWithoutTrustedProxiesIgnoresHeaders(t *testing.T) {
	r := resolver(t)
	if got := r.Resolve("10.0.0.2:443", []string{"1.2.3.4"}); got.String() != "10.0.0.2" {
		t.Fatalf("Resolve = %v", got)
	}
}

func TestResolveUnparseableRemoteAddr(t *testing.T) {
	r := resolver(t, "10.0.0.0/8")
	if got := r.Resolve("pipe", []string{"1.2.3.4"}); got.IsValid() {
		t.Fatalf("Resolve = %v, want invalid address", got)
	}
}

func TestNewRejectsBadCIDRs(t *testing.T) {
	for _, bad := range []string{"10.0.0.0", "10.0.0.0/33", "nope", ""} {
		if _, err := clientip.New([]string{bad}); !errors.Is(err, clientip.ErrInvalidCIDR) {
			t.Errorf("New(%q) err = %v", bad, err)
		}
	}
}

// Property: a client that talks to us directly (not via a trusted proxy) can
// never choose its address through X-Forwarded-For.
func TestPropertyUntrustedPeersCannotSpoof(t *testing.T) {
	r := resolver(t, "10.0.0.0/8")
	prop := func(a, b, c, d byte, port uint16, forged []string) bool {
		if a == 10 {
			a = 11
		}
		peer := netip.AddrFrom4([4]byte{a, b, c, d})
		remote := netip.AddrPortFrom(peer, port).String()
		return r.Resolve(remote, forged) == peer
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Error(err)
	}
}

// The resolved address is always the peer or one of the forwarded hops.
func FuzzResolve(f *testing.F) {
	f.Add("10.0.0.2:443", "198.51.100.9, 10.1.1.1")
	f.Add("[fd00::1]:1", "[2001:db8::9]:80")
	f.Add("1.2.3.4:5", "")
	r, err := clientip.New([]string{"10.0.0.0/8", "fd00::/8"})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, remote, xff string) {
		got := r.Resolve(remote, []string{xff})
		if !got.IsValid() {
			return
		}
		sameAddr := func(h string) bool {
			h = strings.TrimSpace(h)
			if a, err := netip.ParseAddr(h); err == nil {
				return a.Unmap() == got
			}
			ap, err := netip.ParseAddrPort(h)
			return err == nil && ap.Addr().Unmap() == got
		}
		if sameAddr(remote) {
			return
		}
		if !slices.ContainsFunc(strings.Split(xff, ","), sameAddr) {
			t.Fatalf("Resolve(%q, %q) = %v, which is neither the peer nor a hop", remote, xff, got)
		}
	})
}
