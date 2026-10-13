package productruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

const semanticOriginalBody = `{"model":"synthetic","input":"hello","stream":false}`
const semanticOriginalPath = "/backend-api/codex/responses"

// Observe the actual Exchange-created request before the real Client acquires
// admission. This wrapper neither constructs nor changes transport authority.
type capturedSemanticProvider struct {
	providerRuntime
	fixture *capturedOriginalFixture
}

func (p capturedSemanticProvider) Do(ctx context.Context, request providertransport.Request) (*http.Response, providertransport.Evidence, error) {
	p.fixture.mu.Lock()
	p.fixture.semanticRequests = append(p.fixture.semanticRequests, request)
	p.fixture.mu.Unlock()
	return p.providerRuntime.Do(ctx, request)
}

// Catches resolving the default again after Hold, releasing a failed/canceled
// request, credential substitution, retry/fallback, and audit-parent rewriting.
// The existing four positive/frozen choices and G/FP down cases are carried.
func TestActualRuntimeSemanticOriginalHoldAndFailure(t *testing.T) {
	rows := []struct{ choice, edge string }{
		{"global", "hold_edit"}, {"flow_proxy", "hold_edit"},
		{"flow_direct", "hold_edit"}, {"builtin", "hold_edit"},
		{"global", "held_cancel"}, {"global", "held_failed_probe"},
		{"flow_direct", "held_tls_failure"},
		{"flow_direct", "target_dial_failure"}, {"builtin", "target_dial_failure"},
		{"flow_direct", "strict_tls_failure"}, {"builtin", "strict_tls_failure"},
	}
	for _, row := range rows {
		t.Run(row.choice+"/"+row.edge, func(t *testing.T) {
			f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "semantic", choice: row.choice, edge: row.edge})
			// Expected next selection is input, computed before observing audits.
			nextExpected := f.expected
			if row.choice == "global" {
				nextExpected = capturedOriginalExpected{Source: egressprofile.SelectionBuiltin, ProfileID: "profile.direct", DefaultRevision: 3}
			} else if row.choice == "builtin" {
				nextExpected = capturedOriginalExpected{Source: egressprofile.SelectionGlobal, ProfileID: "profile.captured-original-b", DefaultRevision: 2}
			}
			var flight *semanticOriginalFlight
			t.Cleanup(func() {
				// Registered before the caller cleanup: even Fatal queue/assertion
				// paths cancel and join first, then drain real Runtime resources.
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				f.gate.BeginShutdown()
				f.r.exchanges.BeginShutdown()
				f.transport.CloseIdleConnections()
				if flight != nil && !flight.joined {
					t.Error("semantic cleanup reached drain without joined caller")
				}
				for _, resource := range []struct {
					name  string
					drain func(context.Context) error
				}{{"gate", f.gate.Drain}, {"body", f.r.bodyAdmission.Drain}, {"pipeline", f.r.exchanges.Drain}} {
					if err := resource.drain(ctx); err != nil {
						t.Error("semantic cleanup drain", resource.name, err)
					}
				}
				snapshot := f.gate.Snapshot()
				if snapshot.ActiveActions != 0 || snapshot.EnteringActions != 0 || snapshot.ActiveEgress != 0 || snapshot.QueuedRequests != 0 || snapshot.HeldBytes != 0 {
					t.Error("semantic cleanup retained admission", snapshot)
				}
				f.listener.CloseClientConnections()
				f.upstream.CloseClientConnections()
				t.Logf("semantic cleanup joined=%t gate=%+v", flight == nil || flight.joined, f.gate.Snapshot())
			})
			held := strings.HasPrefix(row.edge, "held_") || row.edge == "hold_edit"
			if held {
				if _, err := f.r.EnterOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
					t.Fatal(err)
				}
			} else if row.edge == "target_dial_failure" {
				f.network.failDirect.Store(true)
			}
			flight = startSemanticOriginal(t, f)
			if !held {
				reply := flight.join(t)
				if reply.Err != nil || reply.Status == http.StatusOK || reply.Status == 0 {
					t.Fatalf("semantic transport failure reply: %+v", reply)
				}
				assertCapturedPhysical(t, f, 0, 0, 0, 1)
				f.assertAudits(t, 1)
				f.drain(t)
				return
			}
			targets := f.waitQueue(t)
			assertSemanticOriginalTarget(t, f, targets)
			assertCapturedPhysical(t, f, 0, 0, 0, 0)
			f.assertAudits(t, 0)
			f.editDefault(t)
			if row.edge == "held_cancel" {
				flight.cancel()
				if reply := flight.join(t); !errors.Is(reply.Err, context.Canceled) {
					t.Fatalf("canceled semantic reply: %+v", reply)
				}
				f.drain(t)
				if _, err := f.r.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
					t.Fatal("empty semantic queue resume", err)
				}
				f.drain(t)
				assertCapturedPhysical(t, f, 0, 0, 0, 0)
				f.assertAudits(t, 0)
				return
			}
			if row.edge == "held_failed_probe" {
				if err := f.proxyA.Close(); err != nil {
					t.Fatal(err)
				}
			}
			_, resumeErr := f.r.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision)
			if row.edge != "hold_edit" {
				snapshot := f.gate.Snapshot()
				wantReason := offlinehold.ProbeReasonTransportUnavailable
				if row.edge == "held_tls_failure" {
					wantReason = offlinehold.ProbeReasonTLSRejected
				}
				if resumeErr == nil || snapshot.State != offlinehold.StateHeld || snapshot.QueuedRequests != 1 || snapshot.LastProbeReason != wantReason {
					t.Fatalf("failed semantic probe released queue: snapshot=%+v err=%v", snapshot, resumeErr)
				}
				if row.edge == "held_failed_probe" {
					assertCapturedPhysical(t, f, 0, 0, 0, 0, 1, 0)
				} else {
					assertCapturedPhysical(t, f, 0, 0, 0, 1)
				}
				f.assertAudits(t, 0)
				flight.cancel()
				flight.join(t)
				f.gate.BeginShutdown()
				f.drain(t)
				t.Logf("failed semantic probe reason=%s err=%v; retained queue canceled and joined", wantReason, resumeErr)
				return
			}
			if resumeErr != nil {
				t.Error("semantic resume", resumeErr)
				flight.cancel()
				flight.join(t)
				f.gate.BeginShutdown()
				f.drain(t)
				return
			}
			assertSemanticOriginalReply(t, flight.join(t))
			a, direct := int64(2), int64(0)
			if row.choice == "flow_direct" || row.choice == "builtin" {
				a, direct = 0, 2
			}
			assertCapturedPhysical(t, f, 1, a, 0, direct)
			f.assertAudits(t, 1)
			f.mu.Lock()
			f.expected = nextExpected
			f.mu.Unlock()
			assertSemanticOriginalReply(t, f.send(t.Context(), http.MethodPost, semanticOriginalPath, semanticOriginalBody))
			b := int64(0)
			switch row.choice {
			case "global", "flow_direct":
				direct++
			case "flow_proxy":
				a++
			case "builtin":
				b++
			}
			assertCapturedPhysical(t, f, 2, a, b, direct)
			f.assertAudits(t, 2)
			f.drain(t)
		})
	}
}

type semanticOriginalFlight struct {
	done   chan capturedOriginalReply
	cancel context.CancelFunc
	joined bool
}

func startSemanticOriginal(t *testing.T, f *capturedOriginalFixture) *semanticOriginalFlight {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	flight := &semanticOriginalFlight{done: make(chan capturedOriginalReply, 1), cancel: cancel}
	go func() { flight.done <- f.send(ctx, http.MethodPost, semanticOriginalPath, semanticOriginalBody) }()
	t.Cleanup(func() {
		cancel()
		f.unblock()
		if !flight.joined {
			select {
			case <-flight.done:
				flight.joined = true
			case <-time.After(4 * time.Second):
				t.Error("semantic caller cleanup join deadline")
			}
		}
	})
	return flight
}

func (flight *semanticOriginalFlight) join(t *testing.T) capturedOriginalReply {
	t.Helper()
	select {
	case reply := <-flight.done:
		flight.joined = true
		return reply
	case <-time.After(4 * time.Second):
		t.Fatal("semantic caller join deadline")
		return capturedOriginalReply{Err: errors.New("join deadline")}
	}
}

func assertSemanticOriginalReply(t *testing.T, reply capturedOriginalReply) {
	t.Helper()
	var response struct {
		ID string `json:"id"`
	}
	if reply.Err != nil || reply.Status != http.StatusOK || json.Unmarshal(reply.Body, &response) != nil || response.ID != "synthetic-response" {
		t.Fatalf("semantic reply: status=%d err=%v body=%s", reply.Status, reply.Err, reply.Body)
	}
}

func assertSemanticOriginalTarget(t *testing.T, f *capturedOriginalFixture, targets []offlinehold.ProbeTarget) {
	t.Helper()
	policy := egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxyDirect}, Resolver: egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverSystem, Transport: egressnetwork.ResolverTransportDirect}}
	proxyKind, proxyEndpoint := "direct", ""
	if f.cfg.choice == "global" || f.cfg.choice == "flow_proxy" {
		proxyKind, proxyEndpoint = "socks5", f.proxyA.Addr().String()
		policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: proxyEndpoint}
	}
	// Hand-derived NUL-delimited provenance encoding from literal fixture
	// inputs; do not call the production digest/target constructors here.
	encoded := strings.Join([]string{"captured.original.egress", "1", f.assignment.EnvironmentDigest.String(), "upstream", "route.test", "1", proxyKind, proxyEndpoint, "system", "", "direct"}, "\x00")
	digest := sha256.Sum256([]byte(encoded))
	want := offlinehold.ProbeTarget{Kind: offlinehold.EgressProvider, Transport: offlinehold.ProbeTransportStrictTLS, TargetRef: "target.codex.official", NetworkOrigin: "https://chatgpt.com", HTTPAuthority: "chatgpt.com", TLSServerName: "chatgpt.com", PlanRevision: 1, PlanDigest: hex.EncodeToString(digest[:]), EgressPolicy: policy}
	if len(targets) != 1 || targets[0] != want || f.assignment.EnvironmentRevision != 1 {
		t.Fatalf("semantic frozen probe target=%+v want=%+v", targets, want)
	}
	f.mu.Lock()
	requests := append([]providertransport.Request(nil), f.semanticRequests...)
	f.mu.Unlock()
	if len(requests) != 1 {
		t.Fatal("semantic request count before admission", len(requests))
	}
	account, managed := requests[0].AccountRef()
	if managed || !reflect.DeepEqual(account, providerauth.AccountRef{}) || requests[0].CredentialMode() != providerauth.CredentialClientPassthrough || requests[0].ProbeTarget() != want || f.secrets.reads.Load() != 0 || f.secrets.inspects.Load() != 0 || f.secrets.writes.Load() != 0 || f.network.tokenAttempts.Load() != 0 {
		t.Fatal("semantic held request borrowed managed authority", account, managed)
	}
	t.Logf("semantic queued target=%+v independently-derived-digest=%s AccountRef=0 HTTP=0", targets[0], want.PlanDigest)
}

func (f *capturedOriginalFixture) assertSemanticAudits(t *testing.T, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	// A reply body can reach the downstream just before terminal observation.
	// Drain that real operation before reading persisted Exchange evidence.
	if want > 0 {
		if err := f.r.exchanges.Drain(ctx); err != nil {
			t.Fatal("semantic audit pipeline drain", err)
		}
	}
	page, err := f.r.EgressAttempts().List(ctx, egressaudit.PageRequest{Limit: 100})
	if err != nil || len(page.Items) != want {
		t.Fatalf("semantic audits=%d want=%d err=%v", len(page.Items), want, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.starts) != want || len(f.wants) != want {
		t.Fatal("orphan semantic starts", len(f.starts), len(f.wants), want)
	}
	if want > 0 && len(f.semanticRequests) != want {
		t.Fatal("semantic provider retry/resend", len(f.semanticRequests), want)
	}
	seenParents := map[string]bool{}
	seenExchanges := map[string]bool{}
	for _, record := range page.Items {
		a, d := record.Attempt, record.Attempt.Decision()
		expected, ok := f.wants[a.ID()]
		if !ok || !a.Terminal() || d != f.starts[a.ID()] || d.SelectionSource != expected.Source || d.ProxyID != expected.ProfileID || d.ProxyRevision != 1 || d.DefaultRevision != expected.DefaultRevision || d.AccountID != "" || d.AccountSettingsRevision != 0 || d.Authority != egressaudit.AuthorityEnvironment || d.PolicyID != "captured.original.egress" || d.PolicyRevision != 1 || d.RuleID != "route.test" || a.Purpose() != egressaudit.PurposeProviderAttempt || a.PayloadClass() != egressaudit.PayloadClientSemantic || a.TargetOrigin() != "https://chatgpt.com" {
			t.Fatalf("semantic immutable attempt=%s decision=%+v start=%+v expected=%+v", a.ID(), d, f.starts[a.ID()], expected)
		}
		parent := a.Parent()
		if parent.Kind != egressaudit.ParentUpstreamAttempt || parent.ID == "" || parent.ID == a.ID() || parent.ExchangeID == "" || seenParents[parent.ID] || seenExchanges[parent.ExchangeID] {
			t.Fatal("semantic typed parent", parent)
		}
		seenParents[parent.ID] = true
		seenExchanges[parent.ExchangeID] = true
		bound := 0
		for _, request := range f.semanticRequests {
			if request.EgressAttemptID() == a.ID() {
				account, managed := request.AccountRef()
				if request.RequestID() != parent.ID || request.ParentAttemptID() != parent.ID || request.ExchangeID() != parent.ExchangeID || request.ConnectionID() != a.ConnectionID() || request.CredentialMode() != providerauth.CredentialClientPassthrough || managed || !reflect.DeepEqual(account, providerauth.AccountRef{}) {
					t.Fatal("semantic parent is not real upstream attempt", parent)
				}
				bound++
			}
		}
		if bound != 1 {
			t.Fatal("semantic audit has no unique actual provider request", bound)
		}
		exchange, err := f.r.storage.ActivityRepository().GetExchange(ctx, parent.ExchangeID)
		if err != nil || exchange.Kind != activity.KindExchangeCompleted || exchange.SubjectID != parent.ExchangeID || exchange.ConnectionID != a.ConnectionID() || exchange.EnvironmentID != "captured.original.egress" || exchange.EnvironmentRevision != 1 || exchange.EnvironmentDigest != f.assignment.EnvironmentDigest.String() || exchange.RouteID != "route.test" || exchange.RouteRevision != 1 || exchange.AccountID != "" || exchange.AccountRevision != 0 || exchange.CredentialEpoch != 0 || exchange.ManualCaptureID != f.capture.ID {
			t.Fatalf("semantic audit lacks real Exchange linkage: parent=%+v exchange=%+v err=%v", parent, exchange, err)
		}
		wantOutcome, wantStatus := egressaudit.OutcomeCompleted, activity.StatusSucceeded
		if f.cfg.edge == "target_dial_failure" || f.cfg.edge == "strict_tls_failure" {
			wantOutcome, wantStatus = egressaudit.OutcomeFailed, activity.StatusFailed
		}
		if a.Outcome() != wantOutcome || exchange.Status != wantStatus {
			t.Fatal("semantic terminal outcome", a.Outcome(), exchange.Status)
		}
		if !f.asserted[a.ID()] {
			t.Logf("semantic attempt=%s parent=%+v actual-upstream-attempt=%s Exchange=%s connection=%s outcome=%s decision=%+v", a.ID(), parent, parent.ID, exchange.SubjectID, a.ConnectionID(), a.Outcome(), d)
			f.asserted[a.ID()] = true
		}
	}
}

func (f *capturedOriginalFixture) drainSemantic(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := f.r.exchanges.Drain(ctx); err != nil {
		t.Fatal("semantic pipeline drain", err)
	}
	accounts, err := f.r.ProviderAccounts().List(ctx)
	if err != nil || len(accounts) != 0 || f.secrets.reads.Load() != 0 || f.secrets.inspects.Load() != 0 || f.secrets.writes.Load() != 0 || f.network.tokenAttempts.Load() != 0 {
		t.Fatal("semantic managed-account/token effect", len(accounts), err, f.secrets.reads.Load(), f.secrets.inspects.Load(), f.secrets.writes.Load(), f.network.tokenAttempts.Load())
	}
	t.Logf("semantic pipeline/body/gate drained accounts=0 secretReads=0 secretInspects=0 secretWrites=0 tokenAttempts=0 requests=%d", len(f.semanticRequests))
}
