package upstreamendpoint

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type Manager struct {
	mu         sync.RWMutex
	repository Repository
	endpoints  map[ID]Endpoint
	clock      Clock
	closing    bool
}

var _ Controller = (*Manager)(nil)

func NewManager(
	ctx context.Context,
	repository Repository,
	builtIns []CreateCommand,
	clock Clock,
) (*Manager, error) {
	if ctx == nil || repository == nil {
		return nil, errors.New("UpstreamEndpoint manager dependencies are incomplete")
	}
	if clock == nil {
		clock = systemClock{}
	}
	loaded, err := repository.LoadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("recover UpstreamEndpoints: %w", err)
	}
	manager := &Manager{
		repository: repository,
		endpoints:  make(map[ID]Endpoint, len(loaded)+len(builtIns)),
		clock:      clock,
	}
	for _, endpoint := range loaded {
		if endpoint.Validate() != nil {
			return nil, fmt.Errorf("recover UpstreamEndpoint %q: %w", endpoint.ID, ErrInvalidEndpoint)
		}
		if _, duplicate := manager.endpoints[endpoint.ID]; duplicate {
			return nil, ErrInvalidEndpoint
		}
		manager.endpoints[endpoint.ID] = endpoint.Clone()
	}
	for _, command := range builtIns {
		if _, exists := manager.endpoints[command.ID]; exists {
			continue
		}
		if _, err := manager.createLocked(ctx, command); err != nil {
			return nil, fmt.Errorf("seed UpstreamEndpoint %q: %w", command.ID, err)
		}
	}
	return manager, nil
}

func (manager *Manager) LookupEndpoint(value string) (Endpoint, bool) {
	if manager == nil {
		return Endpoint{}, false
	}
	id, err := NewID(value)
	if err != nil {
		return Endpoint{}, false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closing {
		return Endpoint{}, false
	}
	endpoint, exists := manager.endpoints[id]
	return endpoint.Clone(), exists
}

func (manager *Manager) List(ctx context.Context) ([]Endpoint, error) {
	if manager == nil || ctx == nil {
		return nil, ErrInvalidEndpoint
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closing {
		return nil, ErrManagerClosing
	}
	items := make([]Endpoint, 0, len(manager.endpoints))
	for _, endpoint := range manager.endpoints {
		items = append(items, endpoint.Clone())
	}
	sort.Slice(items, func(left, right int) bool { return items[left].ID < items[right].ID })
	return items, nil
}

func (manager *Manager) Get(ctx context.Context, id ID) (Endpoint, error) {
	if manager == nil || ctx == nil {
		return Endpoint{}, ErrInvalidEndpoint
	}
	if err := ctx.Err(); err != nil {
		return Endpoint{}, err
	}
	parsed, err := NewID(id.String())
	if err != nil || parsed != id {
		return Endpoint{}, ErrInvalidEndpoint
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closing {
		return Endpoint{}, ErrManagerClosing
	}
	endpoint, exists := manager.endpoints[id]
	if !exists {
		return Endpoint{}, ErrEndpointNotFound
	}
	return endpoint.Clone(), nil
}

func (manager *Manager) Create(ctx context.Context, command CreateCommand) (Endpoint, error) {
	if manager == nil || ctx == nil {
		return Endpoint{}, ErrInvalidEndpoint
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closing {
		return Endpoint{}, ErrManagerClosing
	}
	return manager.createLocked(ctx, command)
}

func (manager *Manager) createLocked(ctx context.Context, command CreateCommand) (Endpoint, error) {
	if _, exists := manager.endpoints[command.ID]; exists {
		return Endpoint{}, ErrRevisionConflict
	}
	now := manager.clock.Now().UTC()
	endpoint := Endpoint{
		ID: command.ID, DisplayName: command.DisplayName, Origin: command.Origin,
		RealmID: command.RealmID, BackendProtocols: append([]string(nil), command.BackendProtocols...),
		Capabilities: append([]protocolspec.ProviderCapability(nil), command.Capabilities...),
		Drivers:      append([]providerauth.DriverRef(nil), command.Drivers...), State: StateActive,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if endpoint.Validate() != nil {
		return Endpoint{}, ErrInvalidEndpoint
	}
	result, err := manager.repository.Write(ctx, 0, endpoint)
	if err != nil {
		return Endpoint{}, err
	}
	if result.Outcome != CommitCommitted || !result.Endpoint.Equal(endpoint) {
		if result.Outcome == CommitConflict {
			return Endpoint{}, ErrRevisionConflict
		}
		return Endpoint{}, errors.New("UpstreamEndpoint create did not commit")
	}
	manager.endpoints[endpoint.ID] = endpoint.Clone()
	return endpoint.Clone(), nil
}

func (manager *Manager) Shutdown(_ context.Context) error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	manager.closing = true
	manager.mu.Unlock()
	return nil
}

// HolderLookup reports what would break if an Endpoint went away. The holders
// live outside this package — published Environment routes and the Accounts
// this Endpoint owns — so they arrive as lookups rather than as dependencies.
type HolderLookup func(context.Context, ID) ([]resourcedeletion.Holder, error)

// Delete retires an Endpoint once nothing depends on it.
//
// Retirement, not removal: evidence freezes an Endpoint by id and revision
// rather than by reference, so a deleted row would leave every historical
// Exchange unable to name where it went. Disabling keeps that answer and takes
// the Endpoint out of every live listing, which is what deletion means here.
func (manager *Manager) Delete(
	ctx context.Context,
	id ID,
	holders ...HolderLookup,
) (resourcedeletion.Result, error) {
	if manager == nil || ctx == nil || !validID(id.String()) {
		return resourcedeletion.Result{}, ErrInvalidEndpoint
	}
	if err := ctx.Err(); err != nil {
		return resourcedeletion.Result{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closing {
		return resourcedeletion.Result{}, ErrManagerClosing
	}
	current, present := manager.endpoints[id]
	if !present {
		return resourcedeletion.Result{}, ErrEndpointNotFound
	}
	found := make([]resourcedeletion.Holder, 0)
	for _, lookup := range holders {
		if lookup == nil {
			return resourcedeletion.Result{}, ErrInvalidEndpoint
		}
		values, err := lookup(ctx, id)
		if err != nil {
			return resourcedeletion.Result{}, err
		}
		found = append(found, values...)
	}
	if len(found) != 0 {
		return resourcedeletion.Refused(found)
	}
	retired := current.Clone()
	retired.State = StateDisabled
	retired.Revision = current.Revision + 1
	retired.UpdatedAt = manager.clock.Now().UTC()
	commit, err := manager.repository.Write(ctx, uint64(current.Revision), retired)
	if err != nil {
		return resourcedeletion.Result{}, err
	}
	if commit.Outcome != CommitCommitted {
		return resourcedeletion.Result{}, ErrRevisionConflict
	}
	delete(manager.endpoints, id)
	return resourcedeletion.Completed(), nil
}
