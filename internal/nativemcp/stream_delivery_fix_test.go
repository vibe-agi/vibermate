package nativemcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

type failEventDelivery struct {
	*httptest.ResponseRecorder
	mode                   string
	event, writes, flushes int
}

func (w *failEventDelivery) Write(p []byte) (int, error) {
	w.writes++
	if w.mode == "write" && w.writes == w.event {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(p)
}
func (w *failEventDelivery) FlushError() error {
	w.flushes++
	if w.mode == "flush" && w.flushes == w.event {
		return io.ErrClosedPipe
	}
	w.ResponseRecorder.Flush()
	return nil
}

func TestNativeNoIDTerminalDeliveryFailurePreservesKnownOutcome(t *testing.T) {
	for _, expired := range []bool{false, true} {
		for _, rpcError := range []bool{false, true} {
			for _, mode := range []string{"write", "flush"} {
				t.Run(fmt.Sprintf("expired=%v/error=%v/%s", expired, rpcError, mode), func(t *testing.T) {
					limits := DefaultLimits()
					limits.SessionIdle = time.Second
					f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
					f.handshake(t)
					e := f.executor.(*executor)
					now := time.Now().UTC()
					e.dependencies.Clock = func() time.Time { return now }
					value := `"result":{}`
					want := "completed"
					if rpcError {
						value = `"error":{"code":-32603,"message":"synthetic"}`
						want = "failed"
					}
					wire := "data: {\"jsonrpc\":\"2.0\",\"id\":40," + value + "}\n\n"
					f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
						if expired {
							now = now.Add(2 * time.Second)
						}
						return wireResponse(wire, true), nil
					}
					call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":40,"method":"ping"}`, "synthetic-session")
					f.executor.Serve(context.Background(), call, &failEventDelivery{ResponseRecorder: httptest.NewRecorder(), mode: mode, event: 1})
					op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
					if err != nil || op.State != want || op.BytesIn != nil || op.BytesOut != nil {
						t.Fatalf("known no-ID result lost: state=%s counts=%v/%v error=%v", op.State, op.BytesIn, op.BytesOut, err)
					}
					scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
					if err != nil || scope.ReplayBarrier || scope.State != "active" {
						t.Fatalf("delivery revoked known result: %+v %v", scope, err)
					}
					var outcome, reason string
					var known bool
					if err := f.db.QueryRow(`SELECT outcome,error_class,byte_counts_known FROM native_tool_deliveries WHERE operation_id=?`, call.id).Scan(&outcome, &reason, &known); err != nil || outcome != "failed" || reason != "transport_failed" || known {
						t.Fatalf("physical evidence %s/%s/%v %v", outcome, reason, known, err)
					}
					e.mu.Lock()
					for _, s := range e.scopes[f.capture.Key()].sessions {
						s.mu.Lock()
						if len(s.eventIndex) != 0 || len(s.parents) != 0 || len(s.streams) != 0 {
							t.Error("no-ID finish created retained authority")
						}
						s.mu.Unlock()
					}
					e.mu.Unlock()
				})
			}
		}
	}
}

func TestNativeAcceptedNonterminalDeliveryFailureDetaches(t *testing.T) {
	for _, attempt := range []string{"POST", "GET"} {
		for _, mode := range []string{"write", "flush"} {
			t.Run(attempt+"/"+mode, func(t *testing.T) {
				f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
				f.handshake(t)
				progress := func(id string) string {
					return "id: " + id + "\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\n"
				}
				result := "id: e3\ndata: {\"jsonrpc\":\"2.0\",\"id\":40,\"result\":{}}\n\n"
				gets := 0
				posts := 0
				f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
					wire := progress("e1")
					if r.Method == "POST" {
						posts++
						if attempt == "POST" {
							wire += progress("e2")
						}
					} else {
						gets++
						if attempt == "GET" && gets == 1 {
							wire = progress("e2")
						} else {
							wire = progress("e2") + result
						}
					}
					return wireResponse(wire, true), nil
				}
				call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":40,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "synthetic-session")
				var writer http.ResponseWriter = httptest.NewRecorder()
				if attempt == "POST" {
					writer = &failEventDelivery{ResponseRecorder: httptest.NewRecorder(), mode: mode, event: 2}
				}
				f.executor.Serve(context.Background(), call, writer)
				if attempt == "GET" {
					get := f.captured(t, "GET", "", "synthetic-session")
					get.request.Header.Set("Last-Event-Id", "e1")
					f.executor.Serve(context.Background(), get, &failEventDelivery{ResponseRecorder: httptest.NewRecorder(), mode: mode, event: 1})
				}
				op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
				if err != nil || op.State != "started" {
					t.Fatalf("accepted delivery break retired RPC: %s %v", op.State, err)
				}
				scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
				if err != nil || scope.State != "active" || scope.ReplayBarrier {
					t.Fatalf("delivery break revoked capture: %+v %v", scope, err)
				}
				get := f.captured(t, "GET", "", "synthetic-session")
				get.request.Header.Set("Last-Event-Id", "e1")
				w := httptest.NewRecorder()
				got := f.executor.Serve(context.Background(), get, w)
				if got.Outcome != OutcomeCompleted || !strings.Contains(w.Body.String(), result) || posts != 1 {
					t.Fatalf("recovery failed: %+v posts=%d", got, posts)
				}
			})
		}
	}
}
