package egressnetwork

import (
	"context"
	"errors"
	"net/netip"
)

var ErrPrivateTarget = errors.New("private network target requires explicit Owner authorization")

// TargetAccess is request authority, not persisted network configuration.
// Server ingress starts public-only. A frozen Owner-defined endpoint or
// scoped connection rule may authorize a private target for that connection.
type TargetAccess uint8

const (
	TargetAccessPublicOnly TargetAccess = iota + 1
	TargetAccessOwnerConfigured
)

type targetAccessKey struct{}

func WithTargetAccess(ctx context.Context, access TargetAccess) context.Context {
	return context.WithValue(ctx, targetAccessKey{}, access)
}

func publicTargetsOnly(ctx context.Context) bool {
	access, set := ctx.Value(targetAccessKey{}).(TargetAccess)
	return set && access != TargetAccessOwnerConfigured
}

// IsGlobalUnicast includes private and other non-public unicast ranges.
// Block special-use targets as well as loopback, link-local and multicast.
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func publicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.Zone() != "" {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	if address.Is6() {
		bytes := address.As16()
		// Well-known NAT64 and 6to4 must not smuggle a private IPv4 target.
		if netip.MustParsePrefix("64:ff9b::/96").Contains(address) {
			return publicAddress(netip.AddrFrom4([4]byte(bytes[12:16])))
		}
		if netip.MustParsePrefix("2002::/16").Contains(address) {
			return publicAddress(netip.AddrFrom4([4]byte(bytes[2:6])))
		}
	}
	return true
}
