package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

type captureRecordingProbe struct {
	epoch        *recording.Epoch
	advance      bool
	generation   uint64
	scopes       []recording.Scope
	observations []recording.CaptureObservation
}

func (p *captureRecordingProbe) ScopeStamp(c captureidentity.Reference) recording.Stamp {
	if p.advance {
		p.epoch.Advance(2)
	}
	stamp, _ := recording.NewStamp(p.epoch, c, recording.Fence{ArchiveEpoch: p.epoch.Value(), CaptureGeneration: max(p.generation, 1)})
	return stamp
}
func (p *captureRecordingProbe) ObserveCapture(stamp recording.Stamp, v recording.CaptureObservation) recording.OfferResult {
	p.scopes = append(p.scopes, stamp.Scope())
	p.observations = append(p.observations, v)
	return recording.DroppedUnavailable
}

func TestCaptureAuthorizationIgnoresFailedDisplayWrites(t *testing.T) {
	ctx := context.Background()
	epoch, _ := recording.NewEpoch(1)
	probe := &captureRecordingProbe{epoch: epoch}
	control, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "capture-display", CaptureObserver: probe, RecordingScopes: probe, BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Shutdown(ctx)
	clock := &manualCaptureClock{now: time.Now().UTC()}
	options := capturerun.DefaultOptions(control.CaptureRunRepository())
	options.Clock = clock
	runs, err := capturerun.NewManager(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer runs.Shutdown(ctx)
	grant, err := runs.Create(ctx, capturerun.CreateCommand{CWD: "/workspace", CanonicalExecutablePath: "/bin/test-agent", ExecutableLabel: "codex", CatalogRevision: 1, Lifetime: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manual := newManualCaptureManager(t, control.store, clock, &manualCaptureRandom{})
	defer manual.Shutdown(ctx)
	create := func(lifetime manualcapture.Lifetime, duration time.Duration) manualcapture.Grant {
		t.Helper()
		value, err := manual.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Desktop", ClientClass: manualcapture.ClientDesktopApp, Lifetime: lifetime, ExpiresIn: duration})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	active := create(manualcapture.LifetimeUntilRevoked, 0)
	temporary := create(manualcapture.LifetimeTemporary, time.Minute)
	for _, table := range []string{"capture_runs", "manual_captures"} {
		_, err = control.store.database.Exec(`CREATE TRIGGER fail_` + table + ` BEFORE UPDATE OF observation ON ` + table + ` BEGIN SELECT RAISE(FAIL,'display unavailable'); END`)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runs.AuthorizeProxy(ctx, grant.ProxyCapability); err != nil {
		t.Fatalf("valid run refused by display storage: %v", err)
	}
	if _, err := manual.AuthorizeProxy(ctx, active.Credential); err != nil {
		t.Fatalf("valid manual refused by display storage: %v", err)
	}
	clock.Set(clock.Now().Add(2 * time.Minute))
	if _, err := manual.AuthorizeProxy(ctx, temporary.Credential); !errors.Is(err, manualcapture.ErrCredentialRejected) {
		t.Fatalf("expired credential admitted: %v", err)
	}
	var state string
	if err := control.store.database.QueryRow(`SELECT state FROM manual_captures WHERE capture_id=?`, temporary.Capture.ID).Scan(&state); err != nil || state != "expired" {
		t.Fatalf("expiry control transition lost: %s %v", state, err)
	}
	if len(probe.observations) != 2 || probe.observations[0].CredentialRevision != 0 || probe.observations[1].CredentialRevision != 1 || probe.scopes[0].Capture.ID != grant.Run.ID || probe.scopes[1].Capture.ID != active.Capture.ID {
		t.Fatalf("admitted authentication observations: %+v %+v", probe.scopes, probe.observations)
	}
	probe.advance = true
	if _, err := runs.AuthorizeProxy(ctx, grant.ProxyCapability); err != nil {
		t.Fatal(err)
	}
	if _, err := manual.AuthorizeProxy(ctx, active.Credential); err != nil {
		t.Fatal(err)
	}
	if len(probe.observations) != 2 {
		t.Fatal("old admission relabeled with a new recording epoch")
	}
	probe.advance = false
	probe.epoch, _ = recording.NewEpoch(1)
	probe.generation = 2
	if _, err := manual.AuthorizeProxy(ctx, active.Credential); err != nil {
		t.Fatal(err)
	}
	if len(probe.observations) != 2 {
		t.Fatal("old admission relabeled with a new capture generation")
	}
	id, _ := manualcapture.ParseID(active.Capture.ID)
	if _, err := manual.Revoke(ctx, manualcapture.RevokeCommand{Owner: manualcapture.NewLocalOwnerScope(), ID: id, ExpectedCredentialRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := manual.AuthorizeProxy(ctx, active.Credential); !errors.Is(err, manualcapture.ErrCredentialRejected) {
		t.Fatalf("revocation lost: %v", err)
	}
	if len(probe.observations) != 2 {
		t.Fatal("rejected authority emitted observation")
	}
}

func TestObservationRoleActivitySearchHasNoControlDependency(t *testing.T) {
	ctx := context.Background()
	observations, err := OpenObservations(ctx, ObservationOptions{DatabasePath: filepath.Join(t.TempDir(), "observations.db"), LayoutID: "search-layout", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer observations.Shutdown(ctx)
	now := time.Now().UTC().Truncate(time.Millisecond)
	scope := recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "run-search"}, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}
	record := activity.Record{ID: "activity-search", OccurredAt: now, Kind: activity.KindExchangeCompleted, SubjectID: "exchange-search", Status: activity.StatusSucceeded, SourceKind: activity.SourceCaptureRun, SourceDisplayName: "frozen-agent", SourceRecognition: activity.SourceRecognitionConfigured, CaptureRunID: "run-search", ConnectionID: "connection-search"}
	record.WorkspaceID, record.WorkspaceLabel, record.CaptureLabel = "workspace-frozen", "Frozen workspace", "frozen-executable"
	setFrozenExecutionEvidence(&record, "search")
	if _, err := observations.ActivityRepository().(activity.ScopedRepository).AppendScoped(ctx, scope, record); err != nil {
		t.Fatal(err)
	}
	// Newer unrelated rows must not crowd a matching row out before filtering.
	other := record
	other.ID = "other"
	other.SubjectID = "other-exchange"
	other.CaptureRunID = "other-run"
	other.WorkspaceID = "other"
	other.WorkspaceLabel = "Other"
	other.CaptureLabel = "other-executable"
	other.SourceDisplayName = "other-agent"
	otherScope := scope
	otherScope.Capture.ID = "other-run"
	if _, err := observations.ActivityRepository().(activity.ScopedRepository).AppendScoped(ctx, otherScope, other); err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"workspace-frozen", "Frozen workspace", "frozen-executable", "frozen-agent"} {
		page, err := observations.ActivityRepository().SearchExchanges(ctx, activity.SearchRequest{Query: activity.SearchQuery{Text: needle, Limit: 1}, ContentAvailableAt: now})
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("real observation search %q: %+v %v", needle, page, err)
		}
	}
}

func TestCaptureObservationPreservesScopeAndCredentialRevision(t *testing.T) {
	ctx := context.Background()
	owner, err := OpenObservations(ctx, ObservationOptions{DatabasePath: filepath.Join(t.TempDir(), "observations.db"), LayoutID: "capture-layout", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Shutdown(ctx)
	scope := recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: "manual-test"}, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, v := range []recording.CaptureObservation{{ObservedAt: now, CredentialRevision: 1}, {ObservedAt: now.Add(-time.Minute), CredentialRevision: 1}, {ObservedAt: now.Add(time.Hour), CredentialRevision: 2}} {
		if err := owner.PutCaptureObservation(ctx, scope, v); err != nil {
			t.Fatal(err)
		}
	}
	times, err := owner.CaptureObservation(ctx, scope, 1)
	if err != nil || !times.First.Equal(now.Add(-time.Minute)) || !times.Last.Equal(now) {
		t.Fatalf("revision boundary: %+v %v", times, err)
	}
	scope.Fence.ArchiveEpoch++
	if _, err := owner.CaptureObservation(ctx, scope, 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign original scope exposed: %v", err)
	}
}
