package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
)

func nativeDecisionFixture(t *testing.T, store *Store, id nativetool.ID) (nativetool.Binding, []protocolcore.ToolIntent) {
	t.Helper()
	ctx := context.Background()
	op, err := store.NativeLifecycleRepository().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := newEnvironmentManager(t, store).Resolve(op.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := snapshot.BeginProviderConnection(snapshot.Aggregate().ClientToolAccess.Route.TargetOrigin)
	if err != nil {
		t.Fatal(err)
	}
	request, err := snapshot.ResolveClientToolRequest(connection, environment.RequestFacts{Target: protocolspec.RequestTarget{Method: "POST", Path: environment.ClientToolMCPPath, Transport: protocolspec.ClientOperationTransportHTTP}})
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := request.ClientToolPlan()
	args, _ := protocolcore.NewJSONObject([]byte(`{"path":"a"}`), 1024)
	key, _ := protocolcore.NewCallKey("native", "3")
	intents := []protocolcore.ToolIntent{{ResponseID: "rpc-3", Ordinal: 0, Call: protocolcore.ToolCall{Key: key, Name: "read_file", Arguments: args}}}
	session := nativetool.ID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	observer, err := nativetool.NewCatalogObserver(nativetool.CatalogScope{Capture: op.Capture, Plan: plan, AssignmentRevision: 1, OwnerEpoch: 1, SessionID: session, SessionRevision: 3, CatalogGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := observer.Observe([]nativetool.CatalogPageObservation{{ExactRequest: []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`), ExactResponse: []byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"read_file","inputSchema":{"type":"object"}}]}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	digest := evidence.Digest()
	binding, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestToolsCall, Plan: plan, Capture: op.Capture, AssignmentRevision: 1, OwnerEpoch: 1, SessionID: session, SessionRevision: 3, CatalogGeneration: 1, CatalogDigest: &digest, CatalogEvidence: &evidence, ExactRPC: []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`), Intents: intents})
	if err != nil {
		t.Fatal(err)
	}
	if op.BindingDigest == nil || *op.BindingDigest != binding.Digest() {
		t.Fatal("fixture frozen binding mismatch")
	}
	return binding, intents
}
func TestNativeDecisionUnionNoWorkspace(t *testing.T) {
	store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyObserve)
	binding, intents := nativeDecisionFixture(t, store, id)
	policy, err := exchange.NewToolDecisionContext(binding.Plan().PolicySet(), "", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request, err := exchange.NewClientToolDecisionRequest(id, binding, policy, intents, nil)
	if err != nil {
		t.Fatal(err)
	}
	op, bound, ok := request.NativeExecution()
	if !ok || op != id || bound.Digest() != binding.Digest() {
		t.Fatal("native union lost identity")
	}
	native, ok := request.NativeContext()
	if !ok || native.PolicySet() != binding.Plan().PolicySet() {
		t.Fatal("native policy lost")
	}
	if _, available := native.WorkspaceRoot(); available || native.StructuredWorkspaceTools() || len(native.Tools()) != 0 || len(native.ToolNamespaces()) != 0 {
		t.Fatal("native workspace authority")
	}
	if request.ExchangeID() != "" || request.EnvironmentID() != "" || request.EnvironmentRevision() != 0 || request.EnvironmentDigest() != (environment.CandidateDigest{}) || request.RouteID() != "" || request.RouteRevision() != 0 || request.Context().PolicySet() != (environment.PolicySet{}) {
		t.Fatal("native request leaked model fields")
	}
	workspace, _ := exchange.NewToolDecisionContext(binding.Plan().PolicySet(), t.TempDir(), true, nil, nil)
	if _, err := exchange.NewClientToolDecisionRequest(id, binding, workspace, intents, nil); err == nil {
		t.Fatal("workspace accepted")
	}
	if _, err := exchange.NewClientToolDecisionRequest(id, binding, policy, nil, nil); err == nil {
		t.Fatal("unregistered zero-intent accepted")
	}
}
func TestNativeReviewUsesRealNativeApproval(t *testing.T) {
	store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyReview)
	binding, intents := nativeDecisionFixture(t, store, id)
	policy, _ := exchange.NewToolDecisionContext(binding.Plan().PolicySet(), "", false, nil, nil)
	request, err := exchange.NewClientToolDecisionRequest(id, binding, policy, intents, nil)
	if err != nil {
		t.Fatal(err)
	}
	options := toolapproval.DefaultOptions(store.ToolApprovalRepository())
	options.NativeRepository = store.NativeLifecycleRepository()
	options.Config.DecisionTimeout = time.Second
	authority, err := toolapproval.New(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := toolpolicy.New(authority)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan exchange.ToolDecision, 1)
	failures := make(chan error, 1)
	go func() {
		decision, err := gate.Decide(context.Background(), request)
		results <- decision
		failures <- err
	}()
	deadline := time.Now().Add(time.Second)
	var approvalID *nativetool.ID
	for time.Now().Before(deadline) {
		op, err := store.NativeLifecycleRepository().Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if op.ApprovalID != nil {
			approvalID = op.ApprovalID
			break
		}
		time.Sleep(time.Millisecond)
	}
	if approvalID == nil {
		t.Fatal("no real native question")
	}
	approval, err := store.NativeLifecycleRepository().GetApproval(context.Background(), *approvalID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.NativeLifecycleRepository().DecideApproval(context.Background(), nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "actual-native-answer-key", ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case decision := <-results:
		if decision.Outcome != exchange.ToolDecisionApproved {
			t.Fatalf("decision %+v", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("native wait did not resolve")
	}
	if err := <-failures; err != nil {
		t.Fatal(err)
	}
	var modelQuestions int
	if err := store.database.QueryRow(`SELECT COUNT(*) FROM tool_approvals`).Scan(&modelQuestions); err != nil || modelQuestions != 0 {
		t.Fatalf("model questions %d %v", modelQuestions, err)
	}
}

func TestNativeObserveStrictPersistNeutralOffMetadata(t *testing.T) {
	for _, mode := range []environment.ToolPolicyMode{environment.ToolPolicyObserve, environment.ToolPolicyStrict} {
		t.Run(string(mode), func(t *testing.T) {
			store, id, _ := nativeLifecycleFixture(t, mode)
			binding, intents := nativeDecisionFixture(t, store, id)
			policy, _ := exchange.NewToolDecisionContext(binding.Plan().PolicySet(), "", false, nil, nil)
			request, err := exchange.NewClientToolDecisionRequest(id, binding, policy, intents, nil)
			if err != nil {
				t.Fatal(err)
			}
			options := toolapproval.DefaultOptions(store.ToolApprovalRepository())
			options.NativeRepository = store.NativeLifecycleRepository()
			authority, err := toolapproval.New(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			gate, _ := toolpolicy.New(authority)
			decision, err := gate.Decide(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			want := exchange.ToolDecisionApproved
			state := "validated"
			source := "none"
			if mode == environment.ToolPolicyStrict {
				want = exchange.ToolDecisionRejected
				state = "denied"
				source = "policy"
			}
			if decision.Outcome != want {
				t.Fatalf("mode outcome %+v", decision)
			}
			op, err := store.NativeLifecycleRepository().Get(context.Background(), id)
			if err != nil || op.State != state || op.DecisionSource != source || op.ApprovalID != nil || op.Arguments != nil || op.Result != nil {
				t.Fatalf("neutral native %+v %v", op, err)
			}
			for _, table := range []string{"native_tool_approvals", "native_tool_contents", "runtime_raw_evidence_envelopes", "runtime_activities", "tool_approvals", "runtime_egress_attempts"} {
				var count int
				if err := store.database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("unexpected %s %d %v", table, count, err)
				}
			}
		})
	}
}

func TestNativeReviewAuthorityCancellation(t *testing.T) {
	store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyReview)
	binding, intents := nativeDecisionFixture(t, store, id)
	policy, _ := exchange.NewToolDecisionContext(binding.Plan().PolicySet(), "", false, nil, nil)
	request, err := exchange.NewClientToolDecisionRequest(id, binding, policy, intents, nil)
	if err != nil {
		t.Fatal(err)
	}
	options := toolapproval.DefaultOptions(store.ToolApprovalRepository())
	options.NativeRepository = store.NativeLifecycleRepository()
	options.Config.DecisionTimeout = time.Second
	authority, err := toolapproval.New(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := authority.Decide(ctx, request); result <- err }()
	deadline := time.Now().Add(time.Second)
	var approval *nativetool.ID
	for time.Now().Before(deadline) {
		op, err := store.NativeLifecycleRepository().Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if op.ApprovalID != nil {
			approval = op.ApprovalID
			break
		}
		time.Sleep(time.Millisecond)
	}
	if approval == nil {
		t.Fatal("question missing")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not finish")
	}
	closed, err := store.NativeLifecycleRepository().GetApproval(context.Background(), *approval)
	if err != nil || closed.State != "canceled" {
		t.Fatalf("question retained %+v %v", closed, err)
	}
}

func TestNativeDecisionUnionRegisteredReply(t *testing.T) {
	store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyReview)
	parent, intents := nativeDecisionFixture(t, store, id)
	_ = intents
	observer, err := nativetool.NewParentObserver(id, parent)
	if err != nil {
		t.Fatal(err)
	}
	replyID := nativetool.ID("88888888888888888888888888888888")
	rawRequest := []byte(`{"jsonrpc":"2.0","id":"server-1","method":"ping"}`)
	rawReply := []byte(`{"jsonrpc":"2.0","id":"server-1","result":{}}`)
	witness, err := observer.ObserveReply(replyID, rawRequest, rawReply)
	if err != nil {
		t.Fatal(err)
	}
	serverDigest := nativetool.Digest(sha256.Sum256(rawRequest))
	catalog, _ := parent.CatalogDigest()
	binding, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestServerReply, Plan: parent.Plan(), Capture: parent.Capture(), AssignmentRevision: 1, OwnerEpoch: 1, SessionID: parent.SessionID(), SessionRevision: parent.SessionRevision(), CatalogGeneration: parent.CatalogGeneration(), CatalogDigest: &catalog, ParentOperationID: &id, ServerRequestDigest: &serverDigest, ExactRPC: rawReply})
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := exchange.NewToolDecisionContext(parent.Plan().PolicySet(), "", false, nil, nil)
	if _, err := exchange.NewClientToolDecisionRequest(replyID, binding, policy, nil, &witness); err != nil {
		t.Fatal(err)
	}
	if _, err := exchange.NewClientToolDecisionRequest(replyID, binding, policy, nil, nil); err == nil {
		t.Fatal("raw parent pair admitted")
	}
	if _, err := exchange.NewClientToolDecisionRequest(id, binding, policy, nil, &witness); err == nil {
		t.Fatal("wrong operation witness admitted")
	}
}
