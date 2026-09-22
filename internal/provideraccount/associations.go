package provideraccount

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

// EndpointAssociations is a canonical, comparable set of explicit credential
// grants. An empty set is a usable stored account that no profile may use yet.
type EndpointAssociations struct{ encoded string }

func NewEndpointAssociations(ids []upstreamendpoint.ID) (EndpointAssociations, error) {
	if len(ids) > 128 {
		return EndpointAssociations{}, ErrInvalidAccount
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	for index, id := range ids {
		if _, err := upstreamendpoint.NewID(id.String()); err != nil || index > 0 && ids[index-1] == id {
			return EndpointAssociations{}, ErrInvalidAccount
		}
	}
	if len(ids) == 0 {
		return EndpointAssociations{}, nil
	}
	encoded, err := json.Marshal(ids)
	return EndpointAssociations{encoded: string(encoded)}, err
}

func ParseEndpointAssociations(encoded string) (EndpointAssociations, error) {
	var ids []upstreamendpoint.ID
	if json.Unmarshal([]byte(encoded), &ids) != nil || ids == nil {
		return EndpointAssociations{}, ErrInvalidAccount
	}
	set, err := NewEndpointAssociations(ids)
	if err != nil || set.String() != encoded {
		return EndpointAssociations{}, ErrInvalidAccount
	}
	return set, nil
}

func (set EndpointAssociations) String() string {
	if set.encoded == "" {
		return "[]"
	}
	return set.encoded
}

func (set EndpointAssociations) IDs() []upstreamendpoint.ID {
	ids := []upstreamendpoint.ID{}
	_ = json.Unmarshal([]byte(set.String()), &ids)
	return ids
}

func (set EndpointAssociations) Contains(id upstreamendpoint.ID) bool {
	return slices.Contains(set.IDs(), id)
}

type AssociationCommand struct {
	ID               ID
	EndpointID       upstreamendpoint.ID
	ExpectedRevision uint64
	Linked           bool
}

// SetAssociation changes authorization, never the credential epoch or account
// identity revision. Linking another compatible profile must not invalidate
// existing frozen routes or copy/refresh its OAuth tokens.
func (manager *Manager) SetAssociation(ctx context.Context, command AssociationCommand) (View, error) {
	if ctx == nil || command.ExpectedRevision == 0 || command.ExpectedRevision >= MaxRevision {
		return View{}, ErrInvalidAccount
	}
	if _, err := NewID(command.ID.String()); err != nil {
		return View{}, err
	}
	if _, err := upstreamendpoint.NewID(command.EndpointID.String()); err != nil {
		return View{}, ErrInvalidAccount
	}
	update := func() error { return manager.setAssociation(ctx, command, upstreamendpoint.Endpoint{}) }
	if !command.Linked {
		manager.mu.RLock()
		guard := manager.deletion
		manager.mu.RUnlock()
		if guard == nil {
			return View{}, ErrDeletionUnavailable
		}
		references, err := guard.GuardAccountAssociationRemoval(ctx, command.ID.String(), command.EndpointID.String(), update)
		if err != nil {
			return View{}, err
		}
		if len(references) != 0 {
			return View{}, ErrAccountInUse
		}
	} else {
		if err := manager.endpoints.GuardAccountLink(ctx, command.EndpointID, func(endpoint upstreamendpoint.Endpoint) error {
			return manager.setAssociation(ctx, command, endpoint)
		}); err != nil {
			return View{}, err
		}
	}
	return manager.Get(ctx, command.ID)
}

func (manager *Manager) setAssociation(ctx context.Context, command AssociationCommand, endpoint upstreamendpoint.Endpoint) error {
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
	if account.AssociationRevision != command.ExpectedRevision {
		manager.mu.Unlock()
		return ErrRevisionConflict
	}
	if command.Linked && !account.CompatibleEndpoint(endpoint) {
		manager.mu.Unlock()
		return ErrEndpointMismatch
	}
	if account.Associations.Contains(command.EndpointID) == command.Linked {
		manager.mu.Unlock()
		return nil
	}
	if !command.Linked && manager.active[command.ID] != 0 {
		manager.mu.Unlock()
		return ErrAccountInUse
	}
	ids := account.Associations.IDs()
	if command.Linked {
		ids = append(ids, command.EndpointID)
	} else {
		ids = slices.DeleteFunc(ids, func(id upstreamendpoint.ID) bool { return id == command.EndpointID })
	}
	associations, err := NewEndpointAssociations(ids)
	if err != nil {
		manager.mu.Unlock()
		return err
	}
	candidate := account
	candidate.Associations = associations
	candidate.AssociationRevision++
	candidate.UpdatedAt = manager.clock.Now().UTC()
	manager.operations[command.ID] = accountOperationAssociation
	manager.beginInFlightLocked()
	manager.mu.Unlock()
	defer manager.finishOperation(command.ID, accountOperationAssociation)
	result, err := manager.repository.WriteAssociations(ctx, command.ExpectedRevision, candidate)
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
