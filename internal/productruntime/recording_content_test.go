package productruntime

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

type heldContentWriter struct {
	entered chan struct{}
	release chan struct{}
	text    chan string
}

func (s *heldContentWriter) Record(context.Context, exchangecontent.Record) error {
	panic("owned Source required")
}
func (s *heldContentWriter) RecordSource(ctx context.Context, source *exchangecontent.Source) error {
	close(s.entered)
	<-s.release
	var text string
	err := source.Walk(ctx, func(_ exchangecontent.Part, _ int, m exchangecontent.MessageSource) error {
		return m.WalkBlocks(ctx, func(b exchangecontent.Block) error { text += b.Text; return nil })
	})
	s.text <- text
	return err
}
func TestContentProducerReturnsBeforeBlockedWriterAndOwnsSnapshot(t *testing.T) {
	budget, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 64 << 20})
	queue := newObservationRecorder(context.Background(), budget, nil, nil)
	defer queue.Shutdown(context.Background())
	sink := &heldContentWriter{entered: make(chan struct{}), release: make(chan struct{}), text: make(chan string, 1)}
	defer close(sink.release)
	limits, _ := exchangecontent.DefaultSourceLimits()
	observer := exchangeContentObserver{queue: queue, limits: &limits, recorder: sink, clock: SystemClock{}}
	request := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "original"}}}}}
	observation := exchange.ContentObservation{ObservationStamp: queue.ScopeStamp(captureidentity.Reference{}), ExchangeID: "exchange", EnvironmentID: "env", EnvironmentRevision: 1, EnvironmentDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EndpointID: "ep", EndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, Recording: environment.DefaultContentRecordingPolicy(), Request: request}
	returned := make(chan error, 1)
	go func() { returned <- observer.ObserveContent(context.Background(), observation) }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("optional writer blocked producer")
	}
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("writer never received accepted content")
	}
	request.Messages[0].Blocks[0].Text = "mutated"
	// The worker retains the only shared slot. A second optional offer returns.
	if err := observer.ObserveContent(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	sink.release <- struct{}{}
	select {
	case got := <-sink.text:
		if got != "original" {
			t.Fatalf("borrowed snapshot: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("writer failed")
	}
	if err := queue.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := budget.Snapshot().ReservedRecords; got != 0 {
		t.Fatalf("ownership leaked: %d", got)
	}
}

type failingPreparedActivity struct{ writes chan activity.Record }

func (s *failingPreparedActivity) Record(ctx context.Context, e activity.Event) (activity.Record, error) {
	r, _ := s.Prepare(e)
	return s.RecordPrepared(ctx, r)
}
func (s *failingPreparedActivity) Prepare(e activity.Event) (activity.Record, error) {
	return activity.Record{ID: "prepared", OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), SubjectID: e.SubjectID}, nil
}
func (s *failingPreparedActivity) RecordPrepared(_ context.Context, r activity.Record) (activity.Record, error) {
	s.writes <- r
	return activity.Record{}, errors.New("activity unavailable")
}

type metadataIdentityWriter struct {
	activity.ConversationIdentityRepository
	writes chan agentconversation.ClientIdentity
}

func (s *metadataIdentityWriter) PutConversationIdentity(_ context.Context, _ string, v agentconversation.ClientIdentity) error {
	s.writes <- v
	return nil
}
func TestMetadataIdentityPersistsWhenActivityProjectionFails(t *testing.T) {
	budget, _ := recording.NewBudget(recording.Limits{Records: 4, Bytes: 64 << 20})
	queue := newObservationRecorder(context.Background(), budget, nil, nil)
	defer queue.Shutdown(context.Background())
	writer := &failingPreparedActivity{writes: make(chan activity.Record, 1)}
	identities := &metadataIdentityWriter{writes: make(chan agentconversation.ClientIdentity, 1)}
	observer := activityAttemptObserver{queue: queue, recorder: writer, identities: identities}
	evidence := []protocolcore.ProtocolEvidenceValue{{Name: "openai_responses.session_id", Value: "original-session"}}
	err := observer.ObserveStart(context.Background(), exchange.StartObservation{ObservationStamp: queue.ScopeStamp(captureidentity.Reference{}), ExchangeID: "exchange", ClientProtocolEvidence: evidence})
	if err != nil {
		t.Fatalf("optional projection failure reached producer: %v", err)
	}
	evidence[0].Value = "mutated"
	if err := queue.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-identities.writes:
		if id.SessionID != "original-session" || !id.ObservedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("identity not frozen with activity: %+v", id)
		}
	default:
		t.Fatal("Activity failure suppressed independent identity projection")
	}
}

type trackedRecordingTransport struct {
	endpoint *url.URL
	closed   *atomic.Int32
}

func (t trackedRecordingTransport) RoundTrip(r *http.Request, d providertransport.TransportDispatch) (*http.Response, transportprofile.Evidence, error) {
	response, evidence, err := (privateLongSessionTransport{t.endpoint}).RoundTrip(r, d)
	if err == nil {
		response.Body = &trackedRecordingBody{ReadCloser: response.Body, closed: t.closed}
	}
	return response, evidence, err
}

type trackedRecordingBody struct {
	io.ReadCloser
	closed *atomic.Int32
}

func (b *trackedRecordingBody) Close() error { b.closed.Add(1); return b.ReadCloser.Close() }
func TestRuntimeJSONAndSSEFinishAndCloseBodiesWhileRecordingBlocked(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, mode := range []string{"blocked", "saturated", "tiny_optional"} {
			saturated := mode == "saturated"
			t.Run(fmt.Sprintf("stream=%v/%s", stream, mode), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				f := newAccountReadFixture(t)
				WaitRecordingTestRuntime(t, f.runtime)
				if mode == "tiny_optional" {
					p := f.runtime.bodyAdmission.Policy()
					p.Content.RetainedBytes, p.Content.CanonicalBytes, p.Content.StructureBytes = 1, 1, 1
					p.Content.Scratch.PayloadBytes, p.Content.Scratch.StructureBytes = 1, 1
					gate, e := exchange.NewBodyAdmission(p)
					if e != nil {
						t.Fatal(e)
					}
					f.runtime.bodyAdmission = gate
				}

				f.aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination = environment.DestinationPlan{Kind: environment.DestinationKindOriginal}
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
				resolved, err := f.runtime.egressDefaults.Resolve(ctx, egressprofile.Inherit(), plan.EgressChoice())
				if err != nil {
					t.Fatal(err)
				}
				plan, err = plan.WithResolvedEgress(resolved)
				if err != nil {
					t.Fatal(err)
				}
				response := `{"id":"resp_live","object":"response","status":"completed","model":"m","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"exact live bytes"}]}]}`
				wire := response
				contentType := "application/json"
				if stream {
					contentType = "text/event-stream"
					wire = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", contentType)
					_, _ = io.WriteString(w, wire)
				}))
				defer upstream.Close()
				endpoint, _ := url.Parse(upstream.URL)
				var closed atomic.Int32
				auth, err := providertransport.NewStaticBearerAuthenticator(f.secrets)
				if err != nil {
					t.Fatal(err)
				}
				provider, err := providertransport.NewClient(providertransport.ClientOptions{Coordinator: f.runtime.offlineHold, Authenticators: []providertransport.Authenticator{auth}, Transport: trackedRecordingTransport{endpoint, &closed}, InstanceIDs: NewCryptographicInstanceIDSource(), Audit: f.runtime.egressCompletion})
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
				pipeline, err := buildExchange(exchangeBuildRequest{recorder: f.runtime.recorder, bodyAdmission: f.runtime.bodyAdmission, ownerContext: ctx, actions: f.runtime.offlineHold, accounts: f.runtime.accounts, provider: provider, toolDecisions: decisions, activities: f.runtime.activities, identities: f.runtime.conversationIDs, contents: f.runtime.contents, clock: SystemClock{}, hold: exchange.DefaultHoldPolicy(), annotations: annotations})
				if err != nil {
					t.Fatal(err)
				}
				defer pipeline.Shutdown(ctx)
				sink := &heldContentWriter{entered: make(chan struct{}), release: make(chan struct{}), text: make(chan string, 1)}
				defer close(sink.release)
				req, res := runtimeContentBudgetSemanticFixture(t)
				limits, _ := exchangecontent.DefaultSourceLimits()
				observer := exchangeContentObserver{queue: f.runtime.recorder, limits: &limits, recorder: sink, clock: SystemClock{}}
				stamp := f.runtime.recorder.ScopeStamp(captureidentity.Reference{})
				_ = observer.ObserveContent(ctx, exchange.ContentObservation{ObservationStamp: stamp, ExchangeID: "blocked-prime", EnvironmentID: "env", EnvironmentRevision: 1, EnvironmentDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EndpointID: "ep", EndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, Recording: environment.DefaultContentRecordingPolicy(), Request: req, Response: &res})
				select {
				case <-sink.entered:
				case <-ctx.Done():
					t.Fatal("worker did not block")
				}
				var reservations []*recording.Reservation
				if saturated {
					for {
						r, ok := f.runtime.recordingBudget.TryReserve(recording.Raw, 0)
						if !ok {
							break
						}
						reservations = append(reservations, r)
					}
				}
				defer func() {
					for _, r := range reservations {
						r.Release()
					}
				}()
				operation, err := exchange.NewClientOperationEvidence(plan.Operation().ID(), plan.Operation().Revision(), "POST", "/backend-api/codex/responses", "")
				if err != nil {
					t.Fatal(err)
				}
				lease, err := f.runtime.bodyAdmission.AcquirePlan(ctx, plan)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Release()
				body := []byte(fmt.Sprintf(`{"model":"m","input":[{"role":"user","content":"hello"}],"stream":%v}`, stream))
				request, err := exchange.NewClientRequest("live", plan, operation, body, exchange.ReplayGenerationCostOnly, wireprofile.ApplicationProtocolHTTP1, exchange.WithBodyLease(lease), exchange.WithObservationStamp(stamp), exchange.WithOriginalHeaders(http.Header{"Authorization": {"Bearer synthetic-live"}, "Content-Type": {"application/json"}}))
				if err != nil {
					t.Fatal(err)
				}
				downstream := &longSessionDownstream{}
				if _, err = pipeline.Execute(ctx, request, downstream); err != nil {
					t.Fatal(err)
				}
				if got := string(downstream.Bytes()); got != wire {
					t.Fatalf("forwarded bytes changed: %q", got)
				}
				if closed.Load() != 1 {
					t.Fatalf("actual provider body owner not closed: %d", closed.Load())
				}
				lease.Release()
				if err = pipeline.Drain(ctx); err != nil {
					t.Fatal(err)
				}
				if err = f.runtime.bodyAdmission.Drain(ctx); err != nil {
					t.Fatal(err)
				}
				for _, r := range reservations {
					r.Release()
				}
				reservations = nil
				sink.release <- struct{}{}
				if err = f.runtime.recorder.Flush(ctx); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func (s *heldContentWriter) RecordScopedSource(ctx context.Context, _ recording.Scope, source *exchangecontent.Source) error {
	return s.RecordSource(ctx, source)
}
func (s *failingPreparedActivity) RecordScopedPrepared(ctx context.Context, _ recording.Scope, r activity.Record) (activity.Record, error) {
	return s.RecordPrepared(ctx, r)
}
func (s *metadataIdentityWriter) PutScopedConversationIdentity(ctx context.Context, _ recording.Scope, id string, v agentconversation.ClientIdentity) error {
	return s.PutConversationIdentity(ctx, id, v)
}
