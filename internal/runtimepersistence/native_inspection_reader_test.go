package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/recording"
)

func nativeInspectionFixture(t *testing.T) (*NativeInspectionReader, *ObservationStore, *ControlStore, nativetool.ContentObservation) {
	t.Helper()
	options, current := inspectionSourcesFixture(t)
	control := inspectionControlFixture(t)
	capture := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "native-inspection"}
	nativeExec(t, control.store.database, `INSERT INTO recording_capture_fences VALUES('managed_run','native-inspection',1,0)`)
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close(context.Background()) })
	now := time.Now().UTC().Truncate(time.Millisecond)
	value := nativetool.ContentObservation{Identity: nativetool.ContentIdentity{OperationID: nativetool.ID(strings.Repeat("a", 32)), Scope: recording.Scope{Capture: capture, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}, EnvironmentID: "env", EnvironmentRevision: 1, EnvironmentDigest: environment.CandidateDigest{1}, Kind: nativetool.RequestToolsCall, ToolName: "test", TargetOrigin: "https://example.com", CredentialOwner: "original_client", Recording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}, ObservedAt: now, Part: nativetool.ContentToolResult, Body: []byte(`{"content":[{"type":"text","text":"retained"}]}`)}
	return NewNativeInspectionReader(catalog, control), current, control, value
}
func TestNativeInspectionContentOnlyTailAndVisibility(t *testing.T) {
	r, current, control, v := nativeInspectionFixture(t)
	ctx := context.Background()
	if err := current.PutNativeContent(ctx, v); err != nil {
		t.Fatal(err)
	}
	q := nativetool.InspectionPageRequest{Capture: v.Identity.Scope.Capture, Limit: 1}
	page, err := r.Page(ctx, q)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("content-only discovery: %+v %v", page, err)
	}
	op := page.Items[0]
	if op.Operation.State != "unknown" || op.Operation.Revision != 0 || op.Result == nil || op.Result.ObservedAt == nil || !op.Result.ObservedAt.Equal(v.ObservedAt) {
		t.Fatalf("fabricated lifecycle or lost observed time: %+v", op)
	}
	q.Cursor = page.NextCursor
	empty, err := r.Page(ctx, q)
	if err != nil || len(empty.Items) != 0 || empty.NextCursor != q.Cursor {
		t.Fatalf("empty poll reset tail: %+v %v", empty, err)
	}
	got, err := r.ReadContent(ctx, nativetool.InspectionContentRequest{Token: op.Token, Capture: q.Capture, OperationID: v.Identity.OperationID, Part: v.Part, Descriptor: *op.Result, Limit: 128 << 10})
	if err != nil || string(got) != string(v.Body) {
		t.Fatalf("separate body read: %v", err)
	}
	v.Identity.OperationID = nativetool.ID(strings.Repeat("b", 32))
	if err = current.PutNativeContent(ctx, v); err != nil {
		t.Fatal(err)
	}
	next, err := r.Page(ctx, q)
	if err != nil || len(next.Items) != 1 || next.Items[0].Operation.ID != v.Identity.OperationID {
		t.Fatalf("current append lost: %+v %v", next, err)
	}

	terminal := next.Items[0].Operation
	completed := time.Now().UTC().Truncate(time.Millisecond)
	terminal.Revision = 2
	terminal.State = "completed"
	terminal.Reason = "completed"
	terminal.DecisionSource = "none"
	terminal.CompletedAt = &completed
	terminal.MaximumEventAt = completed
	terminal.MetadataExpiresAt = terminal.CreatedAt.Add(30 * 24 * time.Hour)
	if err = current.PutNativeHistory(ctx, nativetool.RecordedHistory{Scope: terminal.RecordingScope, Observation: nativetool.HistoryObservation{Operation: terminal, EventKind: "completed", EventAt: completed, MaximumEventAt: completed, MetadataExpiresAt: terminal.MetadataExpiresAt}}); err != nil {
		t.Fatal(err)
	}
	q.Cursor = next.NextCursor
	updated, err := r.Page(ctx, q)
	if err != nil || len(updated.Items) != 1 || updated.Items[0].Operation.ID != terminal.ID || updated.Items[0].Operation.State != "completed" {
		t.Fatalf("late history publication not discoverable: %+v %v", updated, err)
	}
	nativeExec(t, control.store.database, `UPDATE recording_capture_fences SET deleted=1; UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
	if _, err = r.Page(ctx, q); !errors.Is(err, ErrInspectionCursor) {
		t.Fatalf("old visibility cursor admitted: %v", err)
	}
	if _, err = r.Get(ctx, q.Capture, v.Identity.OperationID); !errors.Is(err, nativetool.ErrNotFound) {
		t.Fatalf("deleted scope visible: %v", err)
	}
	if err = r.Check(ctx, op.Token); !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("outer guard missed delete: %v", err)
	}
}

// This copies released-format historical bytes before the catalog's first
// acquisition. No archived authority or live lifecycle method serves reads.
func nativeInspectionLegacyFixture(t *testing.T, revisions ...int) (*NativeInspectionReader, *ObservationStore, *ControlStore, nativetool.Operation) {
	t.Helper()
	ctx := context.Background()
	old, id, _ := nativeLifecycleFixtureRecording(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 30})
	seedNativeHistoricalCompletion(t, old, id, time.Now().UTC().Truncate(time.Millisecond))
	op, err := old.NativeToolRepository().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	path, db := legacyNativeFixture(t, old)
	nativeExec(t, db, `UPDATE native_tool_events SET sequence=sequence+1000`)
	if len(revisions) > 0 && revisions[0] == 3 {
		nativeExec(t, db, migration002To003SQL)
		nativeExec(t, db, `UPDATE runtime_metadata SET schema_revision=3`)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := storeEvidenceBody(ctx, tx, []byte("legacy retained body"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO native_tool_contents VALUES(?,?,?,?)`, id, "tool_result", digest, op.CreatedAt.Add(30*24*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	options, current := inspectionSourcesFixture(t)
	if len(revisions) > 0 {
		options.Sources[0].Database.Revision = int64(revisions[0])
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(options.Root, "archives/legacy/runtime.db"), data, 0600); err != nil {
		t.Fatal(err)
	}
	control := inspectionControlFixture(t)
	nativeExec(t, control.store.database, `INSERT INTO recording_capture_fences VALUES(?,?,1,0)`, op.Capture.Kind, op.Capture.ID)
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close(ctx) })
	return NewNativeInspectionReader(catalog, control), current, control, op
}
func TestNativeInspectionLegacyTailDoesNotSkipFirstCurrentPublication(t *testing.T) {
	r, current, _, op := nativeInspectionLegacyFixture(t)
	ctx := context.Background()
	q := nativetool.InspectionPageRequest{Capture: op.Capture, Limit: 199}
	first, err := r.Page(ctx, q)
	if err != nil || len(first.Items) == 0 {
		t.Fatalf("legacy first: %+v %v", first, err)
	}
	var retained *nativetool.InspectionOperation
	for i := range first.Items {
		if first.Items[i].Operation.ID == op.ID {
			retained = &first.Items[i]
		}
	}
	if retained == nil || retained.Operation.State != "completed" || retained.Result == nil || retained.Result.ObservedAt != nil {
		t.Fatalf("legacy facts changed: %+v", retained)
	}

	q.Cursor = first.NextCursor
	tail, err := r.Page(ctx, q)
	if err != nil || len(tail.Items) != 0 {
		t.Fatalf("legacy tail: %v", err)
	}
	identity := nativetool.ContentIdentity{OperationID: nativetool.ID(strings.Repeat("b", 32)), Scope: op.RecordingScope, EnvironmentID: op.EnvironmentID, EnvironmentRevision: op.EnvironmentRevision, EnvironmentDigest: op.EnvironmentDigest, Kind: op.Kind, ToolName: op.ToolName, TargetOrigin: op.TargetOrigin, CredentialOwner: op.CredentialOwner, Recording: op.Recording, CreatedAt: op.CreatedAt, ExpiresAt: op.CreatedAt.Add(30 * 24 * time.Hour)}
	if err = current.PutNativeContent(ctx, nativetool.ContentObservation{Identity: identity, ObservedAt: time.Now(), Part: nativetool.ContentToolResult, Body: []byte("new body")}); err != nil {
		t.Fatal(err)
	}
	q.Cursor = tail.NextCursor
	next, err := r.Page(ctx, q)
	if err != nil || len(next.Items) != 1 || next.Items[0].Operation.ID != identity.OperationID {
		t.Fatalf("archive local position skipped first current publication: %+v %v", next, err)
	}
}

func TestNativeInspectionLatestSnapshotDeduplicatesBeforeCursorAndBodyStaysLazy(t *testing.T) {
	r, current, _, op := nativeInspectionLegacyFixture(t)
	ctx := context.Background()
	// A higher independent terminal snapshot wins before source pagination;
	// archived event positions do not expose obsolete completed projections.
	op.Revision++
	op.State = "canceled"
	op.Reason = "canceled"
	h := nativetool.RecordedHistory{Scope: op.RecordingScope, Observation: nativetool.HistoryObservation{Operation: op, EventKind: "canceled", EventAt: *op.CompletedAt, MaximumEventAt: op.MaximumEventAt, MetadataExpiresAt: op.MetadataExpiresAt}}
	if err := current.PutNativeHistory(ctx, h); err != nil {
		t.Fatal(err)
	}
	q := nativetool.InspectionPageRequest{Capture: op.Capture, Limit: 1}
	count := 0
	for i := 0; i < 20; i++ {
		page, err := r.Page(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range page.Items {
			if v.Operation.ID == op.ID {
				count++
				if v.Operation.State != "canceled" || v.Operation.Revision != op.Revision {
					t.Fatal("obsolete projection escaped before winner")
				}
				if v.Result == nil || v.Result.ObservedAt != nil {
					t.Fatal("legacy body lost")
				}
				body, err := r.ReadContent(ctx, nativetool.InspectionContentRequest{Token: v.Token, Capture: op.Capture, OperationID: op.ID, Part: nativetool.ContentToolResult, Descriptor: *v.Result, Limit: 128 << 10})
				if err != nil || string(body) != "legacy retained body" {
					t.Fatalf("legacy body read: %v", err)
				}
			}
		}
		if page.NextCursor == q.Cursor {
			break
		}
		q.Cursor = page.NextCursor
	}
	if count != 1 {
		t.Fatalf("duplicate operation count %d", count)
	}
	// A catalog generation change rejects a source-qualified body reference.
	before, err := r.Get(ctx, op.Capture, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	maintenance, err := r.catalog.BeginMaintenance(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	maintenance.Close()
	if _, err = r.ReadContent(ctx, nativetool.InspectionContentRequest{Token: before.Token, Capture: op.Capture, OperationID: op.ID, Part: nativetool.ContentToolResult, Descriptor: *before.Result, Limit: 128 << 10}); !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("stale source body reference accepted: %v", err)
	}
}
func testNativeInspectionDescriptorsNeverLoadMalformedBodies(t *testing.T) {
	r, current, _, v := nativeInspectionFixture(t)
	ctx := context.Background()
	if err := current.PutNativeContent(ctx, v); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, current.store.database, `UPDATE runtime_evidence_bodies SET plain_bytes=999`)
	op, err := r.Get(ctx, v.Identity.Scope.Capture, v.Identity.OperationID)
	if err != nil || op.Result == nil || op.Result.Digest != nativetool.Digest(sha256.Sum256(v.Body)) || !op.Result.ExpiresAt.Equal(v.Identity.ExpiresAt) || op.Arguments != nil || op.Operation.Recording != v.Identity.Recording {
		t.Fatalf("descriptor loaded body or lost frozen metadata: %+v %v", op, err)
	}
	if _, err = r.Page(ctx, nativetool.InspectionPageRequest{Capture: op.Operation.Capture, Limit: 10}); err != nil {
		t.Fatalf("page loaded body: %v", err)
	}
	if _, err = r.ReadContent(ctx, nativetool.InspectionContentRequest{Token: op.Token, Capture: op.Operation.Capture, OperationID: op.Operation.ID, Part: v.Part, Descriptor: *op.Result, Limit: 128 << 10}); err == nil {
		t.Fatal("malformed body disclosed")
	}
}

func TestNativeInspectionConflictingImmutableBodyRefusesComposite(t *testing.T) {
	r, current, _, op := nativeInspectionLegacyFixture(t)
	ctx := context.Background()
	identity := nativetool.ContentIdentity{OperationID: op.ID, Scope: op.RecordingScope, EnvironmentID: op.EnvironmentID, EnvironmentRevision: op.EnvironmentRevision, EnvironmentDigest: op.EnvironmentDigest, Kind: op.Kind, ToolName: op.ToolName, TargetOrigin: op.TargetOrigin, CredentialOwner: op.CredentialOwner, Recording: op.Recording, CreatedAt: op.CreatedAt, ExpiresAt: op.CreatedAt.Add(30 * 24 * time.Hour)}
	if err := current.PutNativeContent(ctx, nativetool.ContentObservation{Identity: identity, ObservedAt: time.Now(), Part: nativetool.ContentToolResult, Body: []byte("conflicting immutable body")}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, op.Capture, op.ID); !errors.Is(err, nativetool.ErrConflict) {
		t.Fatalf("different immutable body silently selected: %v", err)
	}
}
func TestNativeInspectionRetainsExpiredDescriptorForKnownLifecycle(t *testing.T) {
	r, current, _, v := nativeInspectionFixture(t)
	ctx := context.Background()
	if err := current.PutNativeContent(ctx, v); err != nil {
		t.Fatal(err)
	}
	bodyOnly, err := r.Get(ctx, v.Identity.Scope.Capture, v.Identity.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	op := bodyOnly.Operation
	completed := op.CreatedAt
	op.State = "completed"
	op.Revision = 2
	op.DecisionSource = "none"
	op.Reason = "completed"
	op.CompletedAt = &completed
	op.MaximumEventAt = completed
	op.MetadataExpiresAt = completed.Add(30 * 24 * time.Hour)
	if err = current.PutNativeHistory(ctx, nativetool.RecordedHistory{Scope: op.RecordingScope, Observation: nativetool.HistoryObservation{Operation: op, EventKind: "completed", EventAt: completed, MaximumEventAt: completed, MetadataExpiresAt: op.MetadataExpiresAt}}); err != nil {
		t.Fatal(err)
	}
	s, err := r.begin(ctx, op.Capture)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	got, err := r.get(s, op.Capture, op.ID, completed.Add(2*24*time.Hour))
	if err != nil || got.Result == nil || !got.Result.ExpiresAt.Equal(v.Identity.ExpiresAt) {
		t.Fatalf("expired original descriptor lost: %+v %v", got, err)
	}
}

func TestNativeInspectionReadContentRechecksOriginalExpiryAfterFinalVisibility(t *testing.T) {
	r, current, control, v := nativeInspectionFixture(t)
	// Complete cold attachment/reference setup before authoring the original
	// short lifetime, without pinning the archive's pre-authoring identity.
	warmInspectionFixtureReferences(t, r.catalog)

	r.catalog.timeout = 30 * time.Second
	v.Identity.ExpiresAt = time.Now().UTC().Truncate(time.Millisecond).Add(2 * time.Second)
	v.Identity.CreatedAt = v.Identity.ExpiresAt.Add(-24 * time.Hour)
	v.ObservedAt = v.Identity.CreatedAt
	if err := current.PutNativeContent(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	op, err := r.Get(context.Background(), v.Identity.Scope.Capture, v.Identity.OperationID)
	if err != nil || op.Result == nil {
		t.Fatalf("original descriptor: %+v %v", op, err)
	}
	checks := 0
	r.visibility = &nativeFinalExpiryVisibility{ControlStore: control, beforeCheck: func() {
		checks++
		if checks == 2 {
			if !time.Now().Before(op.Result.ExpiresAt) {
				t.Fatal("fixture expired before final check")
			}
			time.Sleep(time.Until(op.Result.ExpiresAt) + time.Millisecond)
		}
	}}
	body, err := r.ReadContent(context.Background(), nativetool.InspectionContentRequest{Token: op.Token, Capture: op.Operation.Capture, OperationID: op.Operation.ID, Part: v.Part, Descriptor: *op.Result, Limit: 128 << 10})
	if checks != 2 || !errors.Is(err, ErrInspectionChanged) || len(body) != 0 {
		t.Fatalf("original expiry crossed final check: checks=%d body=%q err=%v", checks, body, err)
	}
}

type nativeFinalExpiryVisibility struct {
	*ControlStore
	beforeCheck func()
}

func (v *nativeFinalExpiryVisibility) RecheckInspectionStamp(ctx context.Context, s recording.InspectionStamp) error {
	v.beforeCheck()
	return v.ControlStore.RecheckInspectionStamp(ctx, s)
}

func TestNativeInspectionPublishedRuntime3PreservesHistoryAndBody(t *testing.T) {
	reader, _, _, op := nativeInspectionLegacyFixture(t, 3)
	page, err := reader.Page(t.Context(), nativetool.InspectionPageRequest{Capture: op.Capture, Limit: 199})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("native3 page %+v %v", page, err)
	}
	got, err := reader.Get(t.Context(), op.Capture, op.ID)
	if err != nil || got.Operation.ID != op.ID || got.Result == nil {
		t.Fatalf("native3 detail %+v %v", got, err)
	}
	body, err := reader.ReadContent(t.Context(), nativetool.InspectionContentRequest{Token: got.Token, Capture: op.Capture, OperationID: op.ID, Part: nativetool.ContentToolResult, Descriptor: *got.Result, Limit: 128 << 10})
	if err != nil || string(body) != "legacy retained body" {
		t.Fatalf("native3 body %q %v", body, err)
	}
}
