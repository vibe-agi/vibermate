package provideraccount

import (
	"context"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
)

type SettingsCommand struct {
	ID               ID
	ExpectedRevision uint64
	EgressProfile    egressprofile.ProfileRevision
	AutomaticRefresh bool
}

func (account Account) credentialScope(realm string, epoch uint64, resolved egressprofile.Resolved) providerauth.AccountRef {
	return providerauth.AccountRef{ID: account.ID.String(), Revision: account.Revision,
		CredentialEpoch: epoch, RealmID: realm, SettingsRevision: account.SettingsRevision, EgressProfile: resolved.Profile(), ResolvedEgress: resolved}
}

// CredentialScope freezes the account profile for callers without a resolved exit.
func (account Account) CredentialScope(realm string, epoch uint64, fallback egressprofile.ProfileRevision) providerauth.AccountRef {
	if fallback == (egressprofile.ProfileRevision{}) {
		fallback = egressprofile.Direct()
	}
	source := egressprofile.SelectionTransport
	if account.EgressProfile != (egressprofile.ProfileRevision{}) {
		fallback = account.EgressProfile
		source = egressprofile.SelectionAccount
	}
	resolved, _ := egressprofile.RestoreResolved(fallback, source, 0)
	return providerauth.AccountRef{ID: account.ID.String(), Revision: account.Revision,
		CredentialEpoch: epoch, RealmID: realm, SettingsRevision: account.SettingsRevision, EgressProfile: fallback, ResolvedEgress: resolved}
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

func (manager *Manager) BindEgressDefaults(defaults egressprofile.Resolver) error {
	if manager == nil || defaults == nil {
		return ErrSettingsUnavailable
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closing || manager.egressDefaults != nil {
		return ErrSettingsUnavailable
	}
	manager.egressDefaults = defaults
	return nil
}

func accountEgressChoices(account Account, flow egressprofile.Choice) (egressprofile.Choice, egressprofile.Choice, error) {
	if flow.Validate() != nil {
		return egressprofile.Choice{}, egressprofile.Choice{}, ErrInvalidAccount
	}
	accountChoice, flowChoice := egressprofile.Inherit(), flow
	var err error
	if account.EgressProfile != (egressprofile.ProfileRevision{}) {
		accountChoice, err = egressprofile.Explicit(account.EgressProfile)
		if err != nil {
			return egressprofile.Choice{}, egressprofile.Choice{}, err
		}
	}
	return accountChoice, flowChoice, nil
}

func (manager *Manager) resolveTargetEgress(ctx context.Context, account Account, flow egressprofile.Choice, target originidentity.ProviderOrigin) (egressprofile.Resolved, error) {
	if err := target.Validate(); err != nil {
		return egressprofile.Resolved{}, err
	}
	switch target.Transport() {
	case originidentity.ProviderTransportStrictTLS:
		return manager.resolveEgress(ctx, account, flow)
	case originidentity.ProviderTransportLoopbackCleartext, originidentity.ProviderTransportPrivateCleartext:
	default:
		return egressprofile.Resolved{}, ErrEndpointMismatch
	}
	// This driver's preparation uses a different, fixed HTTPS token target;
	// its data authenticator already refuses a cleartext provider target.
	if account.Driver == providerauth.CodexOAuthDriverRef() {
		return egressprofile.Resolved{}, ErrEndpointMismatch
	}
	a, f, err := accountEgressChoices(account, flow)
	if err != nil {
		return egressprofile.Resolved{}, err
	}
	for i, choice := range []egressprofile.Choice{a, f} {
		if profile, explicit := choice.ExplicitProfile(); explicit {
			if profile.Policy.Proxy.Kind != egressnetwork.ProxyDirect {
				return egressprofile.Resolved{}, ErrInvalidAccount
			}
			source := egressprofile.SelectionAccount
			if i == 1 {
				source = egressprofile.SelectionFlow
			}
			return egressprofile.RestoreResolved(profile, source, 0)
		}
	}
	return egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionTransport, 0)
}

func (manager *Manager) resolveEgress(ctx context.Context, account Account, flow egressprofile.Choice) (egressprofile.Resolved, error) {
	accountChoice, flowChoice, err := accountEgressChoices(account, flow)
	if err != nil {
		return egressprofile.Resolved{}, err
	}
	manager.mu.RLock()
	resolver := manager.egressDefaults
	manager.mu.RUnlock()
	if resolver != nil {
		return resolver.Resolve(ctx, accountChoice, flowChoice)
	}
	// Explicit choices need no default storage. An unbound inherited path has
	// no knowledge of the real default revision and must not invent Direct.
	if profile, ok := accountChoice.ExplicitProfile(); ok {
		return egressprofile.RestoreResolved(profile, egressprofile.SelectionAccount, 0)
	}
	if profile, ok := flowChoice.ExplicitProfile(); ok {
		return egressprofile.RestoreResolved(profile, egressprofile.SelectionFlow, 0)
	}
	return egressprofile.Resolved{}, ErrSettingsUnavailable
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
