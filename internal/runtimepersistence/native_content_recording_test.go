package runtimepersistence

import (
	"bytes"
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeReceiveReturnsOriginalRecordingCoordinates(t *testing.T) {
	s, id, _ := nativeLifecycleFixtureRecording(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 30})
	op, err := s.NativeToolRepository().Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	result, err := s.NativeToolRepository().Receive(context.Background(), nativetool.ReceiveInput{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Capture: op.Capture, EnvironmentID: op.EnvironmentID, EnvironmentRevision: op.EnvironmentRevision, EnvironmentDigest: op.EnvironmentDigest, ConnectionID: "recording-receive", TargetOrigin: op.TargetOrigin, Method: "POST", Kind: nativetool.RequestPing, Recording: op.Recording, ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if result.RecordingScope != op.RecordingScope || !result.CreatedAt.Equal(time.UnixMilli(now.UnixMilli())) {
		t.Fatalf("fresh Receive lost original coordinates: %+v", result)
	}
}

func nativeRetiredContentFixture(t *testing.T) (*ObservationStore, nativetool.ContentObservation) {
	t.Helper()
	ctx := context.Background()
	directory := t.TempDir()
	control, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(directory, "control.db"), LayoutID: "content-layout", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, id, revision := nativeFixtureOnStore(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 3650}, true, nil, control.store, egressprofile.Direct())
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, control.store.NativeToolRepository(), id, now)
	if _, err := control.NativeToolRepository().Start(ctx, id, "retired-content", revision, now); err != nil {
		t.Fatal(err)
	}
	if err := control.NativeToolRepository().Finish(ctx, id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	op, err := control.NativeToolRepository().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.RetireNativeMetadata(ctx, now.Add(31*24*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := control.NativeToolRepository().Get(ctx, id); !errors.Is(err, nativetool.ErrNotFound) {
		t.Fatalf("control metadata did not retire: %v", err)
	}
	observations, err := OpenObservations(ctx, ObservationOptions{DatabasePath: filepath.Join(directory, "observations.db"), LayoutID: "content-layout", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observations.Shutdown(ctx) })
	identity := nativetool.ContentIdentity{OperationID: op.ID, Scope: op.RecordingScope, EnvironmentID: op.EnvironmentID, EnvironmentRevision: op.EnvironmentRevision, EnvironmentDigest: op.EnvironmentDigest, Kind: op.Kind, ToolName: op.ToolName, TargetOrigin: op.TargetOrigin, CredentialOwner: op.CredentialOwner, Recording: op.Recording, CreatedAt: op.CreatedAt, ExpiresAt: op.CreatedAt.Add(time.Duration(op.Recording.RetentionDays) * 24 * time.Hour)}
	return observations, nativetool.ContentObservation{Identity: identity, ObservedAt: now, Part: nativetool.ContentToolResult, Body: []byte(`{"content":[{"type":"text","text":"independent public body"}]}`)}
}
func TestNativeContentOnlySurvivesControlRetirement(t *testing.T) {
	observations, value := nativeRetiredContentFixture(t)
	identity := value.Identity
	ctx := context.Background()
	if err := observations.PutNativeContent(ctx, value); err != nil {
		t.Fatalf("body required retired control/history: %v", err)
	}
	got, err := observations.GetNativeContent(ctx, identity.OperationID, nativetool.ContentToolResult)
	if err != nil || got.Identity != identity || !bytes.Equal(got.Body, value.Body) {
		t.Fatalf("original body identity/expiry: %+v %v", got, err)
	}
	if _, err := observations.GetNativeHistory(ctx, identity.OperationID); !errors.Is(err, nativetool.ErrNotFound) {
		t.Fatalf("body fabricated lifecycle history: %v", err)
	}
	page, err := observations.PageNativeInspection(ctx, nativetool.InspectionIndexRequest{Capture: identity.Scope.Capture, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].HistoryRevision != 0 {
		t.Fatalf("body-only discovery: %+v %v", page, err)
	}
	if err := observations.PutNativeContent(ctx, value); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	repeat, err := observations.PageNativeInspection(ctx, nativetool.InspectionIndexRequest{Capture: identity.Scope.Capture, AfterPosition: page.NextPosition, Limit: 10})
	if err != nil || len(repeat.Items) != 0 {
		t.Fatal("duplicate republished index", err)
	}
	bad := value
	bad.Body = []byte(`{"content":[]}`)
	if err := observations.PutNativeContent(ctx, bad); !errors.Is(err, nativetool.ErrConflict) {
		t.Fatalf("conflicting body replaced: %v", err)
	}
	bad = value
	bad.Identity.ToolName = "another"
	if err := observations.PutNativeContent(ctx, bad); !errors.Is(err, nativetool.ErrConflict) {
		t.Fatalf("conflicting identity replaced: %v", err)
	}
	bad = value
	bad.Identity.Scope.Fence.CaptureGeneration++
	if err := observations.PutNativeContent(ctx, bad); !errors.Is(err, nativetool.ErrConflict) {
		t.Fatalf("original scope replaced: %v", err)
	}
	bad = value
	bad.Identity.ExpiresAt = bad.Identity.ExpiresAt.Add(time.Hour)
	if err := observations.PutNativeContent(ctx, bad); err == nil {
		t.Fatal("original retention extended")
	}
}

func TestNativeObservationBodyGraphSharesRawRoots(t *testing.T) {
	for _, first := range []string{"raw", "native"} {
		t.Run(first, func(t *testing.T) {
			store, value := nativeRetiredContentFixture(t)
			ctx := context.Background()
			now := time.Now().UTC()
			if err := store.PutNativeContent(ctx, value); err != nil {
				t.Fatal(err)
			}
			raw := rawEvidenceRecordForTest("content-shared-raw", 1, rawevidence.LayerClientIngress, value.Body, []byte(`{"version":1,"headers":[]}`))
			raw.ObservedAt = now
			raw.ExpiresAt = now.Add(time.Hour)
			if err := store.RawEvidenceRepository().AppendBatch(ctx, []rawevidence.StoredEnvelope{raw}, now); err != nil {
				t.Fatal(err)
			}
			if countRows(t, store.store, "runtime_evidence_bodies") != 1 {
				t.Fatal("shared bytes duplicated")
			}
			purgeRaw := func() {
				t.Helper()
				if _, err := store.RawEvidenceRepository().BeginWriterSession(ctx, rawevidence.WriterSession{WriterID: "content-next", StartedAt: now.Add(2 * time.Hour), MaximumUnflushedTime: time.Millisecond}); err != nil {
					t.Fatal(err)
				}
			}
			purgeNative := func() {
				t.Helper()
				tx, err := store.store.database.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := deleteNativeContents(ctx, tx, `operation_id=?`, value.Identity.OperationID); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if first == "raw" {
				purgeRaw()
				if _, err := store.GetNativeContent(ctx, value.Identity.OperationID, value.Part); err != nil {
					t.Fatal("native graph lost", err)
				}
				purgeNative()
			} else {
				purgeNative()
				got, err := store.RawEvidenceRepository().GetEnvelope(ctx, raw.EnvelopeID)
				if err != nil || !bytes.Equal(got.Body, value.Body) {
					t.Fatal("Raw graph lost", err)
				}
				purgeRaw()
			}
			if countRows(t, store.store, "runtime_evidence_bodies") != 0 || countRows(t, store.store, "runtime_evidence_chunks") != 0 {
				t.Fatal("last optional graph root retained bytes")
			}
		})
	}
}

func TestNativeContentLateWriteDoesNotExtendOriginalExpiry(t *testing.T) {
	store, value := nativeRetiredContentFixture(t)
	ctx := context.Background()
	value.Identity.CreatedAt = time.Now().UTC().Truncate(time.Millisecond).Add(-48 * time.Hour)
	value.Identity.Recording.RetentionDays = 1
	value.Identity.ExpiresAt = value.Identity.CreatedAt.Add(24 * time.Hour)
	value.ObservedAt = value.Identity.CreatedAt
	for range 2 {
		if err := store.PutNativeContent(ctx, value); !errors.Is(err, nativetool.ErrNotFound) {
			t.Fatalf("late write refreshed frozen expiry: %v", err)
		}
	}
	if countRows(t, store.store, "native_tool_contents") != 0 || countRows(t, store.store, "runtime_evidence_bodies") != 0 {
		t.Fatal("expired original content retained body graph")
	}
}
