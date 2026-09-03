package localca

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPendingRootResetIsBoundToTheCurrentIdentityAndIsIdempotent(t *testing.T) {
	dataDirectory := t.TempDir()
	if err := os.Chmod(dataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	rootDirectory := filepath.Join(dataDirectory, "local-ca")
	if err := os.Mkdir(rootDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	authority, err := Open(context.Background(), DefaultOptions(rootDirectory, owner))
	if err != nil {
		t.Fatal(err)
	}
	oldIdentity := authority.Identity()
	if err := RequestRootReset(dataDirectory, oldIdentity); err != nil {
		t.Fatal(err)
	}
	if err := RequestRootReset(dataDirectory, oldIdentity); err != nil {
		t.Fatal(err)
	}
	if err := authority.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPendingRootReset(context.Background(), dataDirectory); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPendingRootReset(context.Background(), dataDirectory); err != nil {
		t.Fatal(err)
	}
	newAuthority, err := Open(context.Background(), DefaultOptions(rootDirectory, owner))
	if err != nil {
		t.Fatal(err)
	}
	defer newAuthority.Shutdown(context.Background())
	if newAuthority.Identity().Revision() != oldIdentity.Revision()+1 ||
		newAuthority.Identity().Digest() == oldIdentity.Digest() {
		t.Fatalf("new Root identity did not advance: old=%+v new=%+v", oldIdentity, newAuthority.Identity())
	}
	if _, err := os.Stat(rootResetRequestPath(dataDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed reset request remains: %v", err)
	}
}

func TestPendingRootResetRecoversAInterruptedFileRemoval(t *testing.T) {
	dataDirectory := t.TempDir()
	if err := os.Chmod(dataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	rootDirectory := filepath.Join(dataDirectory, "local-ca")
	if err := os.Mkdir(rootDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	authority, err := Open(context.Background(), DefaultOptions(rootDirectory, owner))
	if err != nil {
		t.Fatal(err)
	}
	identity := authority.Identity()
	if err := RequestRootReset(dataDirectory, identity); err != nil {
		t.Fatal(err)
	}
	if err := authority.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(rootDirectory, rootKeyFile)); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPendingRootReset(context.Background(), dataDirectory); err != nil {
		t.Fatalf("interrupted reset did not recover: %v", err)
	}
	newAuthority, err := Open(context.Background(), DefaultOptions(rootDirectory, owner))
	if err != nil {
		t.Fatal(err)
	}
	defer newAuthority.Shutdown(context.Background())
	if newAuthority.Identity().Revision() != identity.Revision()+1 {
		t.Fatalf("new revision = %d", newAuthority.Identity().Revision())
	}
}

func TestPendingRootResetRecoversAPartiallyCreatedReplacement(t *testing.T) {
	dataDirectory := t.TempDir()
	if err := os.Chmod(dataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	rootDirectory := filepath.Join(dataDirectory, "local-ca")
	if err := os.Mkdir(rootDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	authority, err := Open(context.Background(), DefaultOptions(rootDirectory, owner))
	if err != nil {
		t.Fatal(err)
	}
	identity := authority.Identity()
	if err := RequestRootReset(dataDirectory, identity); err != nil {
		t.Fatal(err)
	}
	if err := authority.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Model a process death after a replacement certificate was written but
	// before its key/manifest set became complete. The pending reset owns these
	// exact paths and must be able to discard the partial next generation.
	otherDirectory := t.TempDir()
	if err := os.Chmod(otherDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	other, err := Open(context.Background(), DefaultOptions(otherDirectory, owner))
	if err != nil {
		t.Fatal(err)
	}
	replacementCertificate := other.Certificate().CertificatePEM()
	if err := other.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{rootKeyFile, rootCertFile, rootManifestFile} {
		if err := os.Remove(filepath.Join(rootDirectory, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(
		filepath.Join(rootDirectory, rootCertFile),
		replacementCertificate,
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if err := ApplyPendingRootReset(context.Background(), dataDirectory); err != nil {
		t.Fatalf("partial replacement did not recover: %v", err)
	}
	newAuthority, err := Open(context.Background(), DefaultOptions(rootDirectory, owner))
	if err != nil {
		t.Fatal(err)
	}
	defer newAuthority.Shutdown(context.Background())
	if newAuthority.Identity().Revision() != identity.Revision()+1 ||
		newAuthority.Identity().Digest() == identity.Digest() {
		t.Fatalf("replacement identity did not advance: %+v", newAuthority.Identity())
	}
}

func TestPendingRootResetKeepsACompleteReplacementAfterMarkerRemovalCrash(t *testing.T) {
	dataDirectory := t.TempDir()
	if err := os.Chmod(dataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	rootDirectory := filepath.Join(dataDirectory, "local-ca")
	if err := os.Mkdir(rootDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	authority, err := Open(context.Background(), DefaultOptions(rootDirectory, owner))
	if err != nil {
		t.Fatal(err)
	}
	oldIdentity := authority.Identity()
	if err := RequestRootReset(dataDirectory, oldIdentity); err != nil {
		t.Fatal(err)
	}
	if err := authority.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPendingRootReset(context.Background(), dataDirectory); err != nil {
		t.Fatal(err)
	}
	replacement, err := Open(context.Background(), DefaultOptions(rootDirectory, owner))
	if err != nil {
		t.Fatal(err)
	}
	newIdentity := replacement.Identity()
	if err := replacement.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Model a crash where the complete replacement reached disk but removing
	// the already-consumed marker did not. Startup must retain the new Root and
	// finish only the marker cleanup.
	marker, err := json.Marshal(rootResetRequest{
		Schema:   rootResetRequestSchema,
		Revision: oldIdentity.Revision(),
		Digest:   oldIdentity.Digest().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootResetRequestPath(dataDirectory), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPendingRootReset(context.Background(), dataDirectory); err != nil {
		t.Fatalf("complete replacement was not recovered: %v", err)
	}
	reopened, err := Open(context.Background(), DefaultOptions(rootDirectory, owner))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Shutdown(context.Background())
	if reopened.Identity().Digest() != newIdentity.Digest() ||
		reopened.Identity().Revision() != newIdentity.Revision() {
		t.Fatalf("complete replacement changed during recovery: %+v", reopened.Identity())
	}
}

func TestConcurrentRootResetRequestsPublishOnlyCompleteIntent(t *testing.T) {
	dataDirectory := t.TempDir()
	if err := os.Chmod(dataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	rootDirectory := filepath.Join(dataDirectory, "local-ca")
	if err := os.Mkdir(rootDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	authority, err := Open(context.Background(), DefaultOptions(rootDirectory, owner))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Shutdown(context.Background())

	const callers = 64
	start := make(chan struct{})
	errorsByCaller := make([]error, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := range errorsByCaller {
		go func() {
			defer wait.Done()
			<-start
			errorsByCaller[index] = RequestRootReset(dataDirectory, authority.Identity())
		}()
	}
	close(start)
	wait.Wait()
	for index, requestErr := range errorsByCaller {
		if requestErr != nil {
			t.Fatalf("concurrent request %d failed: %v", index, requestErr)
		}
	}
	if _, err := readResetRequest(rootResetRequestPath(dataDirectory)); err != nil {
		t.Fatalf("published reset intent is incomplete: %v", err)
	}
}
