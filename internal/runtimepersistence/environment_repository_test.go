package runtimepersistence

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestEnvironmentRepositoryReopensIdenticalSnapshotAndPrivateDraft(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, databasePath)
	manager := newEnvironmentManager(t, store)
	candidate := environmentFixture(t, "work", 1)
	draft, err := manager.SaveDraft(context.Background(), environment.DraftCommand{Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(context.Background(), candidate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), preview); err != nil {
		t.Fatal(err)
	}
	before, err := manager.Resolve(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}

	next := candidate.Clone()
	next.Revision = 2
	next.Name = "Work renamed"
	next.PolicySet = &environment.PolicySet{ToolMode: environment.ToolPolicyReview}
	nextDraft, err := manager.SaveDraft(context.Background(), environment.DraftCommand{
		ExpectedBaseRevision: 1, ExpectedDraftRevision: 0, Candidate: next,
	})
	if err != nil {
		t.Fatal(err)
	}
	if nextDraft.Revision != 2 {
		t.Fatalf("draft revision = %d", nextDraft.Revision)
	}
	if resolved, err := manager.Resolve(candidate.ID); err != nil || resolved.Revision() != 1 || resolved.Name() != "Work" {
		t.Fatalf("private draft became active: %+v, %v", resolved, err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, databasePath)
	defer shutdownTestStore(t, reopened)
	recovered := newEnvironmentManager(t, reopened)
	after, err := recovered.Resolve(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := environment.CanonicalJSON(before.Aggregate())
	afterJSON, _ := environment.CanonicalJSON(after.Aggregate())
	if string(beforeJSON) != string(afterJSON) || before.Digest() != after.Digest() {
		t.Fatalf("snapshot changed across reopen: before=%s after=%s", beforeJSON, afterJSON)
	}
	recoveredDraft, exists, err := reopened.EnvironmentRepository().LoadDraft(context.Background(), candidate.ID)
	if err != nil || !exists || recoveredDraft.Revision != nextDraft.Revision || recoveredDraft.CandidateDigest != nextDraft.CandidateDigest {
		t.Fatalf("recovered draft = %+v exists=%t err=%v", recoveredDraft, exists, err)
	}
	recoveredPreview, err := recovered.Preview(context.Background(), candidate.ID, recoveredDraft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	published, err := recovered.Publish(context.Background(), recoveredPreview)
	if err != nil || published.ActualRevision != 2 {
		t.Fatalf("recovered publish = %+v, %v", published, err)
	}
	historical, err := recovered.GetRevision(context.Background(), candidate.ID, 1)
	if err != nil || historical.Revision() != 1 || historical.Name() != "Work" {
		t.Fatalf("historical revision = %+v, %v", historical, err)
	}
	current, err := recovered.GetRevision(context.Background(), candidate.ID, 2)
	if err != nil || current.Revision() != 2 || current.Name() != "Work renamed" ||
		current.Aggregate().EffectivePolicySet().ToolMode != environment.ToolPolicyReview {
		t.Fatalf("current revision = %+v, %v", current, err)
	}
	third := next.Clone()
	third.Revision = 3
	third.Name = "Work third"
	thirdDraft, err := recovered.SaveDraft(context.Background(), environment.DraftCommand{
		ExpectedBaseRevision: 2, ExpectedDraftRevision: 0, Candidate: third,
	})
	if err != nil || thirdDraft.Revision != 3 {
		t.Fatalf("monotonic third draft = %+v, %v", thirdDraft, err)
	}
}

func TestEnvironmentCommitErrorReconcilesOrRemainsUnpublished(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		committer transactionCommitter
		committed bool
	}{
		{"commit then error", commitThenError{}, true},
		{"rollback then error", rollbackThenError{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
			defer shutdownTestStore(t, store)
			store.environments.committer = test.committer
			manager := newEnvironmentManager(t, store)
			candidate := environmentFixture(t, "work", 1)
			draft, err := manager.SaveDraft(context.Background(), environment.DraftCommand{Candidate: candidate})
			if err != nil {
				t.Fatal(err)
			}
			preview, err := manager.Preview(context.Background(), candidate.ID, draft.Revision)
			if err != nil {
				t.Fatal(err)
			}
			result, publishErr := manager.Publish(context.Background(), preview)
			if test.committed {
				if publishErr != nil || result.Outcome != environment.CommitOutcomeCommitted {
					t.Fatalf("publish = %+v, %v", result, publishErr)
				}
				if _, err := manager.Resolve(candidate.ID); err != nil {
					t.Fatalf("reconciled commit not published: %v", err)
				}
			} else {
				if publishErr == nil || result.Outcome != environment.CommitOutcomeNotCommitted {
					t.Fatalf("publish = %+v, %v", result, publishErr)
				}
				if _, err := manager.Resolve(candidate.ID); !errors.Is(err, environment.ErrEnvironmentNotFound) {
					t.Fatalf("rolled-back candidate published: %v", err)
				}
			}
		})
	}
}

func TestEnvironmentRetiredChildIdentitySurvivesReopen(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, databasePath)
	manager := newEnvironmentManager(t, store)
	first := environmentFixture(t, "work", 1)
	firstDraft, err := manager.SaveDraft(context.Background(), environment.DraftCommand{Candidate: first})
	if err != nil {
		t.Fatal(err)
	}
	firstPreview, err := manager.Preview(context.Background(), first.ID, firstDraft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), firstPreview); err != nil {
		t.Fatal(err)
	}

	second := first.Clone()
	second.Revision = 2
	second.ClientEndpoints = nil
	secondDraft, err := manager.SaveDraft(context.Background(), environment.DraftCommand{
		ExpectedBaseRevision: 1, ExpectedDraftRevision: 0, Candidate: second,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondPreview, err := manager.Preview(context.Background(), second.ID, secondDraft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), secondPreview); err != nil {
		t.Fatal(err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, databasePath)
	defer shutdownTestStore(t, reopened)
	recovered := newEnvironmentManager(t, reopened)
	snapshot, err := recovered.Resolve(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Aggregate().RetiredChildIdentities) != 3 {
		t.Fatalf("recovered retired identities = %+v", snapshot.Aggregate().RetiredChildIdentities)
	}
	third := environmentFixture(t, "work", 3)
	if _, err := recovered.SaveDraft(context.Background(), environment.DraftCommand{
		ExpectedBaseRevision: 2, ExpectedDraftRevision: 0, Candidate: third,
	}); !errors.Is(err, environment.ErrInvalidTransition) {
		t.Fatalf("reused retired identity after reopen = %v", err)
	}
}

func TestEnvironmentRepositoryRejectsRevisionAuthorityDrift(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	manager := newEnvironmentManager(t, store)
	candidate := environmentFixture(t, "work", 1)
	draft, err := manager.SaveDraft(context.Background(), environment.DraftCommand{Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(context.Background(), candidate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), preview); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.Exec(
		`UPDATE environment_revision_counters SET active_revision = 2 WHERE environment_id = ?`,
		candidate.ID.String(),
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnvironmentRepository().LoadActive(context.Background(), candidate.ID); !errors.Is(err, environment.ErrInvalidRepositoryState) {
		t.Fatalf("LoadActive authority drift = %v", err)
	}
	if _, err := environment.NewManager(
		context.Background(), store.EnvironmentRepository(), runtimeEnvironmentCompiler(t), environment.NewAtomicProjection(), nil,
	); !errors.Is(err, environment.ErrInvalidRepositoryState) {
		t.Fatalf("startup authority drift = %v", err)
	}
}

func TestEnvironmentRepositoryRejectsDraftAuthorityDrift(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	manager := newEnvironmentManager(t, store)
	candidate := environmentFixture(t, "work", 1)
	draft, err := manager.SaveDraft(context.Background(), environment.DraftCommand{Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.EnvironmentRepository().LoadAllActive(context.Background())
	if err != nil || len(active) != 0 {
		t.Fatalf("draft-only authority loaded active=%+v err=%v", active, err)
	}
	if _, err := store.database.Exec(
		`UPDATE environment_revision_counters SET draft_revision = ? WHERE environment_id = ?`,
		int64(draft.Revision+1), candidate.ID.String(),
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnvironmentRepository().LoadDraft(context.Background(), candidate.ID); !errors.Is(err, environment.ErrInvalidRepositoryState) {
		t.Fatalf("LoadDraft authority drift = %v", err)
	}
}

func newEnvironmentManager(t *testing.T, store *Store) *environment.Manager {
	t.Helper()
	manager, err := environment.NewManager(context.Background(), store.EnvironmentRepository(), runtimeEnvironmentCompiler(t), environment.NewAtomicProjection(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func environmentFixture(t *testing.T, id string, revision environment.Revision) environment.Environment {
	t.Helper()
	origin := runtimeEnvironmentOrigin(t, "https://relay.example")
	providerOrigin := runtimeProviderOrigin(t, origin.String())
	return environment.Environment{
		ID: environment.EnvironmentID(id), Name: "Work", State: environment.StateActive, Revision: revision,
		ContentRecording: environment.DefaultContentRecordingPolicy(),
		ClientEndpoints: []environment.ClientEndpoint{{
			ID: "endpoint.shared", Revision: 1, ClientOrigin: origin,
			ProtocolPlans: []environment.ClientProtocolPlan{{
				ID: "plan.anthropic", Revision: 1, ClientProtocol: environment.ClientProtocolAnthropicMessages,
				ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "adapter.anthropic", Revision: 1},
				Destination: environment.DestinationPlan{
					Kind: environment.DestinationKindUpstream,
					Upstream: &environment.UpstreamPlan{
						DefaultRouteID: "route.anthropic", RouteSet: environment.RouteSet{
							ID: "routes.anthropic", Revision: 1,
							CandidateRouteIDs: []environment.UpstreamRouteID{"route.anthropic"},
						},
						Routes: []environment.UpstreamRoute{{
							ID: "route.anthropic", Revision: 1,
							ProviderTarget: environment.ProviderTarget{
								ID: "target.anthropic", Revision: 1, Origin: providerOrigin, RealmID: "realm.anthropic",
								Capabilities: []protocolspec.ProviderCapability{
									protocolspec.ProviderCapabilityMessages,
									protocolspec.ProviderCapabilityStreaming,
									protocolspec.ProviderCapabilityToolCalls,
								},
							},
							BackendProtocol: "anthropic_messages", AccountPolicy: environment.RouteAccountPolicy{
								Revision: 1, PreferredAccountID: "account.runtime",
								CandidateAccountIDs: []string{"account.runtime"},
								AccountRevisions:    map[string]environment.Revision{"account.runtime": 1},
								FailoverPolicy:      environment.FailoverOff,
							},
							ModelPolicy:    environment.ModelPolicy{Revision: 1, Mode: "passthrough"},
							WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue,
						}},
					},
				},
			}},
		}},
	}
}

func runtimeEnvironmentCompiler(t *testing.T) environment.Compiler {
	t.Helper()
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	pairs := make([]protocolspec.CodecPairDefinition, 0, 2)
	for _, dialect := range []protocolspec.Dialect{
		protocolspec.DialectAnthropicMessages,
		protocolspec.DialectOpenAIResponses,
	} {
		pairID, err := protocolspec.NewCodecPairID("test." + string(dialect) + ".passthrough")
		if err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, protocolspec.CodecPairDefinition{
			ID: pairID, Revision: 1,
			ClientDialect:      dialect,
			ProviderDialect:    dialect,
			ClientOperationIDs: operations.SemanticOperationIDs(dialect),
			RequiredCapabilities: []protocolspec.ProviderCapability{
				protocolspec.ProviderCapabilityMessages,
				protocolspec.ProviderCapabilityStreaming,
				protocolspec.ProviderCapabilityToolCalls,
			},
		})
	}
	protocols, err := protocolspec.NewCatalog(
		operations.Definitions(),
		pairs,
	)
	if err != nil {
		t.Fatal(err)
	}
	wires, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := environment.NewCompiler(runtimeAccountCatalog{}, nil, protocols, wires)
	if err != nil {
		t.Fatal(err)
	}
	return compiler
}

type runtimeAccountCatalog struct{}

func (runtimeAccountCatalog) LookupAccount(id string) (environment.AccountDescriptor, bool) {
	if id != "account.runtime" {
		return environment.AccountDescriptor{}, false
	}
	return environment.AccountDescriptor{
		ID: id, Revision: 1,
		UpstreamEndpointID: "target.anthropic", UpstreamEndpointRevision: 1,
		RealmID: "realm.anthropic", Active: true,
		BackendProtocols: []string{"anthropic_messages"},
	}, true
}

func runtimeEnvironmentOrigin(t *testing.T, raw string) originidentity.ClientOrigin {
	t.Helper()
	origin, err := originidentity.ParseClientOrigin(raw)
	if err != nil {
		t.Fatal(err)
	}
	return origin
}

func runtimeProviderOrigin(t *testing.T, raw string) originidentity.ProviderOrigin {
	t.Helper()
	origin, err := originidentity.ParseProviderOrigin(raw)
	if err != nil {
		t.Fatal(err)
	}
	return origin
}

// Deleting an Environment must not take the record with it. Every frozen
// Exchange resolves the exact revision it ran under, and design 08 section 4
// requires that history not drift with current configuration, so retirement
// clears the active pointer and leaves the revisions addressable.
func TestRetiredEnvironmentLeavesTheRevisionFrozenEvidencePointsAt(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, databasePath)
	manager := newEnvironmentManager(t, store)
	candidate := environmentFixture(t, "work", 1)
	draft, err := manager.SaveDraft(
		context.Background(), environment.DraftCommand{Candidate: candidate},
	)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(context.Background(), candidate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), preview); err != nil {
		t.Fatal(err)
	}
	published, err := manager.Get(context.Background(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}

	retired, err := store.EnvironmentRepository().Retire(
		context.Background(), candidate.ID,
	)
	if err != nil || !retired {
		t.Fatalf("Retire() = %v, %v", retired, err)
	}

	// Gone from every live listing.
	active, err := store.EnvironmentRepository().LoadAllActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range active {
		if value.ID == candidate.ID {
			t.Fatal("a retired Environment is still listed as active")
		}
	}

	// Still resolvable at the exact revision a Turn froze.
	frozen, found, err := store.EnvironmentRepository().LoadRevision(
		context.Background(), candidate.ID, published.Revision(),
	)
	if err != nil || !found {
		t.Fatalf("LoadRevision() = %v, %v; frozen evidence lost its authority", found, err)
	}
	if frozen.Revision != published.Revision() || frozen.ID != candidate.ID {
		t.Fatalf("frozen revision drifted: %+v", frozen)
	}

	// Retiring twice is not an error the caller has to distinguish.
	again, err := store.EnvironmentRepository().Retire(
		context.Background(), candidate.ID,
	)
	if err != nil || again {
		t.Fatalf("second Retire() = %v, %v, want false with no error", again, err)
	}
}
