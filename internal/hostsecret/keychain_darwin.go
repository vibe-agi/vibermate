//go:build vibermate_native_secrets && darwin

package hostsecret

/*
#cgo LDFLAGS: -framework CoreFoundation -framework Security
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
*/
import "C"

import (
	"context"
	"errors"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

// KeychainStore is the Desktop SecretStore on macOS.
//
// Design 19 §; and 06 §1048 name the OS keychain as the Desktop backend, with
// per-item access control and availability tied to login. The bytes live in
// the keychain and cross this boundary only on an explicit read; everything
// the control plane sees is metadata.
//
// The revision lives in the item's application-specific attribute rather than
// alongside the secret in its data. A revision packed into the data would have
// to be stripped on every read, which means the value a caller receives would
// differ from the value that was stored by a detail of this file.
type KeychainStore struct {
	service string
}

var _ secretstore.Store = (*KeychainStore)(nil)

// KeychainFactory opens the keychain-backed Store.
type KeychainFactory struct {
	service string
}

var _ secretstore.Factory = KeychainFactory{}

// NewKeychainFactory names the keychain service every item is filed under.
func NewKeychainFactory(service string) (KeychainFactory, error) {
	if service == "" || len(service) > 256 {
		return KeychainFactory{}, errors.New("keychain service name is invalid")
	}
	return KeychainFactory{service: service}, nil
}

func (factory KeychainFactory) Open(
	ctx context.Context,
) (secretstore.Store, error) {
	if ctx == nil {
		return nil, errors.New("keychain SecretStore factory context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if factory.service == "" {
		return nil, errors.New("keychain SecretStore factory is invalid")
	}
	return &KeychainStore{service: factory.service}, nil
}

func (store *KeychainStore) Read(
	ctx context.Context,
	reference secretstore.Reference,
) (*secretstore.Value, error) {
	if err := storeReady(ctx, store, reference); err != nil {
		return nil, err
	}
	data, _, err := store.copyItem(reference, true)
	if err != nil {
		return nil, err
	}
	defer wipe(data)
	return secretstore.NewValue(data)
}

// Inspect asks only for attributes. A metadata read must not be a reason for
// the operating system to prompt anyone about a secret.
func (store *KeychainStore) Inspect(
	ctx context.Context,
	reference secretstore.Reference,
) (secretstore.Metadata, error) {
	if err := storeReady(ctx, store, reference); err != nil {
		return secretstore.Metadata{}, err
	}
	_, revision, err := store.copyItem(reference, false)
	switch {
	case errors.Is(err, secretstore.ErrNotFound):
		return secretstore.Metadata{State: secretstore.StateMissing}, nil
	case errors.Is(err, secretstore.ErrLocked),
		errors.Is(err, secretstore.ErrUnavailable):
		return secretstore.Metadata{State: secretstore.StateUnavailable}, nil
	case err != nil:
		return secretstore.Metadata{}, err
	}
	return secretstore.Metadata{
		State:    secretstore.StateConfigured,
		Revision: revision,
	}, nil
}

// Replace swaps one secret under the revision its author read. A stored item
// whose revision moved is refused rather than overwritten.
func (store *KeychainStore) Replace(
	ctx context.Context,
	command secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	if err := storeReady(ctx, store, command.Reference); err != nil {
		return secretstore.Metadata{}, err
	}
	if command.Value == nil {
		return secretstore.Metadata{}, secretstore.ErrInvalidReference
	}
	value, err := command.Value.CopyBytes()
	if err != nil {
		return secretstore.Metadata{}, err
	}
	defer wipe(value)

	_, current, readErr := store.copyItem(command.Reference, false)
	switch {
	case readErr == nil:
		if current != command.ExpectedRevision {
			return secretstore.Metadata{}, secretstore.ErrRevisionConflict
		}
	case errors.Is(readErr, secretstore.ErrNotFound):
		if command.ExpectedRevision != 0 {
			return secretstore.Metadata{}, secretstore.ErrRevisionConflict
		}
	default:
		return secretstore.Metadata{}, readErr
	}
	if current >= secretstore.MaxRevision {
		return secretstore.Metadata{}, secretstore.ErrRevisionExhausted
	}
	next := current + 1
	if errors.Is(readErr, secretstore.ErrNotFound) {
		if err := store.addItem(command.Reference, value, next); err != nil {
			return secretstore.Metadata{}, err
		}
	} else if err := store.updateItem(
		command.Reference,
		value,
		current,
		next,
	); err != nil {
		return secretstore.Metadata{}, err
	}
	return secretstore.Metadata{
		State:    secretstore.StateConfigured,
		Revision: next,
	}, nil
}

// Delete removes one item. It exists for the tests that create items and for
// a future control path that retires a credential; nothing in the read or
// write path calls it.
func (store *KeychainStore) Delete(
	ctx context.Context,
	reference secretstore.Reference,
) error {
	if err := storeReady(ctx, store, reference); err != nil {
		return err
	}
	query := store.query(reference)
	defer C.CFRelease(C.CFTypeRef(query))
	status := C.SecItemDelete(C.CFDictionaryRef(query))
	if status == C.errSecItemNotFound {
		return secretstore.ErrNotFound
	}
	return keychainError(status)
}

func storeReady(
	ctx context.Context,
	store *KeychainStore,
	reference secretstore.Reference,
) error {
	if store == nil || store.service == "" {
		return secretstore.ErrUnavailable
	}
	if ctx == nil {
		return errors.New("keychain SecretStore context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if reference.String() == "" {
		return secretstore.ErrInvalidReference
	}
	return nil
}
