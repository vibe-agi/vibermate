package nativemcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func prepareRealReceipt(t *testing.T, f *nativeFixture, wireOverride ...string) (nativetool.StartReceipt, *http.Request, environment.ClientToolPlan) {
	t.Helper()
	wire := "synthetic-session"
	if len(wireOverride) > 0 {
		wire = wireOverride[0]
	}
	body := `{"jsonrpc":"2.0","id":33,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"callId":"receipt-call-` + wire + `"}}}`
	c := f.captured(t, "POST", body, wire)
	defer c.lease.Release()
	e := f.executor.(*executor)
	plan, _ := c.lease.Plan().ClientToolPlan()
	r, err := parseRequest([]byte(body), e.limits)
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.admitSession(context.Background(), c, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = e.operationPlan(context.Background(), r, s)
	if err != nil {
		t.Fatal(err)
	}
	binding, intents, _, err := e.bindRequest(c, r, s, plan)
	if err != nil {
		t.Fatal(err)
	}
	rpcID, _ := binding.RPCIDDigest()
	digest := binding.RequestDigest()
	sid := binding.SessionID()
	ctx := context.Background()
	if _, err = e.dependencies.Store.Receive(ctx, nativetool.ReceiveInput{ID: c.id, Capture: f.capture, EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "real-native-receipt", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: binding.Kind(), SessionID: &sid, RPCIDDigest: &rpcID, RequestDigest: &digest, Recording: plan.ContentRecording(), ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err = e.dependencies.Store.Bind(ctx, c.id, binding, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	policy, _ := exchange.NewToolDecisionContext(plan.PolicySet(), "", false, nil, nil)
	decision, _ := exchange.NewClientToolDecisionRequest(c.id, binding, policy, intents, nil)
	errors := make(chan error, 1)
	go func() { _, err := e.dependencies.ToolDecisions.Decide(ctx, decision); errors <- err }()
	f.approve(t, c.id)
	if err = <-errors; err != nil {
		t.Fatal(err)
	}
	attempt, _ := f.ids.NewNativeOperationID(ctx)
	receipt, err := e.dependencies.Store.Start(ctx, c.id, attempt.String(), s.owner.revision, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	request, err := outbound(ctx, c.request, r.raw, plan)
	if err != nil {
		t.Fatal(err)
	}
	return receipt, request, plan
}
func TestNativeSendRequiresSameStoreCommittedReceipt(t *testing.T) {
	for _, condition := range []string{"committed", "zero", "public_issuer", "foreign_store", "revoked", "expired", "recovered", "closed"} {
		t.Run(condition, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			f.handshake(t)
			receipt, request, plan := prepareRealReceipt(t, f)
			e := f.executor.(*executor)
			switch condition {
			case "zero":
				receipt = nativetool.StartReceipt{}
			case "public_issuer":
				issuer, _ := nativetool.NewStartReceiptIssuer()
				op, _ := f.store.NativeToolRepository().Get(context.Background(), receipt.OperationID())
				scope, _ := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
				receipt, _ = issuer.Issue(nativetool.StartFacts{OperationID: receipt.OperationID(), AttemptID: receipt.AttemptID(), AuthorityRevision: scope.Revision, OwnerEpoch: scope.OwnerEpoch, BindingDigest: *op.BindingDigest})
			case "foreign_store":
				other, err := runtimepersistence.Open(context.Background(), runtimepersistence.Options{DatabasePath: f.storePath(t), BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
				if err != nil {
					t.Fatal(err)
				}
				defer other.Shutdown(context.Background())
				e.dependencies.Store = other.NativeToolRepository()
			case "revoked":
				if err := e.Revoke(context.Background(), f.capture, RevocationPermissionOff); err != nil {
					t.Fatal(err)
				}
			case "expired":
				e.dependencies.Clock = func() time.Time { return time.Now().Add(time.Hour) }
			case "recovered":
				if err := e.dependencies.Store.Recover(context.Background(), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			case "closed":
				if err := f.store.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			before := f.transport.sends()
			originalReceipt := receipt
			// Direct private callers can observe a phase, but cannot use it to
			// close a real operation. Verify and every SQL fact stay decisive.
			var beforeOnce, beforeAudit []any
			if condition != "zero" {
				beforeOnce = freshGETRow(t, f, `SELECT * FROM native_tool_once WHERE operation_id=?`, receipt.OperationID())
				beforeAudit = freshGETRow(t, f, `SELECT * FROM runtime_egress_attempts WHERE parent_id=?`, receipt.OperationID())
			}
			sent := e.sendCommitted(context.Background(), request, plan, receipt)
			if token := sent.claim.token; token != nil && (token.ownerKey != 0 || token.sessionKey != 0 || token.physicalClass != nativeClaimUnbound) {
				t.Fatal("borrowed/foreign direct receipt gained Serve cleanup affinity")
			}
			response, err := sent.response, sent.err
			if response != nil {
				response.Body.Close()
			}
			if condition == "committed" {
				if err != nil || f.transport.sends() != before+1 || sent.phase != nativeSendTransportEntered {
					t.Fatalf("committed receipt %v sends%d", err, f.transport.sends())
				}
			} else if err == nil || f.transport.sends() != before {
				t.Fatalf("%s receipt sent: %v sends%d", condition, err, f.transport.sends())
			}
			if condition == "zero" && sent.phase != nativeSendUnproven {
				t.Fatal("zero receipt acquired phase proof")
			}
			if condition != "zero" && condition != "committed" && sent.phase != nativeSendFirstClaimUnsent {
				t.Fatal("rejected first claim lost phase distinction")
			}
			sent = e.sendCommitted(context.Background(), request, plan, receipt)
			if sent.claim.token != nil {
				t.Fatal("replay returned another invocation's claim handle")
			}
			response, err = sent.response, sent.err
			if response != nil {
				response.Body.Close()
			}
			if err == nil || f.transport.sends() > before+1 || sent.phase != nativeSendUnproven {
				t.Fatal("copied receipt sent twice")
			}
			if condition != "zero" && (!reflect.DeepEqual(beforeOnce, freshGETRow(t, f, `SELECT * FROM native_tool_once WHERE operation_id=?`, originalReceipt.OperationID())) || !reflect.DeepEqual(beforeAudit, freshGETRow(t, f, `SELECT * FROM runtime_egress_attempts WHERE parent_id=?`, originalReceipt.OperationID()))) {
				t.Fatal("refused receipt invocation rewrote its real once/audit")
			}
		})
	}
}

func TestNativeSendPhaseConcurrentClaimAndReuse(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	receipt, request, plan := prepareRealReceipt(t, f)
	e := f.executor.(*executor)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
		close(entered)
		<-release
		return nil, io.ErrUnexpectedEOF
	}
	before := f.transport.sends()
	snapshot := freshGETRow(t, f, `SELECT * FROM runtime_egress_attempts WHERE parent_id=?`, receipt.OperationID())
	results := make(chan nativeSendResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			results <- e.sendCommitted(context.Background(), request.Clone(context.Background()), plan, receipt)
		}()
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first claim did not enter")
	}
	var refused nativeSendResult
	select {
	case refused = <-results:
	case <-time.After(time.Second):
		t.Fatal("duplicate claim did not refuse")
	}
	if refused.phase != nativeSendUnproven || !errors.Is(refused.err, nativetool.ErrReplay) {
		t.Fatalf("duplicate gained proof: %+v", refused)
	}
	once.Do(func() { close(release) })
	var first nativeSendResult
	select {
	case first = <-results:
	case <-time.After(time.Second):
		t.Fatal("first send did not drain")
	}
	if first.phase != nativeSendTransportEntered || first.err == nil || f.transport.sends() != before+1 {
		t.Fatal("one-shot send phase lost")
	}
	again := e.sendCommitted(context.Background(), request, plan, receipt)
	if again.phase != nativeSendUnproven || !errors.Is(again.err, nativetool.ErrReplay) || !reflect.DeepEqual(snapshot, freshGETRow(t, f, `SELECT * FROM runtime_egress_attempts WHERE parent_id=?`, receipt.OperationID())) {
		t.Fatal("later refusal zeroed an earlier entered attempt")
	}
}

func TestNativeSendPhaseBeforeClaimIsUnproven(t *testing.T) {
	for _, edge := range []string{"nil_context", "canceled_context", "closed_owner", "closed_executor", "claim_budget"} {
		t.Run(edge, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			f.handshake(t)
			receipt, request, plan := prepareRealReceipt(t, f)
			e := f.executor.(*executor)
			ctx := context.Background()
			switch edge {
			case "nil_context":
				ctx = nil
			case "canceled_context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "closed_owner":
				owner, cancel := context.WithCancel(ctx)
				cancel()
				e.dependencies.OwnerContext = owner
			case "closed_executor":
				e.mu.Lock()
				e.closed = true
				e.mu.Unlock()
			case "claim_budget":
				e.budget.mu.Lock()
				remaining := e.budget.maximum - e.budget.used
				e.budget.mu.Unlock()
				if !e.budget.acquire(remaining) {
					t.Fatal("budget setup")
				}
				defer e.budget.release(remaining)
			}
			before := f.transport.sends()
			snapshot := freshGETRow(t, f, `SELECT * FROM runtime_egress_attempts WHERE parent_id=?`, receipt.OperationID())
			result := e.sendCommitted(ctx, request, plan, receipt)
			if result.err == nil || result.phase != nativeSendUnproven || f.transport.sends() != before || !reflect.DeepEqual(snapshot, freshGETRow(t, f, `SELECT * FROM runtime_egress_attempts WHERE parent_id=?`, receipt.OperationID())) {
				t.Fatal("preclaim refusal manufactured zero-effect authority")
			}
		})
	}
}

func TestNativePostEntryFailuresStayUnknown(t *testing.T) {
	for _, kind := range []string{"get", "tools_call"} {
		for _, edge := range []string{"error", "nil_response", "deadline", "cancel"} {
			t.Run(kind+"/"+edge, func(t *testing.T) {
				f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
				f.handshake(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
					switch edge {
					case "nil_response":
						return nil, nil
					case "deadline":
						deadline, stop := context.WithTimeout(r.Context(), time.Millisecond)
						defer stop()
						<-deadline.Done()
						return nil, deadline.Err()
					case "cancel":
						cancel()
						return nil, r.Context().Err()
					default:
						return nil, io.ErrUnexpectedEOF
					}
				}
				method, body := "GET", ""
				if kind == "tools_call" {
					method = "POST"
					body = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`
				}
				request := f.captured(t, method, body, "synthetic-session")
				before := f.transport.sends()
				f.executor.Serve(ctx, request, httptest.NewRecorder())
				requireNativeClaimAfterDrain(t, f.executor.(*executor), request.id, false)
				op, err := f.store.NativeToolRepository().Get(context.Background(), request.id)
				if err != nil {
					t.Fatal(err)
				}
				facts := readFreshGETFacts(t, f, request.id)
				scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
				if err != nil || f.transport.sends() != before+1 || op.State != "uncertain" || op.BytesIn != nil || op.BytesOut != nil || facts.AuditKnown != 0 || !scope.ReplayBarrier {
					t.Fatalf("post-entry error turned into known-zero: op=%+v known=%d scope=%+v", op, facts.AuditKnown, scope)
				}
			})
		}
	}
}

type mismatchedStartReceipt struct {
	nativetool.Repository
	target nativetool.ID
	mode   string
	stolen nativetool.StartReceipt
}

func (r *mismatchedStartReceipt) Start(ctx context.Context, id nativetool.ID, attempt string, revision uint64, now time.Time) (nativetool.StartReceipt, error) {
	receipt, err := r.Repository.Start(ctx, id, attempt, revision, now)
	if err != nil || id != r.target {
		return receipt, err
	}
	switch r.mode {
	case "zero":
		return nativetool.StartReceipt{}, nil
	case "stolen":
		return r.stolen, nil
	case "wrong_attempt":
		op, err := r.Repository.Get(ctx, id)
		if err != nil {
			return nativetool.StartReceipt{}, err
		}
		scope, err := r.Repository.GetScope(ctx, op.Capture)
		if err != nil {
			return nativetool.StartReceipt{}, err
		}
		issuer, err := nativetool.NewStartReceiptIssuer()
		if err != nil {
			return nativetool.StartReceipt{}, err
		}
		return issuer.Issue(nativetool.StartFacts{OperationID: id, AttemptID: "wrong-local-attempt", AuthorityRevision: revision, OwnerEpoch: scope.OwnerEpoch, BindingDigest: *op.BindingDigest})
	}
	return receipt, nil
}

func TestNativeServeUnsentClosureRequiresOwnImmediateStartIdentity(t *testing.T) {
	for _, mode := range []string{"zero", "stolen", "wrong_attempt"} {
		t.Run(mode, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			f.handshake(t)
			e := f.executor.(*executor)
			stolen, _, _ := prepareRealReceipt(t, f)
			request := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"b"}}}`, "synthetic-session")
			e.dependencies.Store = &mismatchedStartReceipt{Repository: f.store.NativeToolRepository(), target: request.id, mode: mode, stolen: stolen}
			before := f.transport.sends()
			done := make(chan Terminal, 1)
			go func() { done <- e.Serve(context.Background(), request, httptest.NewRecorder()) }()
			f.approve(t, request.id)
			var result Terminal
			select {
			case result = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("identity refusal did not finish")
			}
			facts := readFreshGETFacts(t, f, request.id)
			if f.transport.sends() != before || result.Outcome != OutcomeUncertain || facts.OperationState != "uncertain" || facts.AuditKnown != 0 || facts.BytesIn.Valid || facts.BytesOut.Valid {
				t.Fatalf("non-own receipt qualified zero closure or sent: mode=%s result=%+v known=%d sends=%d/%d", mode, result, facts.AuditKnown, f.transport.sends(), before)
			}
		})
	}
}

type heldBeforeNativeVerify struct {
	nativetool.Repository
	target           nativetool.ID
	entered, release chan struct{}
}

func (h *heldBeforeNativeVerify) VerifyStartReceipt(ctx context.Context, receipt nativetool.StartReceipt, now time.Time) error {
	if receipt.OperationID() == h.target {
		close(h.entered)
		// The test releases only after durable revocation; cleanup also
		// releases this barrier, including when the caller was canceled.
		<-h.release
	}
	return h.Repository.VerifyStartReceipt(ctx, receipt, now)
}

func TestNativeUnsentProofCannotUndoIndependentRevocation(t *testing.T) {
	for _, kind := range []string{"get", "tools_call"} {
		for _, reason := range []RevocationReason{RevocationPermissionOff, RevocationCaptureStopped, RevocationAssignmentApplied, RevocationOwnerChanged} {
			t.Run(kind+"/"+string(reason), func(t *testing.T) {
				f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
				f.handshake(t)
				e := f.executor.(*executor)
				method, body := "GET", ""
				if kind == "tools_call" {
					method = "POST"
					body = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`
				}
				request := f.captured(t, method, body, "synthetic-session")
				held := &heldBeforeNativeVerify{Repository: f.store.NativeToolRepository(), target: request.id, entered: make(chan struct{}), release: make(chan struct{})}
				e.dependencies.Store = held
				var once sync.Once
				release := func() { once.Do(func() { close(held.release) }) }
				defer release()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				done := make(chan struct{})
				var terminal Terminal
				before := f.transport.sends()
				go func() { terminal = e.Serve(ctx, request, httptest.NewRecorder()); close(done) }()
				t.Cleanup(func() {
					release()
					cancel()
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("revocation request did not drain")
					}
				})
				if kind == "tools_call" {
					f.approve(t, request.id)
				}
				select {
				case <-held.entered:
				case <-ctx.Done():
					t.Fatal("first-claim Verify barrier missing")
				}
				e.mu.Lock()
				claimed := e.claimed[request.id]
				e.mu.Unlock()
				if claimed == nil {
					t.Fatal("revocation did not race a first claim")
				}
				if err := e.Revoke(context.Background(), f.capture, reason); err != nil {
					t.Fatal(err)
				}
				snapshot := freshGETRow(t, f, `SELECT * FROM runtime_egress_attempts WHERE parent_id=?`, request.id)
				release()
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("revocation did not finish")
				}
				facts := readFreshGETFacts(t, f, request.id)
				scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
				if err != nil || terminal.Outcome != OutcomeUncertain || facts.OperationState != "uncertain" || facts.AuditKnown != 0 || scope.State != "revoked" || !scope.ReplayBarrier || f.transport.sends() != before || !reflect.DeepEqual(snapshot, freshGETRow(t, f, `SELECT * FROM runtime_egress_attempts WHERE parent_id=?`, request.id)) {
					t.Fatal("known-unsent path overwrote independent revocation")
				}
			})
		}
	}
}
func (f *nativeFixture) storePath(t *testing.T) string {
	t.Helper()
	rows, err := f.db.Query(`PRAGMA database_list`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name, path string
		if err = rows.Scan(&seq, &name, &path); err != nil {
			t.Fatal(err)
		}
		if name == "main" {
			return path
		}
	}
	t.Fatal("main Store path absent")
	return ""
}
