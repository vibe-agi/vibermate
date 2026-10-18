package runtimedata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

// The established lifecycle sequence, using only exported role authority seams.
// No live Runtime, provider request, or private combined database participates.
func populateNativeBackup(t *testing.T, store *runtimepersistence.ControlStore, profile egressprofile.ProfileRevision) (nativetool.ID, uint64) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	mode := environment.ToolPolicyReview
	recording := environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 3650}
	execute := true
	aggregate := nativeBackupEnvironment(t)
	aggregate.ContentRecording = recording
	aggregate.PolicySet = &environment.PolicySet{ToolMode: mode}
	aggregate.ClientToolAccess = &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "native.mcp", Revision: 1, TargetOrigin: nativeBackupOrigin(t), Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, EgressProfile: authoredEgress(t, profile), Contract: environment.ClientToolContractCodexLegacy}}
	manager := nativeBackupEnvironmentManager(t, store)
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
	resolved, err := egressprofile.RestoreResolved(profile, egressprofile.SelectionFlow, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = plan.WithResolvedEgress(resolved)
	if err != nil {
		t.Fatal(err)
	}
	captures, err := manualcapture.NewManager(ctx, manualcapture.DefaultOptions(store.ManualCaptureRepository()))
	if err != nil {
		t.Fatal(err)
	}
	created, err := captures.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Synthetic Native", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	capture := captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: created.Capture.ID}
	source := captureassignment.SourceManualCreate
	assignment := captureassignment.Assignment{Capture: capture, EnvironmentID: aggregate.ID, EnvironmentRevision: 1, Revision: 1, Source: source}
	assignment.EnvironmentDigest = plan.EnvironmentDigest()
	assignment.LaunchAuthority, err = environment.NewLaunchAuthorityBoundaryFromScopes(aggregate.ID, 1, plan.EnvironmentDigest(), []string{"chatgpt.com:443"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assignment.UpdatedAt = now
	if result, err := store.CaptureAssignmentRepository().Write(ctx, 0, assignment); err != nil || result.Outcome != captureassignment.CommitOutcomeCommitted {
		t.Fatalf("assignment: %+v %v", result, err)
	}
	repo := store.NativeToolRepository()
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
		full := store.NativeToolRepository()
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
	return operation, revision
}

func nativeBackupOrigin(t *testing.T) originidentity.ProviderOrigin {
	t.Helper()
	v, err := originidentity.ParseProviderOrigin("https://chatgpt.com")
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func nativeBackupEnvironment(t *testing.T) environment.Environment {
	t.Helper()
	origin, err := originidentity.ParseClientOrigin("https://api.example.test")
	if err != nil {
		t.Fatal(err)
	}
	return environment.Environment{ID: "native-backup-environment", Name: "Native backup", State: environment.StateActive, Revision: 1, ContentRecording: environment.DefaultContentRecordingPolicy(), ClientEndpoints: []environment.ClientEndpoint{{ID: "native-endpoint", Revision: 1, ClientOrigin: origin, ProtocolPlans: []environment.ClientProtocolPlan{{ID: "native-plan", Revision: 1, ClientProtocol: environment.ClientProtocolAnthropicMessages, ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "native-adapter", Revision: 1}, EgressProfile: authoredEgress(t, egressprofile.Direct()), Destination: environment.DestinationPlan{Kind: environment.DestinationKindOriginal}}}}}}
}
func nativeBackupEnvironmentManager(t *testing.T, store *runtimepersistence.ControlStore) *environment.Manager {
	t.Helper()
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	var pairs []protocolspec.CodecPairDefinition
	for _, dialect := range []protocolspec.Dialect{protocolspec.DialectAnthropicMessages, protocolspec.DialectOpenAIResponses} {
		pairID, err := protocolspec.NewCodecPairID("backup." + string(dialect) + ".passthrough")
		if err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, protocolspec.CodecPairDefinition{ID: pairID, Revision: 1, ClientDialect: dialect, ProviderDialect: dialect, ClientOperationIDs: operations.SemanticOperationIDs(dialect), RequiredCapabilities: []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages, protocolspec.ProviderCapabilityStreaming, protocolspec.ProviderCapabilityToolCalls}})
	}
	protocols, err := protocolspec.NewCatalog(operations.Definitions(), pairs)
	if err != nil {
		t.Fatal(err)
	}
	wires, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := environment.NewCompiler(nil, nil, protocols, wires)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := environment.NewManager(context.Background(), store.EnvironmentRepository(), compiler, environment.NewAtomicProjection(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}
func nativeBackupAllow(t *testing.T, repo nativetool.AuthorityRepository, id nativetool.ID, now time.Time) {
	t.Helper()
	digest := sha256.Sum256([]byte("backup-approval/" + string(id)))
	approval, err := repo.RequestApproval(context.Background(), id, nativetool.ID(fmt.Sprintf("%x", digest[:16])), now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.DecideApproval(context.Background(), nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "backup-once-" + string(id), ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func TestRoleBackupPopulatedNativeRawSemanticACPAndAuthorityRoundTrip(t *testing.T) {
	source, layout := publishedRoleBackupFixture(t)
	ctx := context.Background()
	control, err := runtimepersistence.OpenControl(ctx, runtimepersistence.ControlOptions{DatabasePath: filepath.Join(source, "runtime.db"), LayoutID: layout.ID, BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := egressprofile.NewManager(control.EgressProfileRepository(), nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := egressnetwork.DefaultPolicy()
	policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:1080"}
	profile, err := profiles.Publish(ctx, egressprofile.PublishCommand{ID: "profile.backup-native", DisplayName: "Native preservation", Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	id, revision := populateNativeBackup(t, control, profile)
	repo := control.NativeToolRepository()
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeBackupAllow(t, repo, id, now)
	if _, err = repo.Start(ctx, id, "backup-native-attempt", revision, now); err != nil {
		t.Fatal(err)
	}
	if err = repo.Finish(ctx, id, nativetool.TerminalInput{Outcome: "uncertain", Reason: "execution_uncertain", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	operation, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if operation.BytesIn != nil || operation.BytesOut != nil {
		t.Fatal("fixture invented uncertain native byte counts")
	}
	obs, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: filepath.Join(source, "observations.db"), LayoutID: layout.ID, ArchiveID: currentBackupSourceID(layout.ID), BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
	if err != nil {
		t.Fatal(err)
	}
	history := nativetool.RecordedHistory{Scope: operation.RecordingScope, Observation: nativetool.HistoryObservation{Operation: operation, EventKind: "uncertain", EventAt: now, MaximumEventAt: operation.MaximumEventAt, MetadataExpiresAt: operation.MetadataExpiresAt}}
	if err = obs.PutNativeHistory(ctx, history); err != nil {
		t.Fatal(err)
	}
	nativeContent := nativetool.ContentObservation{Identity: nativetool.ContentIdentity{OperationID: id, Scope: operation.RecordingScope, EnvironmentID: operation.EnvironmentID, EnvironmentRevision: operation.EnvironmentRevision, EnvironmentDigest: operation.EnvironmentDigest, Kind: operation.Kind, ToolName: operation.ToolName, TargetOrigin: operation.TargetOrigin, CredentialOwner: operation.CredentialOwner, Recording: operation.Recording, CreatedAt: operation.CreatedAt, ExpiresAt: operation.CreatedAt.Add(time.Duration(operation.Recording.RetentionDays) * 24 * time.Hour)}, ObservedAt: now, Part: nativetool.ContentToolResult, Body: []byte(`{"content":[{"type":"text","text":"exact native backup body"}]}`)}
	if err = obs.PutNativeContent(ctx, nativeContent); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"exact-role-raw","messages":[{"role":"user","content":"retained bytes"}]}`)
	bodyHash := sha256.Sum256(body)
	raw := rawevidence.StoredEnvelope{EnvelopeID: "role-raw", WriterID: "role-writer", Watermark: 1, Layer: rawevidence.LayerClientIngress, ScopeKind: rawevidence.ScopeManualCapture, ScopeID: operation.Capture.ID, RecordingFence: operation.RecordingScope.Fence, ExchangeID: "role-exchange", ConnectionID: "role-connection", EnvironmentID: string(operation.EnvironmentID), EnvironmentRevision: uint64(operation.EnvironmentRevision), EnvironmentDigest: operation.EnvironmentDigest.String(), ClientEndpointID: "native-endpoint", ClientEndpointRevision: 1, ProtocolPlanID: "native-plan", ProtocolPlanRevision: 1, RouteID: "role-route", RouteRevision: 1, ObservedAt: now, ExpiresAt: now.Add(24 * time.Hour), Method: "POST", Scheme: "https", Authority: "api.example.test", Path: "/v1/messages", ContentType: "application/json", Representation: "http_message", Canonicalization: "go_net_http_v1", Body: body, BodyBytes: int64(len(body)), BodySHA256: bodyHash, DigestScope: rawevidence.DigestFull, PayloadState: rawevidence.PayloadCaptured, PayloadMetadata: []byte(`{"version":1,"headers":[]}`)}
	salt, err := obs.RawEvidenceRepository().RedactionSalt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = obs.RawEvidenceRepository().AppendBatch(ctx, []rawevidence.StoredEnvelope{raw}, now); err != nil {
		t.Fatal(err)
	}
	text, _ := protocolcore.NewTextBlock("exact semantic backup text")
	request := protocolcore.Request{RequestedModel: "role-model", EffectiveModel: "role-model", MaxOutputTokens: 16, Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{text}}}}
	response := protocolcore.Response{ID: "role-response", RequestedModel: "role-model", EffectiveModel: "role-model", ReportedModel: "role-model", Blocks: []protocolcore.ContentBlock{text}, StopReason: protocolcore.StopReasonEndTurn}
	semantic, err := exchangecontent.NewRecord("role-exchange", exchangecontent.FrozenRef{EnvironmentID: string(operation.EnvironmentID), EnvironmentRevision: 1, EnvironmentDigest: operation.EnvironmentDigest.String(), ClientEndpointID: "native-endpoint", ClientEndpointRevision: 1, ProtocolPlanID: "native-plan", ProtocolPlanRevision: 1, RouteID: "role-route", RouteRevision: 1}, environment.DefaultContentRecordingPolicy(), now, request, &response, exchangecontent.WithParentRef(exchangecontent.ParentRef{ManualCaptureID: operation.Capture.ID}))
	if err != nil {
		t.Fatal(err)
	}
	limits, err := exchangecontent.DefaultSourceLimits()
	if err != nil {
		t.Fatal(err)
	}
	contentSource, err := exchangecontent.SourceFromRecordWithin(limits, semantic)
	if err != nil {
		t.Fatal(err)
	}
	if err = obs.ExchangeContentRepository().(exchangecontent.ScopedSourceRepository).PutScopedSource(ctx, operation.RecordingScope, contentSource); err != nil {
		t.Fatal(err)
	}
	sourceSemantic, sourceReadErr := obs.ExchangeContentRepository().Get(ctx, "role-exchange", now)
	if sourceReadErr != nil {
		t.Fatalf("writer accepted semantic source but ordinary reader refused: %v", sourceReadErr)
	}
	if sourceSemantic.Request.Messages[0].Blocks[0].Text != "exact semantic backup text" || sourceSemantic.Response == nil || sourceSemantic.Response.Blocks[0].Text != "exact semantic backup text" {
		t.Fatal("source semantic body differs from authored sample")
	}
	semantic = sourceSemantic
	acp := acpobservation.Record{RunID: "role-acp", Scope: recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "role-acp"}, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}, Policy: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 7}, ExpiresAtMillis: now.Add(24 * time.Hour).UnixMilli(), Snapshot: acpobservation.Snapshot{Revision: 1, ProtocolVersion: 1, Agent: acpobservation.Agent{Name: "synthetic-agent", Version: "exact"}, Sessions: []acpobservation.Session{{ID: "session-role", CWD: "/synthetic", Operation: "new"}}, Prompts: []acpobservation.Prompt{{Sequence: 1, SessionID: "session-role", State: "completed", StopReason: "end_turn", UserText: "exact ACP user", AgentText: "exact ACP agent"}}, Final: true, ExitCode: new(int)}}
	if err = obs.PutACP(ctx, acp); err != nil {
		t.Fatal(err)
	}
	if err = obs.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err = control.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"native_tool_bindings", "native_tool_binding_egress", "native_tool_approvals", "native_tool_once", "native_tool_inbound", "native_tool_deliveries"} {
		if len(snapshotBackupTable(t, filepath.Join(source, "runtime.db"), table)) == 0 {
			t.Fatalf("empty native preservation fixture: %s", table)
		}
	}
	beforeControl := snapshotTestRows(t, source)
	beforeObs := snapshotTestRows(t, sourceObservationRoot(t, source))
	beforeArchive := snapshotTestRows(t, filepath.Join(source, layout.Archives[0].RelativeDirectory))
	before := snapshotTestFiles(t, source)
	backup := source + "-populated-backup"
	if err = Backup(ctx, source, backup, now); err != nil {
		t.Fatal(err)
	}
	manifest, err := readBackupManifest(backup)
	if err != nil || manifest.HistoryComplete == nil || *manifest.HistoryComplete || len(manifest.Roles) != 3 || manifest.Roles[2].Status != "unverified" {
		t.Fatalf("legacy semantic status lost: %+v %v", manifest.backupRoles, err)
	}
	restored := backup + "-restored"
	if err = Restore(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	// Restore keeps the layout needed to reproduce the same qualification; it
	// does not add a Runtime status registry or silently upgrade legacy semantics.
	rebackup := restored + "-rebackup"
	if err = Backup(ctx, restored, rebackup, now); err != nil {
		t.Fatal(err)
	}
	restoredManifest, err := readBackupManifest(rebackup)
	if err != nil || !reflect.DeepEqual(manifest.backupRoles, restoredManifest.backupRoles) {
		t.Fatal("restore/rebackup silently changed role qualification", err)
	}
	moved := source + "-populated-moved"
	if err = Copy(ctx, source, moved); err != nil {
		t.Fatal(err)
	}
	owner, host := upgradeGuards(t, source)
	private := source + "-populated-private"
	if err = CreateSchemaUpgradeSnapshot(ctx, source, private, owner, host, now); err != nil {
		t.Fatal(err)
	}
	owner.Release()
	host.Release()
	privateRestore := private + "-restored"
	if err = RestoreSchemaUpgradeSnapshot(ctx, private, privateRestore); err != nil {
		t.Fatal(err)
	}
	assertSnapshotSourceBytes(t, source, before)
	for _, target := range []string{restored, moved, privateRestore} {
		if !slices.Equal(beforeControl, snapshotTestRows(t, target)) || !slices.Equal(beforeObs, snapshotTestRows(t, sourceObservationRoot(t, target))) || !slices.Equal(beforeArchive, snapshotTestRows(t, filepath.Join(target, layout.Archives[0].RelativeDirectory))) {
			t.Fatal("role roundtrip changed exact control/observation/archive rows")
		}
		reader, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: filepath.Join(target, "observations.db"), LayoutID: layout.ID, ArchiveID: currentBackupSourceID(layout.ID), BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
		if err != nil {
			t.Fatal(err)
		}
		gotSalt, err := reader.RawEvidenceRepository().RedactionSalt(ctx)
		if err != nil || !bytes.Equal(gotSalt, salt) {
			t.Fatal("Raw redaction salt changed", err)
		}
		gotRaw, err := reader.RawEvidenceRepository().GetEnvelope(ctx, "role-raw")
		if err != nil || !reflect.DeepEqual(gotRaw, raw) {
			t.Fatalf("Raw roundtrip changed frozen fields/body: %v\nwant=%#v\ngot=%#v", err, raw, gotRaw)
		}
		gotContent, err := reader.ExchangeContentRepository().Get(ctx, "role-exchange", now)
		if err != nil || !reflect.DeepEqual(gotContent, semantic) {
			t.Fatalf("semantic roundtrip changed: %v\nwant=%#v\ngot=%#v", err, semantic, gotContent)
		}
		gotACP, err := reader.ACPSnapshots().Read(ctx, "role-acp", now.UnixMilli())
		if err != nil || !reflect.DeepEqual(gotACP, acp) {
			t.Fatalf("ACP roundtrip changed: %v", err)
		}
		gotHistory, err := reader.GetNativeHistory(ctx, id)
		if err != nil || !reflect.DeepEqual(gotHistory, history) {
			t.Fatalf("native history changed: %v", err)
		}
		gotNative, err := reader.GetNativeContent(ctx, id, nativetool.ContentToolResult)
		if err != nil || !reflect.DeepEqual(gotNative, nativeContent) {
			t.Fatalf("native content changed: %v", err)
		}
		reader.Shutdown(ctx)
	}
}
func sourceObservationRoot(t *testing.T, root string) string {
	// snapshotTestRows reads runtime.db; this independent physical copy only
	// supports all-column comparison and never transforms the actual output.
	t.Helper()
	temp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(filepath.Join(root, "observations.db") + suffix)
		if suffix != "" && os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(temp, "runtime.db")+suffix, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return temp
}
