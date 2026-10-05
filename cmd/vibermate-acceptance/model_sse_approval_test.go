//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

type modelApprovalWatch struct {
	done              chan struct{}
	pending, terminal toolapproval.View
	capture           captureidentity.Reference
	hold              time.Duration
	failure           string
}

func startModelApprovalWatch(t *testing.T, ctx context.Context, cancelParent context.CancelFunc, setup nativeModelProbeSetup, trace *modelIdleTrace, client *modelIdleClientObservation, decision string) *modelApprovalWatch {
	t.Helper()
	watch := &modelApprovalWatch{done: make(chan struct{})}
	go func() {
		complete := false
		defer func() {
			if !complete {
				if watch.failure == "" {
					watch.failure = "watcher exited before durable resolution"
				}
				cancelParent()
			}
			close(watch.done)
		}()
		fail := func(reason string) { watch.failure = reason; t.Error(reason) }
		select {
		case <-trace.terminal:
		case <-ctx.Done():
			fail("provider terminal was not observed before watcher cancellation")
			return
		}
		management := setup.Management
		deadline := time.Now().Add(5 * time.Second)
		for watch.pending.ID == "" {
			status, body := management.request(ctx, "GET", "/api/v1/approvals?state=pending&limit=10", management.session.ReadToken, nil, nil)
			var page toolapproval.Page
			if status != 200 || json.Unmarshal(body, &page) != nil {
				fail("authenticated MODEL pending page unavailable")
				return
			}
			if len(page.Items) > 1 {
				fail("unexpected multiple MODEL pending questions")
				return
			}
			if len(page.Items) == 1 {
				watch.pending = page.Items[0]
				break
			}
			if time.Now().After(deadline) {
				fail("actual MODEL pending question not created")
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		pending := watch.pending
		if pending.Kind != "tool_intent" || pending.State != toolapproval.StatePending || len(pending.SubjectRefs) != 1 || pending.SubjectRefs[0] != "plan_call" || len(pending.SubjectLabels) != 1 || pending.SubjectLabels[0] != "update_plan" || pending.ExchangeID == "" || pending.EnvironmentID != setup.Environment.ID().String() || pending.EnvironmentRevision != setup.Environment.Revision() || pending.EnvironmentDigest != setup.Environment.Digest().String() || pending.RouteID != "native.synthetic.route" || pending.RouteRevision != 1 || pending.RequestCount != 1 {
			fail("MODEL question identity/frozen policy differs")
			return
		}
		fixedAccount := false
		for _, endpoint := range setup.Environment.Aggregate().ClientEndpoints {
			for _, plan := range endpoint.ProtocolPlans {
				if plan.Destination.Upstream == nil {
					continue
				}
				for _, route := range plan.Destination.Upstream.Routes {
					if route.ID.String() == pending.RouteID && route.Revision == pending.RouteRevision {
						policy := route.AccountPolicy
						if policy.Mode != environment.AccountSelectionFixed || policy.FixedAccountID != setup.AccountID || len(policy.Accounts) != 1 || policy.Accounts[0].ID != setup.AccountID || policy.Accounts[0].Revision != 1 {
							fail("published pending route fixed account differs")
							return
						}
						fixedAccount = true
					}
				}
			}
		}
		trace.mu.Lock()
		providerProved := trace.requests == 1 && trace.failure == "" && !trace.terminalAt.IsZero()
		trace.mu.Unlock()
		if !fixedAccount || !providerProved {
			fail("pending fixed route or genuine managed-provider terminal missing")
			return
		}
		// Link the actual pending Exchange to its launch and selected account;
		// no manufactured question or aggregate count is accepted as authority.
		page, err := setup.Runtime.Activities().ListExchanges(ctx, activity.PageRequest{Limit: 10})
		if err != nil {
			fail("pending Activity unavailable")
			return
		}
		matched := false
		for _, record := range page.Items {
			if record.SubjectID == pending.ExchangeID {
				if record.Kind != activity.KindExchangeStarted || record.Status != activity.StatusPending || record.CaptureRunID == "" || record.EnvironmentID != pending.EnvironmentID || record.EnvironmentRevision != uint64(pending.EnvironmentRevision) || record.EnvironmentDigest != pending.EnvironmentDigest || record.AccountID != "" || record.AccountRevision != 0 || record.RouteID != pending.RouteID || record.RouteRevision != uint64(pending.RouteRevision) {
					t.Logf("pending Activity kind=%s capturePresent=%t environmentMatch=%t revisionMatch=%t digestMatch=%t accountPresent=%t accountMatch=%t", record.Kind, record.CaptureRunID != "", record.EnvironmentID == pending.EnvironmentID, record.EnvironmentRevision == uint64(pending.EnvironmentRevision), record.EnvironmentDigest == pending.EnvironmentDigest, record.AccountID != "", record.AccountID == setup.AccountID)
					fail("pending Activity/Capture/account mismatch")
					return
				}
				watch.capture = captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: record.CaptureRunID}
				assignment, err := setup.Runtime.CaptureAssignments().Resolve(ctx, watch.capture)
				if err != nil || assignment.EnvironmentDigest.String() != pending.EnvironmentDigest || assignment.EnvironmentRevision != pending.EnvironmentRevision {
					fail("pending frozen launch assignment mismatch")
					return
				}
				matched = true
			}
		}
		if !matched {
			fail("pending Exchange has no actual Activity")
			return
		}
		read := func() (toolapproval.View, bool) {
			status, body := management.request(ctx, "GET", "/api/v1/approvals/"+pending.ID, management.session.ReadToken, nil, nil)
			var view toolapproval.View
			if status != 200 || json.Unmarshal(body, &view) != nil || view.ID != pending.ID || view.ExchangeID != pending.ExchangeID {
				fail("exact MODEL question read failed")
				return view, false
			}
			return view, true
		}
		holdStart := time.Now()
		until := holdStart.Add(17 * time.Second)
		for {
			view, ok := read()
			if !ok {
				return
			}
			if !view.ExpiresAt.Equal(pending.ExpiresAt) {
				fail("heartbeat changed actual approval expiry")
				return
			}
			if decision == "expire" && view.State == toolapproval.StateExpired {
				watch.terminal = view
				break
			}
			if view.State != toolapproval.StatePending || view.Revision != pending.Revision {
				fail("MODEL question resolved before the chosen decision")
				return
			}
			client.mu.Lock()
			plans, idle := client.plans, client.idle
			client.mu.Unlock()
			trace.mu.Lock()
			acks := trace.acks
			trace.mu.Unlock()
			if plans != 0 || acks != 0 || idle {
				fail("held MODEL review exposed a tool or lost client liveness")
				return
			}
			if decision != "expire" && !time.Now().Before(until) {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		watch.hold = time.Since(holdStart)
		if decision == "expire" {
			if watch.terminal.TerminalReason != "approval_expired" || watch.terminal.ResolvedAt == nil || watch.terminal.ResolvedAt.Sub(pending.CreatedAt) < 25*time.Second {
				fail("actual bounded expiry not proved")
				return
			}
		} else {
			input := desktopcontrol.ApprovalDecisionInput{Decision: toolapproval.DecisionAllowOnce, Scope: toolapproval.ScopeRequest}
			want := toolapproval.StateAllowed
			if decision == "deny" {
				input.Decision = toolapproval.DecisionDeny
				input.ReasonCode = "synthetic_denied"
				want = toolapproval.StateDenied
			}
			status, body := management.request(ctx, "POST", "/api/v1/approvals/"+pending.ID+"/actions/decide", management.session.WriteToken, input, map[string]string{"If-Match": strconv.FormatUint(pending.Revision, 10), "Idempotency-Key": "model-sse-" + decision + "-decision-0001"})
			if status != http.StatusOK || json.Unmarshal(body, &watch.terminal) != nil {
				fail(fmt.Sprintf("authenticated MODEL decision status=%d", status))
				return
			}
			view, ok := read()
			if !ok {
				return
			}
			watch.terminal = view
			if view.State != want || view.Decision != input.Decision || view.DecisionScope != toolapproval.ScopeRequest || view.Revision <= pending.Revision || view.ResolvedAt == nil || !view.ExpiresAt.Equal(pending.ExpiresAt) {
				fail("MODEL decision not durably resolved with unchanged expiry")
				return
			}
		}
		complete = true
	}()
	return watch
}

func TestModelSSERealReviewAllow(t *testing.T)  { runModelSSEReview(t, "allow") }
func TestModelSSERealReviewDeny(t *testing.T)   { runModelSSEReview(t, "deny") }
func TestModelSSERealReviewExpiry(t *testing.T) { runModelSSEReview(t, "expire") }

func runModelSSEReview(t *testing.T, decision string) {
	parent, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary, node := modelIdleVerifiedClient(t, parent, t.TempDir())
	trace := &modelIdleTrace{terminal: make(chan struct{})}
	client := &modelIdleClientObservation{}
	var watch *modelApprovalWatch
	expected := 1
	if decision == "allow" {
		expected = 2
	}
	options := &nativeModelProbeOptions{Review: true, ExpireReview: decision == "expire", Handler: modelIdleProvider(trace, "managed_arguments"), ThreadParameters: func(dir string) map[string]any { return modelIdleThreadParams(dir, 20000) }, ClientObservation: client, ExpectedModelRequests: expected,
		BeforeTurn: func(ctx context.Context, setup nativeModelProbeSetup) (func(), error) {
			watch = startModelApprovalWatch(t, ctx, cancel, setup, trace, client, decision)
			return func() {
				cancel()
				select {
				case <-watch.done:
				case <-time.After(5 * time.Second):
					t.Error("MODEL approval watcher did not drain")
				}
			}, nil
		},
		ObserveTerminal: func(ctx context.Context, actual nativeModelProbeObservation) {
			select {
			case <-watch.done:
			case <-time.After(5 * time.Second):
				t.Fatal("MODEL decision observer still running")
			}
			if watch.failure != "" {
				t.Fatal(watch.failure)
			}
			evidence := collectModelLifecycleEvidence(t, ctx, actual)
			client.mu.Lock()
			plans, idle := client.plans, client.idle
			client.mu.Unlock()
			trace.mu.Lock()
			requests, acks, failure := trace.requests, trace.acks, trace.failure
			trace.mu.Unlock()
			t.Logf("real MODEL Review decision=%s heldMS=%d state=%s revisionAdvanced=%t unchangedExpiry=%t complete=%t clientIdle=%t requests=%d plans=%d ack=%d generatedDATA=%d toolBatches=%d", decision, watch.hold.Milliseconds(), watch.terminal.State, watch.terminal.Revision > watch.pending.Revision, watch.terminal.ExpiresAt.Equal(watch.pending.ExpiresAt), actual.Completed, idle, requests, plans, acks, evidence.generatedData, evidence.toolBatches)
			if watch.capture != actual.Capture || idle || requests != expected || evidence.generatedData < 1 || evidence.comments != 0 || failure != "" {
				t.Fatal("real review lifecycle evidence differs")
			}
			if decision == "allow" {
				if !actual.Completed || plans != 1 || acks != 1 || evidence.toolBatches != 1 || !evidence.hasCompleted {
					t.Error("real Allow did not release one successfully acknowledged tool")
				}
			} else {
				if actual.Completed || plans != 0 || acks != 0 || evidence.hasTool || evidence.hasCompleted {
					t.Error("Deny/expiry released a tool or success")
				}
				want := "tool_decision_rejected"
				if decision == "expire" {
					want = "tool_decision_expired"
				}
				if len(evidence.records) != 1 || evidence.records[0].ReasonCode != want {
					t.Errorf("real decision terminal reason=%s want=%s", evidence.records[0].ReasonCode, want)
				}
			}
		}}
	runNativeFixtureSliceWithModelProbe(modelIdleManagedChecks{T: t, ctx: parent}, binary, options, node)
}
