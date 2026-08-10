package provideraccount

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type Manager struct {
	mu sync.RWMutex

	repository Repository
	secrets    secretstore.Store
	endpoints  upstreamendpoint.Catalog
	realms     map[string]Realm
	accounts   map[ID]Account
	clock      Clock
	deletion   environment.AccountDeletionGuard
	active     map[ID]uint64
	closing    bool
}

var (
	_ Controller                     = (*Manager)(nil)
	_ environment.AccountCatalog     = (*Manager)(nil)
	_ exchange.AccountLeaseAuthority = (*Manager)(nil)
)

func BuiltInRealms() []Realm {
	return []Realm{
		{
			ID: "anthropic.official",
			BackendProtocols: []string{
				string(environment.ClientProtocolAnthropicMessages),
			},
			Drivers: []providerauth.DriverRef{
				providerauth.AnthropicAPIKeyDriverRef(),
				providerauth.StaticHeaderDriverRef(),
			},
		},
		{
			ID: "openai.platform",
			BackendProtocols: []string{
				string(environment.ClientProtocolOpenAIResponses),
				string(environment.ClientProtocolOpenAIChat),
			},
			Drivers: []providerauth.DriverRef{
				providerauth.StaticHeaderDriverRef(),
			},
		},
		{
			ID: "openai.chatgpt",
			BackendProtocols: []string{
				string(environment.ClientProtocolOpenAIResponses),
			},
			Drivers: []providerauth.DriverRef{
				providerauth.StaticHeaderDriverRef(),
			},
		},
	}
}

func NewManager(
	ctx context.Context,
	repository Repository,
	secrets secretstore.Store,
	endpoints upstreamendpoint.Catalog,
	realms []Realm,
	clock Clock,
) (*Manager, error) {
	if ctx == nil || repository == nil || secrets == nil || endpoints == nil {
		return nil, errors.New("ProviderAccount manager dependencies are incomplete")
	}
	if clock == nil {
		clock = systemClock{}
	}
	compiledRealms := make(map[string]Realm, len(realms))
	for _, realm := range realms {
		if err := realm.Validate(); err != nil {
			return nil, fmt.Errorf("compile ProviderAuthRealm: %w", err)
		}
		if _, duplicate := compiledRealms[realm.ID]; duplicate {
			return nil, ErrInvalidAccount
		}
		realm.BackendProtocols = slices.Clone(realm.BackendProtocols)
		realm.Drivers = slices.Clone(realm.Drivers)
		compiledRealms[realm.ID] = realm
	}
	if len(compiledRealms) == 0 {
		return nil, errors.New("ProviderAccount manager has no authentication realms")
	}
	loaded, err := repository.LoadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("recover ProviderAccounts: %w", err)
	}
	accounts := make(map[ID]Account, len(loaded))
	for _, account := range loaded {
		if err := validateForEndpoint(account, endpoints, compiledRealms); err != nil {
			return nil, fmt.Errorf("recover ProviderAccount %q: %w", account.ID, err)
		}
		if _, duplicate := accounts[account.ID]; duplicate {
			return nil, ErrInvalidAccount
		}
		accounts[account.ID] = account
	}
	return &Manager{
		repository: repository, secrets: secrets, endpoints: endpoints, realms: compiledRealms,
		accounts: accounts, clock: clock,
		active: make(map[ID]uint64),
	}, nil
}

// BindDeletionGuard completes the one-time runtime composition cycle after
// the Environment manager has been built with this Manager as its account
// catalog. ProductRuntime calls it before exposing either authority.
func (manager *Manager) BindDeletionGuard(guard environment.AccountDeletionGuard) error {
	if manager == nil || guard == nil {
		return ErrDeletionUnavailable
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closing || manager.deletion != nil {
		return ErrDeletionUnavailable
	}
	manager.deletion = guard
	return nil
}

func (manager *Manager) LookupAccount(value string) (environment.AccountDescriptor, bool) {
	if manager == nil {
		return environment.AccountDescriptor{}, false
	}
	id, err := NewID(value)
	if err != nil {
		return environment.AccountDescriptor{}, false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closing {
		return environment.AccountDescriptor{}, false
	}
	account, exists := manager.accounts[id]
	if !exists {
		return environment.AccountDescriptor{}, false
	}
	realm, exists := manager.realms[account.RealmID]
	if !exists {
		return environment.AccountDescriptor{}, false
	}
	endpoint, exists := manager.endpoints.LookupEndpoint(account.UpstreamEndpointID.String())
	if !exists {
		return environment.AccountDescriptor{}, false
	}
	return account.Descriptor(realm, endpoint), true
}

func (manager *Manager) List(ctx context.Context) ([]View, error) {
	if ctx == nil || manager == nil {
		return nil, ErrInvalidAccount
	}
	manager.mu.RLock()
	if manager.closing {
		manager.mu.RUnlock()
		return nil, ErrManagerClosing
	}
	accounts := make([]Account, 0, len(manager.accounts))
	for _, account := range manager.accounts {
		accounts = append(accounts, account)
	}
	manager.mu.RUnlock()
	sort.Slice(accounts, func(left, right int) bool {
		return accounts[left].ID.String() < accounts[right].ID.String()
	})
	views := make([]View, 0, len(accounts))
	for _, account := range accounts {
		view, err := manager.view(ctx, account)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (manager *Manager) Get(ctx context.Context, id ID) (View, error) {
	account, err := manager.account(id)
	if err != nil {
		return View{}, err
	}
	return manager.view(ctx, account)
}

func (manager *Manager) Create(ctx context.Context, command CreateCommand) (View, error) {
	if ctx == nil || command.Secret == nil {
		return View{}, ErrInvalidAccount
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closing {
		return View{}, ErrManagerClosing
	}
	if _, exists := manager.accounts[command.ID]; exists {
		return View{}, ErrRevisionConflict
	}
	reference, err := secretReference(command.ID)
	if err != nil {
		return View{}, err
	}
	now := manager.clock.Now().UTC()
	endpoint, exists := manager.endpoints.LookupEndpoint(command.UpstreamEndpointID.String())
	if !exists || endpoint.State != upstreamendpoint.StateActive {
		return View{}, upstreamendpoint.ErrEndpointNotFound
	}
	account := Account{
		ID: command.ID, DisplayName: command.DisplayName,
		UpstreamEndpointID: command.UpstreamEndpointID, RealmID: endpoint.RealmID,
		Driver: command.Driver, SecretRef: reference, State: StateActive,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := validateForEndpoint(account, manager.endpoints, manager.realms); err != nil {
		return View{}, err
	}
	result, err := manager.repository.Write(ctx, 0, account)
	if err != nil {
		return View{}, err
	}
	if result.Outcome != CommitCommitted || result.Account != account {
		if result.Outcome == CommitConflict {
			return View{}, ErrRevisionConflict
		}
		return View{}, errors.New("ProviderAccount create did not commit")
	}
	manager.accounts[account.ID] = account
	metadata, err := manager.secrets.Replace(ctx, secretstore.ReplaceCommand{
		Reference: reference, ExpectedRevision: 0, Value: command.Secret,
	})
	if err != nil {
		return View{}, fmt.Errorf("store ProviderAccount credential: %w", err)
	}
	if metadata.Validate() != nil || metadata.State != secretstore.StateConfigured || metadata.Revision == 0 {
		return View{}, errors.New("SecretStore returned invalid ProviderAccount metadata")
	}
	return View{Account: account, Health: Health{
		State: HealthReady, CredentialEpoch: uint64(metadata.Revision),
	}}, nil
}

func (manager *Manager) ReplaceSecret(
	ctx context.Context,
	command ReplaceSecretCommand,
) (View, error) {
	if ctx == nil || command.Secret == nil || command.ExpectedCredentialEpoch > MaxRevision {
		return View{}, ErrInvalidAccount
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closing {
		return View{}, ErrManagerClosing
	}
	account, exists := manager.accounts[command.ID]
	if !exists {
		return View{}, ErrAccountNotFound
	}
	metadata, err := manager.secrets.Replace(ctx, secretstore.ReplaceCommand{
		Reference:        account.SecretRef,
		ExpectedRevision: secretstore.Revision(command.ExpectedCredentialEpoch),
		Value:            command.Secret,
	})
	if err != nil {
		if errors.Is(err, secretstore.ErrRevisionConflict) {
			return View{}, ErrRevisionConflict
		}
		return View{}, fmt.Errorf("replace ProviderAccount credential: %w", err)
	}
	if metadata.Validate() != nil || metadata.State != secretstore.StateConfigured || metadata.Revision == 0 {
		return View{}, errors.New("SecretStore returned invalid ProviderAccount metadata")
	}
	return View{Account: account, Health: Health{
		State: HealthReady, CredentialEpoch: uint64(metadata.Revision),
	}}, nil
}

func (manager *Manager) Delete(
	ctx context.Context,
	command DeleteCommand,
) (DeleteResult, error) {
	if ctx == nil || command.ExpectedCredentialEpoch > MaxRevision {
		return DeleteResult{}, ErrInvalidAccount
	}
	id, err := NewID(command.ID.String())
	if err != nil {
		return DeleteResult{}, ErrInvalidAccount
	}
	command.ID = id
	manager.mu.RLock()
	if manager.closing {
		manager.mu.RUnlock()
		return DeleteResult{}, ErrManagerClosing
	}
	guard := manager.deletion
	manager.mu.RUnlock()
	if guard == nil {
		return DeleteResult{}, ErrDeletionUnavailable
	}
	references, err := guard.GuardAccountDeletion(
		ctx,
		command.ID.String(),
		func() error { return manager.deleteUnreferenced(ctx, command) },
	)
	if err != nil {
		return DeleteResult{}, err
	}
	if len(references) != 0 {
		return DeleteResult{References: references}, nil
	}
	return DeleteResult{Deleted: true, References: []environment.AccountReference{}}, nil
}

func (manager *Manager) deleteUnreferenced(
	ctx context.Context,
	command DeleteCommand,
) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closing {
		return ErrManagerClosing
	}
	account, exists := manager.accounts[command.ID]
	if !exists {
		return ErrAccountNotFound
	}
	if manager.active[command.ID] != 0 {
		return ErrAccountInUse
	}
	metadata, inspectErr := manager.secrets.Inspect(ctx, account.SecretRef)
	if inspectErr != nil && !errors.Is(inspectErr, secretstore.ErrNotFound) {
		return fmt.Errorf("inspect ProviderAccount credential before deletion: %w", inspectErr)
	}
	if inspectErr == nil {
		if metadata.Validate() != nil {
			return errors.New("SecretStore returned invalid ProviderAccount metadata")
		}
		switch metadata.State {
		case secretstore.StateConfigured:
			if uint64(metadata.Revision) != command.ExpectedCredentialEpoch {
				return ErrRevisionConflict
			}
			if err := manager.secrets.Delete(ctx, account.SecretRef); err != nil &&
				!errors.Is(err, secretstore.ErrNotFound) {
				return fmt.Errorf("delete ProviderAccount credential: %w", err)
			}
		case secretstore.StateMissing:
			// A previous attempt may have removed the secret before the
			// durable metadata commit failed. The retry can safely finish
			// removing the account record with the caller's last observed
			// credential epoch.
		case secretstore.StateUnavailable:
			return secretstore.ErrUnavailable
		}
	}
	result, err := manager.repository.Delete(ctx, account.ID, account.Revision)
	if err != nil && result.Outcome != CommitCommitted {
		if result.Outcome == CommitConflict {
			return ErrRevisionConflict
		}
		return err
	}
	if result.Outcome != CommitCommitted {
		if result.Outcome == CommitConflict {
			return ErrRevisionConflict
		}
		return errors.New("ProviderAccount deletion did not commit")
	}
	delete(manager.accounts, account.ID)
	delete(manager.active, account.ID)
	return nil
}

func (manager *Manager) Acquire(
	ctx context.Context,
	request exchange.AccountLeaseRequest,
) (providerauth.Lease, error) {
	if ctx == nil || request.AccountRevision() == 0 || request.RealmID() == "" ||
		request.UpstreamEndpointID() == "" || request.UpstreamEndpointRevision() == 0 {
		return nil, ErrInvalidAccount
	}
	id, err := NewID(request.AccountID())
	if err != nil {
		return nil, err
	}
	manager.mu.Lock()
	if manager.closing {
		manager.mu.Unlock()
		return nil, ErrManagerClosing
	}
	account, exists := manager.accounts[id]
	if !exists {
		manager.mu.Unlock()
		return nil, ErrAccountNotFound
	}
	if account.State != StateActive {
		manager.mu.Unlock()
		return nil, ErrAccountDisabled
	}
	endpoint, endpointExists := manager.endpoints.LookupEndpoint(account.UpstreamEndpointID.String())
	if !endpointExists || endpoint.State != upstreamendpoint.StateActive ||
		account.UpstreamEndpointID.String() != request.UpstreamEndpointID() ||
		endpoint.Revision != uint64(request.UpstreamEndpointRevision()) {
		manager.mu.Unlock()
		return nil, ErrEndpointMismatch
	}
	if account.RealmID != request.RealmID() || account.Revision != uint64(request.AccountRevision()) {
		manager.mu.Unlock()
		return nil, ErrRealmMismatch
	}
	metadata, err := manager.secrets.Inspect(ctx, account.SecretRef)
	if err != nil {
		manager.mu.Unlock()
		return nil, fmt.Errorf("inspect ProviderAccount credential: %w", err)
	}
	if metadata.Validate() != nil || metadata.State != secretstore.StateConfigured || metadata.Revision == 0 {
		manager.mu.Unlock()
		return nil, ErrCredentialMissing
	}
	manager.active[id]++
	manager.mu.Unlock()
	return &lease{
		account: providerauth.AccountRef{
			ID: account.ID.String(), Revision: account.Revision,
			CredentialEpoch: uint64(metadata.Revision), RealmID: account.RealmID,
		},
		driver:  account.Driver,
		secret:  account.SecretRef,
		release: func() { manager.release(id) },
	}, nil
}

func (manager *Manager) release(id ID) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.active[id] <= 1 {
		delete(manager.active, id)
		return
	}
	manager.active[id]--
}

// Shutdown closes account admission. It does not close the injected
// SecretStore because that physical store is owned by the Host.
func (manager *Manager) Shutdown(_ context.Context) error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	manager.closing = true
	manager.mu.Unlock()
	return nil
}

func (manager *Manager) account(id ID) (Account, error) {
	if _, err := NewID(id.String()); err != nil || manager == nil {
		return Account{}, ErrInvalidAccount
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closing {
		return Account{}, ErrManagerClosing
	}
	account, exists := manager.accounts[id]
	if !exists {
		return Account{}, ErrAccountNotFound
	}
	return account, nil
}

func (manager *Manager) view(ctx context.Context, account Account) (View, error) {
	if ctx == nil {
		return View{}, ErrInvalidAccount
	}
	if account.State == StateDisabled {
		return View{Account: account, Health: Health{State: HealthDisabled}}, nil
	}
	metadata, err := manager.secrets.Inspect(ctx, account.SecretRef)
	if err != nil {
		if errors.Is(err, secretstore.ErrNotFound) {
			return View{Account: account, Health: Health{State: HealthMissing}}, nil
		}
		return View{Account: account, Health: Health{State: HealthUnavailable}}, nil
	}
	if metadata.Validate() != nil {
		return View{}, errors.New("SecretStore returned invalid ProviderAccount metadata")
	}
	switch metadata.State {
	case secretstore.StateConfigured:
		return View{Account: account, Health: Health{
			State: HealthReady, CredentialEpoch: uint64(metadata.Revision),
		}}, nil
	case secretstore.StateMissing:
		return View{Account: account, Health: Health{State: HealthMissing}}, nil
	case secretstore.StateUnavailable:
		return View{Account: account, Health: Health{
			State: HealthUnavailable, CredentialEpoch: uint64(metadata.Revision),
		}}, nil
	default:
		return View{}, errors.New("SecretStore returned unsupported ProviderAccount metadata")
	}
}

func validateForEndpoint(
	account Account,
	endpoints upstreamendpoint.Catalog,
	realms map[string]Realm,
) error {
	if err := account.Validate(); err != nil {
		return err
	}
	endpoint, exists := endpoints.LookupEndpoint(account.UpstreamEndpointID.String())
	if !exists || endpoint.State != upstreamendpoint.StateActive ||
		endpoint.RealmID != account.RealmID || !slices.Contains(endpoint.Drivers, account.Driver) {
		return ErrInvalidAccount
	}
	realm, exists := realms[account.RealmID]
	if !exists || !slices.Contains(realm.Drivers, account.Driver) ||
		!sameStrings(realm.BackendProtocols, endpoint.BackendProtocols) {
		return ErrInvalidAccount
	}
	return nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for _, value := range left {
		if !slices.Contains(right, value) {
			return false
		}
	}
	return true
}

type lease struct {
	account providerauth.AccountRef
	driver  providerauth.DriverRef
	secret  secretstore.Reference
	release func()
	once    sync.Once
}

func (*lease) Mode() providerauth.CredentialMode             { return providerauth.CredentialManaged }
func (item *lease) Driver() providerauth.DriverRef           { return item.driver }
func (item *lease) Secret() secretstore.Reference            { return item.secret }
func (item *lease) Account() (providerauth.AccountRef, bool) { return item.account, true }
func (item *lease) Release() {
	item.once.Do(func() {
		if item.release != nil {
			item.release()
		}
	})
}
