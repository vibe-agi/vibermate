package nativemcp

import (
	"context"
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type effectiveBudgetClock struct{}

func (effectiveBudgetClock) Now() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) }

// Fill the existing shared budget only after the real Store-backed selection,
// at the exact already-parsed/pre-Bind boundary. This creates no authority.
type exhaustedEffectiveDefaults struct {
	resolver egressprofile.Resolver
	e        *executor
	held     int64
}

func (d *exhaustedEffectiveDefaults) Resolve(ctx context.Context, account, flow egressprofile.Choice) (egressprofile.Resolved, error) {
	resolved, err := d.resolver.Resolve(ctx, account, flow)
	if err != nil {
		return resolved, err
	}
	encoded, err := json.Marshal(resolved.Profile())
	if err != nil {
		return resolved, err
	}
	d.e.budget.mu.Lock()
	d.held = d.e.budget.maximum - d.e.budget.used - (512 + int64(len(encoded)) - 1)
	d.e.budget.mu.Unlock()
	if d.held <= 0 || !d.e.budget.acquire(d.held) {
		return egressprofile.Resolved{}, errBudget
	}
	return resolved, nil
}

func TestNativeEffectiveFactCapacityRefusesBeforeBindStartOrSend(t *testing.T) {
	f := newNativeFixtureWithChoice(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, 0, egressprofile.Inherit())
	profiles, err := egressprofile.NewManager(f.store.EgressProfileRepository(), effectiveBudgetClock{})
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := egressprofile.NewDefaultManager(f.store.EgressDefaultRepository(), profiles)
	if err != nil {
		t.Fatal(err)
	}
	e := f.executor.(*executor)
	e.dependencies.EgressDefaults = defaults
	f.handshake(t)
	baseline := nativeClaimLifetimeFacts(t, f).Budget
	beforeSends := f.transport.sends()
	exhausted := &exhaustedEffectiveDefaults{resolver: defaults, e: e}
	e.dependencies.EgressDefaults = exhausted
	request := f.captured(t, "POST", `{"jsonrpc":"2.0","id":802,"method":"ping"}`, "synthetic-session")
	got := e.Serve(t.Context(), request, httptest.NewRecorder())
	if exhausted.held == 0 {
		t.Fatal("real parsed/pre-Bind budget boundary was not reached")
	}
	e.budget.release(exhausted.held)
	if got.Reason != ReasonBudgetExceeded || f.transport.sends() != beforeSends {
		t.Fatalf("capacity did not refuse before send: %+v", got)
	}
	for _, table := range []string{"native_tool_bindings", "native_tool_binding_egress", "native_tool_once"} {
		var count int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE operation_id=?`, request.id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("pre-capacity %s count=%d err=%v", table, count, err)
		}
	}
	var attempts int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_deliveries WHERE operation_id=?`, request.id).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("capacity minted attempt=%d err=%v", attempts, err)
	}
	if got := nativeClaimLifetimeFacts(t, f).Budget; got != baseline {
		t.Fatalf("rejected consumer leaked %d baseline%d", got, baseline)
	}
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := nativeClaimLifetimeFacts(t, f).Budget; got != 0 {
		t.Fatal("Close leaked", got)
	}
}

func heldEffectiveBudget(t *testing.T, name string, cancelHeld bool, sseClose ...string) int64 {
	t.Helper()
	f := newNativeFixtureWithChoice(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, 0, egressprofile.Inherit())
	profiles, err := egressprofile.NewManager(f.store.EgressProfileRepository(), effectiveBudgetClock{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := profiles.Publish(t.Context(), egressprofile.PublishCommand{ID: "profile.budget", DisplayName: name, Policy: egressnetwork.DefaultPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := egressprofile.NewDefaultManager(f.store.EgressDefaultRepository(), profiles)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := defaults.SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}}); err != nil {
		t.Fatal(err)
	}
	e := f.executor.(*executor)
	e.dependencies.EgressDefaults = defaults
	f.handshake(t)
	baseline := nativeClaimLifetimeFacts(t, f).Budget
	response := wireResponse(`{"jsonrpc":"2.0","id":801,"result":{}}`, false)
	if len(sseClose) > 0 {
		// A complete valid terminal frame with no event ID reaches openStream,
		// then retires on detach rather than retaining an observed cursor.
		response = wireResponse("data: {\"jsonrpc\":\"2.0\",\"id\":801,\"result\":{}}\n\n", true)
	}
	body := &claimHeldCloseBody{ReadCloser: response.Body, entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	response.Body = body
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) { return response, nil }
	request := f.captured(t, "POST", `{"jsonrpc":"2.0","id":801,"method":"ping"}`, "synthetic-session")
	done := make(chan Terminal, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(body.release) }) }
	defer release()
	call, cancel := context.WithCancel(t.Context())
	defer cancel()
	var receiptObserver *effectiveReceiptObserver
	if len(sseClose) > 0 {
		receiptObserver = &effectiveReceiptObserver{Repository: e.dependencies.Store, target: request.id, receipt: make(chan nativetool.StartReceipt, 1)}
		e.dependencies.Store = receiptObserver
	}
	if len(sseClose) > 0 && sseClose[0] == "cancel-entered" {
		e.dependencies.Store = &claimAfterFinishStore{Repository: e.dependencies.Store, target: request.id, after: func() {
			cancel()
			select {
			case <-body.entered:
			case <-time.After(2 * time.Second):
				t.Error("real canceled Close callback did not enter")
			}
		}}
	}
	go func() { done <- e.Serve(call, request, httptest.NewRecorder()) }()
	select {
	case <-body.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("real body cleanup hold absent")
	}
	if len(sseClose) > 0 {
		// For cancellation, entered precedes the final Finish return; wait for
		// actual detach plus the still-live physical consumer, not a sleep guess.
		deadline := time.Now().Add(2 * time.Second)
		for {
			facts := nativeClaimLifetimeFacts(t, f)
			if facts.Streams == 0 && facts.Pending == 1 && facts.Claims == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("no-ID SSE did not reach detached/held consumer: %+v", facts)
			}
			time.Sleep(time.Millisecond)
		}
		physical := readFreshGETFacts(t, f, request.id)
		if physical.OperationState != "completed" || physical.AuditOutcome != "completed" || physical.AuditKnown != 1 {
			t.Fatal("held SSE lacks real terminal Store proof", physical)
		}
		select {
		case receipt := <-receiptObserver.receipt:
			if err := receiptObserver.Repository.VerifyStartReceipt(t.Context(), receipt, time.Now().UTC()); err == nil {
				t.Fatal("old terminal SSE receipt became sendable while Close held")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("actual Store Start receipt absent")
		}
		if f.transport.sends() != 4 {
			t.Fatal("no-ID SSE duplicated real send", f.transport.sends())
		}
		select {
		case got := <-done:
			done <- got
			t.Fatal("Serve returned before actual response Close/join")
		default:
		}
	}
	held := nativeClaimLifetimeFacts(t, f).Budget - baseline
	if cancelHeld {
		cancel()
		// A canceled context does not finish this actual held Close consumer.
		if got := nativeClaimLifetimeFacts(t, f).Budget - baseline; got != held {
			t.Fatalf("cancellation prematurely refunded held effective data %d/%d", got, held)
		}
	}
	release()
	select {
	case terminal := <-done:
		if terminal.Outcome != OutcomeCompleted {
			t.Fatal(terminal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("released native consumer did not drain")
	}
	if got := nativeClaimLifetimeFacts(t, f).Budget; got != baseline {
		t.Fatalf("effective fact leaked after terminal/drain: budget=%d baseline=%d", got, baseline)
	}
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := nativeClaimLifetimeFacts(t, f).Budget; got != 0 {
		t.Fatalf("Close retained native budget=%d", got)
	}
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Logf("nameBytes=%d heldEffectiveDelta=%d drainedBaseline=%d doubleCloseBudget=%d", len(name), held, baseline, nativeClaimLifetimeFacts(t, f).Budget)
	return held
}

type effectiveParentReadFault struct {
	nativetool.Repository
	parent nativetool.ID
}

type effectiveReceiptObserver struct {
	nativetool.Repository
	target  nativetool.ID
	receipt chan nativetool.StartReceipt
}

func (s *effectiveReceiptObserver) Start(ctx context.Context, id nativetool.ID, attempt string, revision uint64, now time.Time) (nativetool.StartReceipt, error) {
	receipt, err := s.Repository.Start(ctx, id, attempt, revision, now)
	if err == nil && id == s.target {
		s.receipt <- receipt
	}
	return receipt, err
}

func (f *effectiveParentReadFault) Get(ctx context.Context, id nativetool.ID) (nativetool.Operation, error) {
	if id == f.parent {
		return nativetool.Operation{}, nativetool.ErrUnavailable
	}
	return f.Repository.Get(ctx, id)
}

func retainedParentEffectiveBudget(t *testing.T, name string) int64 {
	t.Helper()
	f := newNativeFixtureWithChoice(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, 0, egressprofile.Inherit())
	profiles, err := egressprofile.NewManager(f.store.EgressProfileRepository(), effectiveBudgetClock{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := profiles.Publish(t.Context(), egressprofile.PublishCommand{ID: "profile.budget", DisplayName: name, Policy: egressnetwork.DefaultPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := egressprofile.NewDefaultManager(f.store.EgressDefaultRepository(), profiles)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := defaults.SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: 1}}); err != nil {
		t.Fatal(err)
	}
	e := f.executor.(*executor)
	e.dependencies.EgressDefaults = defaults
	f.handshake(t)
	baseline := nativeClaimLifetimeFacts(t, f).Budget
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
		return wireResponse("data: {\"jsonrpc\":\"2.0\",\"id\":\"retained-budget-ping\",\"method\":\"ping\"}\n\n", true), nil
	}
	get := f.captured(t, "GET", "", "synthetic-session")
	if terminal := e.Serve(t.Context(), get, httptest.NewRecorder()); terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	e.mu.Lock()
	var selectedSession *session
	for _, candidate := range e.scopes[f.capture.Key()].sessions {
		selectedSession = candidate
	}
	e.mu.Unlock()
	selectedSession.mu.Lock()
	stream := selectedSession.streams[get.id]
	parent := selectedSession.parents[`"retained-budget-ping"`]
	selectedSession.mu.Unlock()
	if stream == nil || parent == nil {
		t.Fatal("real observed stream/parent missing")
	}
	real := e.dependencies.Store
	e.dependencies.Store = &effectiveParentReadFault{Repository: real, parent: get.id}
	// Actual Store retirement succeeds; only its subsequent projection read
	// fails. The existing module parent still retains the immutable binding.
	e.retireStream(selectedSession, stream)
	e.dependencies.Store = real
	selectedSession.mu.Lock()
	remaining := selectedSession.parents[`"retained-budget-ping"`] == parent && selectedSession.streams[get.id] == nil
	selectedSession.mu.Unlock()
	if !remaining {
		t.Fatal("did not reach real parent-only retained boundary")
	}
	held := nativeClaimLifetimeFacts(t, f).Budget - baseline
	if err := e.closeSession(t.Context(), selectedSession); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := nativeClaimLifetimeFacts(t, f).Budget; got != 0 {
		t.Fatal("parent/session close retained budget", got)
	}
	t.Logf("parentOnly nameBytes=%d extraBudget=%d closedBudget=0", len(name), held)
	return held
}

func TestNativeEffectiveFactRemainsChargedWithParentAfterStreamRemoval(t *testing.T) {
	small := retainedParentEffectiveBudget(t, "small")
	large := retainedParentEffectiveBudget(t, strings.Repeat("&", 256))
	if large-small < 251 {
		t.Fatalf("parent-only effective binding uncharged after real stream removal: small=%d large=%d delta=%d", small, large, large-small)
	}
}

func TestNativeEffectiveFactChargesHeldConsumerAndDrainsOnce(t *testing.T) {
	small := heldEffectiveBudget(t, "small", false)
	large := heldEffectiveBudget(t, strings.Repeat("&", 256), false)
	// At least the additional251 bytes of actual immutable name data must be
	// owned while the real body consumer remains held, not only in SQL.
	if large-small < 251 {
		t.Fatalf("added effective fact bypasses shared budget: small=%d large=%d delta=%d", small, large, large-small)
	}
}

func TestNativeEffectiveFactCancellationKeepsActualConsumerChargeUntilDrain(t *testing.T) {
	small := heldEffectiveBudget(t, "small", true)
	large := heldEffectiveBudget(t, strings.Repeat("&", 256), true)
	if large-small < 251 {
		t.Fatalf("canceled held consumer lost effective reservation %d/%d", small, large)
	}
}

func TestNativeEffectiveFactNoIDSSECloseRemainsChargedUntilConsumerDrain(t *testing.T) {
	for _, edge := range []string{"deferred", "cancel-entered"} {
		t.Run(edge, func(t *testing.T) {
			small := heldEffectiveBudget(t, "small", false, edge)
			large := heldEffectiveBudget(t, strings.Repeat("&", 256), false, edge)
			if large-small < 251 {
				t.Fatalf("no-ID SSE refunded effective fact before actual Close/join: small=%d large=%d delta=%d", small, large, large-small)
			}
		})
	}
}

func retainedRecoveryEffectiveBudget(t *testing.T, name string) int64 {
	t.Helper()
	f := newNativeFixtureWithChoice(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, 0, egressprofile.Inherit())
	profiles, err := egressprofile.NewManager(f.store.EgressProfileRepository(), effectiveBudgetClock{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := profiles.Publish(t.Context(), egressprofile.PublishCommand{ID: "profile.recovery-budget", DisplayName: name, Policy: egressnetwork.DefaultPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := egressprofile.NewDefaultManager(f.store.EgressDefaultRepository(), profiles)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := defaults.SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: 1}}); err != nil {
		t.Fatal(err)
	}
	counted := &countedNativeDefaults{resolver: defaults}
	e := f.executor.(*executor)
	e.dependencies.EgressDefaults = counted
	f.handshake(t)
	baseline := nativeClaimLifetimeFacts(t, f).Budget
	f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
		if r.Method == "POST" {
			return wireResponse("id: budget-progress\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"budget-token\",\"progress\":1}}\n\n", true), nil
		}
		if r.Header.Get("Last-Event-Id") != "budget-progress" {
			return nil, io.ErrUnexpectedEOF
		}
		return wireResponse("id: budget-terminal\ndata: {\"jsonrpc\":\"2.0\",\"id\":804,\"result\":{\"content\":[]}}\n\n", true), nil
	}
	original := f.captured(t, "POST", `{"jsonrpc":"2.0","id":804,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"budget-token"}}}`, "synthetic-session")
	done := make(chan Terminal, 1)
	go func() { done <- e.Serve(t.Context(), original, httptest.NewRecorder()) }()
	f.approve(t, original.id)
	select {
	case got := <-done:
		if got.Outcome != OutcomeFailed || got.Reason != ReasonStreamDetached {
			t.Fatal("physical original did not detach", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approved original did not drain")
	}
	held := nativeClaimLifetimeFacts(t, f).Budget - baseline
	if held <= 0 {
		t.Fatal("detached original lost retained effective binding charge")
	}
	before := counted.calls.Load()
	counted.unavailable.Store(true)
	child := f.captured(t, "GET", "", "synthetic-session")
	child.request.Header.Set("Last-Event-Id", "budget-progress")
	if got := e.Serve(t.Context(), child, httptest.NewRecorder()); got.Outcome != OutcomeCompleted {
		t.Fatal("actual recovery failed", got)
	}
	if counted.calls.Load() != before {
		t.Fatal("retained recovery re-resolved current default")
	}
	op, err := f.store.NativeToolRepository().Get(t.Context(), original.id)
	if err != nil || op.State != "completed" {
		t.Fatal("recovery changed separate logical outcome", err)
	}
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := nativeClaimLifetimeFacts(t, f).Budget; got != 0 {
		t.Fatal("actual retained recovery close leaked", got)
	}
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Logf("retainedRecovery nameBytes=%d extraBudget=%d doubleCloseBudget=0", len(name), held)
	return held
}
func TestNativeEffectiveFactRetainedRecoveryRemainsChargedThroughOriginalChildDrain(t *testing.T) {
	small := retainedRecoveryEffectiveBudget(t, "small")
	large := retainedRecoveryEffectiveBudget(t, strings.Repeat("&", 256))
	if large-small < 251 {
		t.Fatalf("detached original effective binding uncharged %d/%d", small, large)
	}
}
