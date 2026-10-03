package hostsecret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/providerauth"
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
		t.Fatal("Server secret file uses the wrong schema")
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

// These tests catch a missing persistence barrier, publishing a partial write,
// or issuing the old cache after a rename whose durability is uncertain.
func TestServerSecretCommitPhaseFailures(t *testing.T) {
	for _, operation := range []string{"replace", "delete"} {
		for _, phase := range []string{"write", "short-write", "file-sync", "close", "rename", "directory-sync", "cancel-before-rename", "cancel-after-rename", "cancel-after-directory-sync"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				factory, store, reference := openServerCommitFixture(t)
				lease, err := store.Read(t.Context(), reference)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Destroy()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				injectSecretCommitFault(store, phase, cancel)
				value := newServerCommitValue(t, "synthetic-next")
				defer value.Destroy()
				var commitErr error
				if operation == "replace" {
					metadata, err := store.Replace(ctx, secretstore.ReplaceCommand{Reference: reference, ExpectedRevision: 1, Value: value})
					commitErr = err
					if metadata.Revision != 0 {
						t.Fatal("failed commit published a revision")
					}
				} else {
					commitErr = store.Delete(ctx, reference)
				}
				if !errors.Is(commitErr, secretstore.ErrUnavailable) {
					t.Fatalf("failed commit did not return ErrUnavailable: %v", commitErr)
				}
				if strings.Contains(fmt.Sprintf("%+v", commitErr), "synthetic-next") || strings.Contains(fmt.Sprintf("%+v", commitErr), "synthetic-original") {
					t.Fatal("commit error disclosed secret material")
				}
				uncertain := phase == "directory-sync" || phase == "cancel-after-rename" || phase == "cancel-after-directory-sync"
				var classified *secretCommitError
				if !errors.As(commitErr, &classified) || classified.renamed != uncertain {
					t.Fatal("commit lost its rename classification")
				}
				if uncertain {
					assertServerCommitQuarantined(t, store, reference, value)
				} else {
					assertServerCommitValue(t, store, reference, "synthetic-original")
					metadata, err := store.Inspect(t.Context(), reference)
					if err != nil || metadata.Revision != 1 {
						t.Fatalf("confirmed revision lost: %+v %v", metadata, err)
					}
				}
				// A lease already copied out of the store keeps its original lifetime.
				copied, err := lease.CopyBytes()
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(copied, []byte("synthetic-original")) {
					t.Fatal("in-flight lease changed")
				}
				clear(copied)
				// Reopen exercises canonical JSON decoding, not only a disk substring.
				reopened, err := factory.Open(t.Context())
				if err != nil {
					t.Fatalf("reopen did not see a complete document: %v", err)
				}
				if uncertain && operation == "delete" {
					if _, err := reopened.Read(t.Context(), reference); !errors.Is(err, secretstore.ErrNotFound) {
						t.Fatalf("reopened delete: %v", err)
					}
				} else if uncertain {
					assertServerCommitValue(t, reopened, reference, "synthetic-next")
				} else {
					assertServerCommitValue(t, reopened, reference, "synthetic-original")
				}
				entries, err := os.ReadDir(filepath.Dir(store.path))
				if err != nil || len(entries) != 1 || entries[0].Name() != "store.json" {
					t.Fatal("temporary commit file was not cleaned up")
				}
				if !uncertain {
					store.ops = defaultSecretFileOps()
					if operation == "delete" {
						if err := store.Delete(t.Context(), reference); err != nil {
							t.Fatalf("healthy delete retry: %v", err)
						}
					} else {
						metadata, err := store.Replace(t.Context(), secretstore.ReplaceCommand{Reference: reference, ExpectedRevision: 1, Value: value})
						if err != nil || metadata.Revision != 2 {
							t.Fatalf("healthy CAS retry: %+v %v", metadata, err)
						}
					}
				}
			})
		}
	}
}

type secretCommitFaultFile struct {
	secretCommitFile
	phase  string
	cancel context.CancelFunc
}

func (file secretCommitFaultFile) Write(encoded []byte) (int, error) {
	if file.phase == "write" {
		return 0, errors.New("synthetic-next")
	}
	if file.phase == "short-write" {
		return file.secretCommitFile.Write(encoded[:len(encoded)/2])
	}
	return file.secretCommitFile.Write(encoded)
}

func (file secretCommitFaultFile) Sync() error {
	if file.phase == "file-sync" {
		return errors.New("synthetic-next")
	}
	return file.secretCommitFile.Sync()
}

func (file secretCommitFaultFile) Close() error {
	err := file.secretCommitFile.Close()
	if file.phase == "cancel-before-rename" {
		file.cancel()
	}
	if file.phase == "close" {
		return errors.New("synthetic-next")
	}
	return err
}

func injectSecretCommitFault(store *developmentFileStore, phase string, cancel context.CancelFunc) {
	real := store.ops
	store.ops.createTemp = func(directory, pattern string) (secretCommitFile, error) {
		file, err := real.createTemp(directory, pattern)
		return secretCommitFaultFile{secretCommitFile: file, phase: phase, cancel: cancel}, err
	}
	store.ops.rename = func(source, destination string) error {
		if phase == "rename" {
			return errors.New("synthetic-next")
		}
		err := real.rename(source, destination)
		if phase == "cancel-after-rename" {
			cancel()
		}
		return err
	}
	store.ops.syncParent = func(path string) error {
		if phase == "directory-sync" {
			return errors.New("synthetic-next")
		}
		err := real.syncParent(path)
		if phase == "cancel-after-directory-sync" {
			cancel()
		}
		return err
	}
}

func openServerCommitFixture(t *testing.T) (ServerFileFactory, *developmentFileStore, secretstore.Reference) {
	t.Helper()
	factory, err := NewServerFileFactory(filepath.Join(t.TempDir(), "server"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := factory.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store := opened.(*developmentFileStore)
	reference, err := secretstore.ParseReference("secret://provider/account")
	if err != nil {
		t.Fatal(err)
	}
	value := newServerCommitValue(t, "synthetic-original")
	defer value.Destroy()
	metadata, err := store.Replace(t.Context(), secretstore.ReplaceCommand{Reference: reference, Value: value})
	if err != nil || metadata.Revision != 1 {
		t.Fatalf("fixture commit: %+v %v", metadata, err)
	}
	return factory, store, reference
}

func newServerCommitValue(t *testing.T, synthetic string) *secretstore.Value {
	t.Helper()
	value, err := secretstore.NewValue([]byte(synthetic))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertServerCommitValue(t *testing.T, store secretstore.Store, reference secretstore.Reference, expected string) {
	t.Helper()
	value, err := store.Read(t.Context(), reference)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	got, err := value.CopyBytes()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(got)
	if !bytes.Equal(got, []byte(expected)) {
		t.Fatal("stored credential did not match the confirmed commit")
	}
}

func assertServerCommitQuarantined(t *testing.T, store secretstore.Store, reference secretstore.Reference, replacement *secretstore.Value) {
	t.Helper()
	_, readErr := store.Read(t.Context(), reference)
	_, pinnedErr := store.ReadAtRevision(t.Context(), reference, 1)
	_, inspectErr := store.Inspect(t.Context(), reference)
	_, replaceErr := store.Replace(t.Context(), secretstore.ReplaceCommand{Reference: reference, ExpectedRevision: 1, Value: replacement})
	deleteErr := store.Delete(t.Context(), reference)
	for name, err := range map[string]error{"read": readErr, "pinned": pinnedErr, "inspect": inspectErr, "replace": replaceErr, "delete": deleteErr} {
		if !errors.Is(err, secretstore.ErrUnavailable) {
			t.Fatalf("quarantined %s issued cached state: %v", name, err)
		}
	}
}

func TestServerDurableCommitWaitsForDirectorySync(t *testing.T) {
	factory, store, reference := openServerCommitFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	real := store.ops.syncParent
	store.ops.syncParent = func(path string) error {
		close(entered)
		<-release
		return real(path)
	}
	value := newServerCommitValue(t, "synthetic-next")
	defer value.Destroy()
	type result struct {
		metadata secretstore.Metadata
		err      error
	}
	completed := make(chan result, 1)
	go func() {
		metadata, err := store.Replace(t.Context(), secretstore.ReplaceCommand{Reference: reference, ExpectedRevision: 1, Value: value})
		completed <- result{metadata, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("commit skipped the directory barrier")
	}
	// Rename is already visible to a separate opener, but no success may be
	// acknowledged while the directory barrier remains outstanding.
	reopened, err := factory.Open(t.Context())
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	assertServerCommitValue(t, reopened, reference, "synthetic-next")
	select {
	case <-completed:
		close(release)
		t.Fatal("commit acknowledged before directory sync")
	default:
	}
	close(release)
	outcome := <-completed
	if outcome.err != nil || outcome.metadata.Revision != 2 {
		t.Fatalf("durable revision: %+v %v", outcome.metadata, outcome.err)
	}
	store.ops = defaultSecretFileOps()
	if _, err := store.Replace(t.Context(), secretstore.ReplaceCommand{Reference: reference, ExpectedRevision: 1, Value: value}); !errors.Is(err, secretstore.ErrRevisionConflict) {
		t.Fatalf("stale CAS: %v", err)
	}
	if err := store.Delete(t.Context(), reference); err != nil {
		t.Fatal(err)
	}
	reopened, err = factory.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Read(t.Context(), reference); !errors.Is(err, secretstore.ErrNotFound) {
		t.Fatalf("durable delete: %v", err)
	}
}

func TestServerSecretCommitRejectsTruncatedDocuments(t *testing.T) {
	for _, encoded := range []string{`{`, `{"schema":"vibermate.server-secret-store/v1","items":`, `{"schema":"vibermate.server-secret-store/v1","items":{}}` + "\n{}\n"} {
		factory, store, _ := openServerCommitFixture(t)
		if err := os.WriteFile(store.path, []byte(encoded), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := factory.Open(t.Context()); !errors.Is(err, secretstore.ErrUnavailable) {
			t.Fatalf("malformed document accepted: %v", err)
		}
	}
}

func TestServerUncertainCommitCannotReportOAuthRefreshSuccess(t *testing.T) {
	factory, store, reference := openServerCommitFixture(t)
	credential, err := codexoauth.ImportAuthJSON([]byte(`{"auth_mode":"chatgpt","tokens":{"id_token":"synthetic-id","access_token":"synthetic-access","refresh_token":"synthetic-refresh","account_id":"synthetic-account"},"last_refresh":"2026-10-03T00:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	encoded, err := credential.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	material, err := providerauth.NewMaterial(string(encoded), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	payload, err := material.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(payload)
	value, err := secretstore.NewValue(payload)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	if _, err := store.Replace(t.Context(), secretstore.ReplaceCommand{Reference: reference, ExpectedRevision: 1, Value: value}); err != nil {
		t.Fatal(err)
	}
	manager, err := codexoauth.NewManager(codexoauth.Options{Secrets: store, Client: secretCommitRefreshClient{}, Clock: secretCommitClock{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Forget(reference, 3) })
	injectSecretCommitFault(store, "directory-sync", func() {})
	scope := providerauth.AccountRef{ID: "synthetic-account", Revision: 1, CredentialEpoch: 2, SettingsRevision: 1, EgressProfile: egressprofile.Direct(), RealmID: "openai.chatgpt"}
	epoch, refreshErr := manager.Refresh(t.Context(), providerauth.CodexOAuthDriverRef(), reference, scope)
	if epoch != 0 || !errors.Is(refreshErr, secretstore.ErrUnavailable) {
		t.Fatal("manual refresh reported an uncertain credential as saved")
	}
	assertServerCommitQuarantined(t, store, reference, value)
	// The complete rotated document remains parseable even though the active
	// instance cannot certify its persistence and therefore cannot issue it.
	reopened, err := factory.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := reopened.Inspect(t.Context(), reference)
	if err != nil || metadata.Revision != 3 {
		t.Fatalf("reopened refresh metadata: %+v %v", metadata, err)
	}
}

type secretCommitRefreshClient struct{}

func (secretCommitRefreshClient) Do(*http.Request, providerauth.AccountRef) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"access_token":"synthetic-rotated-access","refresh_token":"synthetic-rotated-refresh"}`))}, nil
}

type secretCommitClock struct{}

func (secretCommitClock) Now() time.Time { return time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC) }

func TestServerDurableCommitSerializesConcurrentCAS(t *testing.T) {
	factory, store, reference := openServerCommitFixture(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, synthetic := range []string{"synthetic-writer-one", "synthetic-writer-two"} {
		value := newServerCommitValue(t, synthetic)
		defer value.Destroy()
		go func() {
			<-start
			_, err := store.Replace(t.Context(), secretstore.ReplaceCommand{Reference: reference, ExpectedRevision: 1, Value: value})
			results <- err
		}()
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, secretstore.ErrRevisionConflict):
			conflicts++
		default:
			t.Fatalf("concurrent commit: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent successes=%d conflicts=%d", successes, conflicts)
	}
	reopened, err := factory.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := reopened.Inspect(t.Context(), reference)
	if err != nil || metadata.Revision != 2 {
		t.Fatalf("reopened CAS winner: %+v %v", metadata, err)
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
