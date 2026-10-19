package nativemcp

import (
	"context"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type countedNativeDefaults struct {
	resolver    egressprofile.Resolver
	calls       atomic.Int64
	unavailable atomic.Bool
}

func (d *countedNativeDefaults) Resolve(ctx context.Context, account, flow egressprofile.Choice) (egressprofile.Resolved, error) {
	d.calls.Add(1)
	if d.unavailable.Load() {
		return egressprofile.Resolved{}, egressprofile.ErrInvalidProfile
	}
	return d.resolver.Resolve(ctx, account, flow)
}

func TestNativeRelatedChildrenKeepRealParentExitAcrossDefaultEditOrFailure(t *testing.T) {
	for _, kind := range []string{"callback", "recovery"} {
		for _, unavailable := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unavailable=%t", kind, unavailable), func(t *testing.T) {
				f := newNativeFixtureWithChoice(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, 0, egressprofile.Inherit())
				profiles, err := egressprofile.NewManager(f.store.EgressProfileRepository(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defaults, err := egressprofile.NewDefaultManager(f.store.EgressDefaultRepository(), profiles)
				if err != nil {
					t.Fatal(err)
				}
				setting, err := defaults.SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: egressprofile.DirectID, Revision: 1}})
				if err != nil {
					t.Fatal(err)
				}
				counted := &countedNativeDefaults{resolver: defaults}
				f.executor.(*executor).dependencies.EgressDefaults = counted
				f.handshake(t)
				var parent CapturedRequest
				var childBody string
				f.transport.handler = func(request *http.Request, _ []byte) (*http.Response, error) {
					if kind == "callback" {
						if request.Method == "GET" {
							return wireResponse("data: {\"jsonrpc\":\"2.0\",\"id\":\"egress-parent-ping\",\"method\":\"ping\"}\n\n", true), nil
						}
						return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
					}
					if request.Method == "POST" {
						return wireResponse("id: egress-progress\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"p\",\"progress\":1}}\n\n", true), nil
					}
					if request.Header.Get("Last-Event-Id") != "egress-progress" {
						return nil, fmt.Errorf("unexpected recovery cursor")
					}
					return wireResponse("id: egress-terminal\ndata: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[]}}\n\n", true), nil
				}
				if kind == "callback" {
					parent = f.captured(t, "GET", "", "synthetic-session")
					if terminal := f.executor.Serve(t.Context(), parent, httptest.NewRecorder()); terminal.Outcome != OutcomeCompleted {
						t.Fatal(terminal)
					}
					childBody = `{"jsonrpc":"2.0","id":"egress-parent-ping","result":{}}`
				} else {
					parent = f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"p"}}}`, "synthetic-session")
					parentDone := make(chan Terminal, 1)
					go func() { parentDone <- f.executor.Serve(t.Context(), parent, httptest.NewRecorder()) }()
					f.approve(t, parent.id)
					select {
					case <-parentDone:
					case <-time.After(2 * time.Second):
						t.Fatal("parent did not detach")
					}
				}
				beforeReads, beforeSends := counted.calls.Load(), f.transport.sends()
				if _, err := defaults.SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: setting.Revision}); err != nil {
					t.Fatal(err)
				}
				counted.unavailable.Store(unavailable)
				method := "GET"
				if kind == "callback" {
					method = "POST"
				}
				child := f.captured(t, method, childBody, "synthetic-session")
				if kind == "recovery" {
					child.request.Header.Set("Last-Event-Id", "egress-progress")
				}
				childDone := make(chan Terminal, 1)
				go func() { childDone <- f.executor.Serve(t.Context(), child, httptest.NewRecorder()) }()
				if kind == "callback" {
					f.approve(t, child.id)
				}
				select {
				case terminal := <-childDone:
					if terminal.Outcome != OutcomeCompleted {
						t.Fatalf("child lost real old parent exit: %+v", terminal)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("child did not drain")
				}
				if counted.calls.Load() != beforeReads || f.transport.sends() != beforeSends+1 {
					t.Fatalf("child re-read default or duplicated send: reads=%d/%d sends=%d/%d", counted.calls.Load(), beforeReads, f.transport.sends(), beforeSends)
				}
				var source string
				var revision uint64
				if err := f.db.QueryRow(`SELECT selection_source,default_revision FROM native_tool_deliveries WHERE operation_id=?`, child.id).Scan(&source, &revision); err != nil {
					t.Fatal(err)
				}
				if source != "global" || revision != setting.Revision {
					t.Fatalf("child audit did not retain original selection: %s/%d", source, revision)
				}
				if kind == "callback" {
					var actualParent string
					if err := f.db.QueryRow(`SELECT parent_operation_id FROM native_tool_bindings WHERE operation_id=?`, child.id).Scan(&actualParent); err != nil || actualParent != parent.id.String() {
						t.Fatal("callback parent identity changed", err)
					}
					var approvals int
					if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_approvals WHERE operation_id=? AND consumed_at_unix_ms IS NOT NULL`, child.id).Scan(&approvals); err != nil || approvals != 1 {
						t.Fatal("real Allow was not consumed exactly once", err)
					}
				} else {
					operation, err := f.store.NativeToolRepository().Get(t.Context(), parent.id)
					if err != nil || operation.State != "completed" {
						t.Fatal("recovery did not preserve separate logical completion", err)
					}
				}
			})
		}
	}
}

func TestNativeIndependentOperationsFreezeActualDefaultWithoutResolvingIngressLease(t *testing.T) {
	f := newNativeFixtureWithChoice(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, 0, egressprofile.Inherit())
	profiles, err := egressprofile.NewManager(f.store.EgressProfileRepository(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := egressprofile.NewDefaultManager(f.store.EgressDefaultRepository(), profiles)
	if err != nil {
		t.Fatal(err)
	}
	setting, err := defaults.SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: egressprofile.DirectID, Revision: 1}})
	if err != nil {
		t.Fatal(err)
	}
	counted := &countedNativeDefaults{resolver: defaults}
	ex := f.executor.(*executor)
	ex.dependencies.EgressDefaults = counted
	// The genuine ingress lease is authored. Classification is still pending.
	input := f.captured(t, "POST", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, "")
	native, _ := input.lease.Plan().ClientToolPlan()
	if _, err := native.ResolvedEgress(); err == nil || counted.calls.Load() != 0 {
		input.lease.Release()
		t.Fatal("ingress resolved before native classification")
	}
	input.lease.Release()
	f.handshake(t)
	if counted.calls.Load() != 3 || f.transport.sends() != 3 {
		t.Fatalf("handshake did not freeze each actual independent operation once: resolves=%d sends=%d", counted.calls.Load(), f.transport.sends())
	}
	f.transport.handler = func(request *http.Request, body []byte) (*http.Response, error) {
		if request.Method != "POST" || string(body) != `{"jsonrpc":"2.0","id":13,"method":"ping"}` {
			return nil, fmt.Errorf("unexpected frozen-egress request")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":13,"result":{}}`))}, nil
	}
	setting, err = defaults.SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: setting.Revision})
	if err != nil {
		t.Fatal(err)
	}
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":13,"method":"ping"}`, "synthetic-session")
	if terminal.Outcome != OutcomeCompleted {
		t.Fatalf("new independent ping failed: %+v", terminal)
	}
	var source, proxy string
	var revision uint64
	if err := f.db.QueryRow(`SELECT selection_source,default_revision,proxy_id FROM native_tool_deliveries WHERE operation_id=?`, terminal.OperationID).Scan(&source, &revision, &proxy); err != nil {
		t.Fatal(err)
	}
	if source != "builtin" || revision != setting.Revision || proxy != "profile.direct" || counted.calls.Load() != 4 {
		t.Fatalf("new independent op reused session exit: source=%s revision=%d resolves=%d", source, revision, counted.calls.Load())
	}
	counted.unavailable.Store(true)
	before := f.transport.sends()
	failed, _ := f.serve(t, `{"jsonrpc":"2.0","id":14,"method":"ping"}`, "synthetic-session")
	if failed.Outcome != OutcomeRejected || f.transport.sends() != before || counted.calls.Load() != 5 {
		t.Fatal("unavailable default sent or reused prior session selection")
	}
}
