package provideraccount

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func TestManagerCreatesListsAndRotatesManagedSecretWithoutExposingIt(t *testing.T) {
	t.Parallel()
	repository := &memoryRepository{accounts: make(map[ID]Account)}
	secrets := newMemorySecrets()
	manager, err := NewManager(
		context.Background(), repository, secrets, BuiltInRealms(),
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
		RealmID: "anthropic.official",
		Driver:  providerauth.AnthropicAPIKeyDriverRef(), Secret: value,
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
		ID: "anthropic-work", DisplayName: "Anthropic Work", RealmID: "anthropic.official",
		Driver: providerauth.AnthropicAPIKeyDriverRef(), SecretRef: reference,
		State: StateActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	manager, err := NewManager(
		context.Background(),
		&memoryRepository{accounts: map[ID]Account{account.ID: account}},
		newMemorySecrets(), BuiltInRealms(), fixedClock{now: now},
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
		context.Background(), repository, newMemorySecrets(), BuiltInRealms(),
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
		RealmID: "anthropic.official",
		Driver:  providerauth.StaticHeaderDriverRef(), Secret: value,
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

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

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
