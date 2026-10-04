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
	for _, media := range []string{"json", "sse"} {
		for _, rpcError := range []bool{false, true} {
			for _, fault := range []struct {
				name      string
				committer transactionCommitter
			}{{"commit", commitThenError{}}, {"rollback", rollbackThenError{}}, {"refusal", nil}, {"before-terminal", rollbackThenError{}}} {
				if fault.name == "before-terminal" && (media != "sse" || rpcError) {
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
						terminal, _ := serve(item.body, item.session)
						if terminal.Outcome != nativemcp.OutcomeCompleted {
							t.Fatal(terminal)
						}
					}
					injection := &touchOnlyFault{fault: fault.committer, expires: f.clock.Now().Add(limits.SessionIdle).UnixMilli()}
					f.store.nativeTools.committer = injection
					wire := `{"jsonrpc":"2.0","id":44,"result":{}}`
					if rpcError {
						wire = `{"jsonrpc":"2.0","id":44,"error":{"code":-32603,"message":"Synthetic known error"}}`
					}
					if media == "sse" {
						wire = "data: " + wire + "\n\n"
					}
					if fault.name == "before-terminal" {
						wire = "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\n"
					}
					transport.handler = func(_ *http.Request, body []byte) *http.Response {
						if strings.Contains(string(body), `"id":48`) {
							return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":48,"result":{}}`))}
						}
						f.clock.now = f.clock.now.Add(6 * time.Second)
						if fault.name == "refusal" {
							sibling, _ := serve(`{"jsonrpc":"2.0","id":48,"method":"ping"}`, "session-10")
							if sibling.Outcome != nativemcp.OutcomeCompleted {
								t.Fatal(sibling)
							}
							f.clock.now = f.clock.now.Add(5 * time.Second)
						} else {
							injection.armed.Store(true)
						}
						mime := "application/json"
						if media == "sse" {
							mime = "text/event-stream"
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{mime}}, Body: io.NopCloser(strings.NewReader(wire))}
					}
					terminal, w := serve(`{"jsonrpc":"2.0","id":44,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "session-1")
					f.store.nativeTools.committer = sqlTransactionCommitter{}
					op, err := f.store.NativeToolRepository().Get(ctx, terminal.OperationID)
					scope, scopeErr := f.store.NativeToolRepository().GetScope(ctx, f.capture)
					want := "completed"
					if rpcError {
						want = "failed"
					}
					if fault.name == "before-terminal" {
						if !injection.used.Load() || err != nil || scopeErr != nil || op.State != "uncertain" || op.BytesIn != nil || op.BytesOut != nil || !scope.ReplayBarrier {
							t.Fatalf("unknown effect silently trusted %+v %+v %v %v", op, scope, err, scopeErr)
						}
						return
					}
					used := injection.used.Load() || fault.name == "refusal"
					if !used || err != nil || scopeErr != nil || op.State != want || op.BytesIn == nil || *op.BytesIn != int64(len(wire)) || scope.ReplayBarrier || scope.State != "active" || w.Body.String() != wire || terminal.Reason != nativemcp.ReasonStorageUnavailable || rpcError && op.Reason != "rpc_error" {
						t.Fatalf("known terminal relabeled %+v %+v scope%+v wire%q used%v err%v/%v", terminal, op, scope, w.Body.String(), injection.used.Load(), err, scopeErr)
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
