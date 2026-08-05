package runtimepersistence

import (
	"bytes"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/proxyclient"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
	"github.com/vibe-agi/vibermate/internal/workspaceroute"
)

func TestAccessDeletionCommitsTombstoneAndPreventsIdentityReuse(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openTestStore(t, path)
	repository := store.AccessRepository()
	mutation := accessMutation(t, "access-delete", 0, "Delete me")
	createResult, err := repository.CompareAndSwap(t.Context(), mutation)
	if err != nil || createResult.Outcome != access.CommitOutcomeCommitted {
		t.Fatalf("create Access result=%+v err=%v", createResult, err)
	}
	disabled := mutation.Candidate.Clone()
	disabled.Binding.Revision = 2
	disabled.Binding.Status = access.AccessStatusDisabled
	disableResult, err := repository.CompareAndSwap(t.Context(), access.Mutation{
		ExpectedRevision: 1,
		Candidate:        disabled,
	})
	if err != nil || disableResult.Outcome != access.CommitOutcomeCommitted {
		t.Fatalf("disable Access result=%+v err=%v", disableResult, err)
	}

	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	inspection, exists, err := repository.InspectDeletion(
		t.Context(),
		disabled.Binding.ID,
		now,
	)
	if err != nil || !exists || len(inspection.WorkspaceReferences) != 0 {
		t.Fatalf("deletion inspection exists=%t value=%+v err=%v", exists, inspection, err)
	}
	impact, err := inspection.RepositoryImpactToken()
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := repository.Delete(t.Context(), access.DeleteMutation{
		AccessID:                 disabled.Binding.ID,
		ExpectedRevision:         2,
		ExpectedRepositoryImpact: impact,
		DeletedAt:                now,
	})
	if err != nil || deleted.Outcome != access.DeleteOutcomeCommitted ||
		deleted.Revision != 2 {
		t.Fatalf("delete result=%+v err=%v", deleted, err)
	}
	if _, exists, err := repository.Load(t.Context(), disabled.Binding.ID); err != nil || exists {
		t.Fatalf("deleted Access load exists=%t err=%v", exists, err)
	}
	recreate := mutation
	recreate.Candidate.Binding.Name = "Reused identity"
	recreated, err := repository.CompareAndSwap(t.Context(), recreate)
	if err != nil || recreated.Outcome != access.CommitOutcomeRetired {
		t.Fatalf("retired Access recreation result=%+v err=%v", recreated, err)
	}

	shutdownTestStore(t, store)
	reopened := openTestStore(t, path)
	defer shutdownTestStore(t, reopened)
	recreated, err = reopened.AccessRepository().CompareAndSwap(t.Context(), recreate)
	if err != nil || recreated.Outcome != access.CommitOutcomeRetired {
		t.Fatalf("reopened retired Access recreation result=%+v err=%v", recreated, err)
	}
}

func TestAccessDeletionRequiresExactWorkspaceImpactAndExplicitRetirement(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.AccessRepository()
	mutation := accessMutation(t, "access-delete-routes", 0, "Delete routes")
	if _, err := repository.CompareAndSwap(t.Context(), mutation); err != nil {
		t.Fatal(err)
	}
	disabled := mutation.Candidate.Clone()
	disabled.Binding.Revision = 2
	disabled.Binding.Status = access.AccessStatusDisabled
	if _, err := repository.CompareAndSwap(t.Context(), access.Mutation{
		ExpectedRevision: 1,
		Candidate:        disabled,
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 5, 11, 0, 0, 0, time.UTC)
	before, _, err := repository.InspectDeletion(t.Context(), disabled.Binding.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	beforeImpact, err := before.RepositoryImpactToken()
	if err != nil {
		t.Fatal(err)
	}
	createWorkspaceRoute(t, store, disabled, now)
	changed, err := repository.Delete(t.Context(), access.DeleteMutation{
		AccessID:                 disabled.Binding.ID,
		ExpectedRevision:         2,
		ExpectedRepositoryImpact: beforeImpact,
		RetireWorkspaceBindings:  true,
		DeletedAt:                now,
	})
	if err != nil || changed.Outcome != access.DeleteOutcomeImpactChanged {
		t.Fatalf("changed impact result=%+v err=%v", changed, err)
	}

	current, _, err := repository.InspectDeletion(t.Context(), disabled.Binding.ID, now)
	if err != nil || len(current.WorkspaceReferences) != 1 {
		t.Fatalf("current impact=%+v err=%v", current, err)
	}
	currentImpact, err := current.RepositoryImpactToken()
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := repository.Delete(t.Context(), access.DeleteMutation{
		AccessID:                 disabled.Binding.ID,
		ExpectedRevision:         2,
		ExpectedRepositoryImpact: currentImpact,
		DeletedAt:                now,
	})
	if err != nil || blocked.Outcome != access.DeleteOutcomeBlocked {
		t.Fatalf("unconfirmed workspace retirement result=%+v err=%v", blocked, err)
	}
	deleted, err := repository.Delete(t.Context(), access.DeleteMutation{
		AccessID:                 disabled.Binding.ID,
		ExpectedRevision:         2,
		ExpectedRepositoryImpact: currentImpact,
		RetireWorkspaceBindings:  true,
		DeletedAt:                now,
	})
	if err != nil || deleted.Outcome != access.DeleteOutcomeCommitted {
		t.Fatalf("confirmed workspace retirement result=%+v err=%v", deleted, err)
	}
	if _, err := store.WorkspaceRouteRepository().Get(
		t.Context(),
		currentWorkspaceBindingID(t, disabled.Binding.ID),
	); !errors.Is(err, workspaceroute.ErrBindingNotFound) {
		t.Fatalf("workspace binding survived Access deletion: %v", err)
	}
}

func TestAccessDeletionBlocksDurableProxyClientProfileReferences(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 5, 11, 15, 0, 0, time.UTC)
	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.AccessRepository()
	mutation := accessMutation(t, "access-delete-proxy-client", 0, "Remote route")
	if _, err := repository.CompareAndSwap(t.Context(), mutation); err != nil {
		t.Fatal(err)
	}
	disabled := mutation.Candidate.Clone()
	disabled.Binding.Revision = 2
	disabled.Binding.Status = access.AccessStatusDisabled
	if _, err := repository.CompareAndSwap(t.Context(), access.Mutation{
		ExpectedRevision: 1,
		Candidate:        disabled,
	}); err != nil {
		t.Fatal(err)
	}
	policy, err := proxyclient.NewBindingPolicy(
		[]string{"agent-endpoints"},
		[]string{disabled.Binding.ProfileIDs[0].String()},
		"quota-default",
		[]controlprincipal.GrantKind{controlprincipal.GrantCaptureRun},
	)
	if err != nil {
		t.Fatal(err)
	}
	manager := newProxyClientManager(
		t,
		store.ProxyClientRepository(),
		&proxyClientClock{now: now},
		&proxyClientRandom{},
	)
	binding, err := manager.CreateBinding(
		t.Context(),
		proxyclient.CreateBindingCommand{
			DisplayName: "Remote team",
			Policy:      policy,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	inspection, exists, err := repository.InspectDeletion(
		t.Context(),
		disabled.Binding.ID,
		now,
	)
	if err != nil || !exists || len(inspection.ProxyClientReferences) != 1 {
		t.Fatalf("deletion inspection exists=%t value=%+v err=%v", exists, inspection, err)
	}
	reference := inspection.ProxyClientReferences[0]
	if reference.BindingID != binding.ID || reference.Revision != 1 {
		t.Fatalf("proxy-client reference=%+v binding=%+v", reference, binding)
	}
	impact, err := inspection.RepositoryImpactToken()
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.Delete(t.Context(), access.DeleteMutation{
		AccessID:                 disabled.Binding.ID,
		ExpectedRevision:         2,
		ExpectedRepositoryImpact: impact,
		DeletedAt:                now,
	})
	if err != nil || result.Outcome != access.DeleteOutcomeBlocked {
		t.Fatalf("referenced Access deletion result=%+v err=%v", result, err)
	}
	if _, exists, err := repository.Load(t.Context(), disabled.Binding.ID); err != nil || !exists {
		t.Fatalf("referenced Access survived=%t err=%v", exists, err)
	}
}

func TestAccessDeletionReconcilesCommitDriverOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		committer   transactionCommitter
		wantOutcome access.DeleteOutcome
		wantError   bool
		wantExists  bool
	}{
		{
			name:        "committed_then_error",
			committer:   commitThenError{},
			wantOutcome: access.DeleteOutcomeCommitted,
			wantExists:  false,
		},
		{
			name:        "rolled_back_then_error",
			committer:   rollbackThenError{},
			wantOutcome: access.DeleteOutcomeNotCommitted,
			wantError:   true,
			wantExists:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
			defer shutdownTestStore(t, store)
			repository := store.AccessRepository()
			mutation := accessMutation(t, "access-delete-"+test.name, 0, "Delete outcome")
			if _, err := repository.CompareAndSwap(t.Context(), mutation); err != nil {
				t.Fatal(err)
			}
			disabled := mutation.Candidate.Clone()
			disabled.Binding.Revision = 2
			disabled.Binding.Status = access.AccessStatusDisabled
			if _, err := repository.CompareAndSwap(t.Context(), access.Mutation{
				ExpectedRevision: 1,
				Candidate:        disabled,
			}); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 8, 5, 11, 30, 0, 0, time.UTC)
			inspection, exists, err := repository.InspectDeletion(
				t.Context(),
				disabled.Binding.ID,
				now,
			)
			if err != nil || !exists {
				t.Fatalf("inspect deletion exists=%t err=%v", exists, err)
			}
			impact, err := inspection.RepositoryImpactToken()
			if err != nil {
				t.Fatal(err)
			}
			store.accessRepo.committer = test.committer
			result, deleteErr := repository.Delete(t.Context(), access.DeleteMutation{
				AccessID:                 disabled.Binding.ID,
				ExpectedRevision:         2,
				ExpectedRepositoryImpact: impact,
				DeletedAt:                now,
			})
			if result.Outcome != test.wantOutcome || (deleteErr != nil) != test.wantError {
				t.Fatalf("delete result=%+v err=%v", result, deleteErr)
			}
			_, exists, err = repository.Load(t.Context(), disabled.Binding.ID)
			if err != nil || exists != test.wantExists {
				t.Fatalf("post-delete exists=%t err=%v", exists, err)
			}
		})
	}
}

func createWorkspaceRoute(
	t *testing.T,
	store *Store,
	aggregate access.Aggregate,
	now time.Time,
) {
	t.Helper()
	machineID, workspaceID := deletionWorkspaceIDs(t)
	bindingID, err := workspaceroute.BindingIDFor(
		aggregate.Binding.ID,
		machineID,
		workspaceID,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.WorkspaceRouteRepository().ResolveOrCreate(
		t.Context(),
		workspaceroute.CreateRequest{
			ID:                          bindingID,
			AccessID:                    aggregate.Binding.ID,
			MachineID:                   machineID,
			WorkspaceID:                 workspaceID,
			MachineRegistrationRevision: 1,
			WorkspaceLabel:              "payments-api",
			WorkspaceEvidence:           workspaceidentity.EvidenceLocalLauncher,
			ProfileID:                   aggregate.Binding.ProfileIDs[0],
			UpdatedAt:                   now,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func currentWorkspaceBindingID(
	t *testing.T,
	accessID access.AccessID,
) workspaceroute.BindingID {
	t.Helper()
	machineID, workspaceID := deletionWorkspaceIDs(t)
	bindingID, err := workspaceroute.BindingIDFor(accessID, machineID, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	return bindingID
}

func deletionWorkspaceIDs(
	t *testing.T,
) (workspaceidentity.MachineID, workspaceidentity.WorkspaceID) {
	t.Helper()
	machineRaw := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	workspaceRaw := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	machineID, err := workspaceidentity.ParseMachineID(machineRaw)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID, err := workspaceidentity.ParseWorkspaceID(workspaceRaw)
	if err != nil {
		t.Fatal(err)
	}
	return machineID, workspaceID
}
