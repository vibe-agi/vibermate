//go:build darwin

package desktoptrust

import (
	"context"
	"os"
	"testing"

	"github.com/vibe-agi/vibermate/internal/localca"
)

func TestProductionManagerReadsTheRealMacOSTrustStore(t *testing.T) {
	owner, cancel := context.WithCancel(context.Background())
	defer cancel()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	authority, err := localca.Open(
		context.Background(),
		localca.DefaultOptions(directory, owner),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authority.Shutdown(context.Background()) })
	manager, err := NewOptionalProduction(ProductionOptions{
		OwnerContext: owner,
		Root: testRootProvider{
			identity: authority.Identity(), certificate: authority.Certificate(),
		},
		Clock: localca.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if manager == nil {
		t.Fatal("macOS production trust manager is unavailable")
	}
	defer manager.Shutdown(context.Background())
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.RootValid || !status.Available ||
		status.CertificatePresent != "absent" ||
		status.TrustDecision != "untrusted" ||
		status.EvidenceRevision != "macos-security-v2" {
		t.Fatalf("unexpected real trust status: %+v", status)
	}
}
