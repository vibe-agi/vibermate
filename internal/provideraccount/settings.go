package provideraccount

import (
	"context"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/providerauth"
)

type SettingsCommand struct {
	ID               ID
	ExpectedRevision uint64
	EgressProfile    egressprofile.ProfileRevision
	AutomaticRefresh bool
}

func (account Account) credentialScope(realm string, epoch uint64, fallback egressprofile.ProfileRevision) providerauth.AccountRef {
	return account.CredentialScope(realm, epoch, fallback)
}

// CredentialScope freezes the same exit precedence used by credential leases
// for account operations which must not acquire or refresh an access token.
func (account Account) CredentialScope(realm string, epoch uint64, fallback egressprofile.ProfileRevision) providerauth.AccountRef {
	if fallback == (egressprofile.ProfileRevision{}) {
		fallback = egressprofile.Direct()
	}
	if account.EgressProfile != (egressprofile.ProfileRevision{}) {
		fallback = account.EgressProfile
	}
	return providerauth.AccountRef{ID: account.ID.String(), Revision: account.Revision,
		CredentialEpoch: epoch, RealmID: realm, SettingsRevision: account.SettingsRevision, EgressProfile: fallback}
}

func (manager *Manager) BindEgressProfiles(profiles egressprofile.Controller) error {
	if manager == nil || profiles == nil {
		return ErrSettingsUnavailable
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closing || manager.egressProfiles != nil {
		return ErrSettingsUnavailable
	}
	manager.egressProfiles = profiles
	return nil
}

// SetSettings changes the next lease, not the identity authorized by an
// already published route. In-flight leases retain their original settings.
func (manager *Manager) SetSettings(ctx context.Context, command SettingsCommand) (View, error) {
	if manager == nil || ctx == nil || command.ExpectedRevision >= MaxRevision {
		return View{}, ErrInvalidAccount
	}
	if _, err := NewID(command.ID.String()); err != nil {
		return View{}, err
	}
	if err := ctx.Err(); err != nil {
		return View{}, err
	}
	if command.EgressProfile != (egressprofile.ProfileRevision{}) {
		if command.EgressProfile.Validate() != nil {
			return View{}, ErrInvalidAccount
		}
		manager.mu.RLock()
		profiles := manager.egressProfiles
		manager.mu.RUnlock()
		if profiles == nil {
			return View{}, ErrSettingsUnavailable
		}
		published, err := profiles.GetRevision(ctx, command.EgressProfile.ID, command.EgressProfile.Revision)
		if err != nil {
			return View{}, err
		}
		if !published.Equal(command.EgressProfile) {
			return View{}, ErrInvalidAccount
		}
	}
	if err := manager.setSettings(ctx, command); err != nil {
		return View{}, err
	}
	return manager.Get(ctx, command.ID)
}

func (manager *Manager) setSettings(ctx context.Context, command SettingsCommand) error {
	manager.mu.Lock()
	if manager.closing {
		manager.mu.Unlock()
		return ErrManagerClosing
	}
	account, exists := manager.accounts[command.ID]
	if !exists {
		manager.mu.Unlock()
		return ErrAccountNotFound
	}
	if _, busy := manager.operations[command.ID]; busy {
		manager.mu.Unlock()
		return ErrOperationInProgress
	}
	if account.SettingsRevision != command.ExpectedRevision {
		manager.mu.Unlock()
		return ErrRevisionConflict
	}
	if account.EgressProfile.Equal(command.EgressProfile) && account.AutomaticRefresh == command.AutomaticRefresh {
		manager.mu.Unlock()
		return nil
	}
	candidate := account
	candidate.SettingsRevision++
	candidate.EgressProfile = command.EgressProfile
	candidate.AutomaticRefresh = command.AutomaticRefresh
	candidate.UpdatedAt = manager.clock.Now().UTC()
	if err := candidate.Validate(); err != nil {
		manager.mu.Unlock()
		return err
	}
	manager.operations[command.ID] = accountOperationSettings
	manager.beginInFlightLocked()
	manager.mu.Unlock()
	defer manager.finishOperation(command.ID, accountOperationSettings)
	result, err := manager.repository.WriteSettings(ctx, command.ExpectedRevision, candidate)
	if result.Outcome != CommitCommitted {
		if result.Outcome == CommitConflict {
			return ErrRevisionConflict
		}
		if err != nil {
			return err
		}
		return ErrInvalidAccount
	}
	if result.Account != candidate {
		return ErrInvalidAccount
	}
	manager.mu.Lock()
	manager.accounts[command.ID] = candidate
	manager.mu.Unlock()
	return nil
}
