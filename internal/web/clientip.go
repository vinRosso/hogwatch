package web

import (
	"net/http"
	"net/netip"
	"strings"
)

// parsePrefixes turns the trusted_proxies CIDRs into prefixes.
func parsePrefixes(cidrs []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// clientIP is the address rate limits are keyed on. X-Forwarded-For counts
// only when the connection comes from a trusted proxy, and then the client is
// the rightmost hop that is not itself trusted. A garbled header falls back
// to the connection address.
func clientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	var remote netip.Addr
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		remote = ap.Addr().WithZone("").Unmap()
	} else if a, err := netip.ParseAddr(r.RemoteAddr); err == nil {
		remote = a.WithZone("").Unmap()
	}
	if !remote.IsValid() || !isTrusted(remote, trusted) {
		return remote
	}
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(h, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return remote
		}
		a = a.WithZone("").Unmap()
		if !isTrusted(a, trusted) {
			return a
		}
	}
	return remote
}
