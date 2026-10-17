package runtimepersistence

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/conversationprojection"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"testing"
	"time"
)

type inspectionRepairCapture struct{ calls []activity.ConversationRepair }

func (w *inspectionRepairCapture) RepairConversation(_ context.Context, value activity.ConversationRepair) error {
	w.calls = append(w.calls, value)
	return nil
}

type inspectionRunReader struct{ capturerun.Reader }

func TestActivityIndexerInspectionReadsIdentityWithoutRepairOwner(t *testing.T) {
	f := newEgressInspectionFixture(t)
	identity := agentconversation.ClientIdentity{Client: "codex", SessionID: "independent", SessionResumable: true, ProviderResponseID: "response", Source: agentconversation.ClientIdentitySourceLocalState, Confidence: "exact", ObservedAt: time.Now().UTC().Truncate(time.Millisecond)}
	if err := f.current.ConversationIdentityRepository().(activity.ScopedConversationIdentityWriter).PutScopedConversationIdentity(context.Background(), f.scope, "identity-only", identity); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	indexer, err := conversationprojection.New(conversationprojection.Options{Activities: reader, Contents: reader.content, IdentityInspection: reader, CaptureRuns: inspectionRunReader{}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := indexer.Identity(context.Background(), "identity-only")
	if err != nil || got.SessionID != identity.SessionID {
		t.Fatalf("read-only identity: %+v %v", got, err)
	}
	if err := indexer.Reindex(context.Background(), activity.ConversationIndexRequest{CaptureRunID: "audit-a", Limit: 10}); err != nil {
		t.Fatalf("absent optional repair owner: %v", err)
	}
}

func TestActivityIndexerInspectionRepairsOnlyOriginalCurrentRoot(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	identity := agentconversation.ClientIdentity{Client: "codex", SessionID: "session", SessionResumable: true, ProviderResponseID: "response", Source: agentconversation.ClientIdentitySourceLocalState, Confidence: "exact", ObservedAt: now}
	archived := inspectionActivity("archived", "archived", activity.KindExchangeCompleted, now)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, archived)
	if err := newActivityRepository(f.legacy, f.legacy, newOperationGate()).PutConversationIdentity(context.Background(), archived.SubjectID, identity); err != nil {
		t.Fatal(err)
	}
	current := inspectionActivity("current", "current", activity.KindExchangeCompleted, now)
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, current); err != nil {
		t.Fatal(err)
	}
	if err := f.current.ConversationIdentityRepository().(activity.ScopedConversationIdentityWriter).PutScopedConversationIdentity(context.Background(), f.scope, current.SubjectID, identity); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	writer := &inspectionRepairCapture{}
	indexer, err := conversationprojection.New(conversationprojection.Options{Activities: reader, Contents: reader.content, IdentityInspection: reader, RepairWriter: writer, CaptureRuns: inspectionRunReader{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := indexer.Reindex(context.Background(), activity.ConversationIndexRequest{CaptureRunID: "audit-a", Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if len(writer.calls) != 1 || !writer.calls[0].Root.Current || writer.calls[0].Root.ExchangeID != "current" || writer.calls[0].Root.Scope != f.scope || writer.calls[0].Identity.SessionID != "session" {
		t.Fatalf("repairs: %+v", writer.calls)
	}
}

type changingIdentityInspection struct {
	activity.IdentityInspectionReader
	after func()
}

func (r changingIdentityInspection) GetConversationIdentity(ctx context.Context, id string) (agentconversation.ClientIdentity, error) {
	value, err := r.IdentityInspectionReader.GetConversationIdentity(ctx, id)
	if r.after != nil {
		r.after()
	}
	return value, err
}
func TestActivityIndexerInspectionRechecksIdentityAfterEnrichment(t *testing.T) {
	f := newEgressInspectionFixture(t)
	identity := agentconversation.ClientIdentity{Client: "codex", SessionID: "session", SessionResumable: true, Source: agentconversation.ClientIdentitySourceProtocolEvidence, Confidence: "exact", ObservedAt: time.Now().UTC().Truncate(time.Millisecond)}
	if err := f.current.ConversationIdentityRepository().(activity.ScopedConversationIdentityWriter).PutScopedConversationIdentity(context.Background(), f.scope, "identity", identity); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	identities := changingIdentityInspection{IdentityInspectionReader: reader, after: func() {
		nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
	}}
	indexer, err := conversationprojection.New(conversationprojection.Options{Activities: reader, Contents: reader.content, IdentityInspection: identities, CaptureRuns: inspectionRunReader{}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := indexer.Identity(context.Background(), "identity")
	if !errors.Is(err, ErrInspectionChanged) || got.SessionID != "" {
		t.Fatalf("stale enriched identity: %+v %v", got, err)
	}
}

type delayedEvidenceInspection struct {
	exchangecontent.InspectionReader
	wait bool
}

func (r *delayedEvidenceInspection) GetConversationEvidence(ctx context.Context, id string) (exchangecontent.ConversationEvidence, error) {
	result, err := r.InspectionReader.GetConversationEvidence(ctx, id)
	if err == nil && r.wait {
		time.Sleep(time.Until(result.ExpiresAt) + time.Millisecond)
	}
	return result, err
}
func TestActivityIndexerInspectionRetainsProtocolFallbackExpiry(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	record := transcriptContentRecordFixture(t, "protocol-only", now, exchangecontent.ParentRef{CaptureRunID: "audit-a"}, []transcriptMessage{{role: protocolcore.RoleUser, text: "body must not be read"}}, "response")
	record.Request.ProtocolEvidence = []protocolcore.ProtocolEvidenceValue{{Name: "openai_responses.session_id", Value: "session"}}
	record.ExpiresAt = now.Add(time.Second)
	if err := newExchangeContentRepository(f.legacy, f.legacy, newOperationGate(), nil).Put(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	contents := &delayedEvidenceInspection{InspectionReader: reader.content}
	indexer, err := conversationprojection.New(conversationprojection.Options{Activities: reader, Contents: contents, IdentityInspection: reader, CaptureRuns: inspectionRunReader{}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := indexer.InspectIdentity(context.Background(), record.ExchangeID)
	if err != nil || got.Identity.SessionID != "session" || !got.InspectionExpiresAt.Equal(record.ExpiresAt) {
		t.Fatalf("fallback original expiry: %+v %v", got, err)
	}
	contents.wait = true
	got, err = indexer.InspectIdentity(context.Background(), record.ExchangeID)
	if !errors.Is(err, ErrInspectionChanged) || got.Identity.SessionID != "" || !got.InspectionExpiresAt.IsZero() {
		t.Fatalf("expired enriched identity escaped: %+v %v", got, err)
	}
}

func TestActivityIndexerInspectionDoesNotRepairAfterVisibilityChange(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	record := inspectionActivity("repair-fenced", "repair-fenced", activity.KindExchangeCompleted, now)
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, record); err != nil {
		t.Fatal(err)
	}
	identity := agentconversation.ClientIdentity{Client: "codex", SessionID: "session", SessionResumable: true, ProviderResponseID: "response", Source: agentconversation.ClientIdentitySourceLocalState, Confidence: "exact", ObservedAt: now}
	if err := f.current.ConversationIdentityRepository().(activity.ScopedConversationIdentityWriter).PutScopedConversationIdentity(context.Background(), f.scope, record.SubjectID, identity); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	identities := changingIdentityInspection{IdentityInspectionReader: reader, after: func() {
		nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
	}}
	writer := &inspectionRepairCapture{}
	indexer, err := conversationprojection.New(conversationprojection.Options{Activities: reader, Contents: reader.content, IdentityInspection: identities, RepairWriter: writer, CaptureRuns: inspectionRunReader{}})
	if err != nil {
		t.Fatal(err)
	}
	err = indexer.Reindex(context.Background(), activity.ConversationIndexRequest{CaptureRunID: "audit-a", Limit: 10})
	if !errors.Is(err, ErrInspectionChanged) || len(writer.calls) != 0 || indexer.CaptureRunCaughtUp("audit-a") {
		t.Fatalf("stale repair admitted: %v %+v", err, writer.calls)
	}
}
