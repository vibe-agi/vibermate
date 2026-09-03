package hostsecret

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func TestServerFileFactoryKeepsSecretsInsideTheServerDataBoundary(t *testing.T) {
	t.Parallel()

	dataDirectory := filepath.Join(t.TempDir(), "server-data")
	factory, err := NewServerFileFactory(dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	store, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reference, err := secretstore.ParseReference("secret://provider/account")
	if err != nil {
		t.Fatal(err)
	}
	value, err := secretstore.NewValue([]byte("opaque-provider-secret"))
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	if _, err := store.Replace(context.Background(), secretstore.ReplaceCommand{
		Reference: reference,
		Value:     value,
	}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dataDirectory, "server-secrets", "store.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("Server secret file mode = %o", info.Mode().Perm())
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte(`"schema":"vibermate.server-secret-store/v1"`)) {
		t.Fatalf("Server secret file uses the wrong schema: %s", payload)
	}

	reopened, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := reopened.Read(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Destroy()
	got, err := stored.CopyBytes()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(got)
	if !bytes.Equal(got, []byte("opaque-provider-secret")) {
		t.Fatal("reopened Server secret changed")
	}
}

func TestServerFileFactoryRejectsAnAliasedDataBoundary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realDirectory, alias); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	factory, err := NewServerFileFactory(alias)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := factory.Open(context.Background()); err == nil {
		t.Fatal("aliased Server data boundary was accepted")
	}
}
