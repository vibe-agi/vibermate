package runtimepersistence

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/clienttarget"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

func TestCaptureAssignmentRepositoryCASListAndReopen(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, databasePath)
	repository := store.CaptureAssignmentRepository()
	capture := captureAssignmentReference(t, captureidentity.KindManagedRun, "run.one")
	created := captureAssignmentFixture(capture, "work", 1, captureassignment.SourceLaunch)

	result, err := repository.Write(context.Background(), 0, created)
	if err != nil || result.Outcome != captureassignment.CommitOutcomeCommitted || result.Assignment != created {
		t.Fatalf("create = %+v, %v", result, err)
	}
	stale := captureAssignmentFixture(capture, "personal", 1, captureassignment.SourceLaunch)
	conflict, err := repository.Write(context.Background(), 0, stale)
	if err != nil || conflict.Outcome != captureassignment.CommitOutcomeConflict || conflict.Actual != 1 {
		t.Fatalf("stale write = %+v, %v", conflict, err)
	}
	current, exists, err := repository.Load(context.Background(), capture)
	if err != nil || !exists || current != created {
		t.Fatalf("load after conflict = %+v, exists=%t, err=%v", current, exists, err)
	}
	manual := captureAssignmentFixture(
		captureAssignmentReference(t, captureidentity.KindManualCapture, "manual.one"),
		"personal", 1, captureassignment.SourceManualCreate,
	)
	if result, err = repository.Write(context.Background(), 0, manual); err != nil || result.Outcome != captureassignment.CommitOutcomeCommitted {
		t.Fatalf("manual create = %+v, %v", result, err)
	}
	listed, err := repository.ListByEnvironment(context.Background(), "work", 10)
	if err != nil || len(listed) != 1 || listed[0] != created {
		t.Fatalf("work assignments = %+v, %v", listed, err)
	}
	listed, err = repository.ListByEnvironment(context.Background(), "personal", 10)
	if err != nil || len(listed) != 1 || listed[0] != manual {
		t.Fatalf("personal assignments = %+v, %v", listed, err)
	}

	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, databasePath)
	defer shutdownTestStore(t, reopened)
	recovered, exists, err := reopened.CaptureAssignmentRepository().Load(context.Background(), capture)
	if err != nil || !exists || recovered != created {
		t.Fatalf("recovered = %+v, exists=%t, err=%v", recovered, exists, err)
	}
}

func TestCaptureAssignmentCommitErrorIsReconciled(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		committer transactionCommitter
		committed bool
	}{
		{name: "commit then error", committer: commitThenError{}, committed: true},
		{name: "rollback then error", committer: rollbackThenError{}, committed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
			defer shutdownTestStore(t, store)
			store.captureAssignments.committer = test.committer
			repository := store.CaptureAssignmentRepository()
			capture := captureAssignmentReference(t, captureidentity.KindManagedRun, "run.reconcile")
			candidate := captureAssignmentFixture(capture, "work", 1, captureassignment.SourceLaunch)
			result, err := repository.Write(context.Background(), 0, candidate)
			if test.committed {
				if err != nil || result.Outcome != captureassignment.CommitOutcomeCommitted || result.Assignment != candidate {
					t.Fatalf("reconciled commit = %+v, %v", result, err)
				}
			} else if err == nil || result.Outcome != captureassignment.CommitOutcomeNotCommitted {
				t.Fatalf("reconciled rollback = %+v, %v", result, err)
			}
			current, exists, loadErr := repository.Load(context.Background(), capture)
			if loadErr != nil || exists != test.committed {
				t.Fatalf("load = %+v, exists=%t, err=%v", current, exists, loadErr)
			}
		})
	}
}

func TestCaptureAssignmentRepositoryCancellationAndShutdownRejectWrites(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	repository := store.CaptureAssignmentRepository()
	capture := captureAssignmentReference(t, captureidentity.KindManagedRun, "run.cancelled")
	candidate := captureAssignmentFixture(capture, "work", 1, captureassignment.SourceLaunch)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := repository.Write(cancelled, 0, candidate)
	if !errors.Is(err, context.Canceled) || result.Outcome != captureassignment.CommitOutcomeNotCommitted {
		t.Fatalf("cancelled write = %+v, %v", result, err)
	}
	if _, exists, loadErr := repository.Load(context.Background(), capture); loadErr != nil || exists {
		t.Fatalf("cancelled state exists=%t err=%v", exists, loadErr)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.Load(context.Background(), capture); !errors.Is(err, ErrStoreClosing) {
		t.Fatalf("load after shutdown = %v", err)
	}
}

func captureAssignmentReference(t *testing.T, kind captureidentity.Kind, id string) captureidentity.Reference {
	t.Helper()
	reference, err := captureidentity.New(kind, id)
	if err != nil {
		t.Fatal(err)
	}
	return reference
}

func captureAssignmentFixture(
	capture captureidentity.Reference,
	environmentID environment.EnvironmentID,
	revision captureassignment.Revision,
	source captureassignment.Source,
) captureassignment.Assignment {
	var initialDigest environment.CandidateDigest
	initialDigest[0] = 1
	launchAuthority, err := environment.NewLaunchAuthorityBoundaryFromScopes(
		environmentID, 1, initialDigest,
		[]string{"api.example:443", "relay.example:8443"},
		[]string{"relay.example:8443"},
	)
	if err != nil {
		panic(err)
	}
	assignment := captureassignment.Assignment{
		Capture: capture, EnvironmentID: environmentID, Revision: revision, Source: source,
		LaunchAuthority: launchAuthority,
		UpdatedAt:       time.Date(2026, 8, 7, 12, 0, int(revision), 0, time.UTC),
	}
	if capture.Kind == captureidentity.KindManagedRun {
		actual, actualErr := originidentity.ParseProviderOrigin("http://127.0.0.1:23333")
		canonical, canonicalErr := originidentity.ParseClientOrigin("https://api.anthropic.com")
		target, targetErr := clienttarget.New(actual, canonical)
		if actualErr != nil || canonicalErr != nil || targetErr != nil {
			panic("construct Capture assignment client target fixture")
		}
		assignment.ClientTarget = target
	}
	return assignment
}
