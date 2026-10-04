package nativemcp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func startRuntime(t *testing.T) *productruntime.Runtime {
	t.Helper()
	return startRuntimeWithSecrets(t, newCredentialStoreFixture())
}

func startRuntimeWithSecrets(t *testing.T, secrets secretstore.Store) *productruntime.Runtime {
	t.Helper()
	paths, err := productruntime.NewRuntimePaths(
		filepath.Join(t.TempDir(), "runtime-data"),
	)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	// This starts the complete production runtime and current-schema SQLite
	// stores. Under the full repository race job many such fixtures compete
	// for CPU, so this harness bound must not masquerade as a product deadline.
	ctx, cancel := context.WithTimeout(
		context.Background(),
		60*time.Second,
	)
	defer cancel()
	runtime, err := productruntime.Start(ctx, productruntime.Options{
		Paths:          paths,
		Host:           hostcontract.Desktop(),
		OfflineHold:    gate,
		Secrets:        secrets,
		Approvals:      toolapproval.DefaultConfig(),
		ExchangeHold:   exchange.DefaultHoldPolicy(),
		Clock:          productruntime.SystemClock{},
		InstanceIDs:    productruntime.NewCryptographicInstanceIDSource(),
		SecurityRandom: rand.Reader,
		Lifecycle:      productruntime.DefaultLifecycleOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func shutdownRuntime(t *testing.T, runtime *productruntime.Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

type credentialStoreFixture struct {
	mu    sync.Mutex
	items map[string]credentialStoreItem
}

type credentialStoreItem struct {
	bytes    []byte
	revision secretstore.Revision
}

func newCredentialStoreFixture() *credentialStoreFixture {
	return &credentialStoreFixture{items: make(map[string]credentialStoreItem)}
}

func (store *credentialStoreFixture) Read(
	_ context.Context,
	reference secretstore.Reference,
) (*secretstore.Value, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	item, found := store.items[reference.String()]
	if !found {
		return nil, secretstore.ErrNotFound
	}
	return secretstore.NewValue(item.bytes)
}

func (store *credentialStoreFixture) ReadAtRevision(
	_ context.Context,
	reference secretstore.Reference,
	expected secretstore.Revision,
) (*secretstore.Value, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	item, found := store.items[reference.String()]
	if !found {
		return nil, secretstore.ErrNotFound
	}
	if item.revision != expected {
		return nil, secretstore.ErrRevisionConflict
	}
	return secretstore.NewValue(item.bytes)
}

func (store *credentialStoreFixture) Inspect(
	_ context.Context,
	reference secretstore.Reference,
) (secretstore.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	item, found := store.items[reference.String()]
	if !found {
		return secretstore.Metadata{State: secretstore.StateMissing}, nil
	}
	return secretstore.Metadata{
		State:    secretstore.StateConfigured,
		Revision: item.revision,
	}, nil
}

func (store *credentialStoreFixture) Replace(
	_ context.Context,
	command secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	current := store.items[command.Reference.String()]
	if current.revision != command.ExpectedRevision {
		return secretstore.Metadata{}, secretstore.ErrRevisionConflict
	}
	value, err := command.Value.CopyBytes()
	if err != nil {
		return secretstore.Metadata{}, err
	}
	defer clear(value)
	revision := current.revision + 1
	store.items[command.Reference.String()] = credentialStoreItem{
		bytes:    bytes.Clone(value),
		revision: revision,
	}
	return secretstore.Metadata{
		State:    secretstore.StateConfigured,
		Revision: revision,
	}, nil
}

func (store *credentialStoreFixture) Delete(
	_ context.Context,
	reference secretstore.Reference,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	item, found := store.items[reference.String()]
	if !found {
		return secretstore.ErrNotFound
	}
	clear(item.bytes)
	delete(store.items, reference.String())
	return nil
}

type readyState bool

func (ready readyState) Ready() bool {
	return bool(ready)
}

func capability(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}
