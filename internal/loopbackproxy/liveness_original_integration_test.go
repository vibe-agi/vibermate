package loopbackproxy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/responseschat"
	"github.com/vibe-agi/vibermate/internal/ssewire"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

type originalPipelineObserver struct{}

func (originalPipelineObserver) ObserveStart(context.Context, exchange.StartObservation) error {
	return nil
}
func (originalPipelineObserver) ObserveTerminal(context.Context, exchange.AttemptObservation) error {
	return nil
}
func (originalPipelineObserver) ObserveContent(context.Context, exchange.ContentObservation) error {
	return nil
}

type originalDecisionCounter struct{ calls atomic.Int32 }

func (d *originalDecisionCounter) Decide(context.Context, exchange.ToolDecisionRequest) (exchange.ToolDecision, error) {
	d.calls.Add(1)
	return exchange.ToolDecision{Outcome: exchange.ToolDecisionRejected, ReasonCode: "unexpected_original_decision"}, nil
}

type trackedRealDecision struct {
	next              exchange.ToolDecisionGate
	entered, returned chan struct{}
	active            atomic.Int32
	once              sync.Once
}

func (d *trackedRealDecision) Decide(ctx context.Context, request exchange.ToolDecisionRequest) (exchange.ToolDecision, error) {
	d.active.Add(1)
	defer func() { d.active.Add(-1); close(d.returned) }()
	d.once.Do(func() { close(d.entered) })
	return d.next.Decide(ctx, request)
}

type originalStreamProvider struct {
	wire                 []byte
	hold                 bool
	requests             atomic.Int32
	closed, producerDone chan struct{}
	once                 sync.Once
	active               atomic.Int32
}
type originalStreamBody struct {
	*io.PipeReader
	owner *originalStreamProvider
	stop  func() bool
}

func (b *originalStreamBody) Read(p []byte) (int, error) {
	b.owner.active.Add(1)
	defer b.owner.active.Add(-1)
	return b.PipeReader.Read(p)
}
func (b *originalStreamBody) Close() error {
	b.stop()
	b.owner.once.Do(func() { close(b.owner.closed) })
	return b.PipeReader.Close()
}
func (p *originalStreamProvider) Do(ctx context.Context, _ providertransport.Request) (*http.Response, providertransport.Evidence, error) {
	p.requests.Add(1)
	reader, writer := io.Pipe()
	body := &originalStreamBody{PipeReader: reader, owner: p}
	body.stop = context.AfterFunc(ctx, func() { p.once.Do(func() { close(p.closed) }); _ = reader.CloseWithError(ctx.Err()) })
	go func() {
		defer close(p.producerDone)
		defer writer.Close()
		for start := 0; start < len(p.wire); start += 37 {
			end := min(start+37, len(p.wire))
			if _, err := writer.Write(p.wire[start:end]); err != nil {
				return
			}
			select {
			case <-p.closed:
				return
			case <-time.After(time.Millisecond):
			}
		}
		if p.hold {
			select {
			case <-p.closed:
			case <-ctx.Done():
			}
		}
	}()
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, providertransport.Evidence{}, nil
}

type originalPipelineExecution struct {
	pipeline *exchange.Pipeline
	done     chan struct{}
	result   exchange.Result
	err      error
}

func (e *originalPipelineExecution) Execute(ctx context.Context, r exchange.ClientRequest, d exchange.Downstream) (exchange.Result, error) {
	defer close(e.done)
	e.result, e.err = e.pipeline.Execute(ctx, r, d)
	return e.result, e.err
}

func originalLivenessCompiler(t *testing.T) environment.Compiler {
	t.Helper()
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	id, _ := protocolspec.NewCodecPairID(responseschat.ResponsesPassthroughCodecPairID)
	messagesID, _ := protocolspec.NewCodecPairID(anthropicchat.MessagesCodecPairID)
	protocols, err := protocolspec.NewCatalog(operations.Definitions(), []protocolspec.CodecPairDefinition{
		{ID: id, Revision: responseschat.ResponsesPassthroughCodecRevision, ClientDialect: protocolspec.DialectOpenAIResponses, ProviderDialect: protocolspec.DialectOpenAIResponses, ClientOperationIDs: operations.SemanticOperationIDs(protocolspec.DialectOpenAIResponses), RequiredCapabilities: []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages, protocolspec.ProviderCapabilityStreaming, protocolspec.ProviderCapabilityToolCalls}},
		{ID: messagesID, Revision: anthropicchat.MessagesCodecRevision, ClientDialect: protocolspec.DialectAnthropicMessages, ProviderDialect: protocolspec.DialectAnthropicMessages, ClientOperationIDs: operations.SemanticOperationIDs(protocolspec.DialectAnthropicMessages), RequiredCapabilities: []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages, protocolspec.ProviderCapabilityStreaming, protocolspec.ProviderCapabilityToolCalls}},
	})
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
	return compiler
}

func TestOriginalResponsesRealPipelineDeliveryAndDisconnect(t *testing.T) {
	for _, transform := range []bool{false, true} {
		for _, disconnect := range []bool{false, true} {
			name := "raw"
			if transform {
				name = "transformed"
			}
			if disconnect {
				name += "_disconnect"
			}
			t.Run(name, func(t *testing.T) {
				raw := &rawObserver{}
				composition := &proxyFixtureComposition{compiler: originalLivenessCompiler(t)}
				if transform {
					composition.configure = func(e *environment.Environment) {
						e.ClientEndpoints[0].ProtocolPlans[0].Transforms = []codelibrary.TransformRevision{{ID: "liveness.transform", Revision: 1, CollectionID: "synthetic", DisplayName: "Synthetic response transform", PublishedAt: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), Policy: messagetransform.Policy{ResponseJavaScript: `response.headers["x-liveness-transform"]="applied"; const payload=JSON.parse(response.body); if(payload.type==="response.output_text.delta"){payload.delta="rewritten";} response.body=JSON.stringify(payload);`}}}
					}
				}
				f := newProxyFixtureConfigured(t, protocolspec.DialectOpenAIResponses, nil, allowEverythingTestPolicy(t), raw, "", composition, "https://chatgpt.com")
				defer f.Close(t)
				encode := func(kind string, value any) []byte {
					body, _ := json.Marshal(value)
					wire, err := ssewire.Encode(ssewire.Event{Name: kind, Data: body})
					if err != nil {
						t.Fatal(err)
					}
					return wire
				}
				wire := encode("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "original-liveness", "model": "synthetic", "status": "in_progress", "output": []any{}}})
				wire = append(wire, []byte(": retained raw comment\n\n")...)
				wire = append(wire, encode("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "delta": "original", "output_index": 0, "content_index": 0, "item_id": "msg_original"})...)
				if !disconnect {
					wire = append(wire, encode("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": "original-liveness", "model": "synthetic", "created_at": 1, "status": "completed", "output": []any{}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}})...)
				}
				provider := &originalStreamProvider{wire: wire, hold: disconnect, closed: make(chan struct{}), producerDone: make(chan struct{})}
				gate, err := offlinehold.New(offlinehold.DefaultConfig())
				if err != nil {
					t.Fatal(err)
				}
				if err = gate.Start(context.Background(), offlinehold.RuntimeBinding{InstanceID: "original-liveness"}); err != nil {
					t.Fatal(err)
				}
				path, err := responseschat.NewResponsesPassthroughProtocolPath(openairesponses.DefaultOptions())
				if err != nil {
					t.Fatal(err)
				}
				selector, err := protocolpath.NewSelector(path)
				if err != nil {
					t.Fatal(err)
				}
				signer, err := clientannotation.NewSigner(bytes.Repeat([]byte{0x41}, 32))
				if err != nil {
					t.Fatal(err)
				}
				defer signer.Destroy()
				decisions := &originalDecisionCounter{}
				pipeline, err := exchange.New(exchange.Options{OwnerContext: context.Background(), Actions: gate, ProtocolPaths: selector, Provider: provider, ToolDecisions: decisions, RetryWaiter: exchange.TimerRetryWaiter{}, Observer: originalPipelineObserver{}, ContentObserver: originalPipelineObserver{}, ObservationTimeout: time.Second, Hold: exchange.HoldPolicy{MaxDuration: time.Second}, Stream: exchange.StreamBudgets{KeepaliveInterval: 10 * time.Millisecond, ProviderProgressTimeout: time.Second}, ClientAnnotations: signer, Now: time.Now})
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					provider.once.Do(func() { close(provider.closed) })
					pipeline.BeginShutdown()
					cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					if err := pipeline.Drain(cleanup); err != nil {
						t.Error(err)
					}
					gate.BeginShutdown()
					if err := gate.Drain(cleanup); err != nil {
						t.Error(err)
					}
				}()
				execution := &originalPipelineExecution{pipeline: pipeline, done: make(chan struct{})}
				f.exchanges.delegate = execution
				conn := f.ConnectTLS(t, f.grant.ProxyCapability.Value(), "chatgpt.com:443", "chatgpt.com")
				defer conn.Close()
				body := `{"model":"synthetic","input":"fixture","stream":true}`
				response := writeInnerRequest(t, conn, &http.Request{Method: "POST", URL: mustURL(t, "/backend-api/codex/responses"), Host: "chatgpt.com", Header: http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer synthetic-original-only"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))})
				if response.StatusCode != 200 {
					t.Fatalf("original pipeline status=%d", response.StatusCode)
				}
				var delivered []byte
				if disconnect {
					prefix := make([]byte, 16)
					if _, err := io.ReadFull(response.Body, prefix); err != nil {
						t.Fatal(err)
					}
					_ = conn.Close()
					_ = response.Body.Close()
				} else {
					delivered, err = io.ReadAll(response.Body)
					_ = response.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				select {
				case <-execution.done:
				case <-time.After(2 * time.Second):
					t.Fatal("real Original Pipeline did not return after delivery/disconnect")
				}
				select {
				case <-provider.producerDone:
				case <-time.After(time.Second):
					t.Fatal("Original provider producer did not join")
				}
				if provider.active.Load() != 0 || provider.requests.Load() != 1 || decisions.calls.Load() != 0 {
					t.Fatal("Original reader/attempt/decision lifecycle differs")
				}
				requests := f.exchanges.Requests()
				if len(requests) != 1 || !requests[0].RequestPlan().PreservesOriginalDestination() || requests[0].RequestPlan().Operation().ID().String() != operationcatalog.OpenAICodexResponsesCreateID || requests[0].CaptureRunRef() != f.grant.Run.ID {
					t.Fatal("real Original client plan/capture missing")
				}
				if _, exists := requests[0].RequestPlan().UpstreamRoute(); exists {
					t.Fatal("Original test silently selected managed route")
				}
				if disconnect {
					if execution.result.Outcome == exchange.AttemptSucceeded {
						t.Fatal("disconnected Original was claimed succeeded")
					}
				} else if execution.err != nil || execution.result.Outcome != exchange.AttemptSucceeded {
					t.Fatalf("original execution failed: %v", execution.err)
				}
				if !disconnect && !transform && !bytes.Equal(delivered, wire) {
					t.Fatal("RAW original bytes changed")
				}
				if !disconnect && transform && (response.Header.Get("X-Liveness-Transform") != "applied" || !bytes.Contains(delivered, []byte("rewritten")) || bytes.Contains(delivered, []byte(": retained raw comment"))) {
					t.Fatal("response transform was not actually applied")
				}
				deadline := time.Now().Add(time.Second)
				for len(raw.snapshot()) < 2 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				for _, observation := range raw.snapshot() {
					for _, frame := range observation.Frames {
						if frame.Kind == rawevidence.FrameKeepalive {
							t.Fatal("Original path inserted managed heartbeat timer")
						}
					}
				}
				t.Logf("real Original transform=%t disconnect=%t outcome=%s providerRequests=%d readerActive=%d decisions=%d", transform, disconnect, execution.result.Outcome, provider.requests.Load(), provider.active.Load(), decisions.calls.Load())
			})
		}
	}
}

func TestManagedRealDisconnectJoinsPendingAuthority(t *testing.T) {
	runManagedDecisionDisconnect(t, "")
}

func TestManagedDecisionTimerAllowDenyCancelRace(t *testing.T) {
	for _, decision := range []toolapproval.Decision{toolapproval.DecisionAllowOnce, toolapproval.DecisionDeny} {
		t.Run(string(decision), func(t *testing.T) { runManagedDecisionDisconnect(t, decision) })
	}
}

func runManagedDecisionDisconnect(t *testing.T, decision toolapproval.Decision) {
	raw := &rawObserver{}
	composition := &proxyFixtureComposition{compiler: originalLivenessCompiler(t), configure: func(e *environment.Environment) {
		configureLivenessUpstream(t, e, "https://chatgpt.com")
		e.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].AccountPolicy = environment.RouteAccountPolicy{Revision: 1, Mode: environment.AccountSelectionOriginal}
		e.PolicySet = &environment.PolicySet{ToolMode: environment.ToolPolicyReview}
	}}
	f := newProxyFixtureConfigured(t, protocolspec.DialectOpenAIResponses, nil, allowEverythingTestPolicy(t), raw, "", composition, "https://chatgpt.com")
	defer f.Close(t)
	encode := func(kind string, value any) []byte {
		body, _ := json.Marshal(value)
		wire, err := ssewire.Encode(ssewire.Event{Name: kind, Data: body})
		if err != nil {
			t.Fatal(err)
		}
		return wire
	}
	item := map[string]any{"type": "function_call", "id": "fc_disconnect", "call_id": "call_disconnect", "name": "update_plan", "arguments": `{"plan":[{"step":"Synthetic","status":"completed"}]}`, "status": "completed"}
	wire := encode("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_disconnect", "status": "in_progress", "output": []any{}}})
	wire = append(wire, encode("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})...)
	wire = append(wire, encode("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_disconnect", "model": "synthetic", "created_at": 1, "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}})...)
	provider := &originalStreamProvider{wire: wire, closed: make(chan struct{}), producerDone: make(chan struct{})}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = gate.Start(context.Background(), offlinehold.RuntimeBinding{InstanceID: "managed-disconnect"}); err != nil {
		t.Fatal(err)
	}
	path, err := responseschat.NewResponsesPassthroughProtocolPath(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	selector, err := protocolpath.NewSelector(path)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := clientannotation.NewSigner(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer signer.Destroy()
	tracked := &trackedRealDecision{next: f.approvals, entered: make(chan struct{}), returned: make(chan struct{})}
	policy, err := toolpolicy.New(tracked)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := exchange.New(exchange.Options{OwnerContext: context.Background(), Actions: gate, ProtocolPaths: selector, Provider: provider, ToolDecisions: policy, RetryWaiter: exchange.TimerRetryWaiter{}, Observer: originalPipelineObserver{}, ContentObserver: originalPipelineObserver{}, ObservationTimeout: time.Second, Hold: exchange.HoldPolicy{MaxDuration: time.Second}, Stream: exchange.StreamBudgets{KeepaliveInterval: 10 * time.Millisecond, ProviderProgressTimeout: time.Second}, ClientAnnotations: signer, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		provider.once.Do(func() { close(provider.closed) })
		pipeline.BeginShutdown()
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := pipeline.Drain(cleanup); err != nil {
			t.Error(err)
		}
		gate.BeginShutdown()
		if err := gate.Drain(cleanup); err != nil {
			t.Error(err)
		}
	}()
	execution := &originalPipelineExecution{pipeline: pipeline, done: make(chan struct{})}
	f.exchanges.delegate = execution
	conn := f.ConnectTLS(t, f.grant.ProxyCapability.Value(), "chatgpt.com:443", "chatgpt.com")
	defer conn.Close()
	body := `{"model":"synthetic","input":"fixture","stream":true,"tools":[{"type":"function","name":"update_plan","parameters":{"type":"object","properties":{"plan":{"type":"array","items":{"type":"object","properties":{"step":{"type":"string"},"status":{"type":"string"}},"required":["step","status"]}}},"required":["plan"]}}]}`
	response := writeInnerRequest(t, conn, &http.Request{Method: "POST", URL: mustURL(t, "/backend-api/codex/responses"), Host: "chatgpt.com", Header: http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer synthetic-original-only"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))})
	if response.StatusCode != 200 {
		t.Fatalf("managed pipeline status=%d", response.StatusCode)
	}
	prefix := make([]byte, 16)
	if _, err := io.ReadFull(response.Body, prefix); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tracked.entered:
	case <-time.After(time.Second):
		t.Fatal("real decision authority not entered")
	}
	var pending toolapproval.View
	deadline := time.Now().Add(time.Second)
	for pending.ID == "" && time.Now().Before(deadline) {
		page, err := f.approvals.ListApprovals(context.Background(), toolapproval.PageRequest{State: toolapproval.StatePending, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 1 {
			pending = page.Items[0]
		}
		if pending.ID == "" {
			time.Sleep(time.Millisecond)
		}
	}
	if pending.Kind != "tool_intent" || len(pending.SubjectRefs) != 1 || pending.SubjectRefs[0] != "call_disconnect" || pending.EnvironmentID != f.environment.ID.String() {
		t.Fatal("actual pending decision not bound to request")
	}
	if decision == "" {
		_ = conn.Close()
	} else {
		// A real decision races a real connection close after a local timer
		// becomes eligible. Either durable winner is allowed, never both.
		start := make(chan struct{})
		resolved := make(chan error, 1)
		disconnected := make(chan struct{})
		go func() {
			<-start
			command := toolapproval.DecisionCommand{ApprovalID: pending.ID, ExpectedRevision: pending.Revision, IdempotencyKey: "synthetic-liveness-race-0001", Decision: decision, Scope: toolapproval.ScopeRequest}
			if decision == toolapproval.DecisionDeny {
				command.ReasonCode = "synthetic_race_denied"
			}
			_, err := f.approvals.DecideApproval(context.Background(), command)
			resolved <- err
		}()
		go func() { defer close(disconnected); <-start; _ = conn.Close() }()
		time.AfterFunc(10*time.Millisecond, func() { close(start) })
		select {
		case err := <-resolved:
			if err != nil && !errors.Is(err, toolapproval.ErrRevisionConflict) {
				t.Errorf("unexpected actual decision race error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("real decision race did not return")
		}
		select {
		case <-disconnected:
		case <-time.After(time.Second):
			t.Fatal("real connection close did not return")
		}
	}
	_ = response.Body.Close()
	select {
	case <-execution.done:
	case <-time.After(2 * time.Second):
		t.Fatal("managed Execute did not return after actual TLS close")
	}
	select {
	case <-tracked.returned:
	case <-time.After(2 * time.Second):
		t.Fatal("real Authority worker did not return independently")
	}
	select {
	case <-provider.producerDone:
	case <-time.After(time.Second):
		t.Fatal("managed provider producer not joined")
	}
	view, err := f.approvals.GetApproval(context.Background(), pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	allowedWinner := decision == toolapproval.DecisionAllowOnce && view.State == toolapproval.StateAllowed
	if !allowedWinner && execution.result.Outcome == exchange.AttemptSucceeded || tracked.active.Load() != 0 || provider.active.Load() != 0 || provider.requests.Load() != 1 || view.State == toolapproval.StatePending || view.State == toolapproval.StateAllowed && !allowedWinner {
		t.Fatal("disconnect left live decision/read or approved effect")
	}
	deadline = time.Now().Add(time.Second)
	for len(raw.snapshot()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	for _, observation := range raw.snapshot() {
		if observation.Layer == rawevidence.LayerClientDownstream && bytes.Contains(observation.Body, []byte("function_call")) && !allowedWinner {
			t.Fatal("disconnected pending tool escaped")
		}
	}
	t.Logf("real TLS disconnect outcome=%s approval=%s decisionActive=%d readerActive=%d providerRequests=%d", execution.result.Outcome, view.State, tracked.active.Load(), provider.active.Load(), provider.requests.Load())
}
