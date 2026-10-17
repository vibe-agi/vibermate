package runtimepersistence

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/recording"
	"path/filepath"
	"testing"
	"time"
)

func TestACPControlRegistrationNeedsNoSnapshotStore(t *testing.T) {
	ctx := context.Background()
	control, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "acp-layout", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Shutdown(ctx)
	runs, err := capturerun.NewManager(ctx, capturerun.DefaultOptions(control.CaptureRunRepository()))
	if err != nil {
		t.Fatal(err)
	}
	defer runs.Shutdown(ctx)
	create := func() capturerun.LaunchGrant {
		t.Helper()
		g, err := runs.Create(ctx, capturerun.CreateCommand{CWD: "/workspace", CanonicalExecutablePath: "/bin/test-agent", ExecutableLabel: "agent", CatalogRevision: 1, Lifetime: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	grant := create()
	now := time.Now().UnixMilli()
	repo := control.ACPRegistrations()
	input := acpobservation.Registration{RunID: grant.Run.ID, Policy: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 30}, ExpiresAtMillis: grant.Run.CreatedAt.Add(30 * 24 * time.Hour).UnixMilli()}
	first, err := repo.Register(ctx, input, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fence.ArchiveEpoch != 1 || first.Fence.CaptureGeneration != 1 {
		t.Fatalf("original registration fence: %+v", first)
	}
	repeated, err := repo.Register(ctx, input, now)
	if err != nil || repeated != first {
		t.Fatalf("registration retry: %+v %v", repeated, err)
	}
	markers, err := repo.Markers(ctx, []string{grant.Run.ID, "absent"})
	if err != nil || !markers[grant.Run.ID] || markers["absent"] {
		t.Fatalf("markers without snapshot: %+v %v", markers, err)
	}
	attached := create()
	if _, err := runs.Attach(ctx, attached.Run.ID, attached.ControlCapability, 42); err != nil {
		t.Fatal(err)
	}
	input.RunID = attached.Run.ID
	if _, err := repo.Register(ctx, input, now); !errors.Is(err, acpobservation.ErrConflict) {
		t.Fatalf("attached parent registered: %v", err)
	}
	if err := runs.Finish(ctx, grant.Run.ID, grant.ControlCapability); err != nil {
		t.Fatal(err)
	}
	input.RunID = grant.Run.ID
	if _, err := repo.Register(ctx, input, now); !errors.Is(err, capturerun.ErrCapabilityRejected) {
		t.Fatalf("retired parent registered: %v", err)
	}
}

func TestACPOptionalSnapshotHasNoControlParent(t *testing.T) {
	ctx := context.Background()
	s, err := OpenObservations(ctx, ObservationOptions{DatabasePath: filepath.Join(t.TempDir(), "obs.db"), LayoutID: "acp", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(ctx)
	r := acpobservation.Record{RunID: "run-acp", Scope: recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "run-acp"}, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}, Policy: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingMetadataOnly, RetentionDays: 30}, ExpiresAtMillis: time.Now().Add(time.Hour).UnixMilli(), Snapshot: acpobservation.Snapshot{Revision: 1}}
	if err := s.PutACP(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.PutACP(ctx, r); err != nil {
		t.Fatalf("duplicate snapshot: %v", err)
	}
	got, err := s.ACPSnapshots().Read(ctx, r.RunID, time.Now().UnixMilli())
	if err != nil || got.RunID != r.RunID || got.Scope != r.Scope {
		t.Fatalf("standalone snapshot: %+v %v", got, err)
	}
	r.Snapshot.Agent.Name = "conflict"
	if err := s.PutACP(ctx, r); !errors.Is(err, acpobservation.ErrConflict) {
		t.Fatalf("snapshot conflict overwritten: %v", err)
	}
}

type acpAssignmentResolver struct{ value captureassignment.Assignment }

func (r acpAssignmentResolver) Resolve(context.Context, captureidentity.Reference) (captureassignment.Assignment, error) {
	return r.value, nil
}

type absentACPSnapshots struct{ calls int }

func (r *absentACPSnapshots) Read(context.Context, string, int64) (acpobservation.Record, error) {
	r.calls++
	return acpobservation.Record{}, errors.New("snapshot store unavailable")
}
func (r *absentACPSnapshots) ObserveACP(recording.Stamp, acpobservation.Record) recording.OfferResult {
	return recording.DroppedUnavailable
}
func TestACPStartReturnsCommittedMarkerWithoutSnapshotRead(t *testing.T) {
	ctx := context.Background()
	control, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "acp-start", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Shutdown(ctx)
	runs, err := capturerun.NewManager(ctx, capturerun.DefaultOptions(control.CaptureRunRepository()))
	if err != nil {
		t.Fatal(err)
	}
	defer runs.Shutdown(ctx)
	grant, err := runs.Create(ctx, capturerun.CreateCommand{CWD: "/workspace", CanonicalExecutablePath: "/bin/test-agent", ExecutableLabel: "agent", CatalogRevision: 1, Lifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	environments := newEnvironmentManager(t, control.store)
	aggregate := environmentFixture(t, "acp-start", 1)
	draft, err := environments.SaveDraft(ctx, environment.DraftCommand{Candidate: aggregate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := environments.Preview(ctx, aggregate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := environments.Publish(ctx, preview); err != nil {
		t.Fatal(err)
	}
	snapshot, err := environments.Resolve(aggregate.ID)
	if err != nil {
		t.Fatal(err)
	}
	capture := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: grant.Run.ID}
	assignment := captureAssignmentFixture(capture, aggregate.ID, 1, captureassignment.SourceLaunch)
	assignment.EnvironmentDigest = snapshot.Digest()
	assignment.LaunchAuthority, err = environment.NewLaunchAuthorityBoundaryFromScopes(aggregate.ID, 1, snapshot.Digest(), []string{"api.example:443"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	missing := &absentACPSnapshots{}
	epoch, _ := recording.NewEpoch(1)
	scopes := &captureRecordingProbe{epoch: epoch}
	manager, err := acpobservation.NewManager(control.ACPRegistrations(), runs, acpAssignmentResolver{assignment}, environments, &manualCaptureClock{now: time.Now()}, acpobservation.RecordingOptions{Recorder: missing, Scopes: scopes, Reader: missing})
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.Start(ctx, grant.Run.ID, grant.ControlCapability, false)
	if err != nil || record.Snapshot.Revision != 1 || record.Snapshot.Final || missing.calls != 0 {
		t.Fatalf("startup touched optional storage: %+v %v reads=%d", record, err, missing.calls)
	}
	if err := manager.Publish(ctx, grant.Run.ID, grant.ControlCapability, record.Snapshot); err != nil {
		t.Fatalf("unavailable optional snapshot rejected publication: %v", err)
	}
	markers, err := manager.Markers(ctx, []string{grant.Run.ID})
	if err != nil || !markers[grant.Run.ID] || missing.calls != 0 {
		t.Fatalf("capture catalog consulted snapshots: %+v %v", markers, err)
	}
}
