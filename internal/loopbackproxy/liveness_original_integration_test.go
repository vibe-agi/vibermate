package loopbackproxy_test

import (
	"bytes"
	"context"
	"crypto/sha256"
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
					delivered = prefix
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
				observation, err := awaitOriginalDownstream(context.Background(), time.Now().Add(time.Second), raw.snapshot, requests[0], delivered, !disconnect)
				if err != nil {
					t.Fatal(err)
				}
				for _, frame := range observation.Frames {
					if frame.Kind == rawevidence.FrameKeepalive {
						t.Fatal("Original path inserted managed heartbeat timer")
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
	requests := f.exchanges.Requests()
	if len(requests) != 1 {
		t.Fatal("actual managed request missing")
	}
	observation, err := awaitOriginalDownstream(context.Background(), time.Now().Add(time.Second), raw.snapshot, requests[0], prefix, false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(observation.Body, []byte("function_call")) && !allowedWinner {
		t.Fatal("disconnected pending tool escaped")
	}
	t.Logf("real TLS disconnect outcome=%s approval=%s decisionActive=%d readerActive=%d providerRequests=%d", execution.result.Outcome, view.State, tracked.active.Load(), provider.active.Load(), provider.requests.Load())
}

func awaitOriginalDownstream(ctx context.Context, deadline time.Time, snapshot func() []rawevidence.Observation, request exchange.ClientRequest, peerPrefix []byte, completeRead bool) (rawevidence.Observation, error) {
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	expected, err := request.RawEvidenceContext()
	if err != nil {
		return rawevidence.Observation{}, err
	}
	if len(peerPrefix) == 0 {
		return rawevidence.Observation{}, errors.New("actual client prefix is missing")
	}
	for {
		if err := ctx.Err(); err != nil {
			return rawevidence.Observation{}, err
		}
		var found *rawevidence.Observation
		for _, observation := range snapshot() {
			if observation.Layer != rawevidence.LayerClientDownstream || observation.ExchangeID != request.ExchangeID() {
				continue
			}
			if found != nil {
				return rawevidence.Observation{}, errors.New("duplicate matching downstream Raw")
			}
			if observation.Context != expected {
				return rawevidence.Observation{}, errors.New("downstream Raw frozen identity differs")
			}
			// These fixtures are smaller than the full-retention cap. A partial
			// observation cannot prove absence across all locally committed bytes.
			if observation.Unavailable || !observation.Complete || !observation.DigestAvailable || !observation.FullDigestAvailable || len(observation.Body) == 0 || len(observation.Frames) == 0 || observation.TotalBodyBytes != int64(len(observation.Body)) || observation.BodySHA256 != sha256.Sum256(observation.Body) {
				return rawevidence.Observation{}, errors.New("downstream Raw committed prefix evidence incomplete or inconsistent")
			}
			var covered int64
			for _, frame := range observation.Frames {
				if frame.Offset != covered || frame.Length <= 0 || frame.Length > int64(len(observation.Body))-covered {
					return rawevidence.Observation{}, errors.New("downstream Raw frame range invalid")
				}
				switch frame.Kind {
				case rawevidence.FrameData, rawevidence.FrameKeepalive, rawevidence.FrameAbort:
				default:
					return rawevidence.Observation{}, errors.New("downstream Raw frame kind invalid")
				}
				covered += frame.Length
			}
			if covered != int64(len(observation.Body)) || !bytes.HasPrefix(observation.Body, peerPrefix) || completeRead && !bytes.Equal(observation.Body, peerPrefix) {
				return rawevidence.Observation{}, errors.New("downstream Raw does not cover actual client prefix")
			}
			observation.Body = bytes.Clone(observation.Body)
			observation.Frames = append([]rawevidence.Frame(nil), observation.Frames...)
			observation.Headers = observation.Headers.Clone()
			observation.Trailers = observation.Trailers.Clone()
			observation.ProtectedHeaderNames = append([]string(nil), observation.ProtectedHeaderNames...)
			found = &observation
		}
		if found != nil {
			if err := ctx.Err(); err != nil {
				return rawevidence.Observation{}, err
			}
			return *found, nil
		}
		select {
		case <-ctx.Done():
			return rawevidence.Observation{}, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

func TestOriginalDownstreamEvidenceRequiresPublication(t *testing.T) {
	raw := &rawObserver{}
	f := newProxyFixtureWithRawEvidence(t, raw)
	defer f.Close(t)
	conn := f.ConnectTLS(t, f.grant.ProxyCapability.Value(), "api.anthropic.com:443", "api.anthropic.com")
	defer conn.Close()
	body := `{"model":"synthetic"}`
	response := writeInnerRequest(t, conn, &http.Request{Method: "POST", URL: mustURL(t, "/v1/messages"), Host: "api.anthropic.com", Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))})
	delivered, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	requests := f.exchanges.Requests()
	if len(requests) != 1 {
		t.Fatal("real fixture request missing")
	}
	missing := func() []rawevidence.Observation {
		result := []rawevidence.Observation{}
		for _, observation := range raw.snapshot() {
			if observation.Layer != rawevidence.LayerClientDownstream {
				result = append(result, observation)
			}
		}
		return result
	}
	_, err = awaitOriginalDownstream(context.Background(), time.Now().Add(30*time.Millisecond), missing, requests[0], delivered, true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("missing downstream Raw incorrectly passed the no-leak/no-heartbeat evidence gate")
	}
}

func TestOriginalDownstreamEvidenceLateAndCorruptSnapshots(t *testing.T) {
	raw := &rawObserver{}
	f := newProxyFixtureWithRawEvidence(t, raw)
	defer f.Close(t)
	conn := f.ConnectTLS(t, f.grant.ProxyCapability.Value(), "api.anthropic.com:443", "api.anthropic.com")
	defer conn.Close()
	body := `{"model":"synthetic"}`
	response := writeInnerRequest(t, conn, &http.Request{Method: "POST", URL: mustURL(t, "/v1/messages"), Host: "api.anthropic.com", Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))})
	delivered, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	requests := f.exchanges.Requests()
	if len(requests) != 1 {
		t.Fatal("actual request missing")
	}
	request := requests[0]
	baseline, err := awaitOriginalDownstream(context.Background(), time.Now().Add(time.Second), raw.snapshot, request, delivered, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"late", "wrong_exchange", "wrong_scope", "wrong_environment", "duplicate", "empty_body", "empty_frames", "bad_hash", "bad_total", "bad_range", "frame_gap", "bad_prefix", "truncated", "canceled", "clone"} {
		t.Run(scenario, func(t *testing.T) {
			candidate := baseline
			candidate.Body = bytes.Clone(baseline.Body)
			candidate.Frames = append([]rawevidence.Frame(nil), baseline.Frames...)
			prefix := bytes.Clone(delivered)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "wrong_exchange":
				candidate.ExchangeID = "foreign"
			case "wrong_scope":
				candidate.ScopeID = "foreign"
			case "wrong_environment":
				candidate.EnvironmentDigest = "foreign"
			case "empty_body":
				candidate.Body = nil
			case "empty_frames":
				candidate.Frames = nil
			case "bad_hash":
				candidate.BodySHA256 = [32]byte{}
			case "bad_total":
				candidate.TotalBodyBytes++
			case "bad_range":
				candidate.Frames[0].Length = 1<<63 - 1
			case "frame_gap":
				candidate.Frames[0].Offset = 1
			case "bad_prefix":
				prefix[0] ^= 1
			case "truncated":
				candidate.Complete = false
				candidate.IncompleteReason = "response_payload_limit"
			case "canceled":
				cancel()
			}
			published := &rawObserver{}
			calls := 0
			snapshot := func() []rawevidence.Observation {
				calls++
				if scenario == "late" {
					if calls == 3 {
						_, _ = published.Observe(context.Background(), candidate)
					}
					return published.snapshot()
				}
				if scenario == "duplicate" {
					return []rawevidence.Observation{candidate, candidate}
				}
				return []rawevidence.Observation{candidate}
			}
			observed, err := awaitOriginalDownstream(ctx, time.Now().Add(30*time.Millisecond), snapshot, request, prefix, true)
			if scenario == "late" || scenario == "clone" {
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "late" && calls < 3 {
					t.Fatal("returned before real late observer publication")
				}
				candidate.Body[0] ^= 1
				candidate.Frames[0].Length++
				if !bytes.Equal(observed.Body, baseline.Body) || observed.Frames[0] != baseline.Frames[0] {
					t.Fatal("returned Raw snapshot aliases mutable source")
				}
			} else if err == nil {
				t.Fatal("missing/corrupt evidence passed absence assertion gate")
			}
		})
	}
}
