package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/certidentity"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
)

var errInjectedCommitResult = errors.New("injected commit result error")

func TestAccessRepositoryPersistsWholeAggregateWithCAS(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)

	create := accessMutation(t, "access-cas", 0, "Revision one")
	created, err := store.AccessRepository().CompareAndSwap(context.Background(), create)
	if err != nil ||
		created.Outcome != access.CommitOutcomeCommitted ||
		!created.Aggregate.Equal(create.Candidate) {
		t.Fatalf("create result=%+v err=%v", created, err)
	}
	update := accessMutation(t, "access-cas", 1, "Revision two")
	updated, err := store.AccessRepository().CompareAndSwap(context.Background(), update)
	if err != nil ||
		updated.Outcome != access.CommitOutcomeCommitted ||
		!updated.Aggregate.Equal(update.Candidate) {
		t.Fatalf("update result=%+v err=%v", updated, err)
	}

	stale := accessMutation(t, "access-cas", 1, "Stale")
	staleResult, err := store.AccessRepository().CompareAndSwap(
		context.Background(),
		stale,
	)
	if err != nil ||
		staleResult.Outcome != access.CommitOutcomeConflict ||
		staleResult.ActualRevision != 2 {
		t.Fatalf("stale result=%+v err=%v", staleResult, err)
	}
	aggregates, err := store.AccessRepository().LoadAll(context.Background())
	if err != nil {
		t.Fatalf("load aggregate: %v", err)
	}
	if len(aggregates) != 1 || !aggregates[0].Equal(update.Candidate) {
		t.Fatalf("durable aggregates = %+v", aggregates)
	}
}

func TestAccessRepositoryLoadsAggregateByID(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.AccessRepository()
	mutation := accessMutation(t, "access-load-one", 0, "Loaded")
	if result, err := repository.CompareAndSwap(
		context.Background(),
		mutation,
	); err != nil || result.Outcome != access.CommitOutcomeCommitted {
		t.Fatalf("create Access result=%+v err=%v", result, err)
	}

	loaded, exists, err := repository.Load(
		context.Background(),
		mutation.Candidate.Binding.ID,
	)
	if err != nil || !exists || !loaded.Equal(mutation.Candidate) {
		t.Fatalf("loaded aggregate exists=%t value=%+v err=%v", exists, loaded, err)
	}
}

func TestAccessRepositoryLoadReportsMissingAccess(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)

	loaded, exists, err := store.AccessRepository().Load(
		context.Background(),
		mustAccessID(t, "access-missing"),
	)
	if err != nil || exists {
		t.Fatalf("missing aggregate exists=%t value=%+v err=%v", exists, loaded, err)
	}
	if loaded.Binding.ID.String() != "" ||
		len(loaded.Profiles) != 0 ||
		len(loaded.ProviderTargets) != 0 ||
		len(loaded.AccountBindings) != 0 ||
		len(loaded.RouteSets) != 0 {
		t.Fatalf("missing aggregate returned non-zero state: %+v", loaded)
	}
}

func TestAccessRepositoryLoadReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.AccessRepository()
	mutation := accessMutation(t, "access-load-copy", 0, "Persisted")
	if result, err := repository.CompareAndSwap(
		context.Background(),
		mutation,
	); err != nil || result.Outcome != access.CommitOutcomeCommitted {
		t.Fatalf("create Access result=%+v err=%v", result, err)
	}

	first, exists, err := repository.Load(
		context.Background(),
		mutation.Candidate.Binding.ID,
	)
	if err != nil || !exists {
		t.Fatalf("first load exists=%t err=%v", exists, err)
	}
	first.Binding.ProfileIDs[0] = access.EndpointProfileID{}
	first.Profiles[0].Name = "mutated"
	first.Profiles[0].AccountBindingIDs[0] = access.AccountBindingID{}
	first.ProviderTargets[0].Capabilities[0] = "mutated"
	first.AccountBindings[0].Label = "mutated"
	first.RouteSets[0].CandidateProfileIDs[0] = access.EndpointProfileID{}
	if first.Equal(mutation.Candidate) {
		t.Fatal("test mutation did not change the first loaded value")
	}

	second, exists, err := repository.Load(
		context.Background(),
		mutation.Candidate.Binding.ID,
	)
	if err != nil || !exists || !second.Equal(mutation.Candidate) {
		t.Fatalf("second load aliases first value: exists=%t value=%+v err=%v", exists, second, err)
	}
}

func TestAccessRepositoryLoadRejectsCanceledAndClosedOperations(t *testing.T) {
	t.Parallel()

	t.Run("canceled caller", func(t *testing.T) {
		store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
		defer shutdownTestStore(t, store)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, exists, err := store.AccessRepository().Load(
			ctx,
			mustAccessID(t, "access-canceled-load"),
		)
		if exists || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled load exists=%t err=%v", exists, err)
		}
	})

	t.Run("closed store", func(t *testing.T) {
		store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
		repository := store.AccessRepository()
		if err := store.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown store: %v", err)
		}

		_, exists, err := repository.Load(
			context.Background(),
			mustAccessID(t, "access-closed-load"),
		)
		if exists || !errors.Is(err, ErrStoreClosing) {
			t.Fatalf("closed load exists=%t err=%v", exists, err)
		}
	})
}

func TestAccessRepositoryRejectsDuplicateClientOriginAtomically(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)

	first := accessMutation(t, "access-origin-one", 0, "First")
	if result, err := store.AccessRepository().CompareAndSwap(
		context.Background(),
		first,
	); err != nil || result.Outcome != access.CommitOutcomeCommitted {
		t.Fatalf("create first Access result=%+v err=%v", result, err)
	}
	second := accessMutation(t, "access-origin-two", 0, "Second")
	result, err := store.AccessRepository().CompareAndSwap(
		context.Background(),
		second,
	)
	if err == nil || result.Outcome != access.CommitOutcomeNotCommitted {
		t.Fatalf("duplicate origin result=%+v err=%v", result, err)
	}
	aggregates, loadErr := store.AccessRepository().LoadAll(context.Background())
	if loadErr != nil {
		t.Fatalf("load after duplicate origin: %v", loadErr)
	}
	if len(aggregates) != 1 ||
		aggregates[0].Binding.ID != first.Candidate.Binding.ID {
		t.Fatalf("duplicate origin changed durable state: %+v", aggregates)
	}
}

func TestAggregatePayloadFailureRollsBackRootAndDoesNotPublish(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)
	manager := newTestAccessManager(t, store)
	defer shutdownTestAccessManager(t, manager)
	accessID := mustAccessID(t, "access-transaction-failure")

	if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 0,
		Aggregate:        executableAggregate(t, accessID, 1, "Revision one"),
	}); err != nil {
		t.Fatalf("create initial plan: %v", err)
	}
	before, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve initial plan: %v", err)
	}
	if _, err := store.database.ExecContext(
		context.Background(),
		`CREATE TRIGGER reject_access_plan_payload
		 BEFORE UPDATE ON access_plan_aggregates
		 BEGIN
		   SELECT RAISE(ABORT, 'injected payload write failure');
		 END`,
	); err != nil {
		t.Fatalf("create payload failure trigger: %v", err)
	}

	result, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 1,
		Aggregate:        executableAggregate(t, accessID, 2, "Revision two"),
	})
	if result.Outcome != access.WriteOutcomeNotCommitted ||
		!errors.Is(err, access.ErrWriteNotCommitted) {
		t.Fatalf("failed transaction result=%+v err=%v", result, err)
	}
	after, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve after transaction failure: %v", err)
	}
	if after.Revision() != 1 || after.PlanHash() != before.PlanHash() {
		t.Fatal("transaction failure changed the active plan")
	}
	aggregates, err := store.AccessRepository().LoadAll(context.Background())
	if err != nil {
		t.Fatalf("load after transaction failure: %v", err)
	}
	if len(aggregates) != 1 ||
		aggregates[0].Binding.Revision != 1 ||
		aggregates[0].Binding.Name != "Revision one" {
		t.Fatalf("transaction failure changed durable state: %+v", aggregates)
	}
}

func TestManagerPublishesCommitReconciledAfterDriverError(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)
	manager := newTestAccessManager(t, store)
	defer shutdownTestAccessManager(t, manager)
	store.accessRepo.committer = commitThenError{}
	accessID := mustAccessID(t, "access-reconciled-commit")

	result, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 0,
		Aggregate:        executableAggregate(t, accessID, 1, "Committed"),
	})
	if err != nil ||
		result.Outcome != access.WriteOutcomeCommitted ||
		result.Revision != 1 {
		t.Fatalf("reconciled write result=%+v err=%v", result, err)
	}
	plan, err := manager.ResolveAccess(accessID)
	if err != nil || plan.Revision() != 1 || plan.Binding().Name != "Committed" {
		t.Fatalf(
			"reconciled active plan revision=%d name=%q err=%v",
			plan.Revision(),
			plan.Binding().Name,
			err,
		)
	}
}

func TestManagerDoesNotPublishDefinitelyUncommittedTransaction(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)
	manager := newTestAccessManager(t, store)
	defer shutdownTestAccessManager(t, manager)
	store.accessRepo.committer = rollbackThenError{}
	accessID := mustAccessID(t, "access-not-committed")

	result, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 0,
		Aggregate:        executableAggregate(t, accessID, 1, "Not committed"),
	})
	if result.Outcome != access.WriteOutcomeNotCommitted ||
		!errors.Is(err, access.ErrWriteNotCommitted) {
		t.Fatalf("uncommitted result=%+v err=%v", result, err)
	}
	if _, err := manager.ResolveAccess(accessID); !errors.Is(
		err,
		access.ErrAccessNotConfigured,
	) {
		t.Fatalf("uncommitted transaction published a plan: %v", err)
	}
	aggregates, err := store.AccessRepository().LoadAll(context.Background())
	if err != nil || len(aggregates) != 0 {
		t.Fatalf("uncommitted transaction persisted=%+v err=%v", aggregates, err)
	}
}

func TestIndeterminateRevisionPoisonsUntilRestartRecovery(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openTestStore(t, databasePath)
	manager := newTestAccessManager(t, store)
	accessID := mustAccessID(t, "access-indeterminate")
	revisionOne := executableAggregate(t, accessID, 1, "Revision one")
	revisionTwo := executableAggregate(t, accessID, 2, "Revision two")

	if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 0,
		Aggregate:        revisionOne,
	}); err != nil {
		t.Fatalf("create revision one: %v", err)
	}
	oldHandle, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve revision one: %v", err)
	}
	expectedRevisionTwo, err := testAccessCompiler(t).Compile(revisionTwo)
	if err != nil {
		t.Fatalf("compile expected revision two: %v", err)
	}

	store.accessRepo.committer = commitThenCloseAdmission{
		operations: store.operations,
	}
	result, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 1,
		Aggregate:        revisionTwo,
	})
	if result.Outcome != access.WriteOutcomeIndeterminate ||
		!errors.Is(err, access.ErrCommitOutcomeUnknown) {
		t.Fatalf("indeterminate result=%+v err=%v", result, err)
	}
	if _, err := manager.ResolveAccess(accessID); !errors.Is(
		err,
		access.ErrProjectionUnavailable,
	) {
		t.Fatalf("indeterminate Access served a stale plan: %v", err)
	}
	if oldHandle.Revision() != 1 ||
		oldHandle.Binding().Name != "Revision one" {
		t.Fatal("indeterminate write changed a previously acquired handle")
	}
	if health := manager.ProjectionHealth(); health.State != access.ProjectionStateUnavailable ||
		health.UnavailableAccessCount != 1 {
		t.Fatalf("indeterminate projection health = %+v", health)
	}
	if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 2,
		Aggregate:        executableAggregate(t, accessID, 3, "Rejected"),
	}); !errors.Is(err, access.ErrProjectionUnavailable) {
		t.Fatalf("poisoned Access accepted a write: %v", err)
	}

	shutdownTestAccessManager(t, manager)
	shutdownTestStore(t, store)
	reopened := openTestStore(t, databasePath)
	defer shutdownTestStore(t, reopened)
	recovered := newTestAccessManager(t, reopened)
	defer shutdownTestAccessManager(t, recovered)
	plan, err := recovered.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve recovered indeterminate commit: %v", err)
	}
	if plan.Revision() != 2 ||
		plan.Binding().Name != "Revision two" ||
		plan.PlanHash() != expectedRevisionTwo.PlanHash() {
		t.Fatalf(
			"recovered plan revision=%d name=%q hash=%s, want hash=%s",
			plan.Revision(),
			plan.Binding().Name,
			plan.PlanHash(),
			expectedRevisionTwo.PlanHash(),
		)
	}
	if health := recovered.ProjectionHealth(); health.State != access.ProjectionStateHealthy {
		t.Fatalf("recovered projection health = %+v", health)
	}
}

func TestAccessRecoveryRejectsMissingOrInvalidPlanPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		corrupt func(*testing.T, *Store, access.AccessID)
	}{
		{
			name: "missing payload",
			corrupt: func(t *testing.T, store *Store, accessID access.AccessID) {
				if _, err := store.database.ExecContext(
					context.Background(),
					`DELETE FROM access_plan_aggregates WHERE access_id = ?`,
					accessID.String(),
				); err != nil {
					t.Fatalf("delete payload: %v", err)
				}
			},
		},
		{
			name: "invalid payload",
			corrupt: func(t *testing.T, store *Store, accessID access.AccessID) {
				if _, err := store.database.ExecContext(
					context.Background(),
					`UPDATE access_plan_aggregates
					 SET payload_json = ?
					 WHERE access_id = ?`,
					[]byte(`{"unknown":true}`),
					accessID.String(),
				); err != nil {
					t.Fatalf("corrupt payload: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(
				t,
				filepath.Join(t.TempDir(), "data", "runtime.db"),
			)
			defer shutdownTestStore(t, store)
			mutation := accessMutation(t, "access-corrupt", 0, "Valid")
			if _, err := store.AccessRepository().CompareAndSwap(
				context.Background(),
				mutation,
			); err != nil {
				t.Fatalf("create valid aggregate: %v", err)
			}
			test.corrupt(t, store, mutation.Candidate.Binding.ID)
			if _, exists, err := store.AccessRepository().Load(
				context.Background(),
				mutation.Candidate.Binding.ID,
			); exists || !errors.Is(err, access.ErrInvalidRepositoryState) {
				t.Fatalf(
					"point load accepted invalid durable aggregate: exists=%t err=%v",
					exists,
					err,
				)
			}
			if _, err := store.AccessRepository().LoadAll(
				context.Background(),
			); !errors.Is(err, access.ErrInvalidRepositoryState) {
				t.Fatalf("invalid durable aggregate was accepted: %v", err)
			}
			if _, err := access.NewManager(
				context.Background(),
				store.AccessRepository(),
				testAccessCompiler(t),
				newTestSnapshotProjection(t),
			); !errors.Is(err, access.ErrInvalidRepositoryState) {
				t.Fatalf("manager recovered invalid durable aggregate: %v", err)
			}
		})
	}
}

func TestAccessRepositoryCancellationBeforeCommitLeavesNoState(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := store.AccessRepository().CompareAndSwap(
		ctx,
		accessMutation(t, "access-cancelled", 0, "Cancelled"),
	)
	if !errors.Is(err, context.Canceled) ||
		result.Outcome != access.CommitOutcomeNotCommitted {
		t.Fatalf("cancelled write result=%+v err=%v", result, err)
	}
	aggregates, loadErr := store.AccessRepository().LoadAll(context.Background())
	if loadErr != nil || len(aggregates) != 0 {
		t.Fatalf("cancelled write persisted=%+v err=%v", aggregates, loadErr)
	}
}

type commitThenError struct{}

func (commitThenError) Commit(transaction *sql.Tx) error {
	if err := transaction.Commit(); err != nil {
		return err
	}
	return errInjectedCommitResult
}

type rollbackThenError struct{}

func (rollbackThenError) Commit(transaction *sql.Tx) error {
	if err := transaction.Rollback(); err != nil {
		return err
	}
	return errInjectedCommitResult
}

type commitThenCloseAdmission struct {
	operations *operationGate
}

func (c commitThenCloseAdmission) Commit(transaction *sql.Tx) error {
	if err := transaction.Commit(); err != nil {
		return err
	}
	c.operations.closeAdmission()
	return errInjectedCommitResult
}

func accessMutation(
	t *testing.T,
	accessIDText string,
	expected access.Revision,
	name string,
) access.Mutation {
	t.Helper()
	accessID := mustAccessID(t, accessIDText)
	return access.Mutation{
		ExpectedRevision: expected,
		Candidate: executableAggregate(
			t,
			accessID,
			expected+1,
			name,
		),
	}
}

func executableAggregate(
	t *testing.T,
	accessID access.AccessID,
	revision access.Revision,
	name string,
) access.Aggregate {
	t.Helper()
	endpointID := mustAgentEndpointID(t, accessID.String()+"-endpoint")
	profileID := mustEndpointProfileID(t, accessID.String()+"-profile")
	targetID := mustProviderTargetID(t, accessID.String()+"-target")
	accountID := mustAccountBindingID(t, accessID.String()+"-account")
	routeSetID := mustRouteSetID(t, accessID.String()+"-routes")
	egressID := mustEgressPolicyID(t, accessID.String()+"-egress")
	clientOrigin, err := access.NewClientOrigin("https://api.anthropic.com:443")
	if err != nil {
		t.Fatalf("construct ClientOrigin: %v", err)
	}
	providerOrigin, err := access.NewProviderOrigin("https://api.openai.com:443/v1")
	if err != nil {
		t.Fatalf("construct ProviderOrigin: %v", err)
	}
	model, err := access.NewModelName("gpt-4.1-mini")
	if err != nil {
		t.Fatalf("construct model: %v", err)
	}
	secretRef, err := access.NewSecretRef("secret://provider/" + accessID.String())
	if err != nil {
		t.Fatalf("construct SecretRef: %v", err)
	}
	return access.Aggregate{
		Binding: access.AccessBinding{
			ID:                accessID,
			Revision:          revision,
			Name:              name,
			Description:       "Executable test Access",
			Status:            access.AccessStatusEnabled,
			AgentEndpointID:   endpointID,
			DefaultRouteSetID: routeSetID,
			ProfileIDs:        []access.EndpointProfileID{profileID},
			EgressPolicyID:    egressID,
		},
		AgentEndpoint: access.AgentEndpoint{
			ID:            endpointID,
			Revision:      revision,
			AccessID:      accessID,
			ClientOrigin:  clientOrigin,
			ClientDialect: access.DialectAnthropicMessages,
		},
		Profiles: []access.EndpointProfile{{
			ID:                     profileID,
			Revision:               revision,
			AccessID:               accessID,
			Name:                   "OpenAI Chat",
			Description:            "Fixed M0 profile",
			BackendDialect:         access.DialectOpenAIChat,
			TargetID:               targetID,
			UpstreamWireProfileRef: access.FollowClientUpstreamWireProfileRef(),
			DefaultModelPolicy: access.ModelPolicy{
				Revision:   revision,
				Mode:       access.ModelPolicyModeFixed,
				FixedModel: model,
			},
			AccountBindingIDs:       []access.AccountBindingID{accountID},
			DefaultAccountBindingID: accountID,
		}},
		ProviderTargets: []access.ProviderTarget{{
			ID:        targetID,
			Revision:  revision,
			AccessID:  accessID,
			ProfileID: profileID,
			Origin:    providerOrigin,
			Protocol:  access.DialectOpenAIChat,
			Capabilities: []access.ProviderCapability{
				access.ProviderCapabilityMessages,
				access.ProviderCapabilityStreaming,
				access.ProviderCapabilityToolCalls,
			},
		}},
		AccountBindings: []access.ProviderAccountBinding{{
			ID:            accountID,
			Revision:      revision,
			AccessID:      accessID,
			ProfileID:     profileID,
			Label:         "Primary",
			SecretRef:     secretRef,
			AuthDriverRef: access.StaticHeaderAuthDriverRef(),
			Enabled:       true,
		}},
		RouteSets: []access.RouteSet{{
			ID:                  routeSetID,
			Revision:            revision,
			AccessID:            accessID,
			CandidateProfileIDs: []access.EndpointProfileID{profileID},
		}},
		EgressPolicy: access.AccessEgressPolicy{
			ID:       egressID,
			Revision: revision,
			AccessID: accessID,
			Mode:     access.EgressModeDirect,
		},
		PluginPlan: access.PluginPlan{
			Revision: revision,
			AccessID: accessID,
			Mode:     access.PluginPlanModePassThrough,
		},
	}
}

func testAccessCompiler(t *testing.T) *access.Compiler {
	t.Helper()
	codecID, err := access.NewCodecPairID("anthropic-messages-to-openai-chat")
	if err != nil {
		t.Fatalf("construct codec ID: %v", err)
	}
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatalf("construct operation catalog: %v", err)
	}
	catalog, err := access.NewCatalog(access.CatalogOptions{
		Capabilities: access.PlanCapabilities{
			MaxEndpointProfiles: 1,
			MaxAccountBindings:  1,
			MaxRouteSets:        1,
		},
		ClientOperations: operations.Definitions(),
		CodecPairs: []access.CodecPairDefinition{{
			ID:              codecID,
			Revision:        1,
			ClientDialect:   access.DialectAnthropicMessages,
			ProviderDialect: access.DialectOpenAIChat,
			ClientOperationIDs: operations.SemanticOperationIDs(
				access.DialectAnthropicMessages,
			),
			RequiredCapabilities: []access.ProviderCapability{
				access.ProviderCapabilityMessages,
				access.ProviderCapabilityStreaming,
				access.ProviderCapabilityToolCalls,
			},
		}},
		AuthDrivers: []access.AuthDriverDefinition{{
			Ref:      access.StaticHeaderAuthDriverRef(),
			Revision: 1,
		}},
		EgressModes: []access.EgressModeDefinition{{
			Mode:     access.EgressModeDirect,
			Revision: 1,
		}},
		PluginPlanModes: []access.PluginPlanModeDefinition{{
			Mode:     access.PluginPlanModePassThrough,
			Revision: 1,
		}},
		ModelPolicyModes: []access.ModelPolicyModeDefinition{{
			Mode:     access.ModelPolicyModeFixed,
			Revision: 1,
		}},
		TransportProfiles:    access.BuiltInTransportFingerprintDefinitions(),
		UpstreamWireProfiles: access.BuiltInUpstreamWireProfileDefinitions(),
	})
	if err != nil {
		t.Fatalf("construct catalog: %v", err)
	}
	compiler, err := access.NewCompiler(catalog)
	if err != nil {
		t.Fatalf("construct compiler: %v", err)
	}
	return compiler
}

func newTestAccessManager(t *testing.T, store *Store) *access.Manager {
	t.Helper()
	manager, err := access.NewManager(
		context.Background(),
		store.AccessRepository(),
		testAccessCompiler(t),
		newTestSnapshotProjection(t),
	)
	if err != nil {
		t.Fatalf("construct Access manager: %v", err)
	}
	return manager
}

type discardLeafCacheInvalidator struct{}

func (discardLeafCacheInvalidator) InvalidateLeafCache(
	access.LeafCacheInvalidation,
) {
}

func newTestSnapshotProjection(t *testing.T) *access.AtomicSnapshotProjection {
	t.Helper()
	projection, err := access.NewSnapshotProjection(
		certidentity.InitialRootRevision,
		discardLeafCacheInvalidator{},
	)
	if err != nil {
		t.Fatalf("construct Access projection: %v", err)
	}
	return projection
}

func shutdownTestAccessManager(t *testing.T, manager *access.Manager) {
	t.Helper()
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown Access manager: %v", err)
	}
}

func shutdownTestStore(t *testing.T, store *Store) {
	t.Helper()
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown store: %v", err)
	}
}

func mustAccessID(t *testing.T, value string) access.AccessID {
	t.Helper()
	id, err := access.NewAccessID(value)
	if err != nil {
		t.Fatalf("construct Access ID: %v", err)
	}
	return id
}

func mustAgentEndpointID(t *testing.T, value string) access.AgentEndpointID {
	t.Helper()
	id, err := access.NewAgentEndpointID(value)
	if err != nil {
		t.Fatalf("construct AgentEndpoint ID: %v", err)
	}
	return id
}

func mustEndpointProfileID(t *testing.T, value string) access.EndpointProfileID {
	t.Helper()
	id, err := access.NewEndpointProfileID(value)
	if err != nil {
		t.Fatalf("construct EndpointProfile ID: %v", err)
	}
	return id
}

func mustProviderTargetID(t *testing.T, value string) access.ProviderTargetID {
	t.Helper()
	id, err := access.NewProviderTargetID(value)
	if err != nil {
		t.Fatalf("construct ProviderTarget ID: %v", err)
	}
	return id
}

func mustAccountBindingID(t *testing.T, value string) access.AccountBindingID {
	t.Helper()
	id, err := access.NewAccountBindingID(value)
	if err != nil {
		t.Fatalf("construct account binding ID: %v", err)
	}
	return id
}

func mustRouteSetID(t *testing.T, value string) access.RouteSetID {
	t.Helper()
	id, err := access.NewRouteSetID(value)
	if err != nil {
		t.Fatalf("construct RouteSet ID: %v", err)
	}
	return id
}

func mustEgressPolicyID(t *testing.T, value string) access.EgressPolicyID {
	t.Helper()
	id, err := access.NewEgressPolicyID(value)
	if err != nil {
		t.Fatalf("construct egress policy ID: %v", err)
	}
	return id
}
