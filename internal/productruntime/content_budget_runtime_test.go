package productruntime

import (
	"context"
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

// Test-only bridge lets the external-package test exercise the real control
// HTTP application without introducing a productruntime/desktopcontrol cycle.
func RunContentBudgetConcurrentControlFixture(t *testing.T, control func(*Runtime, exchange.ResourcePolicy) error) {
	policy, err := exchange.DefaultResourcePolicy()
	if err != nil {
		t.Fatal(err)
	}
	mode := concurrentContentFixtureMode()
	t.Logf("CONTENT_BUDGET_COVERAGE mode=%s content=%v outer=%v concurrent=%v items=4111", mode.label, mode.content, mode.outer, mode.exchange)
	testRuntimeLongSessionFixture(t, false, "", false, &longSessionAcceptanceOptions{count: 4111, policy: policy, concurrentMode: &mode, controls: func(runtime *Runtime) error { return control(runtime, policy) }})
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
func (s *concurrentContentBudgetSource) RecordScopedSource(ctx context.Context, scope recording.Scope, source *exchangecontent.Source) error {
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
	err := s.sink.(exchangecontent.ScopedSourceRecorder).RecordScopedSource(ctx, scope, source)
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
	// This watchdog contains the selected fixture; content has its own finite
	// context. Race mode explicitly verifies concurrency rather than default latency.
	ctx, cancel := context.WithTimeout(context.Background(), concurrentContentFixtureMode().exchange)
	defer cancel()
	secondLease, err := f.runtime.bodyAdmission.AcquirePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	defer secondLease.Release()
	second, err := exchange.NewClientRequest("long-session-2", plan, operation, body, exchange.ReplayGenerationCostOnly, wireprofile.ApplicationProtocolHTTP1, exchange.WithObservationStamp(f.runtime.recorder.ScopeStamp(captureidentity.Reference{})), exchange.WithBodyLease(secondLease), exchange.WithOriginalHeaders(http.Header{"Authorization": {credential}, "Content-Type": {"application/json"}}))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for _, request := range []exchange.ClientRequest{first, second} {
		go func() { _, err := pipeline.Execute(ctx, request, &longSessionDownstream{}); done <- err }()
	}

	// One recording worker may block; both actual executions must still finish.
	select {
	case <-source.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("recording blocked actual execution")
		}
	}
	firstLease.Release()
	secondLease.Release()
	if err := pipeline.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.bodyAdmission.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := controls(f.runtime); err != nil {
		t.Fatal(err)
	}
	source.startOnce.Do(func() { close(source.start) })
	source.once.Do(func() { close(source.release) })
	if err := f.runtime.recorder.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{"long-session-4111": true, "long-session-2": true}
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
		if err != nil || len(projection.Request.Messages) != count || projection.Request.Messages[count-1].Blocks[0].Text != tail {
			t.Fatalf("complete concurrent Store record %s: %v", id, err)
		}
		if projection.Response == nil {
			if f.runtime.recordingBudget.Snapshot().ByKind[recording.Content].Offers[recording.DroppedCapacity].Records == 0 {
				t.Fatal("terminal recording disappeared without counted loss")
			}
		} else if projection.Response.Blocks[0].Text != "complete-reply" {
			t.Fatal("accepted response changed")
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
	t.Log("two real Runtime exchanges completed while the optional recording worker was blocked")
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
func TestRuntimeContentObservationDeadlineDoesNotOwnWriterLifetime(t *testing.T) {
	request, response := runtimeContentBudgetSemanticFixture(t)
	limits := longSessionTestPolicy().Content
	b, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 64 << 20})
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	queue := newObservationRecorder(owner, b, nil, nil)
	defer queue.Shutdown(context.Background())
	recorder := &failingBudgetRecorder{entered: make(chan struct{})}
	observer := exchangeContentObserver{queue: queue, limits: &limits, recorder: recorder, clock: SystemClock{}}
	ctx, cancel := context.WithCancel(context.Background())
	err := observer.ObserveContent(ctx, exchange.ContentObservation{ObservationStamp: queue.ScopeStamp(captureidentity.Reference{}), ExchangeID: "deadline-report", EnvironmentID: "env", EnvironmentRevision: 1, EnvironmentDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EndpointID: "ep", EndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1, Recording: environment.DefaultContentRecordingPolicy(), Request: request, Response: &response})
	if err != nil {
		t.Fatal(err)
	}
	<-recorder.entered
	cancel()
	if b.Snapshot().ReservedRecords != 1 {
		t.Fatal("producer cancellation refunded worker ownership")
	}
	cancelOwner()
	if err := queue.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.Snapshot().ByKind[recording.Content].Settlements[recording.Unconfirmed].Records != 1 {
		t.Fatal("actual writer failure unreported")
	}
}

func (s *failingBudgetRecorder) RecordScopedSource(ctx context.Context, _ recording.Scope, source *exchangecontent.Source) error {
	return s.RecordSource(ctx, source)
}
