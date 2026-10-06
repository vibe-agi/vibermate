package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
)

type renewalTransport struct {
	calls   atomic.Int64
	handler func(*http.Request, []byte) *http.Response
}

func (t *renewalTransport) RoundTrip(r *http.Request, _ egressnetwork.Policy) (*http.Response, error) {
	t.calls.Add(1)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	return t.handler(r, body), nil
}

type touchOnlyFault struct {
	armed, used atomic.Bool
	fault       transactionCommitter
	expires     int64
}

type catalogOnlyFault struct{ used atomic.Bool }

func (f *catalogOnlyFault) Commit(tx *sql.Tx) error {
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM native_tool_sessions WHERE catalog_generation<>0`).Scan(&count); err != nil {
		return err
	}
	if count > 0 && f.used.CompareAndSwap(false, true) {
		return (rollbackThenError{}).Commit(tx)
	}
	return tx.Commit()
}

func (f *touchOnlyFault) Commit(tx *sql.Tx) error {
	if f.armed.Load() {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM native_tool_sessions WHERE expires_at_unix_ms>?`, f.expires).Scan(&count); err != nil {
			return err
		}
		if count > 0 && f.armed.CompareAndSwap(true, false) {
			f.used.Store(true)
			return f.fault.Commit(tx)
		}
	}
	return tx.Commit()
}

func TestNativeKnownTerminalSurvivesTouchOnlyFault(t *testing.T) {
	testNativeKnownTerminalSurvivesTouchOnlyFault(t, "ping")
}

func TestNativeKnownListTerminalSurvivesTouchOnlyFault(t *testing.T) {
	testNativeKnownTerminalSurvivesTouchOnlyFault(t, "tools/list")
}

func TestNativeKnownInitializedAcknowledgementSurvivesReadyRefusal(t *testing.T) {
	testNativeKnownTerminalSurvivesTouchOnlyFault(t, "notifications/initialized")
}

func testNativeKnownTerminalSurvivesTouchOnlyFault(t *testing.T, method string) {
	for _, media := range []string{"json", "sse"} {
		for _, rpcError := range []bool{false, true} {
			for _, fault := range []struct {
				name      string
				committer transactionCommitter
			}{{"commit", commitThenError{}}, {"rollback", rollbackThenError{}}, {"refusal", nil}, {"before-terminal", rollbackThenError{}}, {"malformed-terminal", rollbackThenError{}}, {"publication-rollback", nil}} {
				if fault.name == "publication-rollback" && (method != "tools/list" || rpcError) {
					continue
				}
				if fault.name == "malformed-terminal" && (method != "tools/list" || rpcError) {
					continue
				}
				if fault.name == "before-terminal" && (media != "sse" || rpcError) {
					continue
				}
				if method == "notifications/initialized" && (media != "json" || rpcError || fault.name != "refusal") {
					continue
				}
				t.Run(fmt.Sprintf("%s/error%v/%s", media, rpcError, fault.name), func(t *testing.T) {
					f := newNativeLeaseFixture(t, true, false)
					ctx := context.Background()
					ids := &mcpFaultIDs{}
					offline, _ := offlinehold.New(offlinehold.DefaultConfig())
					if err := offline.Start(ctx, offlinehold.RuntimeBinding{InstanceID: "renewal-terminal"}); err != nil {
						t.Fatal(err)
					}
					options := toolapproval.DefaultOptions(f.store.ToolApprovalRepository())
					options.NativeRepository = f.store.NativeToolRepository()
					authority, err := toolapproval.New(ctx, options)
					if err != nil {
						t.Fatal(err)
					}
					gate, _ := toolpolicy.New(authority)
					transport := &renewalTransport{}
					limits := nativemcp.DefaultLimits()
					limits.SessionIdle = 10 * time.Second
					executor, err := nativemcp.New(nativemcp.Dependencies{OwnerContext: ctx, Transport: transport, Store: f.store.NativeToolRepository(), ToolDecisions: gate, Offline: offline, Clock: f.clock.Now, IDs: ids}, limits)
					if err != nil {
						t.Fatal(err)
					}
					defer executor.Close(ctx)
					transport.handler = func(r *http.Request, body []byte) *http.Response {
						var message map[string]json.RawMessage
						_ = json.Unmarshal(body, &message)
						id := string(message["id"])
						method := string(message["method"])
						header := http.Header{"Content-Type": []string{"application/json"}}
						result := `{}`
						if method == `"initialize"` {
							header.Set("Mcp-Session-Id", "session-"+id)
							result = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"synthetic","version":"1"}}`
						}
						if method == `"tools/list"` {
							result = `{"tools":[{"name":"read_file","inputSchema":{"type":"object"}}]}`
						}
						if id == "" {
							return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}
						}
						return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":` + id + `,"result":` + result + `}`))}
					}
					serve := func(body, session string) (nativemcp.Terminal, *httptest.ResponseRecorder) {
						lease, err := f.assignments.BeginRequest(ctx, f.capture, f.connectionID, f.facts)
						if err != nil {
							t.Fatal(err)
						}
						operation, _ := ids.NewNativeOperationID(ctx)
						request := httptest.NewRequest("POST", "https://native.example/backend-api/ps/mcp", strings.NewReader(body))
						request.Header.Set("Content-Type", "application/json")
						request.Header.Set("Authorization", "Bearer synthetic-original-login")
						if session != "" {
							request.Header.Set("Mcp-Session-Id", session)
						}
						captured, err := nativemcp.NewCapturedRequest(lease, f.admission, request, operation)
						if err != nil {
							lease.Release()
							t.Fatal(err)
						}
						w := httptest.NewRecorder()
						return executor.Serve(ctx, captured, w), w
					}
					for _, item := range []struct{ body, session string }{{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, ""}, {`{"jsonrpc":"2.0","method":"notifications/initialized"}`, "session-1"}, {`{"jsonrpc":"2.0","id":10,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, ""}, {`{"jsonrpc":"2.0","method":"notifications/initialized"}`, "session-10"}} {
						if method == "notifications/initialized" && item.session == "session-1" {
							continue
						}
						terminal, _ := serve(item.body, item.session)
						if terminal.Outcome != nativemcp.OutcomeCompleted {
							t.Fatal(terminal)
						}
					}
					receipt := prepareNativeSiblingBookkeepingReceipt(t, f, ids)
					injection := &touchOnlyFault{fault: fault.committer, expires: f.clock.Now().Add(limits.SessionIdle).UnixMilli()}
					f.store.nativeTools.committer = injection
					publication := &catalogOnlyFault{}
					if fault.name == "publication-rollback" {
						f.store.nativeTools.committer = publication
					}
					wire := `{"jsonrpc":"2.0","id":44,"result":{}}`
					if method == "tools/list" {
						wire = `{"jsonrpc":"2.0","id":44,"result":{"tools":[{"name":"read_file","inputSchema":{"type":"object"}}]}}`
					}
					if fault.name == "malformed-terminal" {
						wire = `{"jsonrpc":"2.0","id":44,"result":{"tools":false}}`
					}
					if rpcError {
						wire = `{"jsonrpc":"2.0","id":44,"error":{"code":-32603,"message":"Synthetic known error"}}`
					}
					if media == "sse" {
						wire = "data: " + wire + "\n\n"
					}
					if method == "notifications/initialized" {
						wire = ""
					}
					if fault.name == "before-terminal" {
						wire = "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\n"
					}
					transport.handler = func(_ *http.Request, body []byte) *http.Response {
						if strings.Contains(string(body), `"id":48`) {
							return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":48,"result":{}}`))}
						}
						if fault.name != "publication-rollback" {
							f.clock.now = f.clock.now.Add(6 * time.Second)
						}
						if fault.name == "refusal" {
							sibling, _ := serve(`{"jsonrpc":"2.0","id":48,"method":"ping"}`, "session-10")
							if sibling.Outcome != nativemcp.OutcomeCompleted {
								t.Fatal(sibling)
							}
							f.clock.now = f.clock.now.Add(5 * time.Second)
						} else if fault.name != "publication-rollback" {
							injection.armed.Store(true)
						}
						if method == "notifications/initialized" {
							return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}
						}
						mime := "application/json"
						if media == "sse" {
							mime = "text/event-stream"
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{mime}}, Body: io.NopCloser(strings.NewReader(wire))}
					}
					requestBody := `{"jsonrpc":"2.0","id":44,"method":"` + method + `","params":{"_meta":{"progressToken":9}}}`
					if method == "notifications/initialized" {
						requestBody = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
					}
					terminal, w := serve(requestBody, "session-1")
					f.store.nativeTools.committer = sqlTransactionCommitter{}
					op, err := f.store.NativeToolRepository().Get(ctx, terminal.OperationID)
					scope, scopeErr := f.store.NativeToolRepository().GetScope(ctx, f.capture)
					want := "completed"
					if rpcError {
						want = "failed"
					}
					if fault.name == "before-terminal" || fault.name == "malformed-terminal" {
						if !injection.used.Load() || err != nil || scopeErr != nil || op.State != "uncertain" || op.BytesIn != nil || op.BytesOut != nil || !scope.ReplayBarrier {
							t.Fatalf("unknown effect silently trusted %+v %+v %v %v", op, scope, err, scopeErr)
						}
						return
					}
					used := injection.used.Load() || fault.name == "refusal" || publication.used.Load()
					outcome := nativemcp.OutcomeCompleted
					if rpcError {
						outcome = nativemcp.OutcomeFailed
					}
					if !used || err != nil || scopeErr != nil || op.State != want || op.BytesIn == nil || *op.BytesIn != int64(len(wire)) || op.BytesOut == nil || *op.BytesOut != int64(len(requestBody)) || terminal.Outcome != outcome || scope.ReplayBarrier || scope.State != "active" || w.Body.String() != wire || terminal.Reason != nativemcp.ReasonStorageUnavailable || rpcError && op.Reason != "rpc_error" {
						t.Fatalf("known terminal relabeled %+v %+v scope%+v wire%q used%v err%v/%v", terminal, op, scope, w.Body.String(), injection.used.Load(), err, scopeErr)
					}
					var once int
					if err = f.store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, op.ID).Scan(&once); err != nil || once != 1 {
						t.Fatalf("terminal once changed %d %v", once, err)
					}
					if method == "tools/list" {
						var catalogs int
						if err = f.store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_sessions WHERE catalog_generation<>0 OR catalog_digest IS NOT NULL`).Scan(&catalogs); err != nil || catalogs != 0 {
							t.Fatalf("unavailable collection published %d %v", catalogs, err)
						}
					}
					if err = f.store.NativeToolRepository().VerifyStartReceipt(ctx, receipt, f.clock.Now()); err != nil {
						t.Fatalf("preexisting sibling receipt fenced %v", err)
					}
					before := transport.calls.Load()
					terminal, _ = serve(`{"jsonrpc":"2.0","id":45,"method":"ping"}`, "session-1")
					if terminal.Outcome == nativemcp.OutcomeCompleted || transport.calls.Load() != before {
						t.Fatal("renewal failure did not fence future session")
					}
					transport.handler = func(*http.Request, []byte) *http.Response {
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":46,"result":{}}`))}
					}
					terminal, _ = serve(`{"jsonrpc":"2.0","id":46,"method":"ping"}`, "session-10")
					if terminal.Outcome != nativemcp.OutcomeCompleted {
						t.Fatalf("sibling authority fenced %+v", terminal)
					}
				})
			}
		}
	}
}

// This is a real same-Store Start receipt, not a fabricated issuer or success
// seed. It remains started (unsent) until the fixture's authoritative cleanup.
func prepareNativeSiblingBookkeepingReceipt(t *testing.T, f nativeLeaseFixture, ids *mcpFaultIDs) nativetool.StartReceipt {
	t.Helper()
	ctx := context.Background()
	lease, err := f.assignments.BeginRequest(ctx, f.capture, f.connectionID, f.facts)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	plan, _ := lease.Plan().ClientToolPlan()
	plan = frozenExplicitNativePlan(t, plan)
	var siblingID string
	var revision uint64
	if err = f.store.database.QueryRow(`SELECT session_id,revision FROM native_tool_sessions ORDER BY rowid DESC LIMIT 1`).Scan(&siblingID, &revision); err != nil {
		t.Fatal(err)
	}
	scope, err := f.store.NativeToolRepository().GetScope(ctx, f.capture)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestPing, Plan: plan, Capture: f.capture, AssignmentRevision: uint64(lease.Assignment().Revision), OwnerEpoch: scope.OwnerEpoch, SessionID: nativetool.ID(siblingID), SessionRevision: revision, ExactRPC: []byte(`{"jsonrpc":"2.0","id":33,"method":"ping"}`)})
	if err != nil {
		t.Fatal(err)
	}
	operation, _ := ids.NewNativeOperationID(ctx)
	rpcID, _ := binding.RPCIDDigest()
	digest := binding.RequestDigest()
	sid := binding.SessionID()
	repo := f.store.NativeToolRepository()
	if _, err = repo.Receive(ctx, nativetool.ReceiveInput{ID: operation, Capture: f.capture, EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: f.connectionID, TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: binding.Kind(), SessionID: &sid, RPCIDDigest: &rpcID, RequestDigest: &digest, Recording: plan.ContentRecording(), ObservedAt: f.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if err = repo.Bind(ctx, operation, binding, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	receipt, err := repo.Start(ctx, operation, "sibling-before-bookkeeping", scope.Revision, f.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
