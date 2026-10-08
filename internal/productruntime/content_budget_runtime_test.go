package productruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

// Test-only bridge lets the external-package test exercise the real control
// HTTP application without introducing a productruntime/desktopcontrol cycle.
func RunContentBudgetConcurrentControlFixture(t *testing.T, control func(*Runtime, exchange.ResourcePolicy) error) {
	policy, err := exchange.DefaultResourcePolicy()
	if err != nil {
		t.Fatal(err)
	}
	testRuntimeLongSessionFixture(t, false, "", false, &longSessionAcceptanceOptions{count: 4111, policy: policy, controls: func(runtime *Runtime) error { return control(runtime, policy) }})
}

type concurrentContentBudgetSource struct {
	exchangecontent.Recorder
	sink                   exchangecontent.SourceRecorder
	entered                chan string
	started                chan struct{}
	start, overlap         chan struct{}
	startOnce, overlapOnce sync.Once
	active                 atomic.Int32
	release                chan struct{}
	once                   sync.Once
}

func (s *concurrentContentBudgetSource) RecordSource(ctx context.Context, source *exchangecontent.Source) error {
	request := source.Metadata().Response == nil
	if request {
		s.entered <- source.Metadata().ExchangeID
		select {
		case <-s.start:
		case <-ctx.Done():
			return ctx.Err()
		}
		if s.active.Add(1) == 2 {
			s.overlapOnce.Do(func() { close(s.overlap) })
		}
		s.started <- struct{}{}
	}
	err := s.sink.RecordSource(ctx, source)
	if request {
		s.active.Add(-1)
	}
	if err == nil && request {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
func testContentBudgetConcurrentExecutions(t *testing.T, _ context.Context, f accountReadFixture, pipeline exchangeRuntime, first exchange.ClientRequest, firstLease *exchange.BodyLease, plan environment.RequestPlan, operation exchange.ClientOperationEvidence, body []byte, credential string, source *concurrentContentBudgetSource, controls func(*Runtime) error, calls, diagnostics func() int32, count int, tail string) {
	// Race instrumentation can make the two real 4 MiB Store writes exceed
	// ten seconds while the product's independent content budget remains 30s.
	// This fixture watchdog must contain a test, not censor a valid recording.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	secondLease, err := f.runtime.bodyAdmission.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer secondLease.Release()
	second, err := exchange.NewClientRequest("long-session-2", plan, operation, body, exchange.ReplayGenerationCostOnly, wireprofile.ApplicationProtocolHTTP1, exchange.WithBodyLease(secondLease), exchange.WithOriginalHeaders(http.Header{"Authorization": {credential}, "Content-Type": {"application/json"}}))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for _, request := range []exchange.ClientRequest{first, second} {
		go func() { _, err := pipeline.Execute(ctx, request, &longSessionDownstream{}); done <- err }()
	}
	ids := map[string]bool{}
	for range 2 {
		select {
		case id := <-source.entered:
			ids[id] = true
		case err := <-done:
			t.Fatalf("recording did not remain active: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if len(ids) != 2 {
		t.Fatal("two distinct real recordings were not active")
	}
	source.startOnce.Do(func() { close(source.start) })
	for range 2 {
		select {
		case <-source.started:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	select {
	case <-source.overlap:
	case <-ctx.Done():
		t.Fatal("real recording calls never overlapped")
	}
	if active := source.active.Load(); active != 2 {
		t.Fatalf("controls dispatch outside instrumented sink-call overlap: active=%d", active)
	}
	t.Logf("controls dispatched with instrumented Manager/Store sink calls active=%d; post-commit borrow barrier tracked separately", source.active.Load())
	if err := controls(f.runtime); err != nil {
		t.Fatal(err)
	}
	attempt := runtimeProviderAttempt(t, "content-control-audit")
	if _, err := f.runtime.egressCompletion.Append(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	terminal, err := attempt.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, CompletedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.egressCompletion.Complete(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	page, err := f.runtime.EgressAttempts().List(ctx, egressaudit.PageRequest{Limit: 10, ExchangeID: attempt.Parent().ExchangeID})
	if err != nil || len(page.Items) != 1 || !page.Items[0].Attempt.Terminal() {
		t.Fatalf("real audit completion missing: %v", err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if !errors.Is(pipeline.Drain(canceled), context.Canceled) || !errors.Is(f.runtime.bodyAdmission.Drain(canceled), context.Canceled) {
		t.Fatal("active content observers were declared drained")
	}
	source.once.Do(func() { close(source.release) })
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := pipeline.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	firstLease.Release()
	secondLease.Release()
	if err := f.runtime.bodyAdmission.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Input []struct {
			Content string `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &expected); err != nil {
		t.Fatal(err)
	}
	for id := range ids {
		projection, err := f.runtime.ExchangeContents().GetProjection(ctx, id, exchangecontent.RequestViewFull)
		if err != nil || len(projection.Request.Messages) != count || projection.Request.Messages[count-1].Blocks[0].Text != tail || projection.Response == nil || projection.Response.Blocks[0].Text != "complete-reply" {
			t.Fatalf("complete concurrent Store record %s: %v", id, err)
		}
		for i, message := range projection.Request.Messages {
			if message.Role != "user" || len(message.Blocks) != 1 || message.Blocks[0].Text != expected.Input[i].Content {
				t.Fatalf("concurrent Store changed occurrence%d", i)
			}
		}
	}
	if calls() != 2 || diagnostics() != 0 {
		t.Fatalf("concurrent upstream=%d diagnostics=%d", calls(), diagnostics())
	}
	t.Log("two real Runtime exchanges retained full records; controls ran while synchronous recording ownership was active")
}

func runtimeContentBudgetSemanticFixture(t *testing.T) (protocolcore.Request, protocolcore.Response) {
	block, err := protocolcore.NewTextBlock("TAIL")
	if err != nil {
		t.Fatal(err)
	}
	return protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{block}}}}, protocolcore.Response{ID: "r", RequestedModel: "m", EffectiveModel: "m", ReportedModel: "m", Blocks: []protocolcore.ContentBlock{block}, StopReason: protocolcore.StopReasonEndTurn}
}

type failingBudgetRecorder struct {
	exchangecontent.Recorder
	entered chan struct{}
}

func (s *failingBudgetRecorder) RecordSource(ctx context.Context, _ *exchangecontent.Source) error {
	close(s.entered)
	<-ctx.Done()
	return ctx.Err()
}
func TestRuntimeContentObservationDeadlineReportsRealFailure(t *testing.T) {
	request, response := runtimeContentBudgetSemanticFixture(t)
	limits := longSessionTestPolicy().Content
	recorder := &failingBudgetRecorder{entered: make(chan struct{})}
	diagnostic := make(chan error, 1)
	observer := exchangeContentObserver{limits: &limits, recorder: recorder, clock: SystemClock{}, reportFailure: func(stage string, err error) {
		if stage != "response_content" {
			t.Error(stage)
		}
		diagnostic <- err
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := observer.ObserveContent(ctx, exchange.ContentObservation{ExchangeID: "deadline-report", EnvironmentID: "env", EnvironmentRevision: 1, EnvironmentDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EndpointID: "ep", EndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1, Recording: environment.DefaultContentRecordingPolicy(), Request: request, Response: &response})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	select {
	case failure := <-diagnostic:
		if !errors.Is(failure, context.DeadlineExceeded) {
			t.Fatal(failure)
		}
	default:
		t.Fatal("actual content deadline failure was silent")
	}
}
