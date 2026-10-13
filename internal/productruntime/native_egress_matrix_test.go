package productruntime

import (
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func assertNativeCounts(t *testing.T, f *runtimeNativeEgressFixture, want runtimeNativeCounts) {
	t.Helper()
	got := f.counts()
	if got != want {
		t.Fatalf("native physical counts got=%+v want=%+v", got, want)
	}
	if got.Unexpected != 0 || got.Credentials != 0 {
		t.Fatalf("native containment counters unexpected=%d credentials=%d", got.Unexpected, got.Credentials)
	}
	f.assertDrained(t)
}

// Count methods from full requests actually read by the private endpoint.
// In particular, a recovery cannot hide a second tools/call POST in HTTP totals.
func assertNativeMethods(t *testing.T, f *runtimeNativeEgressFixture, tools, replies, gets, wantEntries int) {
	t.Helper()
	f.mu.Lock()
	requests := append([]runtimeNativeRequest(nil), f.endpointRequests...)
	entries := 0
	for _, observed := range f.observedEntries {
		entries += len(observed)
	}
	f.mu.Unlock()
	var gotTools, gotReplies, gotGETs int
	for _, request := range requests {
		switch {
		case request.Method == "GET":
			gotGETs++
		case request.Method == "POST" && strings.Contains(request.Body, `"method":"tools/call"`):
			gotTools++
		case request.Method == "POST" && strings.Contains(request.Body, `"id":"native-parent-ping"`):
			gotReplies++
		}
	}
	if gotTools != tools || gotReplies != replies || gotGETs != gets || len(requests) != 3+tools+replies+gets {
		t.Fatalf("native endpoint methods tools/replies/GET=%d/%d/%d want=%d/%d/%d requests=%+v", gotTools, gotReplies, gotGETs, tools, replies, gets, requests)
	}
	// Failed proxy dials have a transport entry but no endpoint request.
	if entries != wantEntries {
		t.Fatalf("native entries=%d want=%d endpoint=%d", entries, wantEntries, len(requests))
	}
}

func assertNativeRecovery(t *testing.T, f *runtimeNativeEgressFixture, child, parent nativetool.ID, want runtimeNativeExpected) {
	t.Helper()
	f.mu.Lock()
	entries := append([]runtimeNativeRequest(nil), f.observedEntries[child]...)
	f.mu.Unlock()
	if !reflect.DeepEqual(entries, []runtimeNativeRequest{{"GET", "native-progress", ""}}) {
		t.Fatalf("recovery actual transport cursor/method=%+v", entries)
	}
	// BindRecoveryGET owns a private StreamAuthority association, not a SQL
	// parent column. Compare the real durable frozen facts it validated against
	// the initiating operation; no authority is constructed from this query.
	var method, profileID, source string
	var revision, defaultRevision uint64
	var exact int
	err := f.db.QueryRow(`SELECT o.method,json_extract(e.profile_json,'$.id'),json_extract(e.profile_json,'$.revision'),e.selection_source,e.default_revision,
 b.session_id=p.session_id AND b.route_id=p.route_id AND b.route_id='native.egress.route' AND b.route_revision=p.route_revision AND b.owner_epoch=p.owner_epoch AND b.assignment_revision=p.assignment_revision AND e.profile_json=pe.profile_json AND e.selection_source=pe.selection_source AND e.default_revision=pe.default_revision
 FROM native_tool_operations o JOIN native_tool_bindings b ON b.operation_id=o.operation_id JOIN native_tool_binding_egress e ON e.operation_id=b.operation_id JOIN native_tool_bindings p ON p.operation_id=? JOIN native_tool_binding_egress pe ON pe.operation_id=p.operation_id WHERE o.operation_id=?`, parent.String(), child.String()).Scan(&method, &profileID, &revision, &source, &defaultRevision, &exact)
	if err != nil || method != "GET" || exact != 1 || profileID != want.ProfileID || revision != want.ProfileRevision || source != string(want.Source) || defaultRevision != want.DefaultRevision {
		t.Fatalf("recovery Store/binding association parent=%s child=%s method=%s exact=%d profile=%s/%d/%s/%d err=%v", parent, child, method, exact, profileID, revision, source, defaultRevision, err)
	}
}

func assertNativeOriginal(t *testing.T, f *runtimeNativeEgressFixture, id nativetool.ID, audit egressaudit.Record, state string) {
	t.Helper()
	op, err := f.r.NativeToolRepository().Get(t.Context(), id)
	if err != nil || op.State != state {
		t.Fatalf("initiating logical operation=%+v want=%s err=%v", op, state, err)
	}
	after, err := f.audit(id)
	if err != nil || !reflect.DeepEqual(audit, after) {
		t.Fatalf("original physical audit changed before=%+v after=%+v err=%v", audit, after, err)
	}
}

// TestRuntimeNativeEgressSelectionAndLinkedChildren exercises the three
// authored native choices with separate callback and recovery parents. The
// expected tuple is assigned before each request is launched; linked requests
// are then checked against the parent tuple after a real default edit.
func TestRuntimeNativeEgressSelectionAndLinkedChildren(t *testing.T) {
	cases := []struct {
		name   string
		choice string
		want   runtimeNativeExpected
	}{
		{"flow_proxy", "flow_proxy", runtimeNativeExpected{"profile.native-a", 1, egressprofile.SelectionFlow, 0}},
		{"flow_direct", "flow_direct", runtimeNativeExpected{"profile.direct", 1, egressprofile.SelectionFlow, 0}},
		{"builtin", "builtin", runtimeNativeExpected{"profile.direct", 1, egressprofile.SelectionBuiltin, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRuntimeNativeEgressFixture(t, runtimeNativeEgressConfig{choice: tc.choice})
			f.mode = "matrix"
			var baseline, callbackWant, toolWant, replyWant, recoveryWant, independentWantCounts runtimeNativeCounts
			switch tc.choice {
			case "flow_proxy":
				baseline = runtimeNativeCounts{AAttempts: 3, AConnects: 3, TLS: 3, HTTP: 3}
				callbackWant = runtimeNativeCounts{AAttempts: 4, AConnects: 4, TLS: 4, HTTP: 4}
				toolWant = runtimeNativeCounts{AAttempts: 5, AConnects: 5, TLS: 5, HTTP: 5}
				replyWant = runtimeNativeCounts{AAttempts: 7, AConnects: 7, TLS: 7, HTTP: 6}
				recoveryWant = runtimeNativeCounts{AAttempts: 8, AConnects: 8, TLS: 8, HTTP: 7}
				independentWantCounts = runtimeNativeCounts{AAttempts: 9, AConnects: 9, TLS: 9, HTTP: 8}
			case "flow_direct":
				baseline = runtimeNativeCounts{DirectAttempts: 3, DirectConnections: 3, TLS: 3, HTTP: 3}
				callbackWant = runtimeNativeCounts{DirectAttempts: 4, DirectConnections: 4, TLS: 4, HTTP: 4}
				toolWant = runtimeNativeCounts{DirectAttempts: 5, DirectConnections: 5, TLS: 5, HTTP: 5}
				replyWant = runtimeNativeCounts{DirectAttempts: 6, DirectConnections: 6, TLS: 6, HTTP: 6}
				recoveryWant = runtimeNativeCounts{DirectAttempts: 7, DirectConnections: 7, TLS: 7, HTTP: 7}
				independentWantCounts = runtimeNativeCounts{DirectAttempts: 8, DirectConnections: 8, TLS: 8, HTTP: 8}
			case "builtin":
				baseline = runtimeNativeCounts{DirectAttempts: 3, DirectConnections: 3, TLS: 3, HTTP: 3}
				callbackWant = runtimeNativeCounts{DirectAttempts: 4, DirectConnections: 4, TLS: 4, HTTP: 4}
				toolWant = runtimeNativeCounts{DirectAttempts: 5, DirectConnections: 5, TLS: 5, HTTP: 5}
				replyWant = runtimeNativeCounts{DirectAttempts: 6, DirectConnections: 6, TLS: 6, HTTP: 6}
				recoveryWant = runtimeNativeCounts{DirectAttempts: 8, DirectConnections: 8, TLS: 8, HTTP: 7}
				independentWantCounts = runtimeNativeCounts{DirectAttempts: 8, DirectConnections: 8, BAttempts: 1, BConnects: 1, TLS: 9, HTTP: 8}
			}
			f.handshake(t)
			assertNativeCounts(t, f, baseline)
			assertNativeMethods(t, f, 0, 0, 0, 3)

			callback := f.beginPhase(t, "GET", "", "", tc.want, &baseline)
			if got := f.join(t, callback); got.Outcome != nativemcp.OutcomeCompleted {
				t.Fatalf("callback parent: %+v", got)
			}
			assertNativeCounts(t, f, callbackWant)
			assertNativeMethods(t, f, 0, 0, 1, 4)
			f.assertAudit(t, callback.ID, tc.want, "completed", "")

			toolBody := `{"jsonrpc":"2.0","id":44,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"native-token"}}}`
			tool := f.beginPhase(t, "POST", toolBody, "", tc.want, &callbackWant)
			f.allow(t, tool.ID)
			if got := f.join(t, tool); got.Outcome != nativemcp.OutcomeFailed || got.Reason != nativemcp.ReasonStreamDetached {
				t.Fatalf("tool parent did not detach: %+v", got)
			}
			f.assertAudit(t, tool.ID, tc.want, "failed", "transport_failed")
			assertNativeCounts(t, f, toolWant)
			assertNativeMethods(t, f, 1, 0, 1, 5)
			originalAudit, err := f.audit(tool.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertNativeOriginal(t, f, tool.ID, originalAudit, "started")

			// The linked branches are created only after both real parents have
			// started. Explicit flow rows perform both writes; builtin performs
			// its first real default write here.
			if tc.choice == "builtin" {
				if got := f.setDefault(t, &f.profileB); got != 2 {
					t.Fatalf("builtin default revision=%d", got)
				}
			} else {
				if got := f.setDefault(t, &f.profileA); got != 3 {
					t.Fatalf("first edited default revision=%d", got)
				}
				if got := f.setDefault(t, &f.profileB); got != 4 {
					t.Fatalf("second edited default revision=%d", got)
				}
			}

			holdCallback := tc.choice == "flow_proxy"
			if holdCallback {
				if _, err := f.r.EnterOfflineHold(t.Context(), f.r.OfflineHoldSnapshot().Revision); err != nil {
					t.Fatal(err)
				}
			}
			replyEntry := toolWant
			if holdCallback {
				replyEntry = runtimeNativeCounts{AAttempts: 6, AConnects: 6, TLS: 6, HTTP: 5}
			}
			callbackReply := f.beginPhase(t, "POST", `{"jsonrpc":"2.0","id":"native-parent-ping","result":{}}`, "", tc.want, &replyEntry)
			f.allow(t, callbackReply.ID)
			if holdCallback {
				target := f.waitQueued(t)
				if target.EgressPolicy.Proxy.Endpoint != f.network.a {
					t.Fatalf("flow callback probe changed frozen A policy: %+v", target.EgressPolicy)
				}
				f.assertNoStart(t, callbackReply.ID)
				if got := f.counts(); got != toolWant {
					t.Fatalf("queued callback changed physical counters=%+v want=%+v", got, toolWant)
				}
				if _, err := f.r.ResumeOfflineHold(t.Context(), f.r.OfflineHoldSnapshot().Revision); err != nil {
					t.Fatal(err)
				}
			}
			if got := f.join(t, callbackReply); got.Outcome != nativemcp.OutcomeCompleted {
				parentOp, _ := f.r.NativeToolRepository().Get(t.Context(), callback.ID)
				replyOp, _ := f.r.NativeToolRepository().Get(t.Context(), callbackReply.ID)
				t.Logf("callback parent=%s tool=%s reply=%s", callback.ID, tool.ID, callbackReply.ID)
				t.Logf("callback ops parent=%+v reply=%+v", parentOp, replyOp)
				t.Fatalf("callback reply unavailable after detached tool: %+v", got)
			}
			f.assertAudit(t, callbackReply.ID, tc.want, "completed", "")
			assertNativeCounts(t, f, replyWant)
			assertNativeMethods(t, f, 1, 1, 1, 6)
			var parent string
			if err := f.db.QueryRow(`SELECT parent_operation_id FROM native_tool_bindings WHERE operation_id=?`, callbackReply.ID.String()).Scan(&parent); err != nil {
				t.Fatal(err)
			}
			if parent != callback.ID.String() {
				t.Fatalf("callback parent=%s want=%s", parent, callback.ID)
			}

			holdRecovery := tc.choice == "builtin"
			if holdRecovery {
				if _, err := f.r.EnterOfflineHold(t.Context(), f.r.OfflineHoldSnapshot().Revision); err != nil {
					t.Fatal(err)
				}
			}
			recoveryEntry := replyWant
			if holdRecovery {
				recoveryEntry = runtimeNativeCounts{DirectAttempts: 7, DirectConnections: 7, TLS: 7, HTTP: 6}
			}
			recovery := f.beginPhase(t, "GET", "", "native-progress", tc.want, &recoveryEntry)
			if holdRecovery {
				target := f.waitQueued(t)
				if target.EgressPolicy.Proxy.Kind != egressnetwork.ProxyDirect || target.EgressPolicy.Proxy.Endpoint != "" {
					t.Fatalf("builtin recovery probe changed Direct policy: %+v", target.EgressPolicy)
				}
				f.assertNoStart(t, recovery.ID)
				if got := f.counts(); got != replyWant {
					t.Fatalf("queued recovery changed physical counters=%+v want=%+v", got, replyWant)
				}
				if _, err := f.r.ResumeOfflineHold(t.Context(), f.r.OfflineHoldSnapshot().Revision); err != nil {
					t.Fatal(err)
				}
			}
			if got := f.join(t, recovery); got.Outcome != nativemcp.OutcomeCompleted {
				t.Fatalf("recovery child: %+v", got)
			}
			f.assertAudit(t, recovery.ID, tc.want, "completed", "")
			assertNativeCounts(t, f, recoveryWant)
			assertNativeMethods(t, f, 1, 1, 2, 7)
			assertNativeRecovery(t, f, recovery.ID, tool.ID, tc.want)
			assertNativeOriginal(t, f, tool.ID, originalAudit, "completed")
			if body := recovery.Response.Body.String(); !strings.Contains(body, "id: native-terminal\n") || !strings.Contains(body, `"id":44`) || !strings.Contains(body, `"result"`) {
				t.Fatalf("recovery exact original RPC terminal=%q", body)
			}

			// A fresh independent call uses the authored flow, except for the
			// genuinely inherited row which selects the newly edited B default.
			independentWant := tc.want
			if tc.choice == "builtin" {
				independentWant = runtimeNativeExpected{"profile.native-b", 1, egressprofile.SelectionGlobal, 2}
			}
			next := f.beginPhase(t, "POST", `{"jsonrpc":"2.0","id":45,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"b"}}}`, "", independentWant, &recoveryWant)
			f.allow(t, next.ID)
			if got := f.join(t, next); got.Outcome != nativemcp.OutcomeCompleted {
				t.Fatalf("independent tool: %+v", got)
			}
			f.assertAudit(t, next.ID, independentWant, "completed", "")
			assertNativeCounts(t, f, independentWantCounts)
			assertNativeMethods(t, f, 2, 1, 2, 8)
		})
	}
}

// TestRuntimeNativeEgressConsumedFailureEffects keeps the failure accounting
// at the actual transport-entry barrier. A closed selected proxy proves there
// is no alternate POST, while the accepted-then-dropped case records one
// request and leaves the operation behind the replay barrier.
func TestRuntimeNativeEgressConsumedFailureEffects(t *testing.T) {
	cases := []string{"tool_proxy_down", "reply_proxy_down", "recovery_proxy_down", "tool_accepted_then_dropped"}
	for _, edge := range cases {
		t.Run(edge, func(t *testing.T) {
			f := newRuntimeNativeEgressFixture(t, runtimeNativeEgressConfig{choice: "global", edge: edge})
			t.Cleanup(f.releaseAccepted)
			f.mode = "matrix"
			baseline := runtimeNativeCounts{AAttempts: 3, AConnects: 3, TLS: 3, HTTP: 3}
			parentWant := runtimeNativeCounts{AAttempts: 4, AConnects: 4, TLS: 4, HTTP: 4}
			entryWant := baseline
			var terminalWant runtimeNativeCounts
			wantTools, wantGETs, wantEntries := 0, 0, 4
			switch edge {
			case "tool_proxy_down":
				terminalWant = runtimeNativeCounts{AAttempts: 4, AConnects: 3, TLS: 3, HTTP: 3}
			case "reply_proxy_down":
				entryWant = parentWant
				terminalWant = runtimeNativeCounts{AAttempts: 5, AConnects: 4, TLS: 4, HTTP: 4}
				wantGETs, wantEntries = 1, 5
			case "recovery_proxy_down":
				entryWant = parentWant
				terminalWant = runtimeNativeCounts{AAttempts: 5, AConnects: 4, TLS: 4, HTTP: 4}
				wantTools, wantEntries = 1, 5
			case "tool_accepted_then_dropped":
				terminalWant = runtimeNativeCounts{AAttempts: 4, AConnects: 4, TLS: 4, HTTP: 4}
				wantTools = 1
			}
			f.handshake(t)
			assertNativeCounts(t, f, baseline)
			assertNativeMethods(t, f, 0, 0, 0, 3)
			want := runtimeNativeExpected{"profile.native-a", 1, egressprofile.SelectionGlobal, 2}
			var call runtimeNativeCall
			var parentID nativetool.ID
			var parentAudit egressaudit.Record
			var wantReason string
			switch edge {
			case "tool_proxy_down", "tool_accepted_then_dropped":
				body := `{"jsonrpc":"2.0","id":44,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"native-token"}}}`
				call = f.beginPhase(t, "POST", body, "", want, &entryWant)
				f.allow(t, call.ID)
				wantReason = "execution_uncertain"
			case "reply_proxy_down":
				parent := f.beginPhase(t, "GET", "", "", want, &baseline)
				parentID = parent.ID
				if got := f.join(t, parent); got.Outcome != nativemcp.OutcomeCompleted {
					t.Fatalf("callback parent: %+v", got)
				}
				f.assertAudit(t, parent.ID, want, "completed", "")
				assertNativeCounts(t, f, parentWant)
				assertNativeMethods(t, f, 0, 0, 1, 4)
				call = f.beginPhase(t, "POST", `{"jsonrpc":"2.0","id":"native-parent-ping","result":{}}`, "", want, &entryWant)
				f.allow(t, call.ID)
				wantReason = "execution_uncertain"
			case "recovery_proxy_down":
				parent := f.beginPhase(t, "POST", `{"jsonrpc":"2.0","id":44,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"native-token"}}}`, "", want, &baseline)
				parentID = parent.ID
				f.allow(t, parent.ID)
				if got := f.join(t, parent); got.Outcome != nativemcp.OutcomeFailed || got.Reason != nativemcp.ReasonStreamDetached {
					t.Fatalf("tool parent: %+v", got)
				}
				f.assertAudit(t, parent.ID, want, "failed", "transport_failed")
				var err error
				parentAudit, err = f.audit(parent.ID)
				if err != nil {
					t.Fatal(err)
				}
				assertNativeOriginal(t, f, parentID, parentAudit, "started")
				assertNativeCounts(t, f, parentWant)
				assertNativeMethods(t, f, 1, 0, 0, 4)
				call = f.beginPhase(t, "GET", "", "native-progress", want, &entryWant)
				wantReason = "transport_failed"
			}
			// Closing A after the real Start/transport barrier leaves the
			// executor's conservative post-entry classification intact.
			f.waitStarted(t, call.ID)
			if edge == "tool_accepted_then_dropped" {
				f.waitAcceptedBody(t, `{"jsonrpc":"2.0","id":44,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"native-token"}}}`)
				if got := f.counts(); got != terminalWant {
					t.Fatalf("accepted body barrier counters=%+v want=%+v", got, terminalWant)
				}
			} else if got := f.counts(); got != entryWant {
				t.Fatalf("held transport-entry counters=%+v want=%+v", got, entryWant)
			}
			if got := f.setDefault(t, &f.profileB); got != 3 {
				t.Fatalf("post-Start default revision=%d", got)
			}
			if edge != "tool_accepted_then_dropped" {
				f.proxyA.drop()
			}
			if edge == "tool_proxy_down" || edge == "reply_proxy_down" || edge == "recovery_proxy_down" {
				f.releaseEntry(t, call.ID)
			}
			if edge == "tool_accepted_then_dropped" {
				f.releaseAccepted()
			}
			terminal := f.join(t, call)
			if edge == "recovery_proxy_down" {
				if terminal.Outcome != nativemcp.OutcomeFailed || terminal.Reason != nativemcp.ReasonStreamDetached {
					t.Fatalf("recovery failure terminal=%+v", terminal)
				}
			} else if terminal.Outcome != nativemcp.OutcomeUncertain || terminal.Reason != nativemcp.ReasonExecutionUncertain {
				t.Fatalf("failure terminal=%+v", terminal)
			}
			f.assertAudit(t, call.ID, want, "failed", wantReason)
			if edge == "reply_proxy_down" {
				var actualParent string
				if err := f.db.QueryRow(`SELECT parent_operation_id FROM native_tool_bindings WHERE operation_id=?`, call.ID.String()).Scan(&actualParent); err != nil || actualParent != parentID.String() {
					t.Fatalf("failed callback parent binding=%q want=%s err=%v", actualParent, parentID, err)
				}
			}
			if edge == "recovery_proxy_down" {
				assertNativeRecovery(t, f, call.ID, parentID, want)
				assertNativeOriginal(t, f, parentID, parentAudit, "started")
			}
			assertNativeCounts(t, f, terminalWant)
			assertNativeMethods(t, f, wantTools, 0, wantGETs, wantEntries)
			f.assertReplayAndFreshStart(t, call)
			if edge == "recovery_proxy_down" {
				assertNativeOriginal(t, f, parentID, parentAudit, "started")
			}
			assertNativeCounts(t, f, terminalWant)
			assertNativeMethods(t, f, wantTools, 0, wantGETs, wantEntries)
		})
	}
}
