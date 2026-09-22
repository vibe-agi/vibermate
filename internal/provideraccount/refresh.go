package provideraccount

import (
	"context"
	"fmt"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

// RefreshCredential uses the same preparer as requests. The account operation
// guard excludes replacement/deletion; the expected epoch rejects stale clicks.
// Existing attempts retain their frozen identity and are not retargeted.
func (manager *Manager) RefreshCredential(ctx context.Context, id ID, expectedEpoch uint64) (View, error) {
	if manager == nil || ctx == nil || expectedEpoch == 0 || expectedEpoch > MaxRevision {
		return View{}, ErrInvalidAccount
	}
	if err := ctx.Err(); err != nil {
		return View{}, err
	}
	manager.mu.Lock()
	if manager.closing {
		manager.mu.Unlock()
		return View{}, ErrManagerClosing
	}
	if _, exists := manager.operations[id]; exists {
		manager.mu.Unlock()
		return View{}, ErrOperationInProgress
	}
	account, exists := manager.accounts[id]
	if !exists {
		manager.mu.Unlock()
		return View{}, ErrAccountNotFound
	}
	if account.State != StateActive {
		manager.mu.Unlock()
		return View{}, ErrAccountDisabled
	}
	if account.Driver != providerauth.CodexOAuthDriverRef() {
		manager.mu.Unlock()
		return View{}, ErrInvalidAccount
	}
	refresher, ok := manager.preparer.(CredentialRefresher)
	if !ok {
		manager.mu.Unlock()
		return View{}, ErrPreparationUnavailable
	}
	manager.operations[id] = accountOperationRefresh
	manager.beginInFlightLocked()
	manager.mu.Unlock()
	defer manager.finishOperation(id, accountOperationRefresh)

	metadata, err := manager.secrets.Inspect(ctx, account.SecretRef)
	if err != nil {
		return View{}, fmt.Errorf("inspect credential for refresh: %w", err)
	}
	if metadata.Validate() != nil || metadata.State != secretstore.StateConfigured {
		return View{}, ErrCredentialMissing
	}
	if uint64(metadata.Revision) != expectedEpoch {
		return View{}, ErrRevisionConflict
	}
	prepared, err := refresher.Refresh(ctx, account.Driver, account.SecretRef, metadata.Revision)
	if err != nil {
		return View{}, err
	}
	if prepared <= metadata.Revision || prepared > secretstore.MaxRevision {
		return View{}, ErrCredentialMissing
	}
	manager.observeCredentialEpoch(id, prepared)
	return manager.view(ctx, account)
}
