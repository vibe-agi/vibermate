package runtimepersistence

import (
	"context"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func inspectionActivity(id, subject string, kind activity.Kind, at time.Time) activity.Record {
	record := activity.Record{ID: id, SubjectID: subject, Kind: kind, OccurredAt: at, Status: activity.StatusSucceeded, SourceKind: activity.SourceCaptureRun, SourceDisplayName: "Codex", SourceRecognition: activity.SourceRecognitionVerified, CaptureRunID: "audit-a", ConnectionID: "connection-" + subject}
	setFrozenExecutionEvidence(&record, "inspection")
	if kind == activity.KindExchangeStarted {
		record.Status = activity.StatusPending
		record.AccountID = ""
		record.AccountRevision = 0
		record.CredentialEpoch = 0
	}
	return record
}
func TestActivityInspectionSelectsGlobalTerminalAndSourceCursor(t *testing.T) {
	for _, revision := range []int{1, 2} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) { testActivityInspectionTraversal(t, revision) })
	}
}
func testActivityInspectionTraversal(t *testing.T, revision int) {
	f := newEgressInspectionFixture(t, revision)
	now := time.Now().UTC().Truncate(time.Millisecond)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, inspectionActivity("start", "shared", activity.KindExchangeStarted, now))
	seedFrozenV1Activity(t, &Store{database: f.legacy}, inspectionActivity("legacy", "legacy-only", activity.KindExchangeCompleted, now))
	for _, record := range []activity.Record{inspectionActivity("terminal", "shared", activity.KindExchangeCompleted, now.Add(time.Second)), inspectionActivity("current", "current-only", activity.KindExchangeCompleted, now.Add(time.Second))} {
		if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, record); err != nil {
			t.Fatal(err)
		}
	}
	reader := newActivityInspectionTestReader(t, f)
	got, err := reader.GetExchange(context.Background(), "shared")
	if err != nil || got.ID != "terminal" || got.Status != activity.StatusSucceeded {
		t.Fatalf("global terminal: %+v %v", got, err)
	}
	var ids []string
	cursor := ""
	for pageNumber := 0; pageNumber < 5; pageNumber++ {
		page, err := reader.ListExchanges(context.Background(), activity.PageRequest{CaptureRunID: "audit-a", Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range page.Items {
			ids = append(ids, record.SubjectID)
		}
		if page.NextBeforeSequence != 0 {
			t.Fatal("independent reader emitted bare sequence")
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatal("cursor did not advance")
		}
		cursor = page.NextCursor
	}
	if !reflect.DeepEqual(ids, []string{"current-only", "shared", "legacy-only"}) {
		t.Fatalf("overlapping sequences traversal: %v", ids)
	}
}

func TestActivityInspectionLateCurrentWinnerCannotExposeArchivedCopy(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	archived := inspectionActivity("archived-terminal", "shared", activity.KindExchangeCompleted, now)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, archived)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, inspectionActivity("legacy-other", "legacy-only", activity.KindExchangeCompleted, now))
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, inspectionActivity("current-first", "current-only", activity.KindExchangeCompleted, now)); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	first, err := reader.ListExchanges(context.Background(), activity.PageRequest{Limit: 1, CaptureRunID: "audit-a"})
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, archived); err != nil {
		t.Fatal(err)
	}
	next, err := reader.ListExchanges(context.Background(), activity.PageRequest{Limit: 5, CaptureRunID: "audit-a", Cursor: first.NextCursor})
	// Limit is part of the exact query binding, so continuation cannot change it.
	if !errors.Is(err, ErrInspectionCursor) {
		t.Fatalf("changed query accepted: %+v %v", next, err)
	}
	next, err = reader.ListExchanges(context.Background(), activity.PageRequest{Limit: 1, CaptureRunID: "audit-a", Cursor: first.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].SubjectID != "legacy-only" || next.NextCursor != "" {
		t.Fatalf("obsolete archived copy exposed: %+v %v", next, err)
	}
}

func TestActivityInspectionEventIdentityDeduplicatesAcrossSources(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	copied := inspectionActivity("event-copied", "shared", activity.KindExchangeCompleted, now)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, copied)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, inspectionActivity("event-start", "shared", activity.KindExchangeStarted, now.Add(-time.Second)))
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, copied); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	page, err := reader.List(context.Background(), activity.PageRequest{Limit: 10, CaptureRunID: "audit-a"})
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != "event-copied" || page.Items[1].ID != "event-start" {
		t.Fatalf("event identity/page: %+v %v", page, err)
	}
}

func TestActivityInspectionSummaryCountsCanonicalExchangesAcrossSources(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	start := inspectionActivity("shared-start", "shared", activity.KindExchangeStarted, now)
	terminal := inspectionActivity("shared-terminal", "shared", activity.KindExchangeCompleted, now.Add(time.Second))
	seedFrozenV1Activity(t, &Store{database: f.legacy}, start)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, terminal)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, inspectionActivity("pending", "pending", activity.KindExchangeStarted, now.Add(-time.Second)))
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, terminal); err != nil {
		t.Fatal(err)
	}
	failed := inspectionActivity("failure", "failed", activity.KindExchangeCompleted, now.Add(2*time.Second))
	failed.Status = activity.StatusFailed
	failed.ReasonCode = "provider_transport_failed"
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, failed); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	got, err := reader.SummarizeExchanges(context.Background(), activity.SummaryScope{CaptureRunID: "audit-a"})
	if err != nil || got.Requests != 3 || got.Succeeded != 1 || got.Pending != 1 || got.Failed != 1 || got.FirstObservedAt == nil || !got.FirstObservedAt.Equal(now.Add(-time.Second)) || got.LastObservedAt == nil || !got.LastObservedAt.Equal(failed.OccurredAt) || len(got.Failures) != 1 || got.Failures[0].Count != 1 {
		t.Fatalf("global summary: %+v %v", got, err)
	}
}

func newActivityInspectionTestReader(t *testing.T, f *egressInspectionFixture) *ActivityInspectionReader {
	t.Helper()
	limits, err := exchangecontent.DefaultSourceLimits()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewActivityInspectionReader(f.reader.catalog, f.control, limits)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func TestActivityInspectionFailureSummaryHasBoundedResultMemory(t *testing.T) {
	f := newEgressInspectionFixture(t)
	f.reader.catalog.timeout = 30 * time.Second
	record := inspectionActivity("failure-seed", "failure-seed", activity.KindExchangeCompleted, time.Now().UTC().Truncate(time.Millisecond))
	record.Status = activity.StatusFailed
	record.ReasonCode = "seed"
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, record); err != nil {
		t.Fatal(err)
	}
	// Test-only authored optional rows use the same valid frozen scalar fields;
	// only event/Exchange IDs, projection ID and failure reason differ.
	nativeExec(t, f.current.store.database, `WITH RECURSIVE n(i) AS(VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<65535)
 INSERT INTO runtime_activities(activity_id,occurred_at_unix_ms,kind,environment_id,environment_revision,environment_digest,client_endpoint_id,client_endpoint_revision,protocol_plan_id,protocol_plan_revision,route_id,route_revision,account_id,account_revision,credential_epoch,subject_id,status,reason_code,source_kind,source_display_name,source_recognition,capture_run_id,manual_capture_id,connection_id,conversation_projection_id,conversation_display_name,conversation_kind,conversation_evidence,conversation_actor,recording_archive_epoch,recording_capture_generation)
 SELECT 'failure-'||i,occurred_at_unix_ms,kind,environment_id,environment_revision,environment_digest,client_endpoint_id,client_endpoint_revision,protocol_plan_id,protocol_plan_revision,route_id,route_revision,account_id,account_revision,credential_epoch,'failure-'||i,status,printf('%0128d',i),source_kind,source_display_name,source_recognition,capture_run_id,manual_capture_id,'connection-'||i,'exchange:failure-'||i,conversation_display_name,conversation_kind,conversation_evidence,conversation_actor,recording_archive_epoch,recording_capture_generation FROM runtime_activities,n WHERE activity_id='failure-seed'`)
	reader := newActivityInspectionTestReader(t, f)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := reader.SummarizeExchanges(context.Background(), activity.SummaryScope{CaptureRunID: "audit-a"})
	runtime.ReadMemStats(&after)
	if err != nil || got.Failed != 65536 || len(got.Failures) != 10 || got.OtherFailures != 65526 {
		t.Fatalf("bounded top failures: %+v %v", got, err)
	}
	if bytes := after.TotalAlloc - before.TotalAlloc; bytes > 8<<20 {
		t.Fatalf("unbounded failure reasons materialized: %d bytes", bytes)
	}
}

func TestActivityInspectionIdentityOnlyRootNeedsNoRequestProjection(t *testing.T) {
	f := newEgressInspectionFixture(t)
	identity := agentconversation.ClientIdentity{Client: "codex", SessionID: "identity-only-session", SessionResumable: true, Source: agentconversation.ClientIdentitySourceProtocolEvidence, Confidence: "exact", ObservedAt: time.Now().UTC().Truncate(time.Millisecond)}
	if err := f.current.ConversationIdentityRepository().(activity.ScopedConversationIdentityWriter).PutScopedConversationIdentity(context.Background(), f.scope, "identity-only", identity); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	got, err := reader.GetConversationIdentity(context.Background(), "identity-only")
	if err != nil || !got.Equal(identity) {
		t.Fatalf("independent association lost without Activity/content: %+v %v", got, err)
	}
	if _, err = reader.GetExchange(context.Background(), "identity-only"); !errors.Is(err, activity.ErrExchangeNotFound) {
		t.Fatalf("identity fabricated request: %v", err)
	}
	nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='audit-a'`)
	seedCompleteInspectionMirror(t, f.current, f.control)
	if _, err = reader.GetConversationIdentity(context.Background(), "identity-only"); !errors.Is(err, activity.ErrExchangeNotFound) {
		t.Fatalf("deleted association root visible: %v", err)
	}
}

func TestActivityInspectionLegacyIdentityNeedsSourceLocalParent(t *testing.T) {
	for _, revision := range []int{1, 2} {
		for _, mode := range []string{"activity", "orphan", "semantic", "conflicting-parent"} {
			t.Run(fmt.Sprintf("%d/%s", revision, mode), func(t *testing.T) {
				f := newEgressInspectionFixture(t, revision)
				now := time.Now().UTC().Truncate(time.Millisecond)
				identity := agentconversation.ClientIdentity{Client: "codex", SessionID: "session", SessionResumable: true, Source: agentconversation.ClientIdentitySourceProtocolEvidence, Confidence: "exact", ObservedAt: now}
				if mode != "semantic" {
					seedFrozenV1Activity(t, &Store{database: f.legacy}, inspectionActivity("identity-parent", "legacy-identity", activity.KindExchangeCompleted, now))
				}
				if mode == "semantic" || mode == "conflicting-parent" {
					parent := "audit-a"
					if mode == "conflicting-parent" {
						parent = "audit-b"
					}
					content := transcriptContentRecordFixture(t, "legacy-identity", now, exchangecontent.ParentRef{CaptureRunID: parent}, []transcriptMessage{{role: protocolcore.RoleUser, text: "request"}}, "response")
					if err := newExchangeContentRepository(f.legacy, f.legacy, newOperationGate(), nil).Put(context.Background(), content); err != nil {
						t.Fatal(err)
					}
				}
				if err := newActivityRepository(f.legacy, f.legacy, newOperationGate()).PutConversationIdentity(context.Background(), "legacy-identity", identity); err != nil {
					t.Fatal(err)
				}
				if mode == "orphan" {
					nativeExec(t, f.legacy, `DELETE FROM runtime_activities`)
				}
				if mode == "semantic" {
					nativeExec(t, f.legacy, `DELETE FROM runtime_exchange_content_blocks`)
				}
				got, err := newActivityInspectionTestReader(t, f).GetConversationIdentity(context.Background(), "legacy-identity")
				switch mode {
				case "orphan":
					if !errors.Is(err, activity.ErrExchangeNotFound) {
						t.Fatalf("orphan relabeled global: %+v %v", got, err)
					}
				case "conflicting-parent":
					if err == nil {
						t.Fatalf("conflicting root ownership accepted: %+v", got)
					}
				default:
					if err != nil || !got.Equal(identity) {
						t.Fatalf("retained source-local parent: %+v %v", got, err)
					}
				}
			})
		}
	}
}

func TestActivityInspectionIdentityDeepeningAndUnknownCurrentCoordinates(t *testing.T) {
	for _, mode := range []string{"deepen", "session-conflict", "scope-conflict", "unknown-current"} {
		t.Run(mode, func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			now := time.Now().UTC().Truncate(time.Millisecond)
			seedFrozenV1Activity(t, &Store{database: f.legacy}, inspectionActivity("identity-parent", "deepened", activity.KindExchangeCompleted, now))
			original := agentconversation.ClientIdentity{Client: "codex", SessionID: "session", SessionResumable: true, ActorID: "actor", ProviderResponseID: "response", Source: agentconversation.ClientIdentitySourceProtocolEvidence, Confidence: "exact", ObservedAt: now}
			if err := newActivityRepository(f.legacy, f.legacy, newOperationGate()).PutConversationIdentity(context.Background(), "deepened", original); err != nil {
				t.Fatal(err)
			}
			current := original.Clone()
			current.ActorLabel = "Helper"
			current.ActorType = "worker"
			current.ActorIsSubagent = true
			current.Source = agentconversation.ClientIdentitySourceLocalState
			current.ObservedAt = now.Add(time.Minute)
			scope := f.scope
			if mode == "scope-conflict" {
				scope.Capture.ID = "audit-b"
			}
			if mode == "session-conflict" {
				current.SessionID = "different"
			}
			if err := f.current.ConversationIdentityRepository().(activity.ScopedConversationIdentityWriter).PutScopedConversationIdentity(context.Background(), scope, "deepened", current); err != nil {
				t.Fatal(err)
			}
			if mode == "unknown-current" {
				nativeExec(t, f.current.store.database, `UPDATE runtime_exchange_agent_identities SET recording_archive_epoch=0,recording_capture_generation=0`)
			}
			got, err := newActivityInspectionTestReader(t, f).GetConversationIdentity(context.Background(), "deepened")
			switch mode {
			case "session-conflict", "scope-conflict":
				if err == nil {
					t.Fatalf("conflicting identity accepted: %+v", got)
				}
			case "unknown-current":
				if err != nil || !got.Equal(original) {
					t.Fatalf("unknown current attributes relabeled from history: %+v %v", got, err)
				}
			default:
				if err != nil || got.ActorLabel != "Helper" || !got.ActorIsSubagent || got.Source != agentconversation.ClientIdentitySourceLocalState || !got.ObservedAt.Equal(now) {
					t.Fatalf("exact compatible deepening: %+v %v", got, err)
				}
			}
		})
	}
}

func TestActivityInspectionIdentityRejectsOversizedValueBeforeMaterialization(t *testing.T) {
	f := newEgressInspectionFixture(t)
	identity := agentconversation.ClientIdentity{Client: "codex", SessionID: "session", SessionResumable: true, Source: agentconversation.ClientIdentitySourceProtocolEvidence, Confidence: "exact", ObservedAt: time.Now().UTC().Truncate(time.Millisecond)}
	if err := f.current.ConversationIdentityRepository().(activity.ScopedConversationIdentityWriter).PutScopedConversationIdentity(context.Background(), f.scope, "large-identity", identity); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, f.current.store.database, `UPDATE runtime_exchange_agent_identities SET protocol_ids_json=json_array(json_object('name','key','value',printf('%.*c',8388608,'x')))`)
	reader := newActivityInspectionTestReader(t, f)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := reader.GetConversationIdentity(context.Background(), "large-identity")
	runtime.ReadMemStats(&after)
	if err == nil || got.SessionID != "" {
		t.Fatalf("invalid identity field accepted: %+v %v", got, err)
	}
	if bytes := after.TotalAlloc - before.TotalAlloc; bytes > 4<<20 {
		t.Fatalf("oversized identity field copied: %d bytes", bytes)
	}
}

func TestActivityInspectionSessionSummaryUsesOwnedIdentityAcrossSources(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	shared := inspectionActivity("session-shared", "shared", activity.KindExchangeCompleted, now)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, shared)
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, shared); err != nil {
		t.Fatal(err)
	}
	identity := agentconversation.ClientIdentity{Client: "codex", SessionID: "session", SessionResumable: true, Source: agentconversation.ClientIdentitySourceProtocolEvidence, Confidence: "exact", ObservedAt: now}
	if err := newActivityRepository(f.legacy, f.legacy, newOperationGate()).PutConversationIdentity(context.Background(), "shared", identity); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"shared", "identity-only"} {
		if err := f.current.ConversationIdentityRepository().(activity.ScopedConversationIdentityWriter).PutScopedConversationIdentity(context.Background(), f.scope, id, identity); err != nil {
			t.Fatal(err)
		}
	}
	other := inspectionActivity("session-other-client", "other-client", activity.KindExchangeCompleted, now)
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, other); err != nil {
		t.Fatal(err)
	}
	otherIdentity := identity.Clone()
	otherIdentity.Client = "claude"
	if err := f.current.ConversationIdentityRepository().(activity.ScopedConversationIdentityWriter).PutScopedConversationIdentity(context.Background(), f.scope, other.SubjectID, otherIdentity); err != nil {
		t.Fatal(err)
	}
	// An orphan association in one source cannot borrow a parent from current.
	orphan := inspectionActivity("old-orphan", "orphan", activity.KindExchangeCompleted, now)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, orphan)
	if err := newActivityRepository(f.legacy, f.legacy, newOperationGate()).PutConversationIdentity(context.Background(), orphan.SubjectID, identity); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, f.legacy, `DELETE FROM runtime_activities WHERE subject_id='orphan'`)
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, orphan); err != nil {
		t.Fatal(err)
	}
	result, err := newActivityInspectionTestReader(t, f).SummarizeExchanges(context.Background(), activity.SummaryScope{Client: "codex", SessionID: "session"})
	if err != nil || result.Requests != 1 || result.Succeeded != 1 || result.ContentBodiesAvailable != nil || result.CaptureObservation != nil {
		t.Fatalf("session roots/counts: %+v %v", result, err)
	}
}

func TestActivityInspectionConversationsCountDistinctRequestsAndKeepFirstPublication(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	makeTurn := func(id, group, label string) activity.Record {
		r := inspectionActivity("event-"+id, id, activity.KindExchangeCompleted, now)
		r.Conversation = &agentconversation.Ref{ProjectionID: "capture_run:audit-a:agent:" + group, DisplayName: label, Kind: agentconversation.KindAgent, Evidence: agentconversation.EvidenceExplicitActor, Actor: "actor-" + group}
		return r
	}
	a1, b1 := makeTurn("a1", "a", "Alpha"), makeTurn("b1", "b", "Beta")
	seedFrozenV1Activity(t, &Store{database: f.legacy}, a1)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, b1)
	copied := a1
	projection := *a1.Conversation
	projection.DisplayName = "Deepened Alpha"
	copied.Conversation = &projection
	for _, record := range []activity.Record{copied, makeTurn("a2", "a", "")} {
		if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, record); err != nil {
			t.Fatal(err)
		}
	}
	identity := agentconversation.ClientIdentity{Client: "codex", SessionID: "only-identity", SessionResumable: true, Source: agentconversation.ClientIdentitySourceProtocolEvidence, Confidence: "exact", ObservedAt: now}
	if err := f.current.ConversationIdentityRepository().(activity.ScopedConversationIdentityWriter).PutScopedConversationIdentity(context.Background(), f.scope, "identity-only", identity); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	first, err := reader.ListConversations(context.Background(), activity.ConversationIndexRequest{Limit: 1, CaptureRunID: "audit-a"})
	if err != nil || len(first.Items) != 1 || first.Items[0].Conversation.ProjectionID != b1.Conversation.ProjectionID || first.Items[0].TurnCount != 1 || first.NextCursor == "" || first.NextBeforeFirstSequence != 0 {
		t.Fatalf("first publication ordering: %+v %v", first, err)
	}
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, b1); err != nil {
		t.Fatal(err)
	}
	second, err := reader.ListConversations(context.Background(), activity.ConversationIndexRequest{Limit: 1, CaptureRunID: "audit-a", Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.Items[0].TurnCount != 2 || second.Items[0].Latest.SubjectID != "a2" || second.Items[0].Conversation.DisplayName != "Deepened Alpha" || second.NextCursor != "" {
		t.Fatalf("global distinct turns/stable cursor: %+v %v", second, err)
	}
}

func TestActivityInspectionSearchFiltersGlobalWinnerAndCrossSourceMetadata(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeExec(t, f.legacy, `INSERT INTO capture_runs(run_id,proxy_capability_hash,control_capability_hash,cwd,canonical_executable_path,executable_label,client_catalog_revision,state,created_at_unix_ms,expires_at_unix_ms,updated_at_unix_ms,workspace_id,workspace_label) VALUES('audit-a',randomblob(32),randomblob(32),'/tmp','/bin/tool','Frozen legacy tool',1,'finished',?,?,?,'legacy-workspace','Frozen legacy workspace')`, now.Add(-time.Hour).UnixMilli(), now.Add(time.Hour).UnixMilli(), now.UnixMilli())
	seedFrozenV1Activity(t, &Store{database: f.legacy}, inspectionActivity("search-start", "shared-search", activity.KindExchangeStarted, now))
	terminal := inspectionActivity("search-terminal", "shared-search", activity.KindExchangeCompleted, now.Add(time.Second))
	terminal.WorkspaceID = "current-workspace"
	terminal.WorkspaceLabel = "Saved workspace"
	terminal.CaptureLabel = "Saved caption"
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, terminal); err != nil {
		t.Fatal(err)
	}
	legacy := inspectionActivity("search-legacy", "legacy-search", activity.KindExchangeCompleted, now)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, legacy)
	for i := 0; i < 5; i++ {
		record := inspectionActivity(fmt.Sprintf("recent-%d", i), fmt.Sprintf("recent-%d", i), activity.KindExchangeCompleted, now.Add(time.Minute))
		if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, record); err != nil {
			t.Fatal(err)
		}
	}
	content := transcriptContentRecordFixture(t, legacy.SubjectID, now, exchangecontent.ParentRef{CaptureRunID: f.scope.Capture.ID}, []transcriptMessage{{role: protocolcore.RoleUser, text: "retained request"}}, "retained response")
	content.Frozen = exchangecontent.FrozenRef{EnvironmentID: legacy.EnvironmentID, EnvironmentRevision: legacy.EnvironmentRevision, EnvironmentDigest: legacy.EnvironmentDigest, ClientEndpointID: legacy.ClientEndpointID, ClientEndpointRevision: legacy.ClientEndpointRevision, ProtocolPlanID: legacy.ProtocolPlanID, ProtocolPlanRevision: legacy.ProtocolPlanRevision, RouteID: legacy.RouteID, RouteRevision: legacy.RouteRevision}
	content.Response.ReportedModel = "global-target"
	content.Request.Tools = []exchangecontent.ToolDefinition{{Name: "target_tool"}}
	limits, err := exchangecontent.DefaultSourceLimits()
	if err != nil {
		t.Fatal(err)
	}
	source, err := exchangecontent.SourceFromRecordWithin(limits, content)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.current.ExchangeContentRepository().(exchangecontent.ScopedSourceRepository).PutScopedSource(context.Background(), f.scope, source); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	pending, err := reader.Search(context.Background(), activity.SearchQuery{Limit: 1, Status: activity.StatusPending})
	if err != nil || len(pending.Items) != 0 {
		t.Fatalf("filtered old start resurrected: %+v %v", pending, err)
	}
	model, err := reader.Search(context.Background(), activity.SearchQuery{Limit: 1, Model: "global-target", Tool: "target_tool"})
	if err != nil || len(model.Items) != 1 || model.Items[0].Record.SubjectID != legacy.SubjectID || model.Items[0].Context.ReportedModel != "global-target" || !model.Items[0].Context.ContentAvailable || !model.InspectionExpiresAt.Equal(content.ExpiresAt) {
		t.Fatalf("cross-source metadata before limit: %+v %v", model, err)
	}
	labels, err := reader.Search(context.Background(), activity.SearchQuery{Limit: 1, Text: "Frozen legacy workspace"})
	if err != nil || len(labels.Items) != 1 || labels.Items[0].Record.SubjectID != legacy.SubjectID || labels.Items[0].Context.CaptureLabel != "Frozen legacy tool" {
		t.Fatalf("frozen source-local search labels: %+v %v", labels, err)
	}
	saved, err := reader.Search(context.Background(), activity.SearchQuery{Limit: 1, Text: "Saved caption"})
	if err != nil || len(saved.Items) != 1 || saved.Items[0].Record.SubjectID != terminal.SubjectID {
		t.Fatalf("current original label: %+v %v", saved, err)
	}
}

func TestActivityInspectionSearchDoesNotDecodeUnrelatedTransport(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, record := range []activity.Record{inspectionActivity("unrelated-start", "unrelated", activity.KindExchangeStarted, now), inspectionActivity("unrelated-terminal", "unrelated", activity.KindExchangeCompleted, now), inspectionActivity("wanted-event", "wanted-exchange", activity.KindExchangeCompleted, now)} {
		if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, record); err != nil {
			t.Fatal(err)
		}
	}
	nativeExec(t, f.current.store.database, `UPDATE runtime_activities SET transport_evidence_json=CAST('{}' AS BLOB) WHERE activity_id='unrelated-terminal'`)
	result, err := newActivityInspectionTestReader(t, f).Search(context.Background(), activity.SearchQuery{Limit: 1, Text: "wanted-exchange"})
	if err != nil || len(result.Items) != 1 || result.Items[0].Record.SubjectID != "wanted-exchange" {
		t.Fatalf("unrelated non-search transport decoded: %+v %v", result, err)
	}
}

func TestActivityInspectionCarriesOriginalRepairRootAndAdvancesFilteredPages(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	archived := inspectionActivity("repair-archive", "archived", activity.KindExchangeCompleted, now)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, archived)
	current := inspectionActivity("repair-current", "current", activity.KindExchangeCompleted, now)
	written, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, current)
	if err != nil {
		t.Fatal(err)
	}
	identity := agentconversation.ClientIdentity{Client: "codex", SessionID: "session", SessionResumable: true, ProviderResponseID: "response", Source: agentconversation.ClientIdentitySourceLocalState, Confidence: "exact", ObservedAt: now}
	if err = f.current.ConversationIdentityRepository().(activity.ScopedConversationIdentityWriter).PutScopedConversationIdentity(context.Background(), f.scope, current.SubjectID, identity); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	got, err := reader.GetExchange(context.Background(), current.SubjectID)
	if err != nil || got.InspectionRoot == nil || !got.InspectionRoot.Current || got.InspectionRoot.SourceID != "current" || got.InspectionRoot.Scope != f.scope || got.InspectionRoot.Sequence != written.Sequence || got.InspectionRoot.ActivityID != current.ID || got.InspectionRoot.ExchangeID != current.SubjectID {
		t.Fatalf("original current repair root: %+v %v", got, err)
	}
	old, err := reader.GetExchange(context.Background(), archived.SubjectID)
	if err != nil || old.InspectionRoot == nil || old.InspectionRoot.Current || old.InspectionRoot.SourceID != "legacy" {
		t.Fatalf("archive incorrectly repairable: %+v %v", old, err)
	}
	page, err := reader.ListExchanges(context.Background(), activity.PageRequest{Limit: 1, CaptureRunID: "audit-a", WithoutLocalConversationIdentity: true})
	if err != nil || len(page.Items) != 0 || page.Consumed != 1 || page.NextCursor == "" {
		t.Fatalf("filtered page did not advance: %+v %v", page, err)
	}
	next, err := reader.ListExchanges(context.Background(), activity.PageRequest{Limit: 1, CaptureRunID: "audit-a", WithoutLocalConversationIdentity: true, Cursor: page.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].SubjectID != archived.SubjectID || next.NextCursor != "" {
		t.Fatalf("next retained page: %+v %v", next, err)
	}
}

func TestActivityInspectionAllReadersCloseSQLAndRecheckActualControl(t *testing.T) {
	for _, domain := range []string{"point", "events", "requests", "conversations", "summary", "search", "identity"} {
		t.Run(domain, func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			now := time.Now().UTC().Truncate(time.Millisecond)
			record := inspectionActivity("guard-event", "guard-exchange", activity.KindExchangeCompleted, now)
			if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, record); err != nil {
				t.Fatal(err)
			}
			identity := agentconversation.ClientIdentity{Client: "codex", SessionID: "session", SessionResumable: true, Source: agentconversation.ClientIdentitySourceProtocolEvidence, Confidence: "exact", ObservedAt: now}
			if err := f.current.ConversationIdentityRepository().(activity.ScopedConversationIdentityWriter).PutScopedConversationIdentity(context.Background(), f.scope, record.SubjectID, identity); err != nil {
				t.Fatal(err)
			}
			reader := newActivityInspectionTestReader(t, f)
			checked := false
			reader.visibility = &usageInspectionChangingVisibility{ControlStore: f.control, beforeCheck: func() {
				checked = true
				var busy, frames, checkpointed int
				if err := f.current.store.database.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &checkpointed); err != nil || busy != 0 {
					t.Fatalf("%s retained SQL over control check: %d %v", domain, busy, err)
				}
				f.reader.catalog.mu.Lock()
				active := f.reader.catalog.active
				f.reader.catalog.mu.Unlock()
				if active != 1 {
					t.Fatalf("%s released actual owner early: %d", domain, active)
				}
				nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
			}}
			var err error
			switch domain {
			case "point":
				var result activity.Record
				result, err = reader.GetExchange(context.Background(), record.SubjectID)
				if result.ID != "" {
					t.Fatal("stale point escaped")
				}
			case "events", "requests":
				var result activity.Page
				if domain == "events" {
					result, err = reader.List(context.Background(), activity.PageRequest{Limit: 10})
				} else {
					result, err = reader.ListExchanges(context.Background(), activity.PageRequest{Limit: 10})
				}
				if len(result.Items) != 0 {
					t.Fatal("stale page escaped")
				}
			case "conversations":
				var result activity.ConversationPage
				result, err = reader.ListConversations(context.Background(), activity.ConversationIndexRequest{Limit: 10})
				if len(result.Items) != 0 {
					t.Fatal("stale conversations escaped")
				}
			case "summary":
				var result activity.ExchangeSummary
				result, err = reader.SummarizeExchanges(context.Background(), activity.SummaryScope{CaptureRunID: "audit-a"})
				if result.Requests != 0 {
					t.Fatal("stale count escaped")
				}
			case "search":
				var result activity.SearchPage
				result, err = reader.Search(context.Background(), activity.SearchQuery{Limit: 10, Text: record.SubjectID})
				if len(result.Items) != 0 {
					t.Fatal("stale search escaped")
				}
			case "identity":
				var result agentconversation.ClientIdentity
				result, err = reader.GetConversationIdentity(context.Background(), record.SubjectID)
				if result.SessionID != "" {
					t.Fatal("stale identity escaped")
				}
			}
			if !checked || !errors.Is(err, ErrInspectionChanged) {
				t.Fatalf("%s final guard: checked%v err%v", domain, checked, err)
			}
		})
	}
}

func TestActivityInspectionHiddenOriginalsAndMissingMirrorStayDistinct(t *testing.T) {
	for _, mode := range []string{"deleted", "clear", "unknown", "missing-mirror"} {
		t.Run(mode, func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			record := inspectionActivity("hidden-event", "hidden-exchange", activity.KindExchangeCompleted, time.Now().UTC().Truncate(time.Millisecond))
			if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, record); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "deleted":
				nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='audit-a'`)
				seedCompleteInspectionMirror(t, f.current, f.control)
			case "clear":
				nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET archive_epoch=2`)
				seedCompleteInspectionMirror(t, f.current, f.control)
			case "unknown":
				nativeExec(t, f.current.store.database, `UPDATE runtime_activities SET recording_archive_epoch=0,recording_capture_generation=0`)
			case "missing-mirror":
				nativeExec(t, f.current.store.database, `DELETE FROM inspection_mirror_active`)
			}
			reader := newActivityInspectionTestReader(t, f)
			point, pointErr := reader.GetExchange(context.Background(), record.SubjectID)
			page, pageErr := reader.ListExchanges(context.Background(), activity.PageRequest{Limit: 10})
			conversations, conversationErr := reader.ListConversations(context.Background(), activity.ConversationIndexRequest{Limit: 10})
			summary, summaryErr := reader.SummarizeExchanges(context.Background(), activity.SummaryScope{CaptureRunID: "audit-a"})
			search, searchErr := reader.Search(context.Background(), activity.SearchQuery{Limit: 10, Text: record.SubjectID})
			if point.ID != "" || len(page.Items) != 0 || len(conversations.Items) != 0 || summary.Requests != 0 || len(search.Items) != 0 {
				t.Fatal("hidden original coordinates escaped")
			}
			if mode == "missing-mirror" {
				for _, err := range []error{pointErr, pageErr, conversationErr, summaryErr, searchErr} {
					if err == nil {
						t.Fatal("missing mirror became successful empty result")
					}
				}
			} else if !errors.Is(pointErr, activity.ErrExchangeNotFound) || pageErr != nil || conversationErr != nil || summaryErr != nil || searchErr != nil {
				t.Fatalf("hidden original semantics: %v %v %v %v %v", pointErr, pageErr, conversationErr, summaryErr, searchErr)
			}
		})
	}
}

func TestActivityInspectionConflictingTerminalRefusesBeforeSearchFilter(t *testing.T) {
	f := newEgressInspectionFixture(t)
	record := inspectionActivity("conflict-terminal", "conflict", activity.KindExchangeCompleted, time.Now().UTC().Truncate(time.Millisecond))
	legacy := record
	legacy.Status = activity.StatusFailed
	legacy.ReasonCode = "provider_failed"
	seedFrozenV1Activity(t, &Store{database: f.legacy}, legacy)
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, record); err != nil {
		t.Fatal(err)
	}
	got, err := newActivityInspectionTestReader(t, f).Search(context.Background(), activity.SearchQuery{Limit: 10, Status: activity.StatusFailed})
	if err == nil || len(got.Items) != 0 {
		t.Fatalf("contradictory old terminal disguised by current filter: %+v %v", got, err)
	}
}

func TestActivityInspectionSourceGenerationAfterAggregateWithholdsResults(t *testing.T) {
	f := newEgressInspectionFixture(t)
	record := inspectionActivity("source-guard", "source-guard", activity.KindExchangeCompleted, time.Now().UTC().Truncate(time.Millisecond))
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, record); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		token *InspectionMaintenance
		err   error
	}
	done := make(chan outcome, 1)
	reader := newActivityInspectionTestReader(t, f)
	reader.visibility = &usageInspectionChangingVisibility{ControlStore: f.control, beforeCheck: func() {
		f.reader.catalog.mu.Lock()
		changed := f.reader.catalog.changed
		f.reader.catalog.mu.Unlock()
		go func() {
			token, err := f.reader.catalog.BeginMaintenance(context.Background(), 2)
			done <- outcome{token, err}
		}()
		select {
		case <-changed:
		case <-time.After(time.Second):
			t.Fatal("source maintenance did not reserve generation")
		}
	}}
	got, err := reader.SummarizeExchanges(context.Background(), activity.SummaryScope{CaptureRunID: "audit-a"})
	if !errors.Is(err, ErrInspectionChanged) || got.Requests != 0 {
		t.Fatalf("stale aggregate escaped: %+v %v", got, err)
	}
	finished := <-done
	if finished.err != nil {
		t.Fatal(finished.err)
	}
	finished.token.Close()
}

func TestActivityInspectionSearchAndConversationCursorsBindSourceAndQuery(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, id := range []string{"cursor-one", "cursor-two"} {
		record := inspectionActivity("event-"+id, id, activity.KindExchangeCompleted, now)
		if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, record); err != nil {
			t.Fatal(err)
		}
	}
	reader := newActivityInspectionTestReader(t, f)
	search, err := reader.Search(context.Background(), activity.SearchQuery{Limit: 1, Text: "cursor"})
	if err != nil || search.NextCursor == "" {
		t.Fatalf("search cursor: %+v %v", search, err)
	}
	if _, err = reader.Search(context.Background(), activity.SearchQuery{Limit: 1, Text: "different", Cursor: search.NextCursor}); !errors.Is(err, ErrInspectionCursor) {
		t.Fatalf("changed search filter accepted: %v", err)
	}
	conversations, err := reader.ListConversations(context.Background(), activity.ConversationIndexRequest{Limit: 1})
	if err != nil || conversations.NextCursor == "" {
		t.Fatalf("conversation cursor: %+v %v", conversations, err)
	}
	if _, err = reader.ListConversations(context.Background(), activity.ConversationIndexRequest{Limit: 1, CaptureRunID: "audit-b", Cursor: conversations.NextCursor}); !errors.Is(err, ErrInspectionCursor) {
		t.Fatalf("changed conversation scope accepted: %v", err)
	}
	lease, err := f.reader.catalog.BeginMaintenance(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	if _, err = reader.Search(context.Background(), activity.SearchQuery{Limit: 1, Text: "cursor", Cursor: search.NextCursor}); !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("old search source generation accepted: %v", err)
	}
	if _, err = reader.ListConversations(context.Background(), activity.ConversationIndexRequest{Limit: 1, Cursor: conversations.NextCursor}); !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("old conversation source generation accepted: %v", err)
	}
	if _, err = reader.ListExchanges(context.Background(), activity.PageRequest{Limit: 1, BeforeSequence: 1}); !errors.Is(err, ErrInspectionCursor) {
		t.Fatalf("bare request sequence accepted: %v", err)
	}
	if _, err = reader.Search(context.Background(), activity.SearchQuery{Limit: 1, Text: "cursor", BeforeSequence: 1}); !errors.Is(err, ErrInspectionCursor) {
		t.Fatalf("bare search sequence accepted: %v", err)
	}
}

func TestActivityInspectionPerRecordCursorResumesInsidePage(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < 3; i++ {
		record := inspectionActivity(fmt.Sprintf("inside-%d", i), fmt.Sprintf("inside-%d", i), activity.KindExchangeCompleted, now)
		if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, record); err != nil {
			t.Fatal(err)
		}
	}
	reader := newActivityInspectionTestReader(t, f)
	page, err := reader.List(context.Background(), activity.PageRequest{Limit: 3, CaptureRunID: "audit-a"})
	if err != nil || len(page.Items) != 3 || page.Items[0].InspectionCursor == "" {
		t.Fatalf("missing per-item continuation: %+v %v", page, err)
	}
	resumed, err := reader.List(context.Background(), activity.PageRequest{Limit: 3, CaptureRunID: "audit-a", Cursor: page.Items[0].InspectionCursor})
	if err != nil || len(resumed.Items) != 2 || resumed.Items[0].SubjectID != "inside-1" || resumed.Items[1].SubjectID != "inside-0" {
		t.Fatalf("mid-page continuation skipped candidates: %+v %v", resumed, err)
	}
}
