package certidentity

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
)

// NormalizeServerHosts validates explicitly configured client-facing addresses.
// Loopback identities remain present; bind addresses, URLs and wildcards do not
// identify a Runtime Server. No Docker or host interface discovery is performed.
func NormalizeServerHosts(hosts []string) ([]string, error) {
	if len(hosts) > 32 {
		return nil, errors.New("Runtime Server TLS accepts at most 32 additional hosts")
	}
	names := []string{"localhost", "127.0.0.1", "::1"}
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if address, err := netip.ParseAddr(host); err == nil {
			if address.Zone() != "" || address.IsUnspecified() || address.IsMulticast() {
				return nil, errors.New("Runtime Server TLS host must be a concrete unicast IP")
			}
			host = address.Unmap().String()
		} else if _, err := NewDNSName(host); err != nil {
			return nil, errors.New("Runtime Server TLS hosts must be IPs or DNS names without URLs or ports")
		}
		names = append(names, host)
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}
