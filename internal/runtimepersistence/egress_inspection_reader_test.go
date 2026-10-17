package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type egressInspectionFixture struct {
	reader  *EgressInspectionReader
	current *ObservationStore
	control *ControlStore
	legacy  *sql.DB
	scope   recording.Scope
}

func newEgressInspectionFixture(t *testing.T, revisions ...int) *egressInspectionFixture {
	t.Helper()
	options, current := inspectionSourcesFixture(t)
	options.Timeout = 5 * time.Second
	if len(revisions) > 0 && revisions[0] == 1 {
		path, db := extractionFixture(t, 1)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(options.Root, "archives/legacy/runtime.db"), data, 0600); err != nil {
			t.Fatal(err)
		}
		options.Sources[0].Database.Revision = 1
	}
	control := inspectionControlFixture(t)
	nativeExec(t, control.store.database, `INSERT INTO recording_capture_fences VALUES('managed_run','audit-a',1,0),('managed_run','audit-b',1,0)`)
	seedCompleteInspectionMirror(t, current, control)
	legacy, err := sql.Open("sqlite", filepath.Join(options.Root, "archives/legacy/runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { legacy.Close() })
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close(context.Background()) })
	return &egressInspectionFixture{reader: NewEgressInspectionReader(catalog, control), current: current, control: control, legacy: legacy, scope: recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "audit-a"}, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}}
}
func (f *egressInspectionFixture) put(t *testing.T, current bool, a egressaudit.Attempt) {
	t.Helper()
	if current {
		if err := f.current.EgressAttemptRepository().(egressaudit.ScopedAttemptStore).PutScopedAttempt(context.Background(), f.scope, a); err != nil {
			t.Fatal(err)
		}
	} else {
		input := normalizedEgressStart(a)
		input.Decision.SelectionSource = ""
		input.Decision.DefaultRevision = 0
		start, err := egressaudit.New(input)
		if err != nil {
			t.Fatal(err)
		}
		seedFrozenV1EgressAttempt(t, &Store{database: f.legacy}, start)
		if f.reader.catalog.sources[0].Database.Revision == 2 {
			nativeExec(t, f.legacy, `UPDATE runtime_egress_attempts SET selection_source=?,default_revision=? WHERE attempt_id=?`, a.Decision().SelectionSource, a.Decision().DefaultRevision, a.ID())
		} else if a.Decision().SelectionSource != "" || a.Decision().DefaultRevision != 0 {
			t.Fatal("nonlegacy selection in v1 fixture")
		}
		if a.Terminal() {
			if f.reader.catalog.sources[0].Database.Revision == 1 {
				if !a.ByteCountsKnown() {
					t.Fatal("unobserved byte counts in v1 fixture")
				}
				nativeExec(t, f.legacy, `UPDATE runtime_egress_attempts SET completed_at_unix_ms=?,outcome=?,error_class=?,bytes_out=?,bytes_in=? WHERE attempt_id=?`, a.CompletedAt().UnixMilli(), a.Outcome(), a.ErrorClass(), a.BytesOut(), a.BytesIn(), a.ID())
				return
			}
			if _, err = completeEgressAttempt(context.Background(), f.legacy, a); err != nil {
				t.Fatal(err)
			}
		}
	}
}
func (f *egressInspectionFixture) activityRoot(t *testing.T, a egressaudit.Attempt, capture string) {
	t.Helper()
	value := activity.Record{ID: "root-" + a.ID() + "-" + capture, OccurredAt: a.StartedAt(), Kind: activity.KindExchangeCompleted, SubjectID: a.Parent().ExchangeID, Status: activity.StatusSucceeded, SourceKind: activity.SourceCaptureRun, SourceDisplayName: "Frozen captured client", SourceRecognition: activity.SourceRecognitionConfigured, CaptureRunID: capture, ConnectionID: a.ConnectionID()}
	setFrozenExecutionEvidence(&value, "audit")
	seedFrozenV1Activity(t, &Store{database: f.legacy}, value)
}
func TestEgressInspectionMergesTerminalBeforeDescendingSourceCursor(t *testing.T) {
	f := newEgressInspectionFixture(t)
	ctx := context.Background()
	archived := providerAttempt(t, "archive-only")
	f.put(t, false, archived)
	f.activityRoot(t, archived, "audit-a")
	duplicate := providerAttempt(t, "copied")
	f.put(t, false, duplicate)
	f.activityRoot(t, duplicate, "audit-a")
	terminal, err := duplicate.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, CompletedAt: duplicate.StartedAt().Add(time.Second), BytesIn: 23})
	if err != nil {
		t.Fatal(err)
	}
	f.put(t, true, terminal)
	page, err := f.reader.List(ctx, egressaudit.PageRequest{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Attempt.ID() != "copied" || !page.Items[0].Attempt.Terminal() || page.NextCursor == "" {
		t.Fatalf("canonical current terminal missing: %+v %v", page, err)
	}
	next, err := f.reader.List(ctx, egressaudit.PageRequest{Limit: 1, AfterCursor: page.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].Attempt.ID() != "archive-only" {
		t.Fatalf("overlapping local sequence skipped/replayed: %+v %v", next, err)
	}
}

func (f *egressInspectionFixture) rawRoot(t *testing.T, a egressaudit.Attempt, capture string) {
	t.Helper()
	r := rawEvidenceRecordForTest("audit-raw-"+a.ID(), 1, rawevidence.LayerProviderEgress, nil, []byte(`{"version":1,"headers":[]}`))
	r.ScopeID = capture
	r.AttemptID = a.ID()
	r.ConnectionID = a.ConnectionID()
	r.ExchangeID = "raw-associated-exchange"
	if err := newRawEvidenceRepository(f.legacy, newOperationGate()).AppendBatch(context.Background(), []rawevidence.StoredEnvelope{r}, r.ObservedAt); err != nil {
		t.Fatal(err)
	}
}
func TestEgressInspectionLegacyOwnershipAndCrossCopyVeto(t *testing.T) {
	for _, mode := range []string{"unattributed", "visible", "missing", "deleted", "multiple", "multiple-deleted", "current-known-legacy-unknown", "current-known-legacy-deleted", "raw-root"} {
		t.Run(mode, func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			a := providerAttempt(t, "privacy-copy")
			f.put(t, false, a)
			switch mode {
			case "visible", "missing", "deleted", "multiple", "multiple-deleted":
				f.activityRoot(t, a, "audit-a")
			case "current-known-legacy-deleted":
				f.activityRoot(t, a, "audit-b")
			case "raw-root":
				f.rawRoot(t, a, "audit-a")
			}
			if strings.HasPrefix(mode, "current-known-") {
				f.put(t, true, a)
			}
			if mode == "multiple" || mode == "multiple-deleted" {
				f.activityRoot(t, a, "audit-b")
			}
			if mode == "missing" {
				nativeExec(t, f.control.store.database, `DELETE FROM recording_capture_fences WHERE capture_id='audit-a'`)
			}
			if mode == "deleted" {
				nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='audit-a'`)
			}
			if mode == "multiple-deleted" || mode == "current-known-legacy-deleted" {
				nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='audit-b'`)
			}
			seedCompleteInspectionMirror(t, f.current, f.control)
			page, err := f.reader.List(context.Background(), egressaudit.PageRequest{Limit: 10})
			want := 0
			if mode == "visible" || mode == "multiple" || mode == "raw-root" || mode == "current-known-legacy-unknown" {
				want = 1
			}
			if err != nil || len(page.Items) != want {
				t.Fatalf("legacy privacy %s: %+v %v", mode, page, err)
			}
		})
	}
}
func TestEgressInspectionNativeOnceOnlyAndRuntimeGlobalTaxonomy(t *testing.T) {
	for _, mode := range []string{"native-once", "native-label-only", "runtime-global", "runtime-captured", "fake-runtime"} {
		t.Run(mode, func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			var a egressaudit.Attempt
			if strings.HasPrefix(mode, "native") {
				a = nativeAuditFixture(t, "native-owned-audit")
				f.put(t, false, a)
				if mode == "native-once" {
					nativeExec(t, f.legacy, `INSERT INTO native_tool_scopes VALUES('managed_run','audit-a',1,1,'closed',1,100);INSERT INTO native_tool_once VALUES('managed_run','audit-a',randomblob(32),'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',randomblob(32),'native-owned-audit',1,1)`)
				}
			} else {
				input := normalizedEgressStart(providerAttempt(t, "global-owned-audit"))
				input.ConnectionID = ""
				input.Purpose = egressaudit.PurposeUpdate
				input.PayloadClass = egressaudit.PayloadRuntime
				input.Parent = egressaudit.ParentRef{Kind: egressaudit.ParentRuntimeAction, ID: "update-action"}
				input.Decision.Authority = egressaudit.AuthorityRuntime
				input.Decision.AccountID = ""
				input.Decision.AccountSettingsRevision = 0
				var err error
				a, err = egressaudit.New(input)
				if err != nil {
					t.Fatal(err)
				}
				f.put(t, false, a)
				if mode == "runtime-captured" {
					f.rawRoot(t, a, "audit-a")
					nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='audit-a'`)
					seedCompleteInspectionMirror(t, f.current, f.control)
				}
				if mode == "fake-runtime" {
					nativeExec(t, f.legacy, `UPDATE runtime_egress_attempts SET connection_id='client-connection'`)
				}
			}
			page, err := f.reader.List(context.Background(), egressaudit.PageRequest{Limit: 10})
			if mode == "fake-runtime" {
				if err == nil {
					t.Fatalf("malformed global-looking audit hidden/promoted: %+v", page)
				}
				return
			}
			want := 0
			if mode == "native-once" || mode == "runtime-global" {
				want = 1
			}
			if err != nil || len(page.Items) != want {
				t.Fatalf("provenance %s: %+v %v", mode, page, err)
			}
		})
	}
}
func TestEgressInspectionConflictsPrecedeFilterAndPagination(t *testing.T) {
	for _, mode := range []string{"immutable", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			a := providerAttempt(t, "conflicting-audit")
			b := a
			if mode == "immutable" {
				input := normalizedEgressStart(a)
				input.ConnectionID = "different-connection"
				var err error
				b, err = egressaudit.New(input)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var err error
				a, err = a.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, CompletedAt: a.StartedAt().Add(time.Second), BytesIn: 1})
				if err != nil {
					t.Fatal(err)
				}
				b, err = b.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, CompletedAt: b.StartedAt().Add(time.Second), BytesIn: 2})
				if err != nil {
					t.Fatal(err)
				}
			}
			f.put(t, false, a)
			f.activityRoot(t, a, "audit-a")
			f.put(t, true, b)
			if _, err := f.reader.List(context.Background(), egressaudit.PageRequest{Limit: 1, ConnectionID: a.ConnectionID()}); !errors.Is(err, ErrInspectionUnavailable) {
				t.Fatalf("conflict became filtered/partial success: %v", err)
			}
		})
	}
}

func TestEgressInspectionV1TerminalWinsAndCursorBindsSources(t *testing.T) {
	f := newEgressInspectionFixture(t, 1)
	ctx := context.Background()
	start := providerAttempt(t, "v1-terminal")
	terminal, err := start.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, CompletedAt: start.StartedAt().Add(time.Second), BytesOut: 9})
	if err != nil {
		t.Fatal(err)
	}
	f.put(t, false, terminal)
	f.activityRoot(t, terminal, "audit-a")
	f.put(t, true, start)
	other := providerAttempt(t, "current-other")
	f.put(t, true, other)
	first, err := f.reader.List(ctx, egressaudit.PageRequest{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].Attempt.ID() != other.ID() {
		t.Fatalf("current tuple ordering: %+v %v", first, err)
	}
	if _, err = f.reader.List(ctx, egressaudit.PageRequest{Limit: 1, AfterCursor: first.NextCursor, ExchangeID: start.Parent().ExchangeID}); !errors.Is(err, ErrInspectionCursor) {
		t.Fatalf("query-swapped cursor accepted: %v", err)
	}
	next, err := f.reader.List(ctx, egressaudit.PageRequest{Limit: 1, AfterCursor: first.NextCursor})
	if err != nil || len(next.Items) != 1 || !next.Items[0].Attempt.Terminal() || next.Items[0].Attempt.BytesOut() != 9 {
		t.Fatalf("later start erased v1 terminal: %+v %v", next, err)
	}
	old, _ := egressaudit.Cursor(1)
	if _, err = f.reader.List(ctx, egressaudit.PageRequest{Limit: 1, AfterCursor: old}); !errors.Is(err, ErrInspectionCursor) {
		t.Fatalf("legacy sequence cursor reinterpreted: %v", err)
	}
	m, err := f.reader.catalog.BeginMaintenance(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	if _, err = f.reader.List(ctx, egressaudit.PageRequest{Limit: 1, AfterCursor: first.NextCursor}); !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("source generation reused: %v", err)
	}
}
func TestEgressInspectionLateCanonicalPublicationCannotExposeOldCopy(t *testing.T) {
	f := newEgressInspectionFixture(t)
	ctx := context.Background()
	archived := providerAttempt(t, "late-canonical")
	f.put(t, false, archived)
	f.activityRoot(t, archived, "audit-a")
	f.put(t, true, providerAttempt(t, "first-current"))
	page, err := f.reader.List(ctx, egressaudit.PageRequest{Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatal("first page", err)
	}
	terminal, err := archived.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, CompletedAt: archived.StartedAt().Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	f.put(t, true, terminal)
	next, err := f.reader.List(ctx, egressaudit.PageRequest{Limit: 1, AfterCursor: page.NextCursor})
	if err != nil || len(next.Items) != 0 {
		t.Fatalf("old copy resurrected below a moved winner: %+v %v", next, err)
	}
	fresh, err := f.reader.List(ctx, egressaudit.PageRequest{Limit: 1})
	if err != nil || len(fresh.Items) != 1 || fresh.Items[0].Attempt.ID() != archived.ID() || !fresh.Items[0].Attempt.Terminal() {
		t.Fatalf("fresh current winner absent: %+v %v", fresh, err)
	}
}
func TestEgressInspectionUnattributedBatchesAdvanceAndUnknownCurrentStaysHidden(t *testing.T) {
	f := newEgressInspectionFixture(t)
	ctx := context.Background()
	for i := 0; i < 70; i++ {
		f.put(t, false, providerAttempt(t, fmt.Sprintf("unknown-%d", i)))
	}
	a := providerAttempt(t, "unknown-current")
	if _, err := f.current.EgressAttemptRepository().Append(ctx, a); err != nil {
		t.Fatal(err)
	}
	page, err := f.reader.List(ctx, egressaudit.PageRequest{Limit: 1})
	if err != nil || len(page.Items) != 0 || page.NextCursor == "" {
		t.Fatalf("hidden batch did not advance: %+v %v", page, err)
	}
	next, err := f.reader.List(ctx, egressaudit.PageRequest{Limit: 1, AfterCursor: page.NextCursor})
	if err != nil || len(next.Items) != 0 || next.NextCursor != "" {
		t.Fatalf("hidden continuation looped: %+v %v", next, err)
	}
}
func TestEgressInspectionRefusesMissingMirrorAndFinalVisibilityChange(t *testing.T) {
	f := newEgressInspectionFixture(t)
	f.put(t, true, providerAttempt(t, "current-visible"))
	nativeExec(t, f.current.store.database, `DELETE FROM inspection_mirror_active`)
	if _, err := f.reader.List(context.Background(), egressaudit.PageRequest{Limit: 1}); !errors.Is(err, ErrInspectionUnavailable) {
		t.Fatalf("missing mirror returned partial page: %v", err)
	}
	seedCompleteInspectionMirror(t, f.current, f.control)
	f.reader.visibility = &usageInspectionChangingVisibility{ControlStore: f.control, beforeCheck: func() {
		nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
	}}
	page, err := f.reader.List(context.Background(), egressaudit.PageRequest{Limit: 1})
	if !errors.Is(err, ErrInspectionChanged) || len(page.Items) != 0 {
		t.Fatalf("final visibility ignored: %+v %v", page, err)
	}
}

func TestEgressInspectionTransitiveSemanticOwnership(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprint(deleted), func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			a := providerAttempt(t, "chain-first")
			b := providerAttempt(t, "chain-second")
			f.put(t, false, a)
			f.put(t, false, b)
			record := transcriptContentRecordFixture(t, b.Parent().ExchangeID, b.StartedAt(), exchangecontent.ParentRef{CaptureRunID: "audit-a"}, []transcriptMessage{{role: protocolcore.RoleUser, text: "retained ownership root"}}, "")
			if err := newExchangeContentRepository(f.legacy, f.legacy, newOperationGate(), nil).Put(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			if deleted {
				nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='audit-a'`)
				seedCompleteInspectionMirror(t, f.current, f.control)
			}
			page, err := f.reader.List(context.Background(), egressaudit.PageRequest{Limit: 10, ExchangeID: a.Parent().ExchangeID})
			want := 1
			if deleted {
				want = 0
			}
			if err != nil || len(page.Items) != want {
				t.Fatalf("semantic→other attempt→connection→attempt closure lost: %+v %v", page, err)
			}
		})
	}
}

func TestAuditInspectionOwnershipGraphLimitRefusesBeforePublication(t *testing.T) {
	f := newEgressInspectionFixture(t)
	f.reader.catalog.timeout = 20 * time.Second
	a := providerAttempt(t, "graph-seed")
	f.put(t, false, a)
	f.activityRoot(t, a, "audit-a")
	columns := strings.Split(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(egressAttemptSelect, "SELECT"), "FROM runtime_egress_attempts")), ",")
	names, values := []string{}, []string{}
	for _, name := range columns {
		name = strings.TrimSpace(name)
		if name == "sequence" {
			continue
		}
		names = append(names, name)
		value := "seed." + name
		switch name {
		case "attempt_id":
			value = "'graph-'||n.i"
		case "parent_id":
			value = "'parent-graph-'||n.i"
		case "parent_exchange_id":
			value = "'exchange-graph-'||n.i"
		}
		values = append(values, value)
	}
	nativeExec(t, f.legacy, `WITH RECURSIVE n(i) AS(VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<32768) INSERT INTO runtime_egress_attempts(`+strings.Join(names, ",")+`) SELECT `+strings.Join(values, ",")+` FROM n CROSS JOIN runtime_egress_attempts seed WHERE seed.attempt_id='graph-seed'`)
	page, err := f.reader.List(context.Background(), egressaudit.PageRequest{Limit: 1, ExchangeID: a.Parent().ExchangeID})
	if !errors.Is(err, ErrInspectionSourceLimit) || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("truncated ownership proof produced partial page: %+v %v", page, err)
	}
}
