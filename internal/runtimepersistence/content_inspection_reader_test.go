package runtimepersistence

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestContentInspectionSelectsTerminalBeforeSourceAndKeepsGraphLocal(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	record := transcriptContentRecordFixture(t, "semantic", now, exchangecontent.ParentRef{CaptureRunID: f.scope.Capture.ID}, []transcriptMessage{{role: protocolcore.RoleUser, text: "original request"}}, "archived response")
	if err := newExchangeContentRepository(f.legacy, f.legacy, newOperationGate(), nil).Put(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	pending := record.Clone()
	pending.Response = nil
	pending.Presentation = exchangecontent.RequestPresentation{}
	limits, err := exchangecontent.DefaultSourceLimits()
	if err != nil {
		t.Fatal(err)
	}
	source, err := exchangecontent.SourceFromRecordWithin(limits, pending)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.current.ExchangeContentRepository().(exchangecontent.ScopedSourceRepository).PutScopedSource(context.Background(), f.scope, source); err != nil {
		t.Fatal(err)
	}
	reader, err := NewContentInspectionReader(f.reader.catalog, f.control, limits)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.GetProjection(context.Background(), record.ExchangeID, exchangecontent.RequestViewFull)
	if err != nil || got.Response == nil || len(got.Response.Blocks) != 1 || got.Response.Blocks[0].Text != "archived response" || got.Request.Messages[0].Blocks[0].Text != "original request" {
		t.Fatalf("independent semantic terminal: %+v %v", got, err)
	}
	// The selected source's missing graph must not fall back to equal current bytes.
	nativeExec(t, f.legacy, `DELETE FROM runtime_exchange_content_blocks`)
	if _, err = reader.GetProjection(context.Background(), record.ExchangeID, exchangecontent.RequestViewFull); err == nil {
		t.Fatal("missing selected-source blocks repaired from current")
	}
}

func TestContentInspectionNestedCursorsRemainSourceBound(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	messages := make([]transcriptMessage, 35)
	for i := range messages {
		messages[i] = transcriptMessage{role: protocolcore.RoleUser, text: fmt.Sprintf("request %d", i)}
	}
	record := transcriptContentRecordFixture(t, "semantic-pages", now, exchangecontent.ParentRef{CaptureRunID: f.scope.Capture.ID}, messages, strings.Repeat("reply🙂", 30000))
	if err := newExchangeContentRepository(f.legacy, f.legacy, newOperationGate(), nil).Put(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	limits, err := exchangecontent.DefaultSourceLimits()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewContentInspectionReader(f.reader.catalog, f.control, limits)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.GetPagedProjection(context.Background(), record.ExchangeID, exchangecontent.RequestViewFull)
	if err != nil || page.Page == nil || page.Page.RequestNextCursor == "" || page.Response == nil || len(page.Response.Blocks) != 1 || page.Response.Blocks[0].Deferred == nil {
		t.Fatalf("paged independent content: %+v %v", page, err)
	}
	requestCursor := page.Page.RequestNextCursor
	bodyCursor := page.Response.Blocks[0].Deferred.Cursor
	for _, cursor := range []string{requestCursor, bodyCursor} {
		if !strings.Contains(cursor, ".") || len(cursor) > exchangecontent.MaxPageCursorBytes {
			t.Fatalf("unbound/oversize cursor %q", cursor)
		}
	}
	// Publish an equal current copy after the archive cursor was issued. Its
	// publication preference must not silently redirect the retained cursor.
	source, err := exchangecontent.SourceFromRecordWithin(limits, record)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.current.ExchangeContentRepository().(exchangecontent.ScopedSourceRepository).PutScopedSource(context.Background(), f.scope, source); err != nil {
		t.Fatal(err)
	}
	// Current is the new canonical source, but its deliberately missing graph
	// cannot redirect an already-issued archive cursor. This discriminates the
	// decoder route without depending on immutable archive-file rejection.
	nativeExec(t, f.current.store.database, `PRAGMA foreign_keys=OFF;DELETE FROM runtime_exchange_content_blocks;PRAGMA foreign_keys=ON`)
	if _, err = reader.GetProjection(context.Background(), record.ExchangeID, exchangecontent.RequestViewFull); err == nil || errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("fresh current graph did not fail in decoder: %v", err)
	}
	older, err := reader.GetContentPage(context.Background(), record.ExchangeID, requestCursor)
	if err != nil || len(older.Messages) == 0 {
		t.Fatalf("original source continuation: %+v %v", older, err)
	}
	body, err := reader.GetContentPage(context.Background(), record.ExchangeID, bodyCursor)
	if err == nil && len(body.Blocks) == 1 && body.Blocks[0].Deferred != nil {
		nested := body.Blocks[0].Deferred.Cursor
		if !strings.Contains(nested, ".") {
			t.Fatal("unwrapped nested block cursor")
		}
		body, err = reader.GetContentPage(context.Background(), record.ExchangeID, nested)
	}
	if err != nil || body.NextCursor == "" {
		t.Fatalf("nested body continuation: %+v %v", body, err)
	}
	if _, err = reader.GetContentPage(context.Background(), "other", bodyCursor); err == nil {
		t.Fatal("cross-Exchange cursor accepted")
	}
	nativeExec(t, f.legacy, `DELETE FROM runtime_exchange_content_blocks`)
	if _, err = reader.GetContentPage(context.Background(), record.ExchangeID, body.NextCursor); err == nil {
		t.Fatal("nested cursor rerouted to current copy")
	}
}

func TestContentInspectionReadPortsUseActualSelectedRecord(t *testing.T) {
	f := newEgressInspectionFixture(t, 1)
	record := transcriptContentRecordFixture(t, "content-ports", time.Now().UTC().Truncate(time.Millisecond), exchangecontent.ParentRef{CaptureRunID: f.scope.Capture.ID}, []transcriptMessage{{role: protocolcore.RoleUser, text: "retained user message"}}, "retained result")
	if err := newExchangeContentRepository(f.legacy, f.legacy, newOperationGate(), nil).Put(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	limits, err := exchangecontent.DefaultSourceLimits()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewContentInspectionReader(f.reader.catalog, f.control, limits)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.Get(context.Background(), record.ExchangeID)
	if err != nil || got.Response == nil || got.Response.Blocks[0].Text != "retained result" {
		t.Fatalf("full record: %+v %v", got, err)
	}
	evidence, err := reader.GetConversationEvidence(context.Background(), record.ExchangeID)
	if err != nil || evidence.ExchangeID != record.ExchangeID || evidence.ResponseID != record.Response.ID {
		t.Fatalf("conversation evidence: %+v %v", evidence, err)
	}
	previews, err := reader.RequestPreviews(context.Background(), []string{record.ExchangeID, "absent"})
	if err != nil || previews[record.ExchangeID].Text != "retained user message" || len(previews) != 1 {
		t.Fatalf("selected previews: %+v %v", previews, err)
	}
	available, err := reader.AvailableBodies(context.Background(), []string{record.ExchangeID, "absent"})
	if err != nil || !available[record.ExchangeID] || available["absent"] || len(available) != 2 {
		t.Fatalf("selected availability: %+v %v", available, err)
	}
}

func TestContentInspectionRejectsConflictsAndFiltersOriginalCoordinates(t *testing.T) {
	for _, mode := range []string{"request-conflict", "response-conflict", "frozen-conflict", "deleted", "missing-fence", "generation", "epoch", "unknown-current", "missing-mirror", "expired", "malformed-manifest"} {
		t.Run(mode, func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			now := time.Now().UTC().Truncate(time.Millisecond)
			record := transcriptContentRecordFixture(t, "semantic-visibility", now, exchangecontent.ParentRef{CaptureRunID: f.scope.Capture.ID}, []transcriptMessage{{role: protocolcore.RoleUser, text: "original request"}}, "original response")
			limits, err := exchangecontent.DefaultSourceLimits()
			if err != nil {
				t.Fatal(err)
			}
			reader, err := NewContentInspectionReader(f.reader.catalog, f.control, limits)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "unknown-current" {
				if err = newExchangeContentRepository(f.legacy, f.legacy, newOperationGate(), nil).Put(context.Background(), record); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "request-conflict", "response-conflict", "frozen-conflict", "unknown-current":
				current := record.Clone()
				if mode == "request-conflict" {
					current.Request.Messages[0].Blocks[0].Text = "different request"
				}
				if mode == "response-conflict" {
					current.Response.Blocks[0].Text = "different result"
				}
				if mode == "frozen-conflict" {
					current.Frozen.RouteRevision++
				}
				source, err := exchangecontent.SourceFromRecordWithin(limits, current)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.current.ExchangeContentRepository().(exchangecontent.ScopedSourceRepository).PutScopedSource(context.Background(), f.scope, source); err != nil {
					t.Fatal(err)
				}
				if mode == "unknown-current" {
					nativeExec(t, f.current.store.database, `UPDATE runtime_exchange_contents SET recording_archive_epoch=0,recording_capture_generation=0`)
				}
			case "deleted":
				nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='audit-a'`)
				seedCompleteInspectionMirror(t, f.current, f.control)
			case "missing-fence":
				nativeExec(t, f.control.store.database, `DELETE FROM recording_capture_fences WHERE capture_id='audit-a'`)
				seedCompleteInspectionMirror(t, f.current, f.control)
			case "generation":
				nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET generation=2 WHERE capture_id='audit-a'`)
				seedCompleteInspectionMirror(t, f.current, f.control)
			case "epoch":
				nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET archive_epoch=2`)
				seedCompleteInspectionMirror(t, f.current, f.control)
			case "missing-mirror":
				nativeExec(t, f.current.store.database, `DELETE FROM inspection_mirror_active`)
			case "expired":
				reader.now = func() time.Time { return record.ExpiresAt.Add(time.Second) }
			case "malformed-manifest":
				nativeExec(t, f.legacy, `UPDATE runtime_exchange_contents SET manifest_json=CAST('{}' AS BLOB)`)
			}
			got, err := reader.GetProjection(context.Background(), record.ExchangeID, exchangecontent.RequestViewFull)
			if err == nil || got.ExchangeID != "" {
				t.Fatalf("%s disclosed %+v %v", mode, got, err)
			}
			switch mode {
			case "deleted", "missing-fence", "generation", "epoch", "unknown-current", "expired":
				if !errors.Is(err, exchangecontent.ErrNotFound) {
					t.Fatalf("hidden content: %v", err)
				}
			}
		})
	}
}

func TestContentInspectionRechecksAfterDecodeAndCursorInvalidation(t *testing.T) {
	for _, mode := range []string{"visibility", "expiry", "sources"} {
		t.Run(mode, func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			record := transcriptContentRecordFixture(t, "semantic-recheck", time.Now().UTC().Truncate(time.Millisecond), exchangecontent.ParentRef{CaptureRunID: f.scope.Capture.ID}, []transcriptMessage{{role: protocolcore.RoleUser, text: "request"}}, "response")
			if err := newExchangeContentRepository(f.legacy, f.legacy, newOperationGate(), nil).Put(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			limits, err := exchangecontent.DefaultSourceLimits()
			if err != nil {
				t.Fatal(err)
			}
			reader, err := NewContentInspectionReader(f.reader.catalog, f.control, limits)
			if err != nil {
				t.Fatal(err)
			}
			type maintenanceResult struct {
				token *InspectionMaintenance
				err   error
			}
			done := make(chan maintenanceResult, 1)
			reader.visibility = &usageInspectionChangingVisibility{ControlStore: f.control, beforeCheck: func() {
				if mode == "visibility" {
					nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
				}
				if mode == "expiry" {
					reader.now = func() time.Time { return record.ExpiresAt.Add(time.Second) }
				}
				if mode == "sources" {
					var busy, frames, checkpointed int
					if err := f.current.store.database.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &checkpointed); err != nil || busy != 0 {
						t.Fatalf("semantic SQL held through recheck: %d %v", busy, err)
					}
					f.reader.catalog.mu.Lock()
					changed := f.reader.catalog.changed
					active := f.reader.catalog.active
					f.reader.catalog.mu.Unlock()
					if active != 1 {
						t.Fatalf("semantic source owner released early: %d", active)
					}
					go func() {
						token, err := f.reader.catalog.BeginMaintenance(context.Background(), 2)
						done <- maintenanceResult{token, err}
					}()
					select {
					case <-changed:
					case <-time.After(time.Second):
						t.Fatal("maintenance did not reserve generation")
					}
				}
			}}
			got, err := reader.GetPagedProjection(context.Background(), record.ExchangeID, exchangecontent.RequestViewReading)
			if err == nil || got.ExchangeID != "" {
				t.Fatalf("post-decode %s disclosed %+v %v", mode, got, err)
			}
			if mode == "sources" {
				finished := <-done
				if finished.err != nil {
					t.Fatal(finished.err)
				}
				finished.token.Close()
			}
		})
	}
}

func TestContentInspectionCanonicalAndEvidenceCursors(t *testing.T) {
	f := newEgressInspectionFixture(t)
	record := sourceOnlyRecord(t, "semantic-canonical", exchangecontent.Block{Kind: "reasoning", Availability: exchangecontent.AvailabilityRecorded, ProviderSource: strings.Repeat("p", exchangecontent.PageContentBytes+1), ProviderKind: "kind", Text: "x"})
	record.Parent = exchangecontent.ParentRef{CaptureRunID: f.scope.Capture.ID}
	record.Request.ProtocolEvidence = make([]protocolcore.ProtocolEvidenceValue, 25)
	for i := range record.Request.ProtocolEvidence {
		record.Request.ProtocolEvidence[i] = protocolcore.ProtocolEvidenceValue{Name: fmt.Sprintf("client.item.%02d", i), Value: "retained"}
	}
	limits, err := exchangecontent.DefaultSourceLimits()
	if err != nil {
		t.Fatal(err)
	}
	source, err := exchangecontent.SourceFromRecordWithin(limits, record)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.current.ExchangeContentRepository().(exchangecontent.ScopedSourceRepository).PutScopedSource(context.Background(), f.scope, source); err != nil {
		t.Fatal(err)
	}
	reader, err := NewContentInspectionReader(f.reader.catalog, f.control, limits)
	if err != nil {
		t.Fatal(err)
	}
	reader.now = func() time.Time { return record.RecordedAt }
	projection, err := reader.GetPagedProjection(context.Background(), record.ExchangeID, exchangecontent.RequestViewFull)
	if err != nil || projection.Page == nil {
		t.Fatalf("canonical initial: %+v %v", projection, err)
	}
	evidenceCursor := projection.Page.RequestEvidenceNextCursor
	if evidenceCursor == "" {
		t.Fatal("missing protocol evidence continuation")
	}
	evidence, err := reader.GetContentPage(context.Background(), record.ExchangeID, evidenceCursor)
	if err != nil || len(evidence.ProtocolEvidence) != 1 || !evidence.InspectionExpiresAt.Equal(record.ExpiresAt) {
		t.Fatalf("evidence page: %+v %v", evidence, err)
	}
	cursor := projection.Request.Messages[0].Blocks[0].Deferred.Cursor
	canonical := ""
	for step := 0; step < 5; step++ {
		page, err := reader.GetContentPage(context.Background(), record.ExchangeID, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if page.CanonicalCursor != "" {
			canonical = page.CanonicalCursor
			break
		}
		if len(page.Blocks) != 1 || page.Blocks[0].Deferred == nil {
			t.Fatalf("canonical detail path lost: %+v", page)
		}
		cursor = page.Blocks[0].Deferred.Cursor
	}
	if canonical == "" {
		t.Fatal("no canonical exact route")
	}
	exact, err := reader.GetContentPage(context.Background(), record.ExchangeID, canonical)
	if err != nil || len(exact.Data) == 0 || exact.NextCursor == "" {
		t.Fatalf("exact canonical bytes: %+v %v", exact, err)
	}
	lease, err := reader.catalog.BeginMaintenance(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	if _, err = reader.GetContentPage(context.Background(), record.ExchangeID, exact.NextCursor); !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("old source generation accepted: %v", err)
	}
}

func TestContentInspectionBoundsCorruptRootBeforeMaterialization(t *testing.T) {
	f := newEgressInspectionFixture(t)
	record := transcriptContentRecordFixture(t, "semantic-large-root", time.Now().UTC().Truncate(time.Millisecond), exchangecontent.ParentRef{CaptureRunID: f.scope.Capture.ID}, []transcriptMessage{{role: protocolcore.RoleUser, text: "request"}}, "response")
	if err := newExchangeContentRepository(f.legacy, f.legacy, newOperationGate(), nil).Put(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, f.legacy, `PRAGMA ignore_check_constraints=ON;UPDATE runtime_exchange_contents SET request_transcript_digest=printf('%.*c',8388608,'a');PRAGMA ignore_check_constraints=OFF`)
	limits, err := exchangecontent.DefaultSourceLimits()
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = loadStoredContentReferenceWithin(context.Background(), f.legacy, record.ExchangeID, record.RecordedAt, &limits)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("corrupt oversized root accepted")
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2<<20 {
		t.Fatalf("oversized scalar materialized before refusal: %d bytes", allocated)
	}
}

func TestContentInspectionCarriesOriginalBatchExpiryAndRefusesPartialPreviews(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	limits, err := exchangecontent.DefaultSourceLimits()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewContentInspectionReader(f.reader.catalog, f.control, limits)
	if err != nil {
		t.Fatal(err)
	}
	records := []exchangecontent.Record{}
	for i := 0; i < 2; i++ {
		record := transcriptContentRecordFixture(t, fmt.Sprintf("expiry-%d", i), now, exchangecontent.ParentRef{CaptureRunID: f.scope.Capture.ID}, []transcriptMessage{{role: protocolcore.RoleUser, text: fmt.Sprintf("request%d", i)}}, "response")
		record.ExpiresAt = now.Add(time.Duration(i+1) * time.Hour)
		if err := newExchangeContentRepository(f.legacy, f.legacy, newOperationGate(), nil).Put(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	ids := []string{records[0].ExchangeID, records[1].ExchangeID}
	available, err := reader.InspectAvailableBodies(context.Background(), ids)
	if err != nil || !available.InspectionExpiresAt.Equal(records[0].ExpiresAt) || len(available.Items) != 2 {
		t.Fatalf("batch original expiry: %+v %v", available, err)
	}
	previews, err := reader.RequestPreviews(context.Background(), ids)
	if err != nil || !previews[ids[1]].InspectionExpiresAt.Equal(records[1].ExpiresAt) {
		t.Fatalf("per-preview original expiry: %+v %v", previews, err)
	}
	encoded, err := json.Marshal(previews)
	if err != nil || bytes.Contains(encoded, []byte("InspectionExpiresAt")) || bytes.Contains(encoded, []byte("inspectionExpiresAt")) {
		t.Fatalf("private expiry leaked: %s %v", encoded, err)
	}
	nativeExec(t, f.legacy, `UPDATE runtime_exchange_contents SET manifest_json=CAST('{}' AS BLOB) WHERE exchange_id=?`, ids[1])
	if result, err := reader.RequestPreviews(context.Background(), ids); err == nil || result != nil {
		t.Fatalf("partial batch escaped: %+v %v", result, err)
	}
}

func TestContentInspectionCurrentGraphNeverUsesEqualArchiveBytes(t *testing.T) {
	f := newEgressInspectionFixture(t)
	record := transcriptContentRecordFixture(t, "current-missing-graph", time.Now().UTC().Truncate(time.Millisecond), exchangecontent.ParentRef{CaptureRunID: f.scope.Capture.ID}, []transcriptMessage{{role: protocolcore.RoleUser, text: "same request"}}, "same response")
	if err := newExchangeContentRepository(f.legacy, f.legacy, newOperationGate(), nil).Put(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	limits, err := exchangecontent.DefaultSourceLimits()
	if err != nil {
		t.Fatal(err)
	}
	source, err := exchangecontent.SourceFromRecordWithin(limits, record)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.current.ExchangeContentRepository().(exchangecontent.ScopedSourceRepository).PutScopedSource(context.Background(), f.scope, source); err != nil {
		t.Fatal(err)
	}
	reader, err := NewContentInspectionReader(f.reader.catalog, f.control, limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Get(context.Background(), record.ExchangeID); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, f.current.store.database, `PRAGMA foreign_keys=OFF;DELETE FROM runtime_exchange_content_blocks;PRAGMA foreign_keys=ON`)
	got, err := reader.Get(context.Background(), record.ExchangeID)
	if err == nil || errors.Is(err, ErrInspectionChanged) || got.ExchangeID != "" {
		t.Fatalf("current graph repaired from equal archive bytes: %+v %v", got, err)
	}
	if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
		t.Fatalf("expected actual graph decoder refusal, got %v", err)
	}
}
