// Package ipallowlist decides which client addresses may connect to a Runtime
// Server and keeps the Owner's list of allowed networks.
//
// An empty list allows every address. Loopback is always allowed so the
// machine that runs the Server can never lock itself out. Only the address of
// the TCP peer is judged; forwarded headers are never trusted.
package ipallowlist

import (
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"strings"
)

// MaxRanges bounds the list an Owner keeps and the work done per connection.
const MaxRanges = 256

// ErrInvalidRange is wrapped by every EntryError.
var ErrInvalidRange = errors.New("IP allowlist entry is invalid")

// ErrTooManyRanges rejects a list longer than MaxRanges.
var ErrTooManyRanges = errors.New("IP allowlist has too many entries")

// Reasons an entry is rejected. The UI explains each one next to the entry.
const (
	ReasonSyntax      = "syntax"
	ReasonHostBits    = "host_bits"
	ReasonZone        = "zone"
	ReasonIPv4Mapped  = "ipv4_mapped"
	ReasonEmptyEntry  = "empty"
	ReasonEntryLength = "length"
)

const maxEntryBytes = 64

// EntryError names the rejected entry. Suggestion, when present, is the
// canonical network the Owner probably meant.
type EntryError struct {
	Index      int
	Reason     string
	Suggestion string
}

func (err *EntryError) Error() string {
	return fmt.Sprintf("IP allowlist entry %d is invalid (%s)", err.Index+1, err.Reason)
}

func (err *EntryError) Unwrap() error { return ErrInvalidRange }

// List is an immutable, validated set of allowed networks in the Owner's
// order, without duplicates.
type List struct {
	prefixes []netip.Prefix
}

// Parse accepts CIDR networks ("203.0.113.0/24", "2001:db8::/32") and single
// addresses ("203.0.113.7"). A network with host bits set is rejected rather
// than silently widened or narrowed, so the saved list means what it shows.
func Parse(entries []string) (List, error) {
	if len(entries) > MaxRanges {
		return List{}, ErrTooManyRanges
	}
	prefixes := make([]netip.Prefix, 0, len(entries))
	seen := make(map[netip.Prefix]struct{}, len(entries))
	for index, raw := range entries {
		prefix, err := parseEntry(raw)
		if err != nil {
			err.Index = index
			return List{}, err
		}
		if _, duplicate := seen[prefix]; duplicate {
			continue
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	return List{prefixes: prefixes}, nil
}

func parseEntry(raw string) (netip.Prefix, *EntryError) {
	entry := strings.TrimSpace(raw)
	switch {
	case entry == "":
		return netip.Prefix{}, &EntryError{Reason: ReasonEmptyEntry}
	case len(entry) > maxEntryBytes:
		return netip.Prefix{}, &EntryError{Reason: ReasonEntryLength}
	case strings.Contains(entry, "%"):
		return netip.Prefix{}, &EntryError{Reason: ReasonZone}
	}
	var prefix netip.Prefix
	if strings.Contains(entry, "/") {
		parsed, err := netip.ParsePrefix(entry)
		if err != nil {
			return netip.Prefix{}, &EntryError{Reason: ReasonSyntax}
		}
		prefix = parsed
	} else {
		address, err := netip.ParseAddr(entry)
		if err != nil {
			return netip.Prefix{}, &EntryError{Reason: ReasonSyntax}
		}
		prefix = netip.PrefixFrom(address, address.BitLen())
	}
	if prefix.Addr().Is4In6() {
		// A client never appears in this form (Allows unmaps it first), so the
		// entry could match nothing. Ask for the IPv4 spelling instead.
		return netip.Prefix{}, &EntryError{Reason: ReasonIPv4Mapped}
	}
	if masked := prefix.Masked(); masked != prefix {
		return netip.Prefix{}, &EntryError{
			Reason: ReasonHostBits, Suggestion: format(masked),
		}
	}
	return prefix, nil
}

// Entries returns the canonical spelling of each network. A single address is
// shown without a prefix length.
func (list List) Entries() []string {
	entries := make([]string, len(list.prefixes))
	for index, prefix := range list.prefixes {
		entries[index] = format(prefix)
	}
	return entries
}

func format(prefix netip.Prefix) string {
	if prefix.IsSingleIP() {
		return prefix.Addr().String()
	}
	return prefix.String()
}

// HasCatchAll reports whether the union covers every address of either family.
// Trusting every address as a load balancer would let any client name its own address.
func (list List) HasCatchAll() bool {
	for _, bits := range []int{32, 128} {
		covered := new(big.Int)
		for _, prefix := range list.prefixes {
			if prefix.Addr().BitLen() != bits {
				continue
			}
			// CIDRs are disjoint or nested. Count each address once by skipping
			// children; Parse already removed duplicates.
			// ponytail: O(n²), bounded by 256 ranges; sort/merge if that limit grows.
			nested := false
			for _, parent := range list.prefixes {
				if parent.Bits() < prefix.Bits() && parent.Contains(prefix.Addr()) {
					nested = true
					break
				}
			}
			if !nested {
				covered.Add(covered, new(big.Int).Lsh(big.NewInt(1), uint(bits-prefix.Bits())))
			}
		}
		if covered.BitLen() > bits {
			return true
		}
	}
	return false
}

// Len is the number of distinct networks.
func (list List) Len() int { return len(list.prefixes) }

// Contains reports whether address is inside one of the networks. Unlike
// Allows it has no loopback or empty-list exception, so it suits lists such
// as trusted load balancers.
func (list List) Contains(address netip.Addr) bool {
	address = address.Unmap().WithZone("")
	if !address.IsValid() {
		return false
	}
	for _, prefix := range list.prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// Allows reports whether a client at address may connect.
func (list List) Allows(address netip.Addr) bool {
	address = address.Unmap().WithZone("")
	if !address.IsValid() {
		return false
	}
	return address.IsLoopback() || len(list.prefixes) == 0 || list.Contains(address)
}
