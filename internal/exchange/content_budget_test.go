package exchange

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func setContentBudget(options *Options, budget time.Duration) {
	options.ContentObservationTimeout = budget
}

type blockedBudgetContent struct {
	entered chan context.Context
	release chan struct{}
	once    sync.Once
	failure error
}

func (p *blockedBudgetContent) ObserveContent(ctx context.Context, observation ContentObservation) error {
	if observation.Response == nil {
		return nil
	}
	p.entered <- ctx
	<-p.release
	p.failure = ctx.Err()
	return p.failure
}
func TestContentObservationDeadlineAndCancellationRetainLeaseUntilReturn(t *testing.T) {
	for _, expire := range []bool{true, false} {
		t.Run(map[bool]string{true: "deadline", false: "client_and_shutdown"}[expire], func(t *testing.T) {
			watchdog, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			content := &blockedBudgetContent{entered: make(chan context.Context, 1), release: make(chan struct{})}
			t.Cleanup(func() { content.once.Do(func() { close(content.release) }) })
			gate, err := NewBodyAdmission(admissionTestPolicy(1))
			if err != nil {
				t.Fatal(err)
			}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{destination: environment.DestinationKindUpstream, providerOrigin: "https://provider.example/v1", backend: protocolspec.DialectOpenAIChat, modelMode: environment.ModelModeMap, mappedModel: "gpt-provider", accounts: []testAccount{{id: "account.primary", revision: 1, epoch: 1}}, preferred: "account.primary"})
			provider := &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, completeProviderResponse("gpt-provider"))}}}
			options := contentBudgetOptions(t, content, &attemptObserverDouble{})
			options.Accounts = newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1})
			options.Provider = provider
			options.BodyAdmission = gate
			options.ContentObservationTimeout = time.Second
			if expire {
				options.ContentObservationTimeout = 20 * time.Millisecond
			}
			pipeline, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := gate.Acquire(watchdog)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			request := mustClientRequestWithOptions(t, "content-owned", plan, completeClientRequest(), WithBodyLease(lease))
			client, cancel := context.WithCancel(watchdog)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := pipeline.Execute(client, request, &downstreamRecorder{}); done <- err }()
			var recording context.Context
			select {
			case recording = <-content.entered:
			case <-watchdog.Done():
				t.Fatal("content callback never entered")
			}
			if expire {
				select {
				case <-recording.Done():
				case <-watchdog.Done():
					t.Fatal("finite content deadline did not expire")
				}
				if !errors.Is(recording.Err(), context.DeadlineExceeded) {
					t.Fatal(recording.Err())
				}
			}
			cancel()
			pipeline.BeginShutdown()
			lease.Release()
			if !expire && recording.Err() != nil {
				t.Fatal("caller/shutdown canceled borrowed content")
			}
			canceled, cancelDrain := context.WithCancel(context.Background())
			cancelDrain()
			if !errors.Is(pipeline.Drain(canceled), context.Canceled) || !errors.Is(gate.Drain(canceled), context.Canceled) {
				t.Fatal("observer still entered but operation/lease was refunded")
			}
			select {
			case <-done:
				t.Fatal("Exchange ended before content observer returned")
			default:
			}
			content.once.Do(func() { close(content.release) })
			select {
			case <-done:
			case <-watchdog.Done():
				t.Fatal("observer return did not finish Exchange")
			}
			if err := pipeline.Drain(watchdog); err != nil {
				t.Fatal(err)
			}
			if err := gate.Drain(watchdog); err != nil {
				t.Fatal(err)
			}
			if expire && !errors.Is(content.failure, context.DeadlineExceeded) {
				t.Fatal("real observation deadline error lost")
			}
			if !expire && content.failure != nil {
				t.Fatal(content.failure)
			}
			if len(provider.requestsSnapshot()) != 1 {
				t.Fatal("content failure/cancellation retried provider")
			}
		})
	}
}

type contentBudgetProbe struct {
	minimum   time.Duration
	durations []time.Duration
	outcomes  []error
	terminal  []bool
}

func (p *contentBudgetProbe) ObserveContent(ctx context.Context, observation ContentObservation) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		panic("content observer has no finite deadline")
	}
	p.durations = append(p.durations, time.Until(deadline))
	p.terminal = append(p.terminal, observation.Response != nil)
	if p.minimum > 0 {
		minimum, cancel := context.WithTimeout(context.Background(), p.minimum)
		defer cancel()
		select {
		case <-minimum.Done():
		case <-ctx.Done():
		}
	}
	p.outcomes = append(p.outcomes, ctx.Err())
	return ctx.Err()
}

type ordinaryBudgetProbe struct{ durations []time.Duration }

func (p *ordinaryBudgetProbe) observe(ctx context.Context) error {
	deadline, _ := ctx.Deadline()
	p.durations = append(p.durations, time.Until(deadline))
	return ctx.Err()
}
func (p *ordinaryBudgetProbe) ObserveStart(ctx context.Context, _ StartObservation) error {
	return p.observe(ctx)
}
func (p *ordinaryBudgetProbe) ObserveTerminal(ctx context.Context, _ AttemptObservation) error {
	return p.observe(ctx)
}

func contentBudgetOptions(t *testing.T, content ContentObserver, ordinary ExchangeObserver) Options {
	seed := newTestPipeline(t, nil, &providerDouble{}, approvedDecisions(), &attemptObserverDouble{})
	t.Cleanup(func() { shutdownPipeline(t, seed) })
	return Options{OwnerContext: context.Background(), Actions: seed.actions, ProtocolPaths: seed.protocolPaths, Provider: seed.provider, ToolDecisions: seed.toolDecisions, RetryWaiter: seed.retryWaiter, Observer: ordinary, ContentObserver: content, ObservationTimeout: 20 * time.Millisecond, Hold: seed.hold, Stream: seed.streamBudgets, ClientAnnotations: seed.annotations, Now: time.Now}
}
func contentBudgetRequest(t *testing.T) (ClientRequest, *contentCapture) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{destination: environment.DestinationKindOriginal})
	request := mustClientRequest(t, "content-budget", plan, completeClientRequest())
	block, _ := protocolcore.NewTextBlock("TAIL")
	semantic := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{block}}}}
	return request, &contentCapture{request: &semantic, startedAt: time.Now()}
}

func TestContentObservationIndependentBudgetAndCopiedOptions(t *testing.T) {
	content := &contentBudgetProbe{minimum: 40 * time.Millisecond}
	ordinary := &ordinaryBudgetProbe{}
	options := contentBudgetOptions(t, content, ordinary)
	setContentBudget(&options, 250*time.Millisecond)
	pipeline, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownPipeline(t, pipeline)
	setContentBudget(&options, time.Nanosecond)
	options.ObservationTimeout = time.Nanosecond
	request, captured := contentBudgetRequest(t)
	pipeline.observeStart(request, request.body)
	pipeline.observeRequest(request, captured)
	captured.response = &protocolcore.Response{ID: "r"}
	pipeline.observeContent(request, captured)
	pipeline.observeAttempt(request, Result{Outcome: AttemptSucceeded}, nil, captured)
	if len(content.outcomes) != 2 || len(ordinary.durations) != 2 || content.terminal[0] || !content.terminal[1] {
		t.Fatal("request/final content or ordinary observation ordering lost")
	}
	for i, err := range content.outcomes {
		if err != nil {
			t.Errorf("content%d failed at ordinary cutoff: %v", i, err)
		}
	}
	for _, duration := range content.durations {
		if duration < 200*time.Millisecond || duration > 250*time.Millisecond {
			t.Errorf("copied independent content budget=%v", duration)
		}
	}
	for _, duration := range ordinary.durations {
		if duration <= 0 || duration > 20*time.Millisecond {
			t.Errorf("ordinary budget changed=%v", duration)
		}
	}
}

func TestContentObservationConstructorFallbackPositiveAndNegative(t *testing.T) {
	for _, budget := range []time.Duration{0, 250 * time.Millisecond, -time.Nanosecond} {
		t.Run(budget.String(), func(t *testing.T) {
			content := &contentBudgetProbe{}
			options := contentBudgetOptions(t, content, &ordinaryBudgetProbe{})
			setContentBudget(&options, budget)
			pipeline, err := New(options)
			if budget < 0 {
				if err == nil {
					shutdownPipeline(t, pipeline)
					t.Error("negative content budget accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownPipeline(t, pipeline)
			request, captured := contentBudgetRequest(t)
			pipeline.observeContent(request, captured)
			want := budget
			if want == 0 {
				want = options.ObservationTimeout
			}
			if got := content.durations[0]; got <= want/2 || got > want {
				t.Errorf("effective content budget=%v want=%v", got, want)
			}
			options.ObservationTimeout = 0
			if invalid, err := New(options); err == nil {
				shutdownPipeline(t, invalid)
				t.Error("nonpositive ordinary budget accepted")
			}
		})
	}
}
