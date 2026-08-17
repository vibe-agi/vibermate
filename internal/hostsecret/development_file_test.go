//go:build !vibermate_native_secrets

package hostsecret

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func TestDevelopmentFileStorePersistsCASWithoutAliasing(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "private", "store.json")
	factory, err := NewDevelopmentFileFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reference := mustDevelopmentReference(t, "secret://provider/account")
	initial, err := store.Inspect(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	if initial.State != secretstore.StateMissing || initial.Revision != 0 {
		t.Fatalf("initial metadata = %+v", initial)
	}

	input := []byte("provider-secret")
	value, err := secretstore.NewValue(input)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	input[0] = 'X'
	written, err := store.Replace(
		context.Background(),
		secretstore.ReplaceCommand{
			Reference: reference,
			Value:     value,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if written.State != secretstore.StateConfigured || written.Revision != 1 {
		t.Fatalf("written metadata = %+v", written)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("development secret file mode = %o", info.Mode().Perm())
	}

	first := readDevelopmentValue(t, store, reference)
	first[0] = 'Y'
	if got := readDevelopmentValue(t, store, reference); !bytes.Equal(
		got,
		[]byte("provider-secret"),
	) {
		t.Fatalf("stored value = %q", got)
	}
	clear(first)

	reopened, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reopenedMetadata, err := reopened.Inspect(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	if reopenedMetadata.Revision != 1 ||
		reopenedMetadata.State != secretstore.StateConfigured {
		t.Fatalf("reopened metadata = %+v", reopenedMetadata)
	}
	if got := readDevelopmentValue(t, reopened, reference); !bytes.Equal(
		got,
		[]byte("provider-secret"),
	) {
		t.Fatalf("reopened value = %q", got)
	}

	stale := mustDevelopmentValue(t, "stale-secret")
	defer stale.Destroy()
	if _, err := reopened.Replace(
		context.Background(),
		secretstore.ReplaceCommand{
			Reference:        reference,
			ExpectedRevision: 0,
			Value:            stale,
		},
	); !errors.Is(err, secretstore.ErrRevisionConflict) {
		t.Fatalf("stale Replace() error = %v", err)
	}

	replacement := mustDevelopmentValue(t, "replacement-secret")
	defer replacement.Destroy()
	replaced, err := reopened.Replace(
		context.Background(),
		secretstore.ReplaceCommand{
			Reference:        reference,
			ExpectedRevision: 1,
			Value:            replacement,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.State != secretstore.StateConfigured ||
		replaced.Revision != 2 {
		t.Fatalf("replaced metadata = %+v", replaced)
	}
	reopenedAgain, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := readDevelopmentValue(t, reopenedAgain, reference); !bytes.Equal(
		got,
		[]byte("replacement-secret"),
	) {
		t.Fatalf("twice-reopened value = %q", got)
	}
	twiceReopenedMetadata, err := reopenedAgain.Inspect(
		context.Background(),
		reference,
	)
	if err != nil {
		t.Fatal(err)
	}
	if twiceReopenedMetadata.Revision != 2 ||
		twiceReopenedMetadata.State != secretstore.StateConfigured {
		t.Fatalf("twice-reopened metadata = %+v", twiceReopenedMetadata)
	}
}

func TestDevelopmentFileStoreSerializesConcurrentCAS(t *testing.T) {
	t.Parallel()

	store := openDevelopmentStore(t)
	reference := mustDevelopmentReference(t, "secret://provider/account")
	initial := mustDevelopmentValue(t, "initial-secret")
	defer initial.Destroy()
	if _, err := store.Replace(
		context.Background(),
		secretstore.ReplaceCommand{
			Reference: reference,
			Value:     initial,
		},
	); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	values := []*secretstore.Value{
		mustDevelopmentValue(t, "writer-one-secret"),
		mustDevelopmentValue(t, "writer-two-secret"),
	}
	for _, value := range values {
		defer value.Destroy()
		value := value
		go func() {
			<-start
			_, replaceErr := store.Replace(
				context.Background(),
				secretstore.ReplaceCommand{
					Reference:        reference,
					ExpectedRevision: 1,
					Value:            value,
				},
			)
			results <- replaceErr
		}()
	}
	close(start)
	successes := 0
	conflicts := 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, secretstore.ErrRevisionConflict):
			conflicts++
		default:
			t.Fatalf("concurrent Replace() error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestDevelopmentFileStoreReadsOnlyTheExpectedRevision(t *testing.T) {
	t.Parallel()

	store := openDevelopmentStore(t)
	reference := mustDevelopmentReference(t, "secret://provider/pinned-account")
	first := mustDevelopmentValue(t, "first-secret")
	defer first.Destroy()
	metadata, err := store.Replace(
		t.Context(),
		secretstore.ReplaceCommand{Reference: reference, Value: first},
	)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := store.ReadAtRevision(
		t.Context(),
		reference,
		metadata.Revision,
	)
	if err != nil {
		t.Fatalf("read current revision: %v", err)
	}
	pinned.Destroy()

	second := mustDevelopmentValue(t, "second-secret")
	defer second.Destroy()
	rotated, err := store.Replace(
		t.Context(),
		secretstore.ReplaceCommand{
			Reference:        reference,
			ExpectedRevision: metadata.Revision,
			Value:            second,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadAtRevision(
		t.Context(),
		reference,
		metadata.Revision,
	); !errors.Is(err, secretstore.ErrRevisionConflict) {
		t.Fatalf("read stale revision error = %v", err)
	}
	current, err := store.ReadAtRevision(
		t.Context(),
		reference,
		rotated.Revision,
	)
	if err != nil {
		t.Fatalf("read rotated revision: %v", err)
	}
	defer current.Destroy()
	got, err := current.CopyBytes()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(got)
	if !bytes.Equal(got, []byte("second-secret")) {
		t.Fatalf("rotated value = %q", got)
	}
}

func TestDevelopmentFileStoreDeleteIsDurableAndMissingIsExplicit(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "private", "store.json")
	factory, err := NewDevelopmentFileFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := factory.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reference := mustDevelopmentReference(t, "secret://provider/delete-account")
	value := mustDevelopmentValue(t, "delete-secret")
	defer value.Destroy()
	if _, err := store.Replace(t.Context(), secretstore.ReplaceCommand{
		Reference: reference,
		Value:     value,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), reference); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(t.Context(), reference); !errors.Is(err, secretstore.ErrNotFound) {
		t.Fatalf("deleted secret read error = %v", err)
	}
	if err := store.Delete(t.Context(), reference); !errors.Is(err, secretstore.ErrNotFound) {
		t.Fatalf("repeated delete error = %v", err)
	}
	reopened, err := factory.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := reopened.Inspect(t.Context(), reference)
	if err != nil || metadata.State != secretstore.StateMissing || metadata.Revision != 0 {
		t.Fatalf("reopened deleted metadata=%+v err=%v", metadata, err)
	}
}

func TestDevelopmentFileStoreRejectsInvalidOrAliasedFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, fixture := range []struct {
		name    string
		content string
	}{
		{
			name:    "unknown schema",
			content: `{"schema":"unknown","items":{}}`,
		},
		{
			name:    "noncanonical",
			content: `{"items":{},"schema":"vibermate.development-secret-store/v1"}`,
		},
		{
			name: "invalid secret",
			content: `{"schema":"vibermate.development-secret-store/v1",` +
				`"items":{"secret://provider/account":` +
				`{"revision":1,"value":"%%%"}}}`,
		},
	} {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(root, fixture.name, "store.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(fixture.content), 0o600); err != nil {
				t.Fatal(err)
			}
			factory, err := NewDevelopmentFileFactory(path)
			if err != nil {
				t.Fatal(err)
			}
			if store, err := factory.Open(context.Background()); store != nil ||
				!errors.Is(err, secretstore.ErrUnavailable) {
				t.Fatalf("Open() store=%T error=%v", store, err)
			}
		})
	}

	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias.json")
	if err := os.Symlink(target, alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires host permission")
		}
		t.Fatal(err)
	}
	factory, err := NewDevelopmentFileFactory(alias)
	if err != nil {
		t.Fatal(err)
	}
	if store, err := factory.Open(context.Background()); store != nil ||
		!errors.Is(err, secretstore.ErrUnavailable) {
		t.Fatalf("symlink Open() store=%T error=%v", store, err)
	}
}

func TestDevelopmentFileStoreCancellationDoesNotPublish(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "private", "store.json")
	factory, err := NewDevelopmentFileFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	value := mustDevelopmentValue(t, "provider-secret")
	defer value.Destroy()
	if _, err := store.Replace(ctx, secretstore.ReplaceCommand{
		Reference: mustDevelopmentReference(t, "secret://provider/account"),
		Value:     value,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Replace() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled replacement file error = %v", err)
	}
}

func TestDevelopmentFileStoreWriteFailureDoesNotPublish(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "private", "store.json")
	factory, err := NewDevelopmentFileFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	reference := mustDevelopmentReference(t, "secret://provider/account")
	value := mustDevelopmentValue(t, "provider-secret")
	defer value.Destroy()
	if _, err := store.Replace(
		context.Background(),
		secretstore.ReplaceCommand{
			Reference: reference,
			Value:     value,
		},
	); !errors.Is(err, secretstore.ErrUnavailable) {
		t.Fatalf("Replace() error = %v", err)
	}
	metadata, err := store.Inspect(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.State != secretstore.StateMissing || metadata.Revision != 0 {
		t.Fatalf("metadata after failed write = %+v", metadata)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	written, err := store.Replace(
		context.Background(),
		secretstore.ReplaceCommand{
			Reference: reference,
			Value:     value,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if written.State != secretstore.StateConfigured || written.Revision != 1 {
		t.Fatalf("metadata after retry = %+v", written)
	}
}

func TestBuildFactorySelectsDevelopmentFileDriver(t *testing.T) {
	t.Parallel()

	factory, err := NewBuildFactory()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := factory.(DevelopmentFileFactory); !ok {
		t.Fatalf("NewBuildFactory() = %T", factory)
	}
}

func openDevelopmentStore(t *testing.T) secretstore.Store {
	t.Helper()
	factory, err := NewDevelopmentFileFactory(
		filepath.Join(t.TempDir(), "private", "store.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	store, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func mustDevelopmentReference(
	t *testing.T,
	raw string,
) secretstore.Reference {
	t.Helper()
	reference, err := secretstore.ParseReference(raw)
	if err != nil {
		t.Fatal(err)
	}
	return reference
}

func mustDevelopmentValue(t *testing.T, raw string) *secretstore.Value {
	t.Helper()
	value, err := secretstore.NewValue([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func readDevelopmentValue(
	t *testing.T,
	store secretstore.Store,
	reference secretstore.Reference,
) []byte {
	t.Helper()
	value, err := store.Read(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	bytes, err := value.CopyBytes()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(bytes) })
	return bytes
}
