package serveridentity

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestManagedCertificateStageApplyAndRestart(t *testing.T) {
	t.Parallel()
	ctx, directory := context.Background(), t.TempDir()
	manager, err := openManagerForTest(t, ctx, directory, rand.Reader, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	old := manager.Current()
	pending, err := manager.Stage(ctx, []string{"192.168.1.20", "runtime.example.test"}, old.Fingerprint(), "")
	if err != nil {
		t.Fatal(err)
	}
	if manager.Current().Fingerprint() != old.Fingerprint() {
		t.Fatal("staging changed the active identity")
	}
	reopened, err := openManagerForTest(t, ctx, directory, rand.Reader, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	active, staged := reopened.Snapshot()
	if active.Fingerprint() != old.Fingerprint() || staged.Fingerprint() != pending.Fingerprint() {
		t.Fatal("pending identity did not persist")
	}
	applied, err := reopened.Apply(ctx, old.Fingerprint(), pending.Fingerprint(), "localhost")
	if err != nil || applied.Fingerprint() != pending.Fingerprint() {
		t.Fatalf("apply failed: %v", err)
	}
	if _, err := reopened.Apply(ctx, old.Fingerprint(), pending.Fingerprint(), "localhost"); err != nil {
		t.Fatalf("apply retry: %v", err)
	}
	// Bootstrap defaults, including differing explicit seeds, never reset a
	// configuration already selected in the UI.
	restarted, err := openManagerForTest(t, ctx, directory, rand.Reader, time.Now, "another.example.test")
	if err != nil {
		t.Fatal(err)
	}
	active, staged = restarted.Snapshot()
	if active.Fingerprint() != pending.Fingerprint() || staged.Valid() {
		t.Fatal("applied state was not recovered")
	}
	certificate, _ := restarted.GetCertificate(nil)
	if err := certificate.Leaf.VerifyHostname("192.168.1.20"); err != nil {
		t.Fatal(err)
	}
	backup, err := readStoredIdentity(filepath.Join(directory, identityName+".previous"), time.Now())
	if err != nil || backup.identity.Fingerprint() != old.Fingerprint() {
		t.Fatalf("previous identity not preserved: %v", err)
	}
	for _, name := range []string{identityName, pendingIdentityName, identityName + ".previous"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("identity permissions: %s", name)
		}
	}
}

func TestManagedCertificateRejectsStaleAndUnsafeChanges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	manager, err := openManagerForTest(t, ctx, t.TempDir(), rand.Reader, time.Now, "current.example.test")
	if err != nil {
		t.Fatal(err)
	}
	old := manager.Current()
	pending, err := manager.Stage(ctx, []string{"192.168.1.20"}, old.Fingerprint(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Stage(ctx, []string{"192.168.1.21"}, old.Fingerprint(), ""); !errors.Is(err, ErrCertificateConflict) {
		t.Fatal("stale tab overwrote pending certificate")
	}
	if _, err := manager.Apply(ctx, old.Fingerprint(), pending.Fingerprint(), "current.example.test"); !errors.Is(err, ErrAccessHostMissing) {
		t.Fatal("allowed removal of current access address")
	}
	if _, err := manager.Apply(ctx, old.Fingerprint(), old.Fingerprint()+"bad", "localhost"); !errors.Is(err, ErrCertificateConflict) {
		t.Fatal("applied an unrecognized candidate")
	}
	if _, err := manager.Stage(ctx, []string{"https://192.168.1.20"}, old.Fingerprint(), pending.Fingerprint()); !errors.Is(err, ErrInvalidHosts) {
		t.Fatal("accepted invalid host")
	}
	if manager.Current().Fingerprint() != old.Fingerprint() {
		t.Fatal("rejected change replaced current identity")
	}
}

func TestManagedCertificatePersistenceFailureKeepsActiveIdentity(t *testing.T) {
	t.Parallel()
	ctx, directory := context.Background(), t.TempDir()
	manager, err := openManagerForTest(t, ctx, directory, rand.Reader, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	old := manager.Current()
	pending, err := manager.Stage(ctx, []string{"192.168.1.20"}, old.Fingerprint(), "")
	if err != nil {
		t.Fatal(err)
	}
	// A directory cannot be atomically replaced by the identity file. This
	// exercises a write failure even when tests run as root.
	if err := os.Mkdir(filepath.Join(directory, identityName+".previous"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(ctx, old.Fingerprint(), pending.Fingerprint(), "localhost"); err == nil {
		t.Fatal("expected persistence failure")
	}
	stored, err := readStoredIdentity(filepath.Join(directory, identityName), time.Now())
	if err != nil || stored.identity.Fingerprint() != old.Fingerprint() || manager.Current().Fingerprint() != old.Fingerprint() {
		t.Fatal("failed commit changed active identity")
	}
}

func TestManagedCertificateConcurrentHandshakes(t *testing.T) {
	manager, err := openManagerForTest(t, context.Background(), t.TempDir(), rand.Reader, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 1000 {
				certificate, err := manager.GetCertificate(nil)
				if err != nil || certificate.Leaf == nil {
					t.Error("incomplete TLS identity")
				}
				manager.Snapshot()
			}
		}()
	}
	for range 8 {
		old := manager.Current()
		pending, err := manager.Stage(context.Background(), []string{"192.168.1.20"}, old.Fingerprint(), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Apply(context.Background(), old.Fingerprint(), pending.Fingerprint(), "localhost"); err != nil {
			t.Fatal(err)
		}
	}
	readers.Wait()
}
