package main

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// One subnet per address family keeps allocation predictable and supports existing
// IPv4 data unchanged. IPv6-only and dual-stack services use the same API fields.
func parseAddresses(value string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	seen := map[bool]bool{}
	for _, part := range strings.Split(value, ",") {
		p, err := netip.ParsePrefix(strings.TrimSpace(part))
		if err != nil || p.Addr().Is4In6() || p.Addr().IsUnspecified() || p.Addr().IsMulticast() {
			return nil, errors.New("address must contain IPv4 and/or IPv6 CIDRs, e.g. 10.8.0.1/24, fd00:8::1/64")
		}
		if seen[p.Addr().Is4()] {
			return nil, errors.New("use at most one subnet per address family")
		}
		seen[p.Addr().Is4()] = true
		out = append(out, p)
	}
	return out, nil
}

func joinPrefixes(prefixes []netip.Prefix) string {
	parts := make([]string, len(prefixes))
	for i, p := range prefixes {
		parts[i] = p.String()
	}
	return strings.Join(parts, ", ")
}

func allocateAddress(prefix netip.Prefix, used map[netip.Addr]bool) (netip.Addr, error) {
	// Reserve the subnet address for both families; IPv4 also reserves broadcast
	// except for point-to-point /31 and host /32 prefixes.
	start := prefix.Masked().Addr()
	if !start.Is4() || prefix.Bits() < 31 {
		start = start.Next()
	}
	for a := start; a.IsValid() && prefix.Contains(a); a = a.Next() {
		if a.Is4() && prefix.Bits() < 31 && !prefix.Contains(a.Next()) {
			break
		}
		if !used[a] {
			return a, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("no free addresses in service subnet %s", prefix)
}
