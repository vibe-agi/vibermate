package serveridentity

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const pendingIdentityName = "server-tls-pending.json"

var (
	ErrCertificateConflict  = errors.New("Runtime Server certificate changed; refresh before retrying")
	ErrInvalidHosts         = errors.New("Runtime Server certificate hosts are invalid")
	ErrAccessHostMissing    = errors.New("Runtime Server certificate must cover the current access address")
	ErrCertificateCAChanged = errors.New("pending certificate is not signed by the current Runtime Root CA; generate a new candidate")
)

type storedIdentity struct {
	doc      document
	identity Identity
}

// Manager owns one listener's persistent identity. Certificates returned to TLS
// are immutable; applying a prepared identity atomically switches the pointer.
// The Server generation lock must be held by its caller for its whole lifetime.
type Manager struct {
	mu        sync.Mutex
	active    atomic.Pointer[storedIdentity]
	pending   *storedIdentity
	ca        *authority // Immutable for the lifetime of this manager.
	directory string
	random    io.Reader
	now       func() time.Time
}

// OpenManager uses bootstrap hosts only when no persisted identity exists.
// Later restarts load the addresses selected in the management UI, never
// replacing them with CLI defaults. A matching pending file is an already
// applied candidate left deliberately in place for crash-safe recovery.
func OpenManager(ctx context.Context, directory string, random io.Reader, now func() time.Time, issuer CertificateAuthority, bootstrapHosts ...string) (*Manager, error) {
	if ctx == nil || !validManagedPath(directory) || random == nil || now == nil || now().IsZero() {
		return nil, ErrInvalidIdentity
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, err
	}
	active, err := readStoredIdentity(filepath.Join(directory, identityName), now())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	manager := &Manager{directory: directory, random: random, now: now}
	pending, err := readStoredIdentity(filepath.Join(directory, pendingIdentityName), now())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if active == nil && pending != nil {
		return nil, ErrInvalidIdentity
	}
	if active == nil {
		if _, err := NormalizeHosts(bootstrapHosts); err != nil {
			return nil, err
		}
	}
	manager.ca, err = newAuthority(issuer, now())
	if err != nil {
		return nil, err
	}
	if err := manager.migrateAuthority(active, pending); err != nil {
		return nil, err
	}
	if active == nil {
		doc, identity, err := createLeafDocument(ctx, random, now().UTC(), manager.ca, bootstrapHosts...)
		if err != nil {
			return nil, err
		}
		if err := manager.persist(identityName, doc); err != nil {
			return nil, err
		}
		active = &storedIdentity{doc: doc, identity: identity}
	}
	manager.active.Store(active)
	if pending != nil && pending.identity.Fingerprint() != active.identity.Fingerprint() {
		manager.pending = pending
	}
	return manager, nil
}

func readStoredIdentity(path string, now time.Time) (*storedIdentity, error) {
	return readStoredDocument(path, now, identitySchema)
}

func readStoredDocument(path string, now time.Time, schema string) (*storedIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > maxIdentitySize {
		return nil, ErrInvalidIdentity
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	defer clear(payload)
	identity, err := parseIdentityDocument(payload, now.UTC(), schema)
	if err != nil {
		return nil, err
	}
	var doc document
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, ErrInvalidIdentity
	}
	return &storedIdentity{doc: doc, identity: identity}, nil
}

func (manager *Manager) Current() Identity { return manager.active.Load().identity }

func (manager *Manager) Snapshot() (Identity, Identity) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	var pending Identity
	if manager.pending != nil {
		pending = manager.pending.identity
	}
	return manager.Current(), pending
}

func (manager *Manager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return &manager.active.Load().identity.certificate, nil
}

func (manager *Manager) Stage(ctx context.Context, hosts []string, expectedCurrent, expectedPending string) (Identity, error) {
	if ctx == nil {
		return Identity{}, ErrInvalidIdentity
	}
	if _, err := NormalizeHosts(hosts); err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidHosts, err)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	pendingFingerprint := ""
	if manager.pending != nil {
		pendingFingerprint = manager.pending.identity.Fingerprint()
	}
	if manager.Current().Fingerprint() != expectedCurrent || pendingFingerprint != expectedPending {
		return Identity{}, ErrCertificateConflict
	}
	doc, identity, err := createLeafDocument(ctx, manager.random, manager.now().UTC(), manager.ca, hosts...)
	if err != nil {
		return Identity{}, err
	}
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	if err := manager.persist(pendingIdentityName, doc); err != nil {
		return Identity{}, err
	}
	manager.pending = &storedIdentity{doc: doc, identity: identity}
	return identity, nil
}

func (manager *Manager) Apply(ctx context.Context, expectedCurrent, expectedPending, accessHost string) (Identity, error) {
	if ctx == nil {
		return Identity{}, ErrInvalidIdentity
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	current := manager.active.Load()
	// A lost HTTP response may be retried without rotating the identity again.
	if expectedPending != "" && current.identity.Fingerprint() == expectedPending {
		return current.identity, nil
	}
	if current.identity.Fingerprint() != expectedCurrent || manager.pending == nil ||
		manager.pending.identity.Fingerprint() != expectedPending {
		return Identity{}, ErrCertificateConflict
	}
	pending := manager.pending
	if !manager.IssuedByAuthority(pending.identity) {
		return Identity{}, ErrCertificateCAChanged
	}
	if _, err := identityOf(pending.identity.certificate, manager.now().UTC()); err != nil {
		return Identity{}, err
	}
	if accessHost != "" && pending.identity.certificate.Leaf.VerifyHostname(accessHost) != nil {
		return Identity{}, ErrAccessHostMissing
	}
	if err := manager.persist(identityName+".previous", current.doc); err != nil {
		return Identity{}, err
	}
	if err := manager.persist(identityName, pending.doc); err != nil {
		return Identity{}, err
	}
	// No fallible operation follows the disk commit. Startup ignores a pending
	// file equal to the active file, so crashes cannot silently roll back TLS.
	manager.active.Store(pending)
	manager.pending = nil
	return pending.identity, nil
}

func (manager *Manager) persist(name string, doc document) error {
	payload, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	defer clear(payload)
	if err := preservePreviousIdentity(filepath.Join(manager.directory, name), payload); err != nil {
		return err
	}
	if directory, err := os.Open(manager.directory); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
