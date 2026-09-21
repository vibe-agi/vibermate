package serveridentity

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/vibe-agi/vibermate/internal/certidentity"
)

// NormalizeHosts accepts operator-supplied client-facing addresses, never
// inferring Docker or host interfaces. Loopback names remain valid for local
// health checks. URLs, ports, wildcards and unspecified bind addresses are not
// certificate identities.
func NormalizeHosts(hosts []string) ([]string, error) {
	return certidentity.NormalizeServerHosts(hosts)
}

func identityMatchesHosts(identity Identity, names []string) bool {
	return slices.Equal(identityHosts(identity), names)
}

func identityHosts(identity Identity) []string {
	leaf := identity.certificate.Leaf
	if leaf == nil {
		return nil
	}
	actual := slices.Clone(leaf.DNSNames)
	for _, address := range leaf.IPAddresses {
		actual = append(actual, address.String())
	}
	slices.Sort(actual)
	return slices.Compact(actual)
}

func preservePreviousIdentity(path string, payload []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".server-tls-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
