package provideraccount

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestManagerCreatesListsAndRotatesManagedSecretWithoutExposingIt(t *testing.T) {
	t.Parallel()
	repository := &memoryRepository{accounts: make(map[ID]Account)}
	secrets := newMemorySecrets()
	manager, err := NewManager(
		context.Background(), repository, secrets, testEndpoints(t), BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}
	value, err := secretstore.NewValue([]byte("secret-one"))
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	view, err := manager.Create(context.Background(), CreateCommand{
		ID: "anthropic-work", DisplayName: "Anthropic Work",
		UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID,
		Driver:             providerauth.AnthropicAPIKeyDriverRef(), Secret: value,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Account.ID != "anthropic-work" || view.Account.Revision != 1 ||
		view.Health.State != HealthReady || view.Health.CredentialEpoch != 1 {
		t.Fatalf("created ProviderAccount = %+v", view)
	}
	descriptor, exists := manager.LookupAccount("anthropic-work")
	if !exists || !descriptor.Active || descriptor.RealmID != "anthropic.official" ||
		descriptor.Revision != 1 {
		t.Fatalf("Environment account descriptor = %+v exists=%t", descriptor, exists)
	}
	rotated, err := secretstore.NewValue([]byte("secret-two"))
	if err != nil {
		t.Fatal(err)
	}
	defer rotated.Destroy()
	view, err = manager.ReplaceSecret(context.Background(), ReplaceSecretCommand{
		ID: "anthropic-work", ExpectedCredentialEpoch: 1, Secret: rotated,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Account.Revision != 1 || view.Health.CredentialEpoch != 2 {
		t.Fatalf("rotated ProviderAccount = %+v", view)
	}
	items, err := manager.List(context.Background())
	if err != nil || len(items) != 1 || items[0] != view {
		t.Fatalf("ProviderAccount list = %+v err=%v", items, err)
	}
	stored, err := secrets.Read(context.Background(), view.Account.SecretRef)
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Destroy()
	bytes, err := stored.CopyBytes()
	if err != nil || string(bytes) != "secret-two" {
		t.Fatalf("stored secret mismatch err=%v", err)
	}
	clear(bytes)
	if view.Account.SecretRef.String() == "secret-two" || view.Account.DisplayName == "secret-two" {
		t.Fatal("ProviderAccount view reflected secret bytes")
	}
}

func TestManagerRecoversMissingCredentialFailClosed(t *testing.T) {
	t.Parallel()
	reference, err := secretReference("anthropic-work")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_786_200_000, 0).UTC()
	account := Account{
		ID: "anthropic-work", DisplayName: "Anthropic Work",
		UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID, RealmID: "anthropic.official",
		Driver: providerauth.AnthropicAPIKeyDriverRef(), SecretRef: reference,
		State: StateActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	manager, err := NewManager(
		context.Background(),
		&memoryRepository{accounts: map[ID]Account{account.ID: account}},
		newMemorySecrets(), testEndpoints(t), BuiltInRealms(), fixedClock{now: now},
	)
	if err != nil {
		t.Fatal(err)
	}
	view, err := manager.Get(context.Background(), account.ID)
	if err != nil || view.Health.State != HealthMissing || view.Health.CredentialEpoch != 0 {
		t.Fatalf("missing credential view = %+v err=%v", view, err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown manager: %v", err)
	}
	if _, ok := manager.LookupAccount(account.ID.String()); ok {
		t.Fatal("closing ProviderAccount manager remained an Environment catalog authority")
	}
	if _, err := manager.List(context.Background()); !errors.Is(err, ErrManagerClosing) {
		t.Fatalf("list after shutdown error = %v", err)
	}
}

func TestBuiltInAnthropicRealmAcceptsStaticClaudeOAuthCredential(t *testing.T) {
	t.Parallel()
	repository := &memoryRepository{accounts: make(map[ID]Account)}
	manager, err := NewManager(
		context.Background(), repository, newMemorySecrets(), testEndpoints(t), BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}
	value, err := secretstore.NewValue([]byte("oauth-access-token"))
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	view, err := manager.Create(context.Background(), CreateCommand{
		ID: "claude-oauth", DisplayName: "Claude OAuth",
		UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID,
		Driver:             providerauth.StaticHeaderDriverRef(), Secret: value,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Account.RealmID != "anthropic.official" ||
		view.Account.Driver != providerauth.StaticHeaderDriverRef() ||
		view.Health.State != HealthReady {
		t.Fatalf("Claude OAuth ProviderAccount = %+v", view)
	}
}

func TestManagerDeletesOnlyAnUnreferencedInactiveAccount(t *testing.T) {
	t.Parallel()
	repository := &memoryRepository{accounts: make(map[ID]Account)}
	secrets := newMemorySecrets()
	manager, err := NewManager(
		context.Background(), repository, secrets, testEndpoints(t), BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}
	guard := &deletionGuard{references: []environment.AccountReference{{
		EnvironmentID: "work", EnvironmentName: "Work", EnvironmentRevision: 3,
		RouteID: "route.anthropic", RouteRevision: 2,
	}}}
	if err := manager.BindDeletionGuard(guard); err != nil {
		t.Fatal(err)
	}
	value, err := secretstore.NewValue([]byte("private-account-secret"))
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	view, err := manager.Create(context.Background(), CreateCommand{
		ID: "anthropic-work", DisplayName: "Anthropic Work",
		UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID,
		Driver:             providerauth.AnthropicAPIKeyDriverRef(), Secret: value,
	})
	if err != nil {
		t.Fatal(err)
	}

	blocked, err := manager.Delete(context.Background(), DeleteCommand{
		ID: view.Account.ID, ExpectedCredentialEpoch: view.Health.CredentialEpoch,
	})
	if err != nil || blocked.Deleted || len(blocked.References) != 1 || guard.callbacks != 0 {
		t.Fatalf("referenced account deletion = %+v callbacks=%d err=%v", blocked, guard.callbacks, err)
	}
	if metadata, inspectErr := secrets.Inspect(context.Background(), view.Account.SecretRef); inspectErr != nil || metadata.State != secretstore.StateConfigured {
		t.Fatalf("blocked account secret = %+v err=%v", metadata, inspectErr)
	}

	guard.references = nil
	manager.active[view.Account.ID] = 1
	if _, err := manager.Delete(context.Background(), DeleteCommand{
		ID: view.Account.ID, ExpectedCredentialEpoch: view.Health.CredentialEpoch,
	}); !errors.Is(err, ErrAccountInUse) {
		t.Fatalf("active account deletion error = %v", err)
	}
	manager.active[view.Account.ID] = 0
	deleted, err := manager.Delete(context.Background(), DeleteCommand{
		ID: view.Account.ID, ExpectedCredentialEpoch: view.Health.CredentialEpoch,
	})
	if err != nil || !deleted.Deleted || len(deleted.References) != 0 || guard.callbacks != 2 {
		t.Fatalf("unreferenced account deletion = %+v callbacks=%d err=%v", deleted, guard.callbacks, err)
	}
	if _, err := manager.Get(context.Background(), view.Account.ID); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("deleted account lookup error = %v", err)
	}
	if metadata, inspectErr := secrets.Inspect(context.Background(), view.Account.SecretRef); inspectErr != nil || metadata.State != secretstore.StateMissing {
		t.Fatalf("deleted account secret = %+v err=%v", metadata, inspectErr)
	}
}

type deletionGuard struct {
	references []environment.AccountReference
	callbacks  int
}

func (guard *deletionGuard) GuardAccountDeletion(
	_ context.Context,
	_ string,
	deleteAccount func() error,
) ([]environment.AccountReference, error) {
	if len(guard.references) != 0 {
		return append([]environment.AccountReference(nil), guard.references...), nil
	}
	guard.callbacks++
	return nil, deleteAccount()
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type endpointCatalog map[upstreamendpoint.ID]upstreamendpoint.Endpoint

func (catalog endpointCatalog) LookupEndpoint(rawID string) (upstreamendpoint.Endpoint, bool) {
	id, err := upstreamendpoint.NewID(rawID)
	if err != nil {
		return upstreamendpoint.Endpoint{}, false
	}
	endpoint, exists := catalog[id]
	return endpoint.Clone(), exists
}

func testEndpoints(t *testing.T) endpointCatalog {
	t.Helper()
	commands, err := upstreamendpoint.BuiltInCommands()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_786_200_000, 0).UTC()
	result := make(endpointCatalog, len(commands))
	for _, command := range commands {
		result[command.ID] = upstreamendpoint.Endpoint{
			ID: command.ID, DisplayName: command.DisplayName, Origin: command.Origin,
			RealmID: command.RealmID, BackendProtocols: command.BackendProtocols,
			Capabilities: command.Capabilities, Drivers: command.Drivers,
			State: upstreamendpoint.StateActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}
	}
	return result
}

type memoryRepository struct {
	mu       sync.Mutex
	accounts map[ID]Account
}

func (repository *memoryRepository) LoadAll(context.Context) ([]Account, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	result := make([]Account, 0, len(repository.accounts))
	for _, account := range repository.accounts {
		result = append(result, account)
	}
	return result, nil
}

func (repository *memoryRepository) Load(_ context.Context, id ID) (Account, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	account, exists := repository.accounts[id]
	return account, exists, nil
}

func (repository *memoryRepository) Write(_ context.Context, expected uint64, candidate Account) (CommitResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	current, exists := repository.accounts[candidate.ID]
	actual := uint64(0)
	if exists {
		actual = current.Revision
	}
	if actual != expected {
		return CommitResult{Outcome: CommitConflict, Account: current, Actual: actual}, nil
	}
	repository.accounts[candidate.ID] = candidate
	return CommitResult{Outcome: CommitCommitted, Account: candidate, Actual: candidate.Revision}, nil
}

func (repository *memoryRepository) Delete(_ context.Context, id ID, expected uint64) (CommitResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	current, exists := repository.accounts[id]
	if !exists || current.Revision != expected {
		actual := uint64(0)
		if exists {
			actual = current.Revision
		}
		return CommitResult{Outcome: CommitConflict, Account: current, Actual: actual}, nil
	}
	delete(repository.accounts, id)
	return CommitResult{Outcome: CommitCommitted}, nil
}

type memorySecrets struct {
	mu       sync.Mutex
	values   map[secretstore.Reference][]byte
	revision map[secretstore.Reference]secretstore.Revision
}

func newMemorySecrets() *memorySecrets {
	return &memorySecrets{
		values:   make(map[secretstore.Reference][]byte),
		revision: make(map[secretstore.Reference]secretstore.Revision),
	}
}

func (store *memorySecrets) Read(_ context.Context, reference secretstore.Reference) (*secretstore.Value, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, exists := store.values[reference]
	if !exists {
		return nil, secretstore.ErrNotFound
	}
	return secretstore.NewValue(value)
}

func (store *memorySecrets) Inspect(_ context.Context, reference secretstore.Reference) (secretstore.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	revision := store.revision[reference]
	if revision == 0 {
		return secretstore.Metadata{State: secretstore.StateMissing}, nil
	}
	return secretstore.Metadata{State: secretstore.StateConfigured, Revision: revision}, nil
}

func (store *memorySecrets) Replace(_ context.Context, command secretstore.ReplaceCommand) (secretstore.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.revision[command.Reference] != command.ExpectedRevision {
		return secretstore.Metadata{}, secretstore.ErrRevisionConflict
	}
	bytes, err := command.Value.CopyBytes()
	if err != nil {
		return secretstore.Metadata{}, err
	}
	store.revision[command.Reference]++
	store.values[command.Reference] = bytes
	return secretstore.Metadata{
		State: secretstore.StateConfigured, Revision: store.revision[command.Reference],
	}, nil
}

func (store *memorySecrets) Delete(_ context.Context, reference secretstore.Reference) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.values, reference)
	delete(store.revision, reference)
	return nil
}
