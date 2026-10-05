package nativemcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// Delegation holds only a positively committed receipt, after the actual
// Store transaction and issuer lock have returned. Verify is never replaced.
type heldFreshGETStart struct {
	nativetool.Repository
	target          nativetool.ID
	started         chan nativetool.StartReceipt
	release         chan struct{}
	verified        chan error
	claim           func() bool
	claimedAtVerify chan bool
	beforeVerify    func()
}

func (h *heldFreshGETStart) Start(ctx context.Context, id nativetool.ID, attempt string, revision uint64, now time.Time) (nativetool.StartReceipt, error) {
	receipt, err := h.Repository.Start(ctx, id, attempt, revision, now)
	if id == h.target && err == nil {
		select {
		case h.started <- receipt:
		case <-ctx.Done():
			return receipt, ctx.Err()
		}
		select {
		case <-h.release:
		case <-ctx.Done():
			return receipt, ctx.Err()
		}
	}
	return receipt, err
}
func (h *heldFreshGETStart) VerifyStartReceipt(ctx context.Context, receipt nativetool.StartReceipt, now time.Time) error {
	if receipt.OperationID() == h.target && h.claim != nil {
		h.claimedAtVerify <- h.claim()
		if h.beforeVerify != nil {
			h.beforeVerify()
		}
	}
	err := h.Repository.VerifyStartReceipt(ctx, receipt, now)
	if receipt.OperationID() == h.target {
		h.verified <- err
	}
	return err
}

type freshGETTransportEntries struct {
	next        *countingTransport
	total, gets atomic.Int64
	toolCalls   atomic.Int64
	entered     chan struct{}
}

type diagnosticToolCallContextKey struct{}

func (p *freshGETTransportEntries) RoundTrip(r *http.Request, policy egressnetwork.Policy) (*http.Response, error) {
	p.total.Add(1)
	if r.Context().Value(diagnosticToolCallContextKey{}) == true {
		p.toolCalls.Add(1)
	}
	if r.Method == http.MethodGet {
		p.gets.Add(1)
		select {
		case p.entered <- struct{}{}:
		default:
		}
	}
	return p.next.RoundTrip(r, policy)
}

type freshGETFacts struct {
	BindingRevision, BindingGeneration, SessionRevision, SessionGeneration   int64
	BindingCatalogBytes, SessionCatalogBytes                                 int
	SessionState, OperationState, OperationReason, AuditOutcome, AuditReason string
	AuditKnown                                                               int
	AuditBytesIn, AuditBytesOut                                              int64
	BytesIn, BytesOut                                                        sql.NullInt64
	Binding, Once, Audit                                                     []any
}

func freshGETRow(t *testing.T, f *nativeFixture, query string, id nativetool.ID) []any {
	t.Helper()
	rows, err := f.db.Query(query, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatal("missing real GET SQL row")
	}
	values := make([]any, len(columns))
	targets := make([]any, len(columns))
	for i := range values {
		targets[i] = &values[i]
	}
	if err = rows.Scan(targets...); err != nil {
		t.Fatal(err)
	}
	for i, v := range values {
		if b, ok := v.([]byte); ok {
			values[i] = append([]byte{}, b...)
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("ambiguous real GET SQL row")
	}
	return values
}
func readFreshGETFacts(t *testing.T, f *nativeFixture, id nativetool.ID) freshGETFacts {
	t.Helper()
	var out freshGETFacts
	err := f.db.QueryRow(`SELECT b.session_revision,b.catalog_generation,COALESCE(length(b.catalog_digest),0),s.revision,s.catalog_generation,COALESCE(length(s.catalog_digest),0),s.state,o.state,o.terminal_reason,o.bytes_in,o.bytes_out,a.outcome,a.error_class,a.byte_counts_known,a.bytes_in,a.bytes_out FROM native_tool_operations o JOIN native_tool_bindings b USING(operation_id) JOIN native_tool_sessions s USING(session_id) JOIN native_tool_once n USING(operation_id) JOIN runtime_egress_attempts a USING(attempt_id) WHERE o.operation_id=?`, id).Scan(&out.BindingRevision, &out.BindingGeneration, &out.BindingCatalogBytes, &out.SessionRevision, &out.SessionGeneration, &out.SessionCatalogBytes, &out.SessionState, &out.OperationState, &out.OperationReason, &out.BytesIn, &out.BytesOut, &out.AuditOutcome, &out.AuditReason, &out.AuditKnown, &out.AuditBytesIn, &out.AuditBytesOut)
	if err != nil {
		t.Fatal(err)
	}
	out.Binding = freshGETRow(t, f, `SELECT * FROM native_tool_bindings WHERE operation_id=?`, id)
	out.Once = freshGETRow(t, f, `SELECT * FROM native_tool_once WHERE operation_id=?`, id)
	out.Audit = freshGETRow(t, f, `SELECT * FROM runtime_egress_attempts WHERE parent_kind='client_operation' AND parent_id=?`, id)
	return out
}

type freshGETDiagnostic struct {
	fixture  *nativeFixture
	entries  *freshGETTransportEntries
	receipt  nativetool.StartReceipt
	terminal Terminal
	facts    freshGETFacts
	scope    nativetool.Scope
}

func runFreshGETReceiptOrder(t *testing.T, order string) freshGETDiagnostic {
	t.Helper()
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	e := f.executor.(*executor)
	entries := &freshGETTransportEntries{next: f.transport, entered: make(chan struct{}, 1)}
	e.dependencies.Transport = entries
	for _, message := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`} {
		session := "synthetic-session"
		if strings.Contains(message, `"initialize"`) {
			session = ""
		}
		result, _ := f.serve(t, message, session)
		if result.Outcome != OutcomeCompleted {
			t.Fatalf("handshake=%+v", result)
		}
	}
	var revision, generation int64
	var state string
	var catalogBytes int
	if err := f.db.QueryRow(`SELECT revision,catalog_generation,state,COALESCE(length(catalog_digest),0) FROM native_tool_sessions`).Scan(&revision, &generation, &state, &catalogBytes); err != nil {
		t.Fatal(err)
	}
	if revision != 2 || generation != 0 || state != "ready" || catalogBytes != 0 || entries.total.Load() != 2 {
		t.Fatalf("initial session rev=%d gen=%d state=%s catalogBytes=%d sends=%d", revision, generation, state, catalogBytes, entries.total.Load())
	}
	streamDone := make(chan struct{}, 1)
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		if r.Method == http.MethodGet {
			reader, writer := io.Pipe()
			go func() {
				defer writer.Close()
				defer func() { streamDone <- struct{}{} }()
				_, _ = io.WriteString(writer, ": control\n\n")
				<-r.Context().Done()
			}()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(body, &request) != nil {
			return nil, errProtocol
		}
		result := `{}`
		if request.Method == "tools/list" {
			result = `{"tools":[{"name":"read_file","description":"Synthetic public tool","inputSchema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`
		}
		return wireResponse(`{"jsonrpc":"2.0","id":`+string(request.ID)+`,"result":`+result+`}`, false), nil
	}
	publish := func() {
		t.Helper()
		result, _ := f.serve(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "synthetic-session")
		if result.Outcome != OutcomeCompleted {
			t.Fatalf("real catalog publication=%+v", result)
		}
	}
	if order == "before" {
		publish()
	}
	get := f.captured(t, http.MethodGet, "", "synthetic-session")
	held := &heldFreshGETStart{Repository: f.store.NativeToolRepository(), target: get.id, started: make(chan nativetool.StartReceipt, 1), release: make(chan struct{}), verified: make(chan error, 1), claimedAtVerify: make(chan bool, 1)}
	held.claim = func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.claimed[get.id] }
	e.dependencies.Store = held
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(held.release) }) }
	done := make(chan struct{})
	var terminal Terminal
	go func() { terminal = f.executor.Serve(ctx, get, httptest.NewRecorder()); close(done) }()
	t.Cleanup(func() {
		release()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("held GET did not drain")
		}
		if entries.gets.Load() > 0 {
			select {
			case <-streamDone:
			case <-time.After(3 * time.Second):
				t.Error("control stream producer did not drain")
			}
		}
	})
	var receipt nativetool.StartReceipt
	select {
	case receipt = <-held.started:
	case <-ctx.Done():
		t.Fatal("real GET Start was not reached")
	}
	before := readFreshGETFacts(t, f, get.id)
	if receipt.OperationID() != get.id || receipt.AttemptID() == "" || before.OperationState != "started" || before.AuditOutcome != "" || entries.gets.Load() != 0 || held.claim() {
		t.Fatal("hold did not contain a real committed unsent GET")
	}
	wantRev, wantGen := int64(2), int64(0)
	if order == "before" {
		wantRev, wantGen = 3, 1
	}
	if before.BindingRevision != wantRev || before.SessionRevision != wantRev || before.BindingGeneration != wantGen || before.SessionGeneration != wantGen {
		t.Fatalf("unexpected held binding/session=%+v", before)
	}
	t.Logf("held GET operation=%s attempt=%s binding=%d/%d session=%d/%d audit=open GETentries=%d total=%d", get.id, receipt.AttemptID(), before.BindingRevision, before.BindingGeneration, before.SessionRevision, before.SessionGeneration, entries.gets.Load(), entries.total.Load())
	if order == "during" {
		publish()
		after := readFreshGETFacts(t, f, get.id)
		if after.SessionRevision != 3 || after.SessionGeneration != 1 || after.SessionCatalogBytes != 32 || !reflect.DeepEqual(before.Binding, after.Binding) || !reflect.DeepEqual(before.Once, after.Once) || !reflect.DeepEqual(before.Audit, after.Audit) || entries.total.Load() != 3 || entries.gets.Load() != 0 {
			t.Fatal("actual catalog publication did not advance current session alone")
		}
		t.Logf("published catalog binding=%d/%d catalogBytes=%d session=%d/%d catalogBytes=%d unchanged once/audit; GETentries=0 total=3", after.BindingRevision, after.BindingGeneration, after.BindingCatalogBytes, after.SessionRevision, after.SessionGeneration, after.SessionCatalogBytes)
	}
	release()
	var verifyErr error
	select {
	case verifyErr = <-held.verified:
	case <-ctx.Done():
		t.Fatal("delegated final Verify not reached")
	}
	if !<-held.claimedAtVerify {
		t.Fatal("final Verify did not follow the new exclusive first claim")
	}
	if order == "during" {
		if !errors.Is(verifyErr, nativetool.ErrConflict) {
			t.Fatalf("hypothesis falsified: final Verify=%v", verifyErr)
		}
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("rejected GET did not complete")
		}
		if entries.gets.Load() != 0 || entries.total.Load() != 3 {
			t.Fatal("rejected receipt entered transport")
		}
	} else {
		if verifyErr != nil {
			t.Fatalf("control Verify=%v", verifyErr)
		}
		select {
		case <-entries.entered:
		case <-ctx.Done():
			t.Fatal("valid control never entered transport")
		}
		if order == "after" {
			publish()
		}
		scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
		if err != nil || scope.State != "active" || scope.ReplayBarrier || entries.gets.Load() != 1 || entries.total.Load() != 4 {
			t.Fatal("valid current control lacked one authorized GET")
		}
		t.Logf("control final Verify=nil GETentries=1 total=4 scope=active barrier=false before cancellation")
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("control GET did not drain after cancellation")
		}
	}
	facts := readFreshGETFacts(t, f, get.id)
	scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result firstClaim=true Verify=%v GETentries=%d terminal=%s/%s op=%s/%s opCountsValid=%t/%t audit=%s/%s byte_counts_known=%d numericAuditBytes=%d/%d scope=%s barrier=%t session=%s", verifyErr, entries.gets.Load(), terminal.Outcome, terminal.Reason, facts.OperationState, facts.OperationReason, facts.BytesIn.Valid, facts.BytesOut.Valid, facts.AuditOutcome, facts.AuditReason, facts.AuditKnown, facts.AuditBytesIn, facts.AuditBytesOut, scope.State, scope.ReplayBarrier, facts.SessionState)
	return freshGETDiagnostic{f, entries, receipt, terminal, facts, scope}
}

func TestFreshGETReceiptCatalogInterleavingDiagnostic(t *testing.T) {
	runFreshGETReceiptOrder(t, "during")
}
func TestFreshGETReceiptCatalogOrderingControls(t *testing.T) {
	for _, order := range []string{"after", "before"} {
		t.Run(order, func(t *testing.T) { runFreshGETReceiptOrder(t, order) })
	}
}

func TestFreshGETReceiptRejectionBeforeTransportDoesNotInventUpstreamEffects(t *testing.T) {
	d := runFreshGETReceiptOrder(t, "during")
	if d.facts.OperationState != "failed" || !d.facts.BytesIn.Valid || !d.facts.BytesOut.Valid || d.facts.BytesIn.Int64 != 0 || d.facts.BytesOut.Int64 != 0 || d.facts.AuditKnown != 1 || d.scope.State != "active" || d.scope.ReplayBarrier {
		t.Fatalf("known no-send refusal invented uncertainty/poisoned authority: op=%s countsKnown=%t/%t auditKnown=%d scope=%s barrier=%t", d.facts.OperationState, d.facts.BytesIn.Valid, d.facts.BytesOut.Valid, d.facts.AuditKnown, d.scope.State, d.scope.ReplayBarrier)
	}
	if err := d.fixture.store.NativeToolRepository().VerifyStartReceipt(context.Background(), d.receipt, time.Now().UTC()); err == nil {
		t.Fatal("stale receipt became reusable")
	}
	before := d.entries.total.Load()
	result, _ := d.fixture.serve(t, `{"jsonrpc":"2.0","id":40,"method":"ping"}`, "synthetic-session")
	if result.Outcome != OutcomeCompleted || d.entries.total.Load() != before+1 {
		t.Fatal("new genuine current-plan operation could not proceed")
	}
}

// Adjacent bug-family diagnostic: a committed tools/call has really consumed
// its own AllowOnce before publication makes its final send receipt stale.
// The context marker counts this exact Serve at the RoundTrip entry; it is
// neither a wire header nor any authority used by product code.
func runAdjacentToolsCallReceiptOrder(t *testing.T) freshGETDiagnostic {
	t.Helper()
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	e := f.executor.(*executor)
	entries := &freshGETTransportEntries{next: f.transport, entered: make(chan struct{}, 1)}
	e.dependencies.Transport = entries
	f.handshake(t)
	if entries.total.Load() != 3 {
		t.Fatal("catalog handshake did not make three real sends")
	}
	call := f.captured(t, http.MethodPost, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	held := &heldFreshGETStart{Repository: f.store.NativeToolRepository(), target: call.id, started: make(chan nativetool.StartReceipt, 1), release: make(chan struct{}), verified: make(chan error, 1), claimedAtVerify: make(chan bool, 1)}
	held.claim = func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.claimed[call.id] }
	e.dependencies.Store = held
	// Only the following refresh reaches this transport in the expected path.
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(body, &request) != nil {
			return nil, errProtocol
		}
		result := `{"content":[]}`
		if request.Method == "tools/list" {
			result = `{"tools":[{"name":"read_file","description":"Synthetic public tool","inputSchema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`
		}
		return wireResponse(`{"jsonrpc":"2.0","id":`+string(request.ID)+`,"result":`+result+`}`, false), nil
	}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), diagnosticToolCallContextKey{}, true), 5*time.Second)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(held.release) }) }
	done := make(chan struct{})
	var terminal Terminal
	go func() { terminal = f.executor.Serve(ctx, call, httptest.NewRecorder()); close(done) }()
	t.Cleanup(func() {
		release()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("held tools/call did not drain")
		}
	})
	f.approve(t, call.id)
	var receipt nativetool.StartReceipt
	select {
	case receipt = <-held.started:
	case <-ctx.Done():
		t.Fatal("approved real tools/call Start not reached")
	}
	before := readFreshGETFacts(t, f, call.id)
	op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
	if err != nil || op.Kind != nativetool.RequestToolsCall || op.ApprovalID == nil {
		t.Fatal("real tools/call decision missing")
	}
	approval, err := f.store.NativeToolRepository().GetApproval(context.Background(), *op.ApprovalID)
	if err != nil || approval.State != "allowed" || approval.ConsumedAt == nil {
		t.Fatal("real AllowOnce not consumed by Start")
	}
	approvalRow := freshGETRow(t, f, `SELECT * FROM native_tool_approvals WHERE approval_id=?`, approval.ID)
	if receipt.OperationID() != call.id || receipt.AttemptID() == "" || before.OperationState != "started" || before.AuditOutcome != "" || before.BindingRevision != 3 || before.BindingGeneration != 1 || before.SessionRevision != 3 || before.SessionGeneration != 1 || entries.toolCalls.Load() != 0 || held.claim() {
		t.Fatal("target was not a newly committed, unclaimed tools/call")
	}
	t.Logf("held tools/call operation=%s attempt=%s binding=3/1 session=3/1 Allow=consumed revision=%d firstClaim=false audit=open toolEntries=0 total=3", call.id, receipt.AttemptID(), approval.Revision)
	result, _ := f.serve(t, `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`, "synthetic-session")
	if result.Outcome != OutcomeCompleted {
		t.Fatalf("real refresh=%+v", result)
	}
	after := readFreshGETFacts(t, f, call.id)
	if after.SessionRevision != 4 || after.SessionGeneration != 2 || !reflect.DeepEqual(before.Binding, after.Binding) || !reflect.DeepEqual(before.Once, after.Once) || !reflect.DeepEqual(before.Audit, after.Audit) || !reflect.DeepEqual(approvalRow, freshGETRow(t, f, `SELECT * FROM native_tool_approvals WHERE approval_id=?`, approval.ID)) || entries.toolCalls.Load() != 0 || entries.total.Load() != 4 {
		t.Fatal("real refresh altered the committed call/consumed decision")
	}
	t.Logf("published refresh binding=3/1 session=4/2; original binding/once/open audit/consumed approval unchanged; toolEntries=0 total=4")
	release()
	var verifyErr error
	select {
	case verifyErr = <-held.verified:
	case <-ctx.Done():
		t.Fatal("tools/call final Verify not reached")
	}
	claimed := <-held.claimedAtVerify
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("tools/call refusal did not complete")
	}
	if !claimed || !errors.Is(verifyErr, nativetool.ErrConflict) || entries.toolCalls.Load() != 0 || entries.total.Load() != 4 {
		t.Fatalf("adjacent hypothesis falsified: firstClaim=%t Verify=%v entries=%d total=%d", claimed, verifyErr, entries.toolCalls.Load(), entries.total.Load())
	}
	final := readFreshGETFacts(t, f, call.id)
	scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := f.store.NativeToolRepository().GetApproval(context.Background(), approval.ID)
	if err != nil || !reflect.DeepEqual(approval, retained) {
		t.Fatal("refusal changed consumed approval")
	}
	t.Logf("tools/call result firstClaim=true Verify=%v toolEntries=0 terminal=%s/%s op=%s/%s opCountsValid=%t/%t audit=%s/%s byte_counts_known=%d numericAuditBytes=%d/%d approval=allowed/consumed revision=%d onceUnchanged=%t scope=%s barrier=%t", verifyErr, terminal.Outcome, terminal.Reason, final.OperationState, final.OperationReason, final.BytesIn.Valid, final.BytesOut.Valid, final.AuditOutcome, final.AuditReason, final.AuditKnown, final.AuditBytesIn, final.AuditBytesOut, retained.Revision, reflect.DeepEqual(before.Once, final.Once), scope.State, scope.ReplayBarrier)
	return freshGETDiagnostic{f, entries, receipt, terminal, final, scope}
}

func TestFreshGETAdjacentToolsCallReceiptCatalogDiagnostic(t *testing.T) {
	runAdjacentToolsCallReceiptOrder(t)
}

func TestFreshGETAdjacentToolsCallRejectionBeforeTransportDoesNotInventUpstreamEffects(t *testing.T) {
	d := runAdjacentToolsCallReceiptOrder(t)
	if d.facts.OperationState != "failed" || !d.facts.BytesIn.Valid || !d.facts.BytesOut.Valid || d.facts.BytesIn.Int64 != 0 || d.facts.BytesOut.Int64 != 0 || d.facts.AuditKnown != 1 || d.scope.State != "active" || d.scope.ReplayBarrier {
		t.Fatalf("consumed but never-sent tools/call invented uncertainty: op=%s opCountsKnown=%t/%t byte_counts_known=%d scope=%s barrier=%t", d.facts.OperationState, d.facts.BytesIn.Valid, d.facts.BytesOut.Valid, d.facts.AuditKnown, d.scope.State, d.scope.ReplayBarrier)
	}
	if d.fixture.store.NativeToolRepository().VerifyStartReceipt(context.Background(), d.receipt, time.Now().UTC()) == nil {
		t.Fatal("spent tools/call receipt became reusable")
	}
	before := d.entries.total.Load()
	d.fixture.serve(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	if d.entries.total.Load() != before {
		t.Fatal("original tools/call was resent")
	}
	next := d.fixture.captured(t, http.MethodPost, `{"jsonrpc":"2.0","id":40,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"b"}}}`, "synthetic-session")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	done := make(chan struct{})
	var result Terminal
	go func() { result = d.fixture.executor.Serve(ctx, next, httptest.NewRecorder()); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("new approved call did not drain")
		}
	})
	d.fixture.approve(t, next.id)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("new current-plan call did not complete")
	}
	if result.Outcome != OutcomeCompleted || d.entries.total.Load() != before+1 {
		t.Fatal("new independently approved current-plan call could not proceed")
	}
}
