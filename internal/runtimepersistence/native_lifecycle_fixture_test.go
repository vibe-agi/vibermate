package runtimepersistence

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func nativeLifecycleFixture(t *testing.T, mode environment.ToolPolicyMode) (*Store, nativetool.ID, uint64) {
	return nativeLifecycleFixtureRecording(t, mode, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
}
func nativeLifecycleFixtureRecording(t *testing.T, mode environment.ToolPolicyMode, recording environment.ContentRecordingPolicy) (*Store, nativetool.ID, uint64) {
	return nativeFixture(t, mode, recording, false)
}
func nativeExecutionHandshakeFixture(t *testing.T) (*Store, nativetool.ID, uint64) {
	return nativeFixture(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, true)
}
func nativeFixture(t *testing.T, mode environment.ToolPolicyMode, recording environment.ContentRecordingPolicy, execute bool) (*Store, nativetool.ID, uint64) {
	return nativeFixtureWithCapture(t, mode, recording, execute, nil)
}
func nativeFixtureWithCapture(t *testing.T, mode environment.ToolPolicyMode, recording environment.ContentRecordingPolicy, execute bool, create func(*Store, time.Time) captureidentity.Reference) (*Store, nativetool.ID, uint64) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	t.Cleanup(func() { shutdownTestStore(t, store) })
	aggregate := environmentFixture(t, "native", 1)
	aggregate.ContentRecording = recording
	aggregate.PolicySet = &environment.PolicySet{ToolMode: mode}
	aggregate.ClientToolAccess = &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "native.mcp", Revision: 1, TargetOrigin: runtimeProviderOrigin(t, "https://chatgpt.com"), Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, EgressProfile: egressprofile.Direct(), Contract: environment.ClientToolContractCodexLegacy}}
	manager := newEnvironmentManager(t, store)
	draft, err := manager.SaveDraft(ctx, environment.DraftCommand{Candidate: aggregate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(ctx, aggregate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Publish(ctx, preview); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Resolve(aggregate.ID)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := snapshot.BeginProviderConnection(aggregate.ClientToolAccess.Route.TargetOrigin)
	if err != nil {
		t.Fatal(err)
	}
	request, err := snapshot.ResolveClientToolRequest(connection, environment.RequestFacts{Target: protocolspec.RequestTarget{Method: "POST", Path: environment.ClientToolMCPPath, Transport: protocolspec.ClientOperationTransportHTTP}})
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := request.ClientToolPlan()
	captures := newManualCaptureManager(t, store, &manualCaptureClock{now: now}, &manualCaptureRandom{})
	created, err := captures.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Synthetic Native", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	capture := captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: created.Capture.ID}
	source := captureassignment.SourceManualCreate
	if create != nil {
		capture = create(store, now)
		source = captureassignment.SourceLaunch
	}
	assignment := captureAssignmentFixture(capture, aggregate.ID, 1, source)
	assignment.EnvironmentDigest = plan.EnvironmentDigest()
	assignment.LaunchAuthority, err = environment.NewLaunchAuthorityBoundaryFromScopes(aggregate.ID, 1, plan.EnvironmentDigest(), []string{"chatgpt.com:443"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assignment.UpdatedAt = now
	if result, err := store.CaptureAssignmentRepository().Write(ctx, 0, assignment); err != nil || result.Outcome != captureassignment.CommitOutcomeCommitted {
		t.Fatalf("assignment: %+v %v", result, err)
	}
	repo := store.NativeLifecycleRepository()
	revision, err := repo.EnsureScope(ctx, nativetool.ScopeInput{Capture: capture, OwnerEpoch: 1, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	session := nativetool.ID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := repo.RegisterSession(ctx, nativetool.SessionInput{ID: session, Capture: capture, Plan: plan, AssignmentRevision: 1, ExpectedAuthorityRevision: revision, OwnerEpoch: 1, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	input := nativetool.BindingInput{Plan: plan, Capture: capture, AssignmentRevision: 1, OwnerEpoch: 1, SessionID: session, SessionRevision: 1}
	bind := func(id nativetool.ID, kind nativetool.RequestKind, raw string, intents []protocolcore.ToolIntent) nativetool.Binding {
		t.Helper()
		input.Kind = kind
		input.ExactRPC = []byte(raw)
		input.Intents = intents
		b, err := nativetool.NewBinding(input)
		if err != nil {
			t.Fatal(err)
		}
		rpc, ok := b.RPCIDDigest()
		if !ok {
			t.Fatal("missing RPC identity")
		}
		digest := b.RequestDigest()
		received, err := repo.Receive(ctx, nativetool.ReceiveInput{ID: id, Capture: capture, EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "synthetic-native", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: kind, SessionID: &session, RPCIDDigest: &rpc, RequestDigest: &digest, Recording: plan.ContentRecording(), ObservedAt: now})
		if err != nil || received.Duplicate {
			t.Fatalf("receive: %+v %v", received, err)
		}
		if err := repo.Bind(ctx, id, b, now); err != nil && !(kind == nativetool.RequestToolsCall && mode == environment.ToolPolicyStrict && errors.Is(err, nativetool.ErrDenied)) {
			t.Fatal(err)
		}
		return b
	}
	initialize := nativetool.ID("11111111111111111111111111111111")
	bind(initialize, nativetool.RequestInitialize, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, nil)
	handshakeAttempt := 0
	complete := func(id nativetool.ID) {
		t.Helper()
		if !execute {
			seedNativeHistoricalCompletion(t, store, id, now)
			return
		}
		full := nativeFullRepository(t, store)
		var generation int
		if err := store.database.QueryRowContext(ctx, `SELECT catalog_generation FROM native_tool_sessions WHERE session_id=?`, session).Scan(&generation); err != nil || generation != 0 {
			t.Fatalf("handshake invented catalog: %d %v", generation, err)
		}
		handshakeAttempt++
		receipt, err := full.Start(ctx, id, fmt.Sprintf("native-handshake-%d", handshakeAttempt), revision, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := full.VerifyStartReceipt(ctx, receipt, now); err != nil {
			t.Fatal(err)
		}
		if err := full.Finish(ctx, id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
			t.Fatal(err)
		}
		if countRows(t, store, "native_tool_approvals") != 0 {
			t.Fatal("handshake/list invented approval")
		}
	}
	complete(initialize)
	bind("22222222222222222222222222222222", nativetool.RequestInitialized, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if execute {
		complete("22222222222222222222222222222222")
	}
	sessionRevision, err := repo.ReadySession(ctx, session, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	input.SessionRevision = sessionRevision
	list := nativetool.ID("33333333333333333333333333333333")
	bind(list, nativetool.RequestToolsList, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	complete(list)
	observer, err := nativetool.NewCatalogObserver(nativetool.CatalogScope{Capture: capture, Plan: plan, AssignmentRevision: 1, OwnerEpoch: 1, SessionID: session, SessionRevision: sessionRevision + 1, CatalogGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := observer.Observe([]nativetool.CatalogPageObservation{{ExactRequest: []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`), ExactResponse: []byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"read_file","inputSchema":{"type":"object"}}]}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	input.SessionRevision, err = repo.SetCatalog(ctx, session, sessionRevision, evidence, now)
	if err != nil {
		t.Fatal(err)
	}
	input.CatalogGeneration = 1
	digest := evidence.Digest()
	input.CatalogDigest = &digest
	input.CatalogEvidence = &evidence
	args, _ := protocolcore.NewJSONObject([]byte(`{"path":"a"}`), 1024)
	key, _ := protocolcore.NewCallKey("native", "3")
	intents := []protocolcore.ToolIntent{{ResponseID: "rpc-3", Ordinal: 0, Call: protocolcore.ToolCall{Key: key, Name: "read_file", Arguments: args}}}
	operation := nativetool.ID("44444444444444444444444444444444")
	bind(operation, nativetool.RequestToolsCall, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, intents)
	return store, operation, revision
}

// HISTORICAL SYNTHETIC DATA: constrained datastore history only. No execution,
// authenticated observation, egress audit, receipt or network send is proved.
func seedNativeHistoricalCompletion(t *testing.T, store *Store, id nativetool.ID, now time.Time) {
	t.Helper()
	ctx := context.Background()
	tx, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE native_tool_operations SET state='completed',revision=revision+1,completed_at_unix_ms=?,terminal_reason='completed' WHERE operation_id=? AND state='validated' AND EXISTS(SELECT 1 FROM native_tool_bindings WHERE operation_id=?)`, now.UnixMilli(), id, id)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		t.Fatal("historical binding missing")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO native_tool_events(operation_id,operation_revision,kind,occurred_at_unix_ms) SELECT operation_id,revision,'completed',? FROM native_tool_operations WHERE operation_id=?`, now.UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var invalid int
	if err := store.database.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_operations WHERE operation_id=? AND (bytes_in IS NOT NULL OR bytes_out IS NOT NULL)`, id).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatal(fmt.Sprintf("historical unknown byte counts: %d %v", invalid, err))
	}
}
