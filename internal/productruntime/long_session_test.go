package productruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	goRuntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountselector"
	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

// The external transport is redirected to a private HTTP server. Compilation,
// account acquisition, codec execution, observers, Manager and Store are real.
func TestRuntimeLongSessionCompleteTail(t *testing.T) {
	t.Run("managed", func(t *testing.T) { testRuntimeLongSession(t, false) })
	t.Run("original_credentials", func(t *testing.T) { testRuntimeLongSession(t, true) })
	t.Run("original_destination", func(t *testing.T) { testRuntimeLongSessionScenario(t, true, "original_destination", false) })
	t.Run("original_invalid_tail", func(t *testing.T) { testRuntimeLongSessionScenario(t, true, "original_invalid_tail", false) })
}
func TestRuntimeLongSessionFinalSourceDrain(t *testing.T) { testRuntimeLongSession(t, false, true) }
func TestRuntimeLongSessionScriptCancellationDrainsOwnedSlot(t *testing.T) {
	testRuntimeLongSessionScenario(t, false, "script_cancel", false)
}
func TestRuntimeLongSessionResponseApprovalKeepsHistoryInert(t *testing.T) {
	testRuntimeLongSessionScenario(t, false, "tool_approval", false)
}
func TestRuntimeCandidateFinalSourceDrainControl(t *testing.T) {
	testRuntimeLongSessionScenario(t, false, "", true, 2)
}
func TestRuntimeCandidateCanceledContentDrainControl(t *testing.T) {
	testRuntimeLongSessionScenario(t, false, "content_cancel_drain", true, 2)
}
func TestRuntimeCandidateResponseApprovalControl(t *testing.T) {
	testRuntimeLongSessionScenario(t, false, "tool_approval", false, 3)
}
func testRuntimeLongSession(t *testing.T, original bool, holdFinal ...bool) {
	testRuntimeLongSessionScenario(t, original, "", len(holdFinal) > 0 && holdFinal[0])
}
func TestRuntimeLongSessionPolicyMatrix(t *testing.T) {
	for _, scenario := range []string{"selector_noop", "body_edit", "model_mapping", "invalid_tail", "transform_expansion", "metadata_only", "off", "cross_chat_body_edit", "cross_chat_invalid_tail"} {
		t.Run(scenario, func(t *testing.T) { testRuntimeLongSessionScenario(t, false, scenario, false) })
	}
}
func testRuntimeLongSessionScenario(t *testing.T, original bool, scenario string, holdFinal bool, fixtureCount ...int) {
	testRuntimeLongSessionFixture(t, original, scenario, holdFinal, nil, fixtureCount...)
}

// Acceptance options describe the fixture; ordinary construction reaches the
// copied production defaults without a test-only resource policy.
func testRuntimeLongSessionFixture(t *testing.T, original bool, scenario string, holdFinal bool, acceptance *longSessionAcceptanceOptions, fixtureCount ...int) {
	count := 4111
	if len(fixtureCount) > 0 {
		count = fixtureCount[0]
	}
	// This fixture watchdog covers setup, HTTP controls and final full-content
	// readbacks; product observation deadlines remain independent.
	fixtureBudget := 2 * time.Minute
	if acceptance != nil && acceptance.concurrentMode != nil {
		fixtureBudget = acceptance.concurrentMode.outer
	}
	ctx, stopFixture := context.WithTimeout(context.Background(), fixtureBudget)
	defer stopFixture()
	if acceptance != nil {
		count = acceptance.count
	}
	f := newAccountReadFixtureWithDriver(t, providerauth.StaticHeaderDriverRef())
	wantCredential := "Bearer " + f.token
	if original {
		f.aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].AccountPolicy = environment.RouteAccountPolicy{Revision: 1, Mode: environment.AccountSelectionOriginal}
		wantCredential = "Bearer original-long-session"
	}
	var calls atomic.Int32
	var diagnostics atomic.Int32
	var wantBody []byte
	var items []map[string]string
	const sentinel = "complete-long-session-final-sentinel-4111"
	wantTail, wantModel := sentinel, "long-model"
	protocol := &f.aggregate.ClientEndpoints[0].ProtocolPlans[0]
	route := &protocol.Destination.Upstream.Routes[0]
	crossChat := strings.HasPrefix(scenario, "cross_chat")
	if crossChat {
		endpoint, err := f.runtime.endpoints.Create(ctx, upstreamendpoint.CreateCommand{ID: "long-chat", DisplayName: "Long Chat", Origin: route.ProviderTarget.Origin, RealmID: "long-chat", BackendProtocols: []string{string(protocolspec.DialectOpenAIChat)}, Capabilities: route.ProviderTarget.Capabilities, Drivers: []providerauth.DriverRef{providerauth.StaticHeaderDriverRef()}})
		if err != nil {
			t.Fatal(err)
		}
		material, err := providerauth.NewMaterial(f.token, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer material.Destroy()
		encoded, err := material.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		defer clear(encoded)
		secret, err := secretstore.NewValue(encoded)
		if err != nil {
			t.Fatal(err)
		}
		defer secret.Destroy()
		account, err := f.runtime.accounts.Create(ctx, provideraccount.CreateCommand{ID: "long-chat-account", DisplayName: "Long Chat Account", UpstreamEndpointID: endpoint.ID, Driver: providerauth.StaticHeaderDriverRef(), Secret: secret})
		if err != nil {
			t.Fatal(err)
		}
		route.ProviderTarget = environment.ProviderTarget{ID: endpoint.ID.String(), Revision: environment.Revision(endpoint.Revision), Origin: endpoint.Origin, RealmID: endpoint.RealmID, Capabilities: endpoint.Capabilities}
		route.BackendProtocol = string(protocolspec.DialectOpenAIChat)
		route.AccountPolicy = environment.RouteAccountPolicy{Revision: 1, Mode: environment.AccountSelectionFixed, FixedAccountID: account.Account.ID.String(), Accounts: []environment.RouteAccountReference{{ID: account.Account.ID.String(), Revision: environment.Revision(account.Account.Revision), DisplayName: account.Account.DisplayName}}}
	}
	script := ""
	switch scenario {
	case "original_destination", "original_invalid_tail":
		protocol.Destination = environment.DestinationPlan{Kind: environment.DestinationKindOriginal}
	case "selector_noop":
		route.AccountPolicy.Mode = environment.AccountSelectionJavaScript
		route.AccountPolicy.FixedAccountID = ""
		route.AccountPolicy.Selector = &codelibrary.AccountSelectorRevision{ID: "long-selector", Revision: 1, CollectionID: "long", DisplayName: "Long selector", PublishedAt: time.Now().UTC(), Policy: accountselector.Policy{JavaScript: `if(JSON.parse(request.body).input[4110].content!=="` + sentinel + `")throw new Error("missing tail");selection.accountId=accounts[0].id;`}}
		script = `if(JSON.parse(request.body).input[4110].content!=="` + sentinel + `")throw new Error("missing tail");`
	case "body_edit":
		wantTail = "edited-" + sentinel
		script = `const p=JSON.parse(request.body);p.input[4110].content="` + wantTail + `";request.body=JSON.stringify(p);`
	case "model_mapping":
		wantModel = "mapped-long-model"
		route.ModelPolicy = environment.ModelPolicy{Revision: 1, Mode: environment.ModelModeMap, Mappings: []environment.ModelMapping{{RequestedModel: "long-model", UpstreamModel: wantModel}}}
	case "transform_expansion":
		script = `const p=JSON.parse(request.body);p.extension="x".repeat(16*1024*1024);request.body=JSON.stringify(p);`
	case "script_cancel":
		script = `for(;;){}`
	case "tool_approval":
		f.aggregate.PolicySet = &environment.PolicySet{ToolMode: environment.ToolPolicyReview}
	case "cross_chat_body_edit":
		wantTail = "edited-" + sentinel
		script = `const p=JSON.parse(request.body);p.messages[4110].content="` + wantTail + `";request.body=JSON.stringify(p);`
	case "cross_chat_invalid_tail":
		script = `const p=JSON.parse(request.body);p.messages[4110].role="invalid";request.body=JSON.stringify(p);`
	case "metadata_only":
		f.aggregate.ContentRecording.Mode = environment.ContentRecordingMetadataOnly
	case "off":
		f.aggregate.ContentRecording = environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}
	}
	if script != "" {
		protocol.Transforms = []codelibrary.TransformRevision{{ID: "long-transform", Revision: 1, CollectionID: "long", DisplayName: "Long transform", PublishedAt: time.Now().UTC(), Policy: messagetransform.Policy{RequestJavaScript: script}}}
	}
	stages := 0
	if scenario == "transform_http_two" || scenario == "transform_http_excess" {
		stages = 2
	}
	if scenario == "transform_http_three" {
		stages = 3
	}
	for i := 1; i <= stages; i++ {
		previous := wantTail
		wantTail += fmt.Sprintf("-%d", i)
		script := fmt.Sprintf(`const p=JSON.parse(request.body);if(p.input[0].content!==%q)throw new Error("stage order");p.input[0].content=%q;request.body=JSON.stringify(p);request.headers["x-stage"]=%q;`, previous, wantTail, fmt.Sprint(i))
		protocol.Transforms = append(protocol.Transforms, codelibrary.TransformRevision{ID: codelibrary.TransformID(fmt.Sprintf("stage-%d", i)), Revision: 1, CollectionID: "long", DisplayName: "Ordered stage", PublishedAt: time.Now().UTC(), Policy: messagetransform.Policy{RequestJavaScript: script}})
	}
	if scenario == "transform_http_excess" {
		policy := f.runtime.bodyAdmission.Policy()
		policy.ActiveBytes = policy.SlotBytes
		var err error
		f.runtime.bodyAdmission, err = exchange.NewBodyAdmission(policy)
		if err != nil {
			t.Fatal(err)
		}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		type wireMessage struct {
			Content string `json:"content"`
		}
		var body struct {
			Model    string        `json:"model"`
			Input    []wireMessage `json:"input"`
			Messages []wireMessage `json:"messages"`
		}
		received, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if stages == 0 && !crossChat && scenario != "body_edit" && scenario != "model_mapping" && !bytes.Equal(received, wantBody) {
			t.Error("same-dialect no-op changed complete wire bytes")
		}
		if stages > 0 && r.Header.Get("X-Stage") != fmt.Sprint(stages) {
			t.Error("ordered transform header missing")
		}
		if err := json.Unmarshal(received, &body); err != nil {
			t.Error(err)
		}
		if crossChat {
			body.Input = body.Messages
		}
		if len(body.Input) != count || body.Input[len(body.Input)-1].Content != wantTail || body.Model != wantModel {
			t.Errorf("upstream complete tail: items=%d", len(body.Input))
		}
		for i, v := range body.Input {
			want := items[i]["content"]
			if i == count-1 {
				want = wantTail
			}
			if v.Content != want {
				t.Errorf("upstream changed item %d", i)
			}
		}
		if r.Header.Get("Authorization") != wantCredential {
			t.Error("frozen credential missing")
		}
		w.Header().Set("Content-Type", "application/json")
		if scenario == "tool_approval" {
			io.WriteString(w, `{"id":"resp_long","object":"response","model":"long-model","status":"completed","output":[{"id":"fc_new","type":"function_call","call_id":"new_call","name":"f","arguments":"{}","status":"completed"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
			return
		}
		if crossChat {
			io.WriteString(w, `{"id":"resp_long","object":"chat.completion","created":1,"model":"`+wantModel+`","choices":[{"index":0,"message":{"role":"assistant","content":"complete-reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			return
		}
		io.WriteString(w, `{"id":"resp_long","object":"response","model":"`+wantModel+`","status":"completed","output":[{"id":"msg_reply","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"complete-reply","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	endpoint, _ := url.Parse(upstream.URL)
	// Use the real account authenticator and real provider Client around the
	// private network edge; no success is manufactured by a Provider fake.
	auth, err := providertransport.NewStaticBearerAuthenticator(f.secrets)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := providertransport.NewClient(providertransport.ClientOptions{Coordinator: f.runtime.offlineHold, Authenticators: []providertransport.Authenticator{auth}, Transport: privateLongSessionTransport{endpoint}, InstanceIDs: NewCryptographicInstanceIDSource(), Audit: f.runtime.egressCompletion})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Shutdown(ctx)
	annotations, err := clientannotation.Open(ctx, f.secrets, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer annotations.Destroy()
	decisions, err := toolpolicy.New(f.runtime.approvals)
	if err != nil {
		t.Fatal(err)
	}
	var recorder exchangecontent.Recorder = f.runtime.contents
	var transformedHTTP *transformHTTPRecorder
	if stages > 0 {
		transformedHTTP = &transformHTTPRecorder{Recorder: recorder, sink: recorder.(exchangecontent.SourceRecorder), completed: make(chan string, 1)}
		recorder = transformedHTTP
	}
	var four *fourHTTPContentSource
	if acceptance != nil && acceptance.httpFour {
		four = &fourHTTPContentSource{Recorder: recorder, sink: recorder.(exchangecontent.SourceRecorder), entered: make(chan string, 4), release: make(chan struct{})}
		recorder = four
	}
	var concurrent *concurrentContentBudgetSource
	if acceptance != nil && acceptance.controls != nil && !acceptance.httpFour {
		concurrent = &concurrentContentBudgetSource{Recorder: recorder, sink: f.runtime.contents.(exchangecontent.SourceRecorder), entered: make(chan string, 2), started: make(chan struct{}, 2), start: make(chan struct{}), overlap: make(chan struct{}), release: make(chan struct{})}
		recorder = concurrent
		t.Cleanup(func() { concurrent.once.Do(func() { close(concurrent.release) }) })
		t.Cleanup(func() { concurrent.startOnce.Do(func() { close(concurrent.start) }) })
	}
	if acceptance != nil {
		recorder = &longSessionAcceptanceRecorder{Recorder: recorder, sink: recorder.(exchangecontent.SourceRecorder), t: t}
	}
	var requestRecorded chan struct{}
	if scenario == "script_cancel" {
		requestRecorded = make(chan struct{})
		recorder = &notifyingLongSessionSource{Recorder: f.runtime.contents, sink: f.runtime.contents.(exchangecontent.SourceRecorder), done: requestRecorded}
	}
	var held *heldLongSessionSource
	if holdFinal {
		held = &heldLongSessionSource{Recorder: f.runtime.contents, sink: f.runtime.contents.(exchangecontent.SourceRecorder), entered: make(chan struct{}), release: make(chan struct{})}
		recorder = held
	}
	build := buildExchange
	if concurrent != nil {
		build = buildConcurrentContentFixtureExchange
	}
	pipeline, err := build(exchangeBuildRequest{bodyAdmission: f.runtime.bodyAdmission, ownerContext: ctx, actions: f.runtime.offlineHold, accounts: f.runtime.accounts, provider: provider, toolDecisions: decisions, activities: f.runtime.activities, identities: f.runtime.conversationIDs, contents: recorder, clock: SystemClock{}, hold: exchange.DefaultHoldPolicy(), annotations: annotations, reportObservationFailure: func(stage string, err error) {
		diagnostics.Add(1)
		t.Errorf("recording diagnostic: %s: %v", stage, err)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer pipeline.Shutdown(ctx)
	if four != nil {
		defer four.once.Do(func() { close(four.release) })
	}
	// Release borrowed barriers before Shutdown on every assertion failure.
	if held != nil {
		defer held.releaseOnce.Do(func() { close(held.release) })
	}
	if concurrent != nil {
		defer concurrent.once.Do(func() { close(concurrent.release) })
		defer concurrent.startOnce.Do(func() { close(concurrent.start) })
	}
	if acceptance != nil {
		acceptance.observePipelineBudget(t, pipeline)
	}
	compiler, err := productionEnvironmentCompiler(f.runtime.accounts, f.runtime.endpoints)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := compiler.Compile(f.aggregate)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := snapshot.ResolveRequest(f.aggregate.ClientEndpoints[0].ClientOrigin, environment.RequestFacts{Target: protocolspec.RequestTarget{Method: "POST", Path: "/backend-api/codex/responses", Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := exchange.NewClientOperationEvidence(plan.Operation().ID(), plan.Operation().Revision(), "POST", "/backend-api/codex/responses", "")
	if err != nil {
		t.Fatal(err)
	}
	var lease *exchange.BodyLease
	if stages == 0 {
		lease, err = f.runtime.bodyAdmission.AcquirePlan(ctx, plan)
		if err != nil {
			t.Fatal(err)
		}
	}
	defer lease.Release()
	items = make([]map[string]string, count)
	for i := range items {
		items[i] = map[string]string{"role": "user", "content": fmt.Sprintf("%08d", i) + strings.Repeat("x", 972)}
		if acceptance != nil && acceptance.dense {
			items[i]["content"] = ""
		}
	}
	items[len(items)-1]["content"] = sentinel
	if scenario == "tool_approval" {
		items[0] = map[string]string{"type": "function_call", "id": "history_item", "call_id": "history_call", "name": "f", "arguments": "{}"}
		items[1] = map[string]string{"type": "function_call_output", "call_id": "history_call", "output": "historical result"}
	}
	if scenario == "invalid_tail" || scenario == "original_invalid_tail" {
		items[len(items)-1]["role"] = "invalid-role"
	}
	payload := map[string]any{"model": "long-model", "input": items, "stream": false}
	if scenario == "tool_approval" {
		payload["tools"] = []any{map[string]any{"type": "function", "name": "f", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}
	}
	body, err := json.Marshal(payload)
	wantBody = body
	if err != nil {
		t.Fatal(err)
	}
	if four != nil {
		lease.Release()
		testDefaultFourHTTPExecutions(t, ctx, f, pipeline, body, four, acceptance.controls, func() int32 { return calls.Load() }, func() int32 { return diagnostics.Load() }, count, sentinel)
		return
	}
	if transformedHTTP != nil {
		lease.Release()
		excess := scenario == "transform_http_excess"
		testTransformHTTPExecution(t, ctx, f, pipeline, plan, body, transformedHTTP, sentinel, excess)
		wantCalls := int32(1)
		if excess {
			wantCalls = 0
		}
		if calls.Load() != wantCalls || diagnostics.Load() != 0 {
			t.Fatalf("upstream=%d diagnostics=%d", calls.Load(), diagnostics.Load())
		}
		return
	}
	t.Logf("complete request: items=%d wire=%d sentinel=%s", count, len(body), sentinel)
	if acceptance != nil {
		acceptance.measureBody(t, body)
	}
	request, err := exchange.NewClientRequest("long-session-4111", plan, operation, body, exchange.ReplayGenerationCostOnly, wireprofile.ApplicationProtocolHTTP1, exchange.WithBodyLease(lease), exchange.WithOriginalHeaders(http.Header{"Authorization": {wantCredential}, "Content-Type": {"application/json"}}))
	if err != nil {
		t.Fatal(err)
	}
	var result exchange.Result
	if concurrent != nil {
		testContentBudgetConcurrentExecutions(t, ctx, f, pipeline, request, lease, plan, operation, body, wantCredential, concurrent, acceptance.controls, func() int32 { return calls.Load() }, func() int32 { return diagnostics.Load() }, count, sentinel)
		return
	}
	if scenario == "tool_approval" {
		downstream := &longSessionDownstream{}
		done := make(chan error, 1)
		go func() { _, err := pipeline.Execute(ctx, request, downstream); done <- err }()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		var pending toolapproval.View
		for pending.ID == "" {
			select {
			case err := <-done:
				t.Fatalf("response did not wait for real approval: %v", err)
			case <-ticker.C:
			}
			page, err := f.runtime.approvals.ListApprovals(ctx, toolapproval.PageRequest{State: toolapproval.StatePending, Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) > 0 {
				if len(page.Items) != 1 {
					t.Fatal("history produced new approval intents")
				}
				pending = page.Items[0]
			}
		}
		if calls.Load() != 1 || downstream.Len() != 0 || len(pending.SubjectRefs) != 1 || pending.SubjectRefs[0] != "new_call" {
			t.Fatalf("approval boundary changed: %+v bytes=%d calls=%d", pending, downstream.Len(), calls.Load())
		}
		if _, err := f.runtime.approvals.DecideApproval(ctx, toolapproval.DecisionCommand{ApprovalID: pending.ID, ExpectedRevision: pending.Revision, IdempotencyKey: "long-response-approval-once", Decision: toolapproval.DecisionAllowOnce, Scope: toolapproval.ScopeRequest}); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(downstream.Bytes(), []byte("new_call")) {
			t.Fatal("approved response tool was not released")
		}
		projection, err := f.runtime.ExchangeContents().GetProjection(ctx, "long-session-4111", exchangecontent.RequestViewFull)
		if err != nil {
			t.Fatal(err)
		}
		if len(projection.Request.Messages) != count || projection.Request.Messages[0].Blocks[0].CallID != "history_call" || projection.Response == nil || projection.Response.Blocks[0].CallID != "new_call" || diagnostics.Load() != 0 {
			t.Fatal("approved response/history evidence incomplete")
		}
		return
	}
	if scenario == "script_cancel" {
		scriptContext, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := pipeline.Execute(scriptContext, request, &longSessionDownstream{}); done <- err }()
		// Finish the actual synchronous Store write before stack observation:
		// repeatedly taking all-goroutine stacks during SQLite work perturbs its
		// unchanged observer deadline and is not a valid script-entry barrier.
		select {
		case <-requestRecorded:
		case err := <-done:
			t.Fatalf("request recording did not finish: %v", err)
		}
		// Observe the real VM executing the operator function. Sampling is
		// throttled because Stack(all=true) stops the world; elapsed time is
		// never the causal proof and failure to observe entry fails this test.
		stack := make([]byte, 128<<10)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case err := <-done:
				t.Fatalf("script ended before entry observation: %v", err)
			case <-ticker.C:
			}
			n := goRuntime.Stack(stack, true)
			if bytes.Contains(stack[:n], []byte("messagetransform.executeAndExportStage")) && bytes.Contains(stack[:n], []byte("goja.(*vm).run")) {
				break
			}
		}
		lease.Release()
		canceled, stop := context.WithCancel(ctx)
		stop()
		pipelineErr, gateErr := pipeline.Drain(canceled), f.runtime.bodyAdmission.Drain(canceled)
		cancel()
		err := <-done
		if err == nil || calls.Load() != 0 || !errors.Is(pipelineErr, context.Canceled) || !errors.Is(gateErr, context.Canceled) {
			t.Fatalf("live script ownership lost: execute=%v pipeline=%v gate=%v calls=%d", err, pipelineErr, gateErr, calls.Load())
		}
		if err := f.runtime.bodyAdmission.Drain(ctx); err != nil {
			t.Fatal(err)
		}
		return
	}
	if held == nil {
		result, err = pipeline.Execute(ctx, request, &longSessionDownstream{})
	} else {
		type execution struct {
			result exchange.Result
			err    error
		}
		done := make(chan execution, 1)
		client, cancelClient := context.WithCancel(ctx)
		defer cancelClient()
		go func() { r, e := pipeline.Execute(client, request, &longSessionDownstream{}); done <- execution{r, e} }()
		select {
		case <-held.entered:
		case <-ctx.Done():
			t.Fatal("Source entry watchdog: ", ctx.Err())
		}
		if scenario == "content_cancel_drain" {
			cancelClient()
			pipeline.BeginShutdown()
			if held.observed.Err() != nil {
				t.Fatal("caller/shutdown canceled real borrowed Source context")
			}
		}
		lease.Release()
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		pipelineErr, gateErr := pipeline.Drain(canceled), f.runtime.bodyAdmission.Drain(canceled)
		held.releaseOnce.Do(func() { close(held.release) })
		var completed execution
		select {
		case completed = <-done:
		case <-ctx.Done():
			t.Fatal("Source completion watchdog: ", ctx.Err())
		}
		result, err = completed.result, completed.err
		if !errors.Is(pipelineErr, context.Canceled) || !errors.Is(gateErr, context.Canceled) {
			t.Fatalf("held real Source was refunded: Pipeline=%v gate=%v", pipelineErr, gateErr)
		}
		if err := pipeline.Drain(ctx); err != nil {
			t.Fatal(err)
		}
		if err := f.runtime.bodyAdmission.Drain(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if scenario == "invalid_tail" || scenario == "original_invalid_tail" || scenario == "transform_expansion" || scenario == "cross_chat_invalid_tail" {
		if err == nil || calls.Load() != 0 {
			t.Fatalf("invalid complete input forwarded: %v calls=%d", err, calls.Load())
		}
		if (scenario == "invalid_tail" || scenario == "original_invalid_tail") && result.AccountID != "" {
			t.Fatal("invalid last item acquired credential before complete validation")
		}
		return
	}
	if err != nil {
		t.Fatalf("real Runtime/Pipeline complete request: result=%+v error=%v upstream_calls=%d", result, err, calls.Load())
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d", calls.Load())
	}
	var projectionBefore, projectionAfter goRuntime.MemStats
	var projectionStart time.Time
	if acceptance != nil {
		goRuntime.ReadMemStats(&projectionBefore)
		projectionStart = time.Now()
	}
	projection, err := f.runtime.ExchangeContents().GetProjection(ctx, "long-session-4111", exchangecontent.RequestViewFull)
	if acceptance != nil {
		goRuntime.ReadMemStats(&projectionAfter)
		t.Logf("TASK6_FULL_PROJECTION elapsed_ns=%d total_alloc_bytes=%d error=%q", time.Since(projectionStart).Nanoseconds(), projectionAfter.TotalAlloc-projectionBefore.TotalAlloc, errorText(err))
	}
	if scenario == "off" {
		if !errors.Is(err, exchangecontent.ErrNotFound) {
			t.Fatalf("Off retained content: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if scenario == "metadata_only" {
		if len(projection.Request.Messages) != 4111 || projection.Request.Messages[4110].Blocks[0].Text != "" || projection.Response == nil || projection.Response.Blocks[0].Text != "" {
			t.Fatal("metadata-only retained private bodies or lost shape")
		}
		return
	}
	if len(projection.Request.Messages) != count || projection.Request.Messages[count-1].Blocks[0].Text != sentinel {
		t.Fatalf("Store complete tail: messages=%d", len(projection.Request.Messages))
	}
	for i, m := range projection.Request.Messages {
		if m.Role != "user" || len(m.Blocks) != 1 || m.Blocks[0].Text != items[i]["content"] {
			t.Fatalf("Store changed item %d", i)
		}
	}
	if projection.Response == nil || projection.Response.ID != "resp_long" || len(projection.Response.Blocks) != 1 || projection.Response.Blocks[0].Text != "complete-reply" || !projection.Response.Usage.Output.Known || projection.Response.Usage.Output.Tokens != 1 {
		t.Fatalf("Store response not complete: %+v", projection.Response)
	}
	if diagnostics.Load() != 0 {
		t.Fatalf("recording diagnostics=%d", diagnostics.Load())
	}
	t.Logf("upstream_calls=1 items=%d tail=%s Store_messages=%d", count, sentinel, len(projection.Request.Messages))
}

// Deliberately explicit test-policy override; production selects its defaults. This fixture
// does not establish the later whole-Runtime capacity/RSS calibration.
func longSessionTestPolicy() exchange.ResourcePolicy {
	content := exchangecontent.SourceLimits{
		Semantic: protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}},
		Scratch:  protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 32 << 20}, CanonicalBytes: 256 << 20, RetainedBytes: 64 << 20, StructureBytes: 32 << 20,
	}
	reserve, scratch, err := exchange.RequiredResponseReservation(content.Semantic)
	if err != nil {
		panic(err)
	}
	content.RetainedBytes += reserve.RetainedBytes
	content.CanonicalBytes += reserve.CanonicalBytes
	content.StructureBytes += reserve.StructureBytes
	content.Scratch = scratch
	request, response, err := exchange.RequiredExecutionEnvelope(content)
	if err != nil {
		panic(err)
	}
	request += 64 << 20 // finite test-only transform retention headroom; not a stage-count limit
	return exchange.ResourcePolicy{Content: content, RequestBytes: request, ResponseBytes: response, SlotBytes: request + response, ActiveBytes: 4 * (request + response)}
}

type privateLongSessionTransport struct{ endpoint *url.URL }

func (p privateLongSessionTransport) RoundTrip(r *http.Request, _ providertransport.TransportDispatch) (*http.Response, transportprofile.Evidence, error) {
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Scheme, u.Host = p.endpoint.Scheme, p.endpoint.Host
	copy.URL, copy.Host = &u, p.endpoint.Host
	response, err := http.DefaultTransport.RoundTrip(copy)
	return response, transportprofile.Evidence{}, err
}

// This holds the real synchronous Source handoff, then records into the same
// Runtime Manager/Store. It does not synthesize recording success or content.
type heldLongSessionSource struct {
	exchangecontent.Recorder
	sink             exchangecontent.SourceRecorder
	entered, release chan struct{}
	observed         context.Context
	releaseOnce      sync.Once
}

func (h *heldLongSessionSource) RecordSource(ctx context.Context, s *exchangecontent.Source) error {
	if s.Metadata().Response != nil {
		h.observed = ctx
		close(h.entered)
		<-h.release
	}
	return h.sink.RecordSource(ctx, s)
}

type notifyingLongSessionSource struct {
	exchangecontent.Recorder
	sink exchangecontent.SourceRecorder
	done chan struct{}
}

func (h *notifyingLongSessionSource) RecordSource(ctx context.Context, s *exchangecontent.Source) error {
	err := h.sink.RecordSource(ctx, s)
	if s.Metadata().Response == nil {
		close(h.done)
	}
	return err
}

type longSessionDownstream struct{ bytes.Buffer }

func (*longSessionDownstream) Begin(context.Context, exchange.ResponseEnvelope) error { return nil }
func (d *longSessionDownstream) Write(_ context.Context, b []byte) (int, error) {
	return d.Buffer.Write(b)
}
func (*longSessionDownstream) Keepalive(context.Context) error                     { return nil }
func (*longSessionDownstream) Abort(context.Context, exchange.FailureNotice) error { return nil }
