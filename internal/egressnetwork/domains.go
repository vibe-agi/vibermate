package egressnetwork

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// DirectDomains is a canonical newline encoding so frozen policies remain
// comparable. Bare domains include their apex; *. selectors exclude it.
type DirectDomains string

// CanonicalHost uses the same identity rules as ordinary target routing.
func CanonicalHost(host string) (string, error) { return normalizeHost(host) }

func (rules DirectDomains) Normalize() (DirectDomains, error) {
	if len(rules) > 16384 {
		return "", fmt.Errorf("%w: direct domains are too large", ErrInvalidPolicy)
	}
	unique := map[string]struct{}{}
	for _, line := range strings.Split(string(rules), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		prefix := ""
		if strings.HasPrefix(line, "*.") {
			prefix = "*."
			line = strings.TrimPrefix(line, "*.")
		}
		host, err := normalizeHost(line)
		if _, ipErr := netip.ParseAddr(host); err != nil || ipErr == nil {
			return "", fmt.Errorf("%w: invalid direct domain", ErrInvalidPolicy)
		}
		unique[prefix+host] = struct{}{}
	}
	names := make([]string, 0, len(unique))
	for name := range unique {
		names = append(names, name)
	}
	sort.Strings(names)
	canonical := strings.Join(names, "\n")
	// IDNA expansion must not produce a value rejected on the next read.
	if len(canonical) > 16384 {
		return "", fmt.Errorf("%w: canonical direct domains are too large", ErrInvalidPolicy)
	}
	return DirectDomains(canonical), nil
}

func (rules DirectDomains) Matches(host string) bool {
	host, err := normalizeHost(host)
	if err != nil {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	for _, rule := range strings.Split(string(rules), "\n") {
		if rule == "" {
			continue
		}
		subdomains := strings.HasPrefix(rule, "*.")
		name := strings.TrimPrefix(rule, "*.")
		if (!subdomains && host == name) || strings.HasSuffix(host, "."+name) {
			return true
		}
	}
	return false
}
