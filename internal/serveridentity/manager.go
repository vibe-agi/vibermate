package serveridentity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

const managedIdentityRenewalWindow = 30 * 24 * time.Hour

type storedIdentity struct {
	doc      document
	identity Identity
}

// Manager loads one listener's persistent identity at startup. It does not
// stage, apply or hot-reload certificates while the listener is running.
// The Server generation lock must be held by its caller for its whole lifetime.
type Manager struct {
	active    Identity
	ca        *authority // Immutable for the lifetime of this manager.
	directory string
	now       func() time.Time
}

// OpenManager uses bootstrap hosts only when no persisted identity exists.
// Restarts preserve the active certificate and its addresses. Historical pending
// files are left untouched and are never loaded or applied.
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
	manager := &Manager{directory: directory, now: now}
	requestedHosts, err := NormalizeHosts(bootstrapHosts)
	if err != nil {
		return nil, err
	}
	manager.ca, err = newAuthority(issuer, now())
	if err != nil {
		return nil, err
	}
	if err := manager.migrateAuthority(active); err != nil {
		return nil, err
	}
	if active == nil {
		doc, identity, err := createLeafDocument(ctx, random, now().UTC(), manager.ca, bootstrapHosts...)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := manager.persist(identityName, doc); err != nil {
			return nil, err
		}
		active = &storedIdentity{doc: doc, identity: identity}
	} else if manager.ca.signs(active.identity) {
		reconcileHosts := len(bootstrapHosts) != 0 &&
			!identityMatchesHosts(active.identity, requestedHosts)
		renew := !manager.now().Add(managedIdentityRenewalWindow).
			Before(active.identity.certificate.Leaf.NotAfter)
		if reconcileHosts || renew {
			hosts := requestedHosts
			if !reconcileHosts {
				hosts = identityHosts(active.identity)
			}
			doc, identity, err := createLeafDocument(
				ctx, random, manager.now().UTC(), manager.ca, hosts...,
			)
			if err != nil {
				return nil, err
			}
			previous, err := json.Marshal(active.doc)
			if err != nil {
				return nil, err
			}
			defer clear(previous)
			if err := preservePreviousIdentity(
				filepath.Join(directory, identityName+".previous"), previous,
			); err != nil {
				return nil, err
			}
			if err := manager.persist(identityName, doc); err != nil {
				return nil, err
			}
			active = &storedIdentity{doc: doc, identity: identity}
		}
	}
	manager.active = active.identity
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

func (manager *Manager) Current() Identity { return manager.active }

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
