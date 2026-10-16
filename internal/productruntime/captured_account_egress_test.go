package productruntime

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

// A captured quota must resume through Runtime's concrete prober while its
// actual send retains the account lease and frozen Environment authority.
func TestActualRuntimeCapturedManagedReadConcreteRecovery(t *testing.T) {
	f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "managed_read", choice: "global"})
	var flight *capturedReadFlight
	// Register before the flight so its cancel/unblock/bounded join cleanup
	// runs first, including Fatal paths in waitQueue or editDefault. Cleanup
	// owns its context because testing has already canceled t.Context here.
	t.Cleanup(func() {
		f.transport.CloseIdleConnections()
		f.gate.BeginShutdown()
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if flight != nil && !flight.joined {
			t.Error("captured recovery cleanup reached drain without a joined caller")
		}
		if err := f.gate.Drain(cleanup); err != nil {
			t.Error("captured recovery cleanup gate drain", err)
		}
		if err := f.r.bodyAdmission.Drain(cleanup); err != nil {
			t.Error("captured recovery cleanup body admission drain", err)
		}
		snapshot := f.gate.Snapshot()
		if snapshot.ActiveActions != 0 || snapshot.ActiveEgress != 0 || snapshot.QueuedRequests != 0 || snapshot.HeldBytes != 0 {
			t.Error("captured recovery cleanup retained admission", snapshot)
		}
		t.Logf("captured recovery cleanup callerStarted=%t joined=%t HTTP=%d A=%d B=%d Direct=%d writes=%d drain=%+v", flight != nil, flight == nil || flight.joined, f.httpCalls.Load(), f.callsA.Load(), f.callsB.Load(), f.network.direct.Load(), f.secrets.writes.Load(), snapshot)
	})
	if _, err := f.r.EnterOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	flight = startCapturedRead(t, f, "/backend-api/wham/usage")
	targets := f.waitQueue(t)
	t.Logf("captured managed queue=%+v targets=%+v", f.gate.Snapshot(), targets)
	if len(targets) != 1 || f.httpCalls.Load() != 0 || f.callsA.Load() != 0 || f.callsB.Load() != 0 || f.network.direct.Load() != 0 {
		t.Error("captured held request sent before Resume")
	}
	f.editDefault(t)
	_, resumeErr := f.r.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision)
	if resumeErr != nil {
		t.Errorf("captured managed concrete recovery: %v", resumeErr)
		flight.cancel()
		flight.join(t)
		f.gate.BeginShutdown()
		f.drain(t)
		return
	}
	reply := flight.join(t)
	if reply.Err != nil || reply.Status != http.StatusOK {
		t.Errorf("captured quota reply: %+v", reply)
	}
	if f.callsA.Load() != 2 || f.callsB.Load() != 0 || f.network.direct.Load() != 0 || f.httpCalls.Load() != 1 {
		t.Error("captured recovery did not use exactly one A probe and one A quota send")
	}
	f.assertAudits(t, 1)
	f.drain(t)
}

type capturedReadFlight struct {
	done   chan capturedOriginalReply
	cancel context.CancelFunc
	joined bool
}

func startCapturedRead(t *testing.T, f *capturedOriginalFixture, path string) *capturedReadFlight {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	flight := &capturedReadFlight{done: make(chan capturedOriginalReply, 1), cancel: cancel}
	go func() { flight.done <- f.send(ctx, http.MethodGet, path, "") }()
	t.Cleanup(func() {
		cancel()
		f.unblock()
		if !flight.joined {
			select {
			case <-flight.done:
				flight.joined = true
			case <-time.After(4 * time.Second):
				t.Error("captured caller did not join")
			}
		}
	})
	return flight
}
func (flight *capturedReadFlight) join(t *testing.T) capturedOriginalReply {
	t.Helper()
	select {
	case reply := <-flight.done:
		flight.joined = true
		return reply
	case <-time.After(4 * time.Second):
		t.Fatal("captured caller join deadline")
		return capturedOriginalReply{Err: errors.New("join deadline")}
	}
}
func assertCapturedReply(t *testing.T, f *capturedOriginalFixture, reply capturedOriginalReply, path string) {
	t.Helper()
	if reply.Err != nil || reply.Status != http.StatusOK {
		t.Fatalf("captured reply status=%d err=%v body=%s", reply.Status, reply.Err, reply.Body)
	}
	if path == "/backend-api/wham/profiles/me" {
		if string(reply.Body) != `{"stats":{"lifetime_tokens":1200}}` {
			t.Fatal("history bytes changed", string(reply.Body))
		}
	} else {
		workspace := "workspace-A"
		if f.cfg.family == "managed_read" {
			workspace = "workspace-B"
		}
		want := `{"plan_type":"pro","account_id":"` + workspace + `","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_after_seconds":3600,"reset_at":1800000000}}}`
		if string(reply.Body) != want {
			t.Fatal("quota bytes changed", string(reply.Body))
		}
	}
	wantCache := ""
	if f.cfg.family == "managed_read" {
		wantCache = "no-store"
	}
	if reply.Header.Get("Cache-Control") != wantCache {
		t.Fatal("captured response cache contract changed", reply.Header)
	}
}
func assertCapturedPhysical(t *testing.T, f *capturedOriginalFixture, httpCalls, a, b, direct int64, proxyAttempts ...int64) {
	t.Helper()
	if f.httpCalls.Load() != httpCalls || f.callsA.Load() != a || f.callsB.Load() != b || f.network.direct.Load() != direct || f.network.denied.Load() != 0 || f.bad.Load() != 0 {
		t.Fatalf("captured physical want HTTP/A/B/Direct=%d/%d/%d/%d got=%d/%d/%d/%d denied=%d bad=%d", httpCalls, a, b, direct, f.httpCalls.Load(), f.callsA.Load(), f.callsB.Load(), f.network.direct.Load(), f.network.denied.Load(), f.bad.Load())
	}
	wantAttemptsA, wantAttemptsB := a, b
	if len(proxyAttempts) == 2 {
		wantAttemptsA, wantAttemptsB = proxyAttempts[0], proxyAttempts[1]
	}
	if f.network.proxyAttemptsA.Load() != wantAttemptsA || f.network.proxyAttemptsB.Load() != wantAttemptsB {
		t.Fatalf("captured selected proxy attempts A=%d B=%d want=%d/%d", f.network.proxyAttemptsA.Load(), f.network.proxyAttemptsB.Load(), wantAttemptsA, wantAttemptsB)
	}
}
func waitCapturedEntry(t *testing.T, f *capturedOriginalFixture, flight *capturedReadFlight) {
	t.Helper()
	select {
	case <-f.entered:
	case reply := <-flight.done:
		flight.joined = true
		t.Fatalf("captured ended before send boundary: %+v", reply)
	case <-time.After(2 * time.Second):
		t.Fatal("captured send boundary deadline")
	}
}
func TestActualRuntimeCapturedAccountReadEgressMatrix(t *testing.T) {
	for _, family := range []string{"managed_read", "original_read"} {
		choices := []string{"account_proxy", "account_direct", "flow_proxy", "flow_direct", "global", "builtin"}
		if family == "original_read" {
			choices = []string{"flow_proxy", "flow_direct", "builtin"}
		}
		for _, choice := range choices {
			for _, path := range []string{"/backend-api/wham/usage", "/backend-api/wham/profiles/me"} {
				t.Run(family+"/"+choice+"/"+path, func(t *testing.T) {
					f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: family, choice: choice, edge: "send_edit", history: true})
					flight := startCapturedRead(t, f, path)
					waitCapturedEntry(t, f, flight)
					assertCapturedPhysical(t, f, 0, 0, 0, 0)
					f.editDefault(t)
					f.unblock()
					reply := flight.join(t)
					assertCapturedReply(t, f, reply, path)
					a, d := int64(1), int64(0)
					if choice == "account_direct" || choice == "flow_direct" || choice == "builtin" {
						a, d = 0, 1
					}
					assertCapturedPhysical(t, f, 1, a, 0, d)
					f.assertAudits(t, 1)
					f.mu.Lock()
					if choice == "global" {
						f.expected.Source, f.expected.ProfileID, f.expected.DefaultRevision = egressprofile.SelectionBuiltin, "profile.direct", 3
					} else if choice == "builtin" {
						f.expected.Source, f.expected.ProfileID, f.expected.DefaultRevision = egressprofile.SelectionGlobal, "profile.captured-original-b", 2
					}
					f.mu.Unlock()
					assertCapturedReply(t, f, f.send(t.Context(), http.MethodGet, path, ""), path)
					b := int64(0)
					if choice == "builtin" {
						b = 1
					} else if choice == "global" || d == 1 {
						d++
					} else {
						a++
					}
					assertCapturedPhysical(t, f, 2, a, b, d)
					f.assertAudits(t, 2)
					f.drain(t)
				})
			}
		}
	}
}

func TestActualRuntimeCapturedAccountReadHoldAndFailure(t *testing.T) {
	for _, row := range []struct{ family, choice, path, edge string }{
		{"managed_read", "flow_proxy", "/backend-api/wham/profiles/me", "hold"},
		{"managed_read", "account_direct", "/backend-api/wham/usage", "hold"},
		{"managed_read", "builtin", "/backend-api/wham/usage", "hold"},
		{"original_read", "global", "/backend-api/wham/usage", "hold"},
		{"original_read", "flow_proxy", "/backend-api/wham/profiles/me", "hold"},
		{"managed_read", "global", "/backend-api/wham/usage", "held_cancel"},
		{"original_read", "global", "/backend-api/wham/usage", "held_cancel"},
		{"managed_read", "global", "/backend-api/wham/usage", "closed_probe"},
		{"original_read", "global", "/backend-api/wham/usage", "closed_probe"},
		{"managed_read", "flow_direct", "/backend-api/wham/usage", "wrong_name"},
		{"original_read", "flow_direct", "/backend-api/wham/usage", "wrong_name"},
		{"managed_read", "flow_direct", "/backend-api/wham/usage", "untrusted"},
	} {
		t.Run(row.family+"/"+row.choice+"/"+row.edge+"/"+row.path, func(t *testing.T) {
			f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: row.family, choice: row.choice, edge: row.edge, history: true})
			if _, err := f.r.EnterOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
				t.Fatal(err)
			}
			flight := startCapturedRead(t, f, row.path)
			targets := f.waitQueue(t)
			t.Logf("captured queued target=%+v", targets)
			if len(targets) != 1 || targets[0].Kind != offlinehold.EgressAuxiliary || targets[0].NetworkOrigin != "https://chatgpt.com" || targets[0].TargetRef != "https://chatgpt.com" || targets[0].PlanRevision != 0 || targets[0].PlanDigest != "" || targets[0].TargetAccess != egressnetwork.TargetAccessOwnerConfigured {
				t.Fatal("closed held target changed", targets)
			}
			assertCapturedPhysical(t, f, 0, 0, 0, 0)
			f.editDefault(t)
			if row.edge == "held_cancel" {
				flight.cancel()
				flight.join(t)
				f.drain(t)
				if _, err := f.r.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
					t.Fatal("empty canceled queue resume", err)
				}
				assertCapturedPhysical(t, f, 0, 0, 0, 0)
				f.assertAudits(t, 0)
				f.drain(t)
				return
			}
			if row.edge == "closed_probe" {
				if err := f.proxyA.Close(); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.r.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision)
			if row.edge != "hold" {
				if err == nil || f.gate.Snapshot().State != offlinehold.StateHeld || f.gate.Snapshot().QueuedRequests != 1 {
					t.Fatal("failed captured probe opened admission", f.gate.Snapshot(), err)
				}
				wantDirect := int64(0)
				if row.edge == "wrong_name" || row.edge == "untrusted" {
					wantDirect = 1
				}
				wantAttemptsA := int64(0)
				if row.edge == "closed_probe" {
					wantAttemptsA = 1
				}
				assertCapturedPhysical(t, f, 0, 0, 0, wantDirect, wantAttemptsA, 0)
				flight.cancel()
				flight.join(t)
				f.gate.BeginShutdown()
				f.assertAudits(t, 0)
				f.drain(t)
				return
			}
			if err != nil {
				t.Fatal("captured recovery", err)
			}
			assertCapturedReply(t, f, flight.join(t), row.path)
			a, d := int64(2), int64(0)
			if row.choice == "account_direct" || row.choice == "builtin" {
				a, d = 0, 2
			}
			assertCapturedPhysical(t, f, 1, a, 0, d)
			f.assertAudits(t, 1)
			f.drain(t)
		})
	}
	for _, family := range []string{"managed_read", "original_read"} {
		choices := []string{"account_proxy", "flow_proxy", "global", "account_direct", "builtin"}
		if family == "original_read" {
			choices = []string{"flow_proxy", "flow_direct", "builtin"}
		}
		for _, choice := range choices {
			edges := []string{"proxy_loss"}
			direct := choice == "account_direct" || choice == "flow_direct" || choice == "builtin"
			if direct {
				edges = []string{"dial_failure", "tls_failure"}
			}
			for _, edge := range edges {
				t.Run(family+"/"+choice+"/"+edge, func(t *testing.T) {
					f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: family, choice: choice, edge: edge})
					flight := startCapturedRead(t, f, "/backend-api/wham/usage")
					waitCapturedEntry(t, f, flight)
					assertCapturedPhysical(t, f, 0, 0, 0, 0)
					f.editDefault(t)
					if edge == "proxy_loss" {
						if err := f.proxyA.Close(); err != nil {
							t.Fatal(err)
						}
					} else if edge == "dial_failure" {
						f.network.failDirect.Store(true)
					}
					f.unblock()
					reply := flight.join(t)
					if reply.Err != nil || reply.Status == http.StatusOK {
						t.Fatalf("captured transport failure surfaced incorrectly: %+v", reply)
					}
					wantDirect := int64(0)
					if direct {
						wantDirect = 1
					}
					wantAttemptsA := int64(0)
					if edge == "proxy_loss" {
						wantAttemptsA = 1
					}
					assertCapturedPhysical(t, f, 0, 0, 0, wantDirect, wantAttemptsA, 0)
					f.assertAudits(t, 1)
					f.drain(t)
				})
			}
		}
	}
}

// Authority rows use the real Runtime proxy; transport counters detect any
// accidental permission gained by resolving an egress selection.
func TestActualRuntimeCapturedAccountReadAuthority(t *testing.T) {
	for _, row := range []struct {
		name, method, path string
		want               int
	}{
		{"history_denied", http.MethodGet, "/backend-api/wham/profiles/me", http.StatusForbidden},
		{"reset_details", http.MethodGet, "/backend-api/wham/rate-limit-reset-credits", http.StatusUnprocessableEntity},
		{"consume", http.MethodPost, "/backend-api/wham/rate-limit-reset-credits/consume", http.StatusUnprocessableEntity},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "managed_read", choice: "global"})
			reply := f.send(t.Context(), row.method, row.path, "")
			if reply.Err != nil || reply.Status != row.want {
				t.Fatalf("captured authority %s: %+v", row.name, reply)
			}
			if f.secrets.inspects.Load() != 0 || f.secrets.reads.Load() != 0 {
				t.Fatal("denied captured operation acquired/read a managed credential")
			}
			assertCapturedPhysical(t, f, 0, 0, 0, 0)
			f.assertAudits(t, 0)
			f.drain(t)
		})
	}
	t.Run("oauth_quota_no_rotation", func(t *testing.T) {
		f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "managed_read", choice: "global", oauth: true})
		reply := f.send(t.Context(), http.MethodGet, "/backend-api/wham/usage", "")
		assertCapturedReply(t, f, reply, "/backend-api/wham/usage")
		assertCapturedPhysical(t, f, 1, 1, 0, 0)
		f.assertAudits(t, 1)
		f.drain(t)
	})
	t.Run("original_history_requires_no_managed_permission", func(t *testing.T) {
		f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "original_read", choice: "flow_proxy"})
		reply := f.send(t.Context(), http.MethodGet, "/backend-api/wham/profiles/me", "")
		if !strings.Contains(string(reply.Body), "1200") {
			t.Fatal("original history permission changed", reply)
		}
		assertCapturedReply(t, f, reply, "/backend-api/wham/profiles/me")
		assertCapturedPhysical(t, f, 1, 1, 0, 0)
		f.assertAudits(t, 1)
		f.drain(t)
	})
	t.Run("managed_launch_native_base_path", func(t *testing.T) {
		f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "managed_read", choice: "global", edge: "managed_launch"})
		assertCapturedReply(t, f, f.send(t.Context(), http.MethodGet, "/backend-api/wham/usage", ""), "/backend-api/wham/usage")
		assertCapturedPhysical(t, f, 1, 1, 0, 0)
		f.assertAudits(t, 1)
		f.drain(t)
	})
	t.Run("association_revoked_before_acquisition", func(t *testing.T) {
		f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "managed_read", choice: "global"})
		// Retain the admitted capture's revision1 assignment while replacing
		// current published references through the real Environment manager.
		f.aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination = environment.DestinationPlan{Kind: environment.DestinationKindOriginal}
		f.aggregate.Revision++
		f.aggregate.ClientEndpoints[0].Revision++
		f.aggregate.ClientEndpoints[0].ProtocolPlans[0].Revision++
		f.publish(t)
		if _, err := f.r.ProviderAccounts().SetAssociation(t.Context(), provideraccount.AssociationCommand{ID: f.account.Account.ID, EndpointID: upstreamendpoint.ChatGPTOfficialID, ExpectedRevision: f.account.Account.AssociationRevision, Linked: false}); err != nil {
			t.Fatal(err)
		}
		f.secrets.reads.Store(0)
		f.secrets.inspects.Store(0)
		reply := f.send(t.Context(), http.MethodGet, "/backend-api/wham/usage", "")
		if reply.Err != nil || reply.Status != http.StatusBadGateway {
			t.Fatal("revoked capture account was admitted", reply)
		}
		if f.secrets.reads.Load() != 0 || f.secrets.inspects.Load() != 0 {
			t.Fatal("revoked association acquired/read a credential")
		}
		assertCapturedPhysical(t, f, 0, 0, 0, 0)
		f.assertAudits(t, 0)
		f.drain(t)
	})
	t.Run("unbound_selector", func(t *testing.T) {
		f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "managed_read", choice: "global", edge: "selector"})
		reply := f.send(t.Context(), http.MethodGet, "/backend-api/wham/usage", "")
		if reply.Err != nil || reply.Status != http.StatusUnprocessableEntity || !strings.Contains(string(reply.Body), "account_read_ambiguous") {
			t.Fatal("unbound selector gained account authority", reply)
		}
		if f.secrets.inspects.Load() != 0 || f.secrets.reads.Load() != 0 {
			t.Fatal("selector acquired a credential")
		}
		assertCapturedPhysical(t, f, 0, 0, 0, 0)
		f.assertAudits(t, 0)
		f.drain(t)
	})
	for _, edge := range []string{"original_base_mismatch", "original_cross_origin"} {
		t.Run(edge, func(t *testing.T) {
			f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "original_read", choice: "flow_proxy"})
			aggregate := f.aggregate.Clone()
			route := &aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
			if edge == "original_cross_origin" {
				endpoint, err := f.r.UpstreamEndpoints().Get(t.Context(), upstreamendpoint.OpenAIPlatformID)
				if err != nil {
					t.Fatal(err)
				}
				route.ProviderTarget = environment.ProviderTarget{ID: endpoint.ID.String(), Revision: environment.Revision(endpoint.Revision), Origin: endpoint.Origin, RealmID: endpoint.RealmID, Capabilities: endpoint.Capabilities}
				route.BackendProtocol = "openai_responses"
			} else {
				origin, err := originidentity.ParseProviderOrigin("https://chatgpt.com/backend-api")
				if err != nil {
					t.Fatal(err)
				}
				endpoint, err := f.r.UpstreamEndpoints().Create(t.Context(), upstreamendpoint.CreateCommand{ID: "target.captured-base", DisplayName: "Captured base mismatch", Origin: origin, RealmID: route.ProviderTarget.RealmID, BackendProtocols: []string{"openai_responses"}, Capabilities: route.ProviderTarget.Capabilities, Drivers: []providerauth.DriverRef{providerauth.StaticHeaderDriverRef()}})
				if err != nil {
					t.Fatal(err)
				}
				route.ProviderTarget.ID, route.ProviderTarget.Revision, route.ProviderTarget.Origin = endpoint.ID.String(), environment.Revision(endpoint.Revision), endpoint.Origin
			}
			compiler, err := productionEnvironmentCompiler(f.r.accounts, f.r.endpoints)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := compiler.Compile(aggregate); !errors.Is(err, environment.ErrInvalidEnvironment) {
				t.Fatal("compiler admitted mismatched original account", err)
			} else {
				t.Logf("original authority rejected before publication: %v", err)
			}
			assertCapturedPhysical(t, f, 0, 0, 0, 0)
			f.assertAudits(t, 0)
			f.drain(t)
		})
	}
	for _, edge := range []string{"missing_capability", "invalid_capability"} {
		t.Run(edge, func(t *testing.T) {
			f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "managed_read", choice: "global"})
			proxyURL, err := f.transport.Proxy(nil)
			if err != nil {
				t.Fatal(err)
			}
			clone := *proxyURL
			clone.User = nil
			if edge == "invalid_capability" {
				clone.User = url.UserPassword("capture", "invalid-synthetic-capability")
			}
			f.transport.Proxy = http.ProxyURL(&clone)
			reply := f.send(t.Context(), http.MethodGet, "/backend-api/wham/usage", "")
			if reply.Err == nil {
				t.Fatal("unauthenticated CONNECT admitted", reply)
			}
			if f.secrets.inspects.Load() != 0 || f.secrets.reads.Load() != 0 {
				t.Fatal("unauthenticated capture acquired a credential")
			}
			assertCapturedPhysical(t, f, 0, 0, 0, 0)
			f.assertAudits(t, 0)
			f.drain(t)
		})
	}
}
