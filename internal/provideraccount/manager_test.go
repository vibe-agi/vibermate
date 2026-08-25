package provideraccount

import (
	"context"
	"errors"
	"reflect"
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
	value, err := newTestCredentialValue(t, "secret-one")
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
	rotated, err := newTestCredentialValue(t, "secret-two")
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
	if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0], view) {
		t.Fatalf("ProviderAccount list = %+v err=%v", items, err)
	}
	stored, err := secrets.Read(context.Background(), view.Account.SecretRef)
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Destroy()
	encoded, err := stored.CopyBytes()
	if err != nil {
		t.Fatalf("stored secret mismatch err=%v", err)
	}
	defer clear(encoded)
	material, err := providerauth.ParseMaterial(encoded)
	if err != nil {
		t.Fatalf("parse stored credential material: %v", err)
	}
	defer material.Destroy()
	credential := material.CredentialBytes()
	defer clear(credential)
	if string(credential) != "secret-two" {
		t.Fatal("stored credential material did not contain the replacement")
	}
	if view.Account.SecretRef.String() == "secret-two" || view.Account.DisplayName == "secret-two" {
		t.Fatal("ProviderAccount view reflected secret bytes")
	}
}

func newTestCredentialValue(
	t *testing.T,
	credential string,
) (*secretstore.Value, error) {
	t.Helper()
	material, err := providerauth.NewMaterial(credential, nil, nil)
	if err != nil {
		return nil, err
	}
	defer material.Destroy()
	encoded, err := material.MarshalBinary()
	if err != nil {
		return nil, err
	}
	defer clear(encoded)
	return secretstore.NewValue(encoded)
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
	value, err := newTestCredentialValue(t, "oauth-access-token")
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
	value, err := newTestCredentialValue(t, "private-account-secret")
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

func TestManagerCreateDoesNotHoldTheAccountAuthorityLockDuringRepositoryWrite(
	t *testing.T,
) {
	t.Parallel()

	repository := &blockingWriteRepository{
		memoryRepository: &memoryRepository{accounts: make(map[ID]Account)},
		started:          make(chan struct{}),
		release:          make(chan struct{}),
	}
	manager, err := NewManager(
		t.Context(),
		repository,
		newMemorySecrets(),
		testEndpoints(t),
		BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}
	value, err := newTestCredentialValue(t, "private-account-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()

	createDone := make(chan error, 1)
	go func() {
		_, createErr := manager.Create(context.Background(), CreateCommand{
			ID:                 "anthropic-work",
			DisplayName:        "Anthropic Work",
			UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID,
			Driver:             providerauth.AnthropicAPIKeyDriverRef(),
			Secret:             value,
		})
		createDone <- createErr
	}()
	select {
	case <-repository.started:
	case <-time.After(time.Second):
		t.Fatal("repository write did not start")
	}

	lookupDone := make(chan bool, 1)
	go func() {
		_, exists := manager.LookupAccount("unrelated-account")
		lookupDone <- exists
	}()
	select {
	case exists := <-lookupDone:
		if exists {
			t.Fatal("unrelated account unexpectedly existed")
		}
	case <-time.After(100 * time.Millisecond):
		close(repository.release)
		<-createDone
		t.Fatal("blocked repository write held the account authority lock")
	}

	close(repository.release)
	select {
	case createErr := <-createDone:
		if createErr != nil {
			t.Fatalf("create account: %v", createErr)
		}
	case <-time.After(time.Second):
		t.Fatal("account creation did not finish after repository resumed")
	}
}

func TestManagerReplaceDoesNotHoldTheAccountAuthorityLockDuringSecretWrite(
	t *testing.T,
) {
	t.Parallel()

	secrets := &blockingReplaceSecretStore{
		memorySecrets: newMemorySecrets(),
		started:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	manager, err := NewManager(
		t.Context(),
		&memoryRepository{accounts: make(map[ID]Account)},
		secrets,
		testEndpoints(t),
		BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := newTestCredentialValue(t, "secret-one")
	if err != nil {
		t.Fatal(err)
	}
	defer initial.Destroy()
	view, err := manager.Create(t.Context(), CreateCommand{
		ID:                 "anthropic-work",
		DisplayName:        "Anthropic Work",
		UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID,
		Driver:             providerauth.AnthropicAPIKeyDriverRef(),
		Secret:             initial,
	})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := newTestCredentialValue(t, "secret-two")
	if err != nil {
		t.Fatal(err)
	}
	defer rotated.Destroy()

	replaceDone := make(chan error, 1)
	go func() {
		_, replaceErr := manager.ReplaceSecret(context.Background(), ReplaceSecretCommand{
			ID:                      view.Account.ID,
			ExpectedCredentialEpoch: view.Health.CredentialEpoch,
			Secret:                  rotated,
		})
		replaceDone <- replaceErr
	}()
	select {
	case <-secrets.started:
	case <-time.After(time.Second):
		t.Fatal("credential replacement did not start")
	}

	lookupDone := make(chan bool, 1)
	go func() {
		_, exists := manager.LookupAccount(view.Account.ID.String())
		lookupDone <- exists
	}()
	select {
	case exists := <-lookupDone:
		if !exists {
			t.Fatal("account disappeared during credential replacement")
		}
	case <-time.After(100 * time.Millisecond):
		close(secrets.release)
		<-replaceDone
		t.Fatal("blocked credential replacement held the account authority lock")
	}

	close(secrets.release)
	select {
	case replaceErr := <-replaceDone:
		if replaceErr != nil {
			t.Fatalf("replace credential: %v", replaceErr)
		}
	case <-time.After(time.Second):
		t.Fatal("credential replacement did not finish after SecretStore resumed")
	}
}

func TestManagerCredentialReplacementReservesTheAccountFromNewLeases(
	t *testing.T,
) {
	t.Parallel()

	secrets := &blockingReplaceSecretStore{
		memorySecrets: newMemorySecrets(),
		started:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	manager, err := NewManager(
		t.Context(),
		&memoryRepository{accounts: make(map[ID]Account)},
		secrets,
		testEndpoints(t),
		BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := newTestCredentialValue(t, "secret-one")
	if err != nil {
		t.Fatal(err)
	}
	defer initial.Destroy()
	view, err := manager.Create(t.Context(), CreateCommand{
		ID:                 "anthropic-work",
		DisplayName:        "Anthropic Work",
		UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID,
		Driver:             providerauth.AnthropicAPIKeyDriverRef(),
		Secret:             initial,
	})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := newTestCredentialValue(t, "secret-two")
	if err != nil {
		t.Fatal(err)
	}
	defer rotated.Destroy()

	replaceDone := make(chan error, 1)
	go func() {
		_, replaceErr := manager.ReplaceSecret(context.Background(), ReplaceSecretCommand{
			ID:                      view.Account.ID,
			ExpectedCredentialEpoch: view.Health.CredentialEpoch,
			Secret:                  rotated,
		})
		replaceDone <- replaceErr
	}()
	select {
	case <-secrets.started:
	case <-time.After(time.Second):
		t.Fatal("credential replacement did not start")
	}

	lease, acquireErr := manager.acquire(t.Context(), accountLeaseScope{
		id:                       view.Account.ID,
		accountRevision:          view.Account.Revision,
		realmID:                  view.Account.RealmID,
		upstreamEndpointID:       view.Account.UpstreamEndpointID.String(),
		upstreamEndpointRevision: 1,
	})
	if lease != nil {
		lease.Release()
		t.Fatal("account lease was admitted during credential replacement")
	}
	if !errors.Is(acquireErr, ErrOperationInProgress) {
		t.Fatalf("lease error during credential replacement = %v", acquireErr)
	}

	close(secrets.release)
	select {
	case replaceErr := <-replaceDone:
		if replaceErr != nil {
			t.Fatalf("replace credential: %v", replaceErr)
		}
	case <-time.After(time.Second):
		t.Fatal("credential replacement did not finish after SecretStore resumed")
	}
}

func TestManagerDeletionDoesNotHoldTheAccountAuthorityLockDuringSecretInspection(
	t *testing.T,
) {
	t.Parallel()

	repository := &memoryRepository{accounts: make(map[ID]Account)}
	delegate := newMemorySecrets()
	secrets := &blockingInspectSecretStore{
		memorySecrets: delegate,
		started:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	manager, err := NewManager(
		t.Context(),
		repository,
		secrets,
		testEndpoints(t),
		BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}
	guard := &deletionGuard{}
	if err := manager.BindDeletionGuard(guard); err != nil {
		t.Fatal(err)
	}
	value, err := newTestCredentialValue(t, "private-account-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	view, err := manager.Create(t.Context(), CreateCommand{
		ID:                 "anthropic-work",
		DisplayName:        "Anthropic Work",
		UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID,
		Driver:             providerauth.AnthropicAPIKeyDriverRef(),
		Secret:             value,
	})
	if err != nil {
		t.Fatal(err)
	}

	deleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := manager.Delete(context.Background(), DeleteCommand{
			ID:                      view.Account.ID,
			ExpectedCredentialEpoch: view.Health.CredentialEpoch,
		})
		deleteDone <- deleteErr
	}()
	select {
	case <-secrets.started:
	case <-time.After(time.Second):
		t.Fatal("credential inspection did not start")
	}

	lookupDone := make(chan bool, 1)
	go func() {
		_, exists := manager.LookupAccount(view.Account.ID.String())
		lookupDone <- exists
	}()
	select {
	case exists := <-lookupDone:
		if !exists {
			t.Fatal("account disappeared before deletion committed")
		}
	case <-time.After(100 * time.Millisecond):
		close(secrets.release)
		<-deleteDone
		t.Fatal("blocked credential inspection held the account authority lock")
	}

	close(secrets.release)
	select {
	case deleteErr := <-deleteDone:
		if deleteErr != nil {
			t.Fatalf("delete account: %v", deleteErr)
		}
	case <-time.After(time.Second):
		t.Fatal("account deletion did not finish after inspection resumed")
	}
}

func TestManagerAcquireDoesNotHoldTheAccountAuthorityLockDuringSecretInspection(
	t *testing.T,
) {
	t.Parallel()

	repository := &memoryRepository{accounts: make(map[ID]Account)}
	delegate := newMemorySecrets()
	seedManager, err := NewManager(
		t.Context(),
		repository,
		delegate,
		testEndpoints(t),
		BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}
	value, err := newTestCredentialValue(t, "private-account-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	view, err := seedManager.Create(t.Context(), CreateCommand{
		ID:                 "anthropic-work",
		DisplayName:        "Anthropic Work",
		UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID,
		Driver:             providerauth.AnthropicAPIKeyDriverRef(),
		Secret:             value,
	})
	if err != nil {
		t.Fatal(err)
	}

	// A recovered manager intentionally starts without an in-memory epoch
	// observation. Its first lease admission performs one health inspection.
	secrets := &blockingInspectSecretStore{
		memorySecrets: delegate,
		started:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	manager, err := NewManager(
		t.Context(),
		repository,
		secrets,
		testEndpoints(t),
		BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}

	type acquireResult struct {
		lease providerauth.Lease
		err   error
	}
	acquireDone := make(chan acquireResult, 1)
	go func() {
		lease, acquireErr := manager.acquire(context.Background(), accountLeaseScope{
			id:                       view.Account.ID,
			accountRevision:          view.Account.Revision,
			realmID:                  view.Account.RealmID,
			upstreamEndpointID:       view.Account.UpstreamEndpointID.String(),
			upstreamEndpointRevision: 1,
		})
		acquireDone <- acquireResult{lease: lease, err: acquireErr}
	}()
	select {
	case <-secrets.started:
	case <-time.After(time.Second):
		t.Fatal("credential inspection did not start")
	}

	lookupDone := make(chan bool, 1)
	go func() {
		_, exists := manager.LookupAccount(view.Account.ID.String())
		lookupDone <- exists
	}()
	select {
	case exists := <-lookupDone:
		if !exists {
			t.Fatal("account disappeared while a lease was being admitted")
		}
	case <-time.After(100 * time.Millisecond):
		close(secrets.release)
		result := <-acquireDone
		if result.lease != nil {
			result.lease.Release()
		}
		t.Fatal("blocked credential inspection held the account authority lock")
	}

	close(secrets.release)
	select {
	case result := <-acquireDone:
		if result.err != nil || result.lease == nil {
			t.Fatalf("acquire account lease = %v, %v", result.lease, result.err)
		}
		result.lease.Release()
	case <-time.After(time.Second):
		t.Fatal("account lease did not finish after inspection resumed")
	}
}

func TestManagerLeasePinsCredentialEpochAcrossSecretRotation(t *testing.T) {
	t.Parallel()

	secrets := newMemorySecrets()
	manager, err := NewManager(
		t.Context(),
		&memoryRepository{accounts: make(map[ID]Account)},
		secrets,
		testEndpoints(t),
		BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := newTestCredentialValue(t, "secret-one")
	if err != nil {
		t.Fatal(err)
	}
	defer initial.Destroy()
	view, err := manager.Create(t.Context(), CreateCommand{
		ID:                 "anthropic-work",
		DisplayName:        "Anthropic Work",
		UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID,
		Driver:             providerauth.AnthropicAPIKeyDriverRef(),
		Secret:             initial,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := manager.acquire(t.Context(), accountLeaseScope{
		id:                       view.Account.ID,
		accountRevision:          view.Account.Revision,
		realmID:                  view.Account.RealmID,
		upstreamEndpointID:       view.Account.UpstreamEndpointID.String(),
		upstreamEndpointRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	account, available := lease.Account()
	if !available || account.CredentialEpoch != view.Health.CredentialEpoch {
		t.Fatalf("leased account = %+v available=%t", account, available)
	}

	rotated, err := newTestCredentialValue(t, "secret-two")
	if err != nil {
		t.Fatal(err)
	}
	defer rotated.Destroy()
	rotatedView, err := manager.ReplaceSecret(t.Context(), ReplaceSecretCommand{
		ID:                      view.Account.ID,
		ExpectedCredentialEpoch: view.Health.CredentialEpoch,
		Secret:                  rotated,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rotatedView.Health.CredentialEpoch == account.CredentialEpoch {
		t.Fatalf("credential rotation did not advance epoch: before=%d after=%d",
			account.CredentialEpoch, rotatedView.Health.CredentialEpoch)
	}
	stale, err := secrets.ReadAtRevision(
		t.Context(),
		lease.Secret(),
		secretstore.Revision(account.CredentialEpoch),
	)
	if stale != nil {
		stale.Destroy()
		t.Fatal("stale credential read unexpectedly returned secret bytes")
	}
	if !errors.Is(err, secretstore.ErrRevisionConflict) {
		t.Fatalf("stale credential read error = %v", err)
	}
}

func TestManagerAcquireUsesObservedCredentialEpochWithoutAnotherInspection(t *testing.T) {
	t.Parallel()

	secrets := newMemorySecrets()
	manager, err := NewManager(
		t.Context(),
		&memoryRepository{accounts: make(map[ID]Account)},
		secrets,
		testEndpoints(t),
		BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}
	value, err := newTestCredentialValue(t, "private-account-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	view, err := manager.Create(t.Context(), CreateCommand{
		ID:                 "anthropic-work",
		DisplayName:        "Anthropic Work",
		UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID,
		Driver:             providerauth.AnthropicAPIKeyDriverRef(),
		Secret:             value,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := secrets.inspectCount()
	lease, err := manager.acquire(t.Context(), accountLeaseScope{
		id:                       view.Account.ID,
		accountRevision:          view.Account.Revision,
		realmID:                  view.Account.RealmID,
		upstreamEndpointID:       view.Account.UpstreamEndpointID.String(),
		upstreamEndpointRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if after := secrets.inspectCount(); after != before {
		t.Fatalf("steady-state Acquire inspected SecretStore: before=%d after=%d", before, after)
	}
}

func TestManagerAcquireInspectsCredentialOnlyOnceAfterRecovery(t *testing.T) {
	t.Parallel()

	repository := &memoryRepository{accounts: make(map[ID]Account)}
	secrets := newMemorySecrets()
	seedManager, err := NewManager(
		t.Context(),
		repository,
		secrets,
		testEndpoints(t),
		BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}
	value, err := newTestCredentialValue(t, "private-account-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	view, err := seedManager.Create(t.Context(), CreateCommand{
		ID:                 "anthropic-work",
		DisplayName:        "Anthropic Work",
		UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID,
		Driver:             providerauth.AnthropicAPIKeyDriverRef(),
		Secret:             value,
	})
	if err != nil {
		t.Fatal(err)
	}

	recovered, err := NewManager(
		t.Context(),
		repository,
		secrets,
		testEndpoints(t),
		BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}
	scope := accountLeaseScope{
		id:                       view.Account.ID,
		accountRevision:          view.Account.Revision,
		realmID:                  view.Account.RealmID,
		upstreamEndpointID:       view.Account.UpstreamEndpointID.String(),
		upstreamEndpointRevision: 1,
	}
	before := secrets.inspectCount()
	first, err := recovered.acquire(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	first.Release()
	afterFirst := secrets.inspectCount()
	if afterFirst != before+1 {
		t.Fatalf("first recovered Acquire inspections = %d, want %d", afterFirst, before+1)
	}
	second, err := recovered.acquire(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	second.Release()
	if afterSecond := secrets.inspectCount(); afterSecond != afterFirst {
		t.Fatalf("second recovered Acquire inspections = %d, want %d", afterSecond, afterFirst)
	}
}

func TestManagerShutdownDrainsAdmittedCredentialInspection(t *testing.T) {
	t.Parallel()

	repository := &memoryRepository{accounts: make(map[ID]Account)}
	delegate := newMemorySecrets()
	seedManager, err := NewManager(
		t.Context(),
		repository,
		delegate,
		testEndpoints(t),
		BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}
	value, err := newTestCredentialValue(t, "private-account-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	view, err := seedManager.Create(t.Context(), CreateCommand{
		ID:                 "anthropic-work",
		DisplayName:        "Anthropic Work",
		UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID,
		Driver:             providerauth.AnthropicAPIKeyDriverRef(),
		Secret:             value,
	})
	if err != nil {
		t.Fatal(err)
	}

	secrets := &blockingInspectSecretStore{
		memorySecrets: delegate,
		started:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	manager, err := NewManager(
		t.Context(),
		repository,
		secrets,
		testEndpoints(t),
		BuiltInRealms(),
		fixedClock{now: time.Unix(1_786_200_000, 0).UTC()},
	)
	if err != nil {
		t.Fatal(err)
	}

	type acquireResult struct {
		lease providerauth.Lease
		err   error
	}
	acquireDone := make(chan acquireResult, 1)
	go func() {
		lease, acquireErr := manager.acquire(context.Background(), accountLeaseScope{
			id:                       view.Account.ID,
			accountRevision:          view.Account.Revision,
			realmID:                  view.Account.RealmID,
			upstreamEndpointID:       view.Account.UpstreamEndpointID.String(),
			upstreamEndpointRevision: 1,
		})
		acquireDone <- acquireResult{lease: lease, err: acquireErr}
	}()
	select {
	case <-secrets.started:
	case <-time.After(time.Second):
		t.Fatal("credential inspection did not start")
	}

	shutdownContext, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	if shutdownErr := manager.Shutdown(shutdownContext); !errors.Is(
		shutdownErr,
		context.DeadlineExceeded,
	) {
		close(secrets.release)
		result := <-acquireDone
		if result.lease != nil {
			result.lease.Release()
		}
		t.Fatalf("shutdown during admitted inspection error = %v", shutdownErr)
	}

	close(secrets.release)
	select {
	case result := <-acquireDone:
		if result.err != nil || result.lease == nil {
			t.Fatalf("admitted acquire during shutdown = %v, %v", result.lease, result.err)
		}
		result.lease.Release()
	case <-time.After(time.Second):
		t.Fatal("admitted account lease did not finish")
	}
	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatalf("retry shutdown after drain: %v", err)
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

type blockingWriteRepository struct {
	*memoryRepository
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (repository *blockingWriteRepository) Write(
	ctx context.Context,
	expected uint64,
	candidate Account,
) (CommitResult, error) {
	repository.once.Do(func() { close(repository.started) })
	select {
	case <-repository.release:
	case <-ctx.Done():
		return CommitResult{}, ctx.Err()
	}
	return repository.memoryRepository.Write(ctx, expected, candidate)
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
	mu           sync.Mutex
	values       map[secretstore.Reference][]byte
	revision     map[secretstore.Reference]secretstore.Revision
	inspectCalls int
}

type blockingInspectSecretStore struct {
	*memorySecrets
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type blockingReplaceSecretStore struct {
	*memorySecrets
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (store *blockingReplaceSecretStore) Replace(
	ctx context.Context,
	command secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	if command.ExpectedRevision == 0 {
		return store.memorySecrets.Replace(ctx, command)
	}
	store.once.Do(func() { close(store.started) })
	select {
	case <-store.release:
	case <-ctx.Done():
		return secretstore.Metadata{}, ctx.Err()
	}
	return store.memorySecrets.Replace(ctx, command)
}

func (store *blockingInspectSecretStore) Inspect(
	ctx context.Context,
	reference secretstore.Reference,
) (secretstore.Metadata, error) {
	store.once.Do(func() { close(store.started) })
	select {
	case <-store.release:
	case <-ctx.Done():
		return secretstore.Metadata{}, ctx.Err()
	}
	return store.memorySecrets.Inspect(ctx, reference)
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

func (store *memorySecrets) ReadAtRevision(
	_ context.Context,
	reference secretstore.Reference,
	expected secretstore.Revision,
) (*secretstore.Value, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, exists := store.values[reference]
	if !exists {
		return nil, secretstore.ErrNotFound
	}
	if store.revision[reference] != expected {
		return nil, secretstore.ErrRevisionConflict
	}
	return secretstore.NewValue(value)
}

func (store *memorySecrets) Inspect(_ context.Context, reference secretstore.Reference) (secretstore.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.inspectCalls++
	revision := store.revision[reference]
	if revision == 0 {
		return secretstore.Metadata{State: secretstore.StateMissing}, nil
	}
	return secretstore.Metadata{State: secretstore.StateConfigured, Revision: revision}, nil
}

func (store *memorySecrets) inspectCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.inspectCalls
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
