//go:build vibermate_native_secrets && darwin

package hostsecret_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

// This test writes to the real login keychain, so it is opt-in. It files its
// item under a service name that is obviously a test and deletes it before it
// returns; a keychain is the person's, not the suite's.
const keychainEnvironment = "VIBERMATE_KEYCHAIN_TEST"

func keychainStore(t *testing.T) *hostsecret.KeychainStore {
	t.Helper()

	if os.Getenv(keychainEnvironment) == "" {
		t.Skipf(
			"set %s=1 to run against the login keychain",
			keychainEnvironment,
		)
	}
	factory, err := hostsecret.NewKeychainFactory("io.vibermate.test")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store, ok := opened.(*hostsecret.KeychainStore)
	if !ok {
		t.Fatalf("factory returned %T", opened)
	}
	return store
}

func testReference(t *testing.T) secretstore.Reference {
	t.Helper()

	reference, err := secretstore.ParseReference("secret://provider/keychain-test")
	if err != nil {
		t.Fatal(err)
	}
	return reference
}

// The bytes go in and come back exactly, the revision advances, and a stale
// write is refused rather than overwriting what somebody else stored.
func TestTheKeychainStoresReadsAndGuardsARevision(t *testing.T) {
	store := keychainStore(t)
	reference := testReference(t)
	_ = store.Delete(context.Background(), reference)
	t.Cleanup(func() {
		_ = store.Delete(context.Background(), reference)
	})

	metadata, err := store.Inspect(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.State != secretstore.StateMissing {
		t.Fatalf("an absent secret reported %+v", metadata)
	}

	first, err := secretstore.NewValue([]byte("first-secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	written, err := store.Replace(
		context.Background(),
		secretstore.ReplaceCommand{
			Reference:        reference,
			ExpectedRevision: 0,
			Value:            first,
		},
	)
	if err != nil {
		t.Fatalf("write the first secret: %v", err)
	}
	if written.State != secretstore.StateConfigured || written.Revision != 1 {
		t.Fatalf("first write = %+v", written)
	}

	read, err := store.Read(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := read.CopyBytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes) != "first-secret-value" {
		t.Fatalf("the keychain returned %q", bytes)
	}

	// A write prepared against the revision that was just replaced is refused.
	stale, err := secretstore.NewValue([]byte("stale-secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Replace(
		context.Background(),
		secretstore.ReplaceCommand{
			Reference:        reference,
			ExpectedRevision: 0,
			Value:            stale,
		},
	); !errors.Is(err, secretstore.ErrRevisionConflict) {
		t.Fatalf("stale write error = %v", err)
	}
	after, err := store.Read(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := after.CopyBytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != "first-secret-value" {
		t.Fatalf("a refused write changed the secret to %q", unchanged)
	}

	second, err := secretstore.NewValue([]byte("second-secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := store.Replace(
		context.Background(),
		secretstore.ReplaceCommand{
			Reference:        reference,
			ExpectedRevision: 1,
			Value:            second,
		},
	)
	if err != nil {
		t.Fatalf("replace under the current revision: %v", err)
	}
	if replaced.Revision != 2 {
		t.Fatalf("replacement revision = %d", replaced.Revision)
	}
}

// Reading a secret that is not there is not an error state of the store, and
// inspecting one must not be reported as configured.
func TestAnAbsentKeychainSecretIsNotFound(t *testing.T) {
	store := keychainStore(t)
	reference, err := secretstore.ParseReference("secret://provider/absent-test")
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Delete(context.Background(), reference)
	if _, err := store.Read(
		context.Background(),
		reference,
	); !errors.Is(err, secretstore.ErrNotFound) {
		t.Fatalf("absent read error = %v", err)
	}
}
