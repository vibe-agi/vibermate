package productruntime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestActualRuntimeEndpointModelsEgressMatrix(t *testing.T) {
	for _, choice := range []string{"AP", "AD", "G", "B"} {
		t.Run(choice, func(t *testing.T) { runEndpointModelsAuxCase(t, choice, "send_edit") })
	}
}

func TestActualRuntimeEndpointModelsHoldAndFailure(t *testing.T) {
	for _, row := range [][2]string{
		{"G", "held_edit"}, {"G", "held_cancel"}, {"G", "failed_probe_cancel"},
		{"AP", "proxy_down"}, {"G", "proxy_down"},
		{"AD", "tls_failure"}, {"B", "tls_failure"},
		{"G", "wrong_endpoint"}, {"G", "wrong_realm"},
	} {
		t.Run(row[0]+"/"+row[1], func(t *testing.T) { runEndpointModelsAuxCase(t, row[0], row[1]) })
	}
}

func endpointModelsExpected(t *testing.T, f *auxFixture, choice string, next bool) egressprofile.Resolved {
	t.Helper()
	profile, source, revision := egressprofile.Direct(), egressprofile.SelectionBuiltin, uint64(1)
	switch choice {
	case "AP":
		profile, source, revision = f.profileA, egressprofile.SelectionAccount, 0
	case "AD":
		source, revision = egressprofile.SelectionAccount, 0
	case "G":
		if next {
			revision = 3
		} else {
			profile, source, revision = f.profileB, egressprofile.SelectionGlobal, 2
		}
	case "B":
		if next {
			profile, source, revision = f.profileA, egressprofile.SelectionGlobal, 2
		}
	default:
		t.Fatal("unknown endpoint models choice", choice)
	}
	resolved, err := egressprofile.RestoreResolved(profile, source, revision)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

type endpointModelsCounts struct {
	dialA, dialB, direct, connectA, connectB, sends, gets, dns, creates, rotations, epoch int64
}

// Pair independently named operations with SQL IDs, never with page order or
// with an expected route inferred from the terminal decision.
func endpointModelsAudit(t *testing.T, f *auxFixture, account provideraccount.View, expected map[string]egressprofile.Resolved, outcome egressaudit.Outcome) {
	t.Helper()
	page, err := productruntime.SettledEgressTestPage(t, f.runtime, egressaudit.PageRequest{Limit: 100})
	if err != nil || len(page.Items) != len(expected) {
		t.Fatalf("models SQL terminals=%d want=%d err=%v", len(page.Items), len(expected), err)
	}
	authority, err := egressaudit.AuthorityForPurpose(egressaudit.PurposeUpstreamModelDiscovery)
	if err != nil {
		t.Fatal(err)
	}
	f.startsMu.Lock()
	defer f.startsMu.Unlock()
	if len(f.starts) != len(expected) || len(f.parents) != len(expected) {
		t.Fatalf("models SQL starts=%d parents=%d want=%d", len(f.starts), len(f.parents), len(expected))
	}
	parents := map[string]bool{}
	for _, record := range page.Items {
		attempt := record.Attempt
		resolved, exists := expected[attempt.ID()]
		if !exists {
			t.Fatalf("unpaired models SQL attempt=%s", attempt.ID())
		}
		want := egressaudit.BuiltInDirectDecision(authority)
		want.SelectionSource, want.DefaultRevision = resolved.Source(), resolved.DefaultRevision()
		want.ProxyID, want.ProxyRevision = resolved.Profile().ID.String(), uint64(resolved.Profile().Revision)
		want.PolicyID, want.PolicyRevision, want.RuleID = account.Account.ID.String(), account.Account.SettingsRevision, "account.egress"
		want.AccountID, want.AccountSettingsRevision = account.Account.ID.String(), account.Account.SettingsRevision
		start, startOK := f.starts[attempt.ID()]
		parent, parentOK := f.parents[attempt.ID()]
		if !startOK || !parentOK || start != want || attempt.Decision() != want || parent != attempt.Parent() ||
			!attempt.Terminal() || attempt.Outcome() != outcome || attempt.Purpose() != egressaudit.PurposeUpstreamModelDiscovery ||
			attempt.TargetOrigin() != "https://api.openai.com" || attempt.PayloadClass() != egressaudit.PayloadRuntime ||
			attempt.Caller() != egressaudit.CallerCore || parent.Kind != egressaudit.ParentRuntimeAction || parent.ID == "" || parent.ExchangeID != "" || parents[parent.ID] {
			t.Fatalf("models immutable audit id=%s start=%+v terminal=%+v want=%+v parent=%+v outcome=%s", attempt.ID(), start, attempt.Decision(), want, parent, attempt.Outcome())
		}
		parents[parent.ID] = true
		t.Logf("models audit id=%s start=%+v terminal=%+v purpose=%s origin=%s parent=%+v outcome=%s", attempt.ID(), start, attempt.Decision(), attempt.Purpose(), attempt.TargetOrigin(), parent, attempt.Outcome())
	}
}

func runEndpointModelsAuxCase(t *testing.T, choice, edge string) {
	t.Helper()
	var gets, bad atomic.Int64
	f := newAuxFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1))
		valid := err == nil && len(body) == 0 && r.ContentLength == 0 && r.Method == http.MethodGet &&
			r.Host == "api.openai.com" && r.URL.RequestURI() == "/v1/models" && r.URL.RawQuery == "" &&
			r.Header.Get("Authorization") == "Bearer synthetic-aux-bearer" && r.Header.Get("X-Aux-Fixture") == "owned" &&
			len(r.Header.Values("X-Aux-Deleted")) == 0
		if !valid {
			bad.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gets.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"synthetic-model"}]}`)
	}))
	account := auxSetChoice(t, f, auxCreateAccount(t, f, "account.models-aux", false), choice)
	expected := endpointModelsExpected(t, f, choice, false)
	f.secrets.creates.Store(0)
	f.secrets.rotations.Store(0)
	readSecret := func() []byte {
		t.Helper()
		value, err := f.secrets.ReadAtRevision(t.Context(), account.Account.SecretRef, 1)
		if err != nil {
			t.Fatal(err)
		}
		defer value.Destroy()
		encoded, err := value.CopyBytes()
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	initialSecret := readSecret()
	defer clear(initialSecret)
	assertNoAudit := func() {
		t.Helper()
		page, err := f.runtime.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 100})
		f.startsMu.Lock()
		starts, parents := len(f.starts), len(f.parents)
		f.startsMu.Unlock()
		if err != nil || len(page.Items) != 0 || starts != 0 || parents != 0 {
			t.Fatalf("models unsent SQL terminals=%d starts=%d parents=%d err=%v", len(page.Items), starts, parents, err)
		}
		t.Log("models outgoing SQL starts=0 terminals=0")
	}
	endpoint, err := f.runtime.UpstreamEndpoints().Get(t.Context(), upstreamendpoint.OpenAIPlatformID)
	if err != nil {
		t.Fatal(err)
	}
	wrong := edge == "wrong_endpoint" || edge == "wrong_realm"
	if wrong {
		anthropic, err := f.runtime.UpstreamEndpoints().Get(t.Context(), upstreamendpoint.AnthropicOfficialID)
		if err != nil {
			t.Fatal(err)
		}
		if edge == "wrong_endpoint" {
			endpoint = anthropic
		} else {
			endpoint.RealmID = anthropic.RealmID
			if err := endpoint.Validate(); err != nil {
				t.Fatalf("cross-realm clone must be structurally valid: %v", err)
			}
		}
		lease, err := f.runtime.ProviderAccounts().AcquireEndpointCredential(t.Context(), account.Account.ID, endpoint)
		if lease != nil {
			lease.Release()
		}
		if !errors.Is(err, provideraccount.ErrEndpointMismatch) || lease != nil {
			t.Fatalf("actual %s acquisition lease=%v err=%v", edge, lease, err)
		}
		assertNoAudit()
		t.Logf("actual %s rejected ErrEndpointMismatch before lease/send/queue/DNS", edge)
	} else {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		lease, err := f.runtime.ProviderAccounts().AcquireEndpointCredential(ctx, account.Account.ID, endpoint)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Release()
		ref, ok := lease.Account()
		assertRef := func(ref providerauth.AccountRef, ok bool, resolved egressprofile.Resolved) {
			t.Helper()
			if !ok || ref.Validate() != nil || ref.ID != account.Account.ID.String() || ref.Revision != account.Account.Revision ||
				ref.SettingsRevision != account.Account.SettingsRevision || ref.RealmID != endpoint.RealmID || ref.CredentialEpoch != 1 ||
				!ref.EgressProfile.Equal(resolved.Profile()) || ref.ResolvedEgress.Source() != resolved.Source() || ref.ResolvedEgress.DefaultRevision() != resolved.DefaultRevision() {
				t.Fatalf("actual endpoint lease ref=%+v want=%+v", ref, resolved)
			}
		}
		assertRef(ref, ok, expected)
		t.Logf("first actual endpoint lease account=%s realm=%s epoch=%d settings=%d source=%s default=%d profile=%s/%d", ref.ID, ref.RealmID, ref.CredentialEpoch, ref.SettingsRevision, expected.Source(), expected.DefaultRevision(), expected.Profile().ID, expected.Profile().Revision)
		editDefault := func() error {
			var profile *egressprofile.ProfileRef
			revision := uint64(2)
			if choice == "B" {
				revision = 1
				profile = &egressprofile.ProfileRef{ID: f.profileA.ID, Revision: f.profileA.Revision}
			}
			_, err := f.runtime.EgressDefaults().SetDefault(context.Background(), egressprofile.SetDefaultCommand{ExpectedRevision: revision, Profile: profile})
			return err
		}
		capture := f.wire.before
		if edge == "send_edit" {
			f.wire.before = func() {
				capture()
				if f.wire.sends.Load() == 1 {
					f.startsMu.Lock()
					starts := len(f.starts)
					f.startsMu.Unlock()
					if starts != 1 || f.network.a.Load()+f.network.b.Load()+f.network.direct.Load() != 0 {
						t.Error("default edit must follow SQL start and precede first dial")
					}
					if err := editDefault(); err != nil {
						t.Error("wire default edit", err)
					}
				}
			}
		}
		if edge == "proxy_down" {
			f.wire.before = func() {
				capture()
				proxy := f.proxyB
				if choice == "AP" {
					proxy = f.proxyA
				}
				if err := proxy.Close(); err != nil {
					t.Error(err)
				}
			}
		}
		f.wire.rejectTLS = edge == "tls_failure"
		held := edge == "held_edit" || edge == "held_cancel" || edge == "failed_probe_cancel"
		if held {
			if _, err := f.runtime.EnterOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
				t.Fatal(err)
			}
		}
		fetch := func(ctx context.Context, lease providerauth.Lease) error {
			response, err := f.runtime.FetchEndpointModels(ctx, endpoint, lease, false)
			if response != nil && response.Body != nil {
				_, readErr := io.Copy(io.Discard, response.Body)
				err = errors.Join(err, readErr, response.Body.Close())
				if response.StatusCode != http.StatusOK {
					err = errors.Join(err, fmt.Errorf("models response status=%d", response.StatusCode))
				}
			}
			return err
		}
		done := make(chan error, 1)
		joined := false
		defer func() {
			cancel()
			if !joined {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("models worker did not join during cleanup")
				}
			}
		}()
		go func() { done <- fetch(ctx, lease) }()
		if held {
			auxWait(t, func() bool { return f.gate.Snapshot().QueuedRequests == 1 })
			catalog, err := wireprofile.BuiltInCatalog()
			if err != nil {
				t.Fatal(err)
			}
			planRef, err := wireprofile.NewTransportProfileRef(wireprofile.TransportProfileStandardH1Value)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := catalog.ResolveTransport(planRef)
			if err != nil {
				t.Fatal(err)
			}
			requested := plan.Requested()
			digest := sha256.Sum256([]byte(strings.Join([]string{string(egressaudit.PurposeUpstreamModelDiscovery), endpoint.ID.String(), endpoint.Origin.String(), upstreamendpoint.ModelsPath(endpoint.Origin), http.MethodGet, requested.Ref().String(), fmt.Sprintf("%d", requested.Revision())}, "\x00")))
			want := offlinehold.ProbeTarget{Kind: offlinehold.EgressProvider, Transport: offlinehold.ProbeTransportStrictTLS,
				TargetRef: endpoint.ID.String(), NetworkOrigin: "https://api.openai.com", HTTPAuthority: "api.openai.com", TLSServerName: "api.openai.com",
				PlanRevision: uint64(requested.Revision()), PlanDigest: hex.EncodeToString(digest[:]), EgressPolicy: expected.Profile().Policy}
			targets := f.gate.PendingProbeTargets()
			if len(targets) != 1 || targets[0] != want || want.PlanRevision == 0 || want.PlanDigest == "" || want.Validate() != nil || f.gate.Snapshot().QueuedByKind[offlinehold.EgressProvider] != 1 {
				t.Fatalf("actual Provider queue got=%+v want=%+v gate=%+v", targets, want, f.gate.Snapshot())
			}
			if f.network.a.Load()+f.network.b.Load()+f.network.direct.Load()+f.network.dns.Load()+f.wire.sends.Load()+gets.Load()+f.liveTLS.Load() != 0 {
				t.Fatal("held models operation started physical traffic")
			}
			assertNoAudit()
			t.Logf("queued actual Provider target=%+v gate=%+v sockets=0 GET=0 epoch=1", targets[0], f.gate.Snapshot())
			if edge != "held_cancel" {
				if err := editDefault(); err != nil {
					t.Fatal(err)
				}
			}
			if edge == "failed_probe_cancel" {
				if err := f.proxyB.Close(); err != nil {
					t.Fatal(err)
				}
				_, err := f.runtime.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision)
				if err == nil || f.gate.Snapshot().State != offlinehold.StateHeld || f.gate.Snapshot().QueuedRequests != 1 || gets.Load() != 0 {
					t.Fatalf("actual failed Provider Resume err=%v gate=%+v GET=%d", err, f.gate.Snapshot(), gets.Load())
				}
				assertNoAudit()
				t.Logf("failed real Provider Resume err=%v gate=%+v GET=0", err, f.gate.Snapshot())
			}
			if edge == "held_cancel" || edge == "failed_probe_cancel" {
				cancel()
			} else if _, err := f.runtime.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
				t.Fatal(err)
			}
		}
		var result error
		select {
		case result = <-done:
			joined = true
		case <-time.After(5 * time.Second):
			t.Fatal("models worker did not join")
		}
		success := edge == "send_edit" || edge == "held_edit"
		switch {
		case edge == "held_cancel" || edge == "failed_probe_cancel":
			if !errors.Is(result, context.Canceled) {
				t.Fatalf("models caller cancellation err=%v", result)
			}
		case success:
			if result != nil {
				t.Fatal(result)
			}
		default:
			if result == nil {
				t.Fatal("expected actual models send failure")
			}
			if edge == "tls_failure" {
				var unknown x509.UnknownAuthorityError
				if !errors.As(result, &unknown) {
					t.Fatalf("models TLS fault lost real certificate verification failure: %v", result)
				}
			}
		}
		t.Logf("first models worker joined err=%v", result)
		f.assertDrained(t)
		// Observe the first physical operation before acquiring or sending the
		// next one, so aggregate counters cannot hide exchanged routes.
		first := endpointModelsCounts{epoch: 1}
		if success || edge == "proxy_down" || edge == "tls_failure" {
			first.sends = 1
			if success {
				first.gets = 1
			}
			switch choice {
			case "AP":
				first.dialA, first.connectA, first.dns = 1, 1, 1
			case "G":
				first.dialB, first.connectB, first.dns = 1, 1, 1
			case "AD", "B":
				first.direct = 1
			}
			if edge == "proxy_down" {
				first.connectA, first.connectB = 0, 0
			}
		}
		if edge == "held_edit" {
			first.dialB++
			first.connectB++
			first.dns++
		} else if edge == "failed_probe_cancel" {
			first.dialB, first.dns = 1, 1
		}
		firstMetadata, err := f.secrets.Inspect(t.Context(), account.Account.SecretRef)
		if err != nil {
			t.Fatal(err)
		}
		firstGot := endpointModelsCounts{f.network.a.Load(), f.network.b.Load(), f.network.direct.Load(), f.callsA.Load(), f.callsB.Load(), f.wire.sends.Load(), gets.Load(), f.network.dns.Load(), f.secrets.creates.Load(), f.secrets.rotations.Load(), int64(firstMetadata.Revision)}
		if firstGot != first || bad.Load() != 0 || f.network.denied.Load() != 0 {
			t.Fatalf("first models physical counts got=%+v want=%+v badHTTP=%d denied=%d", firstGot, first, bad.Load(), f.network.denied.Load())
		}
		t.Logf("named models operation=first physical counts=%+v denied=0 badHTTP=0", firstGot)
		if success || edge == "failed_probe_cancel" {
			setting, err := f.runtime.EgressDefaults().GetDefault(t.Context())
			wantRevision := uint64(3)
			if choice == "B" {
				wantRevision = 2
			}
			if err != nil || setting.Revision != wantRevision || (choice == "B" && (setting.Profile == nil || !setting.Profile.Equal(f.profileA))) || (choice != "B" && setting.Profile != nil) {
				t.Fatalf("actual edited default=%+v err=%v want revision=%d", setting, err, wantRevision)
			}
			t.Logf("actual default edit persisted revision=%d", setting.Revision)
		}
		retainedRef, retainedOK := lease.Account()
		if !retainedOK || retainedRef != ref {
			t.Fatal("original endpoint lease changed after wait/default edit/send")
		}
		lease.Release()
		if edge == "held_cancel" || edge == "failed_probe_cancel" {
			assertNoAudit()
			if _, err := f.runtime.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
				t.Fatal("empty models queue resume", err)
			}
			f.assertDrained(t)
			assertNoAudit()
			t.Log("empty queue Resume completed; canceled models operation made no late GET")
		} else {
			outcome := egressaudit.OutcomeCompleted
			if !success {
				outcome = egressaudit.OutcomeFailed
			}
			auxAssertAudit(t, f, egressaudit.PurposeUpstreamModelDiscovery, "https://api.openai.com", expected, account.Account.ID.String(), account.Account.SettingsRevision, outcome)
			f.startsMu.Lock()
			paired := map[string]egressprofile.Resolved{}
			for id := range f.starts {
				paired[id] = expected
				t.Logf("named models operation=first attempt=%s", id)
			}
			f.startsMu.Unlock()
			if success {
				next := endpointModelsExpected(t, f, choice, true)
				nextLease, err := f.runtime.ProviderAccounts().AcquireEndpointCredential(t.Context(), account.Account.ID, endpoint)
				if err != nil {
					t.Fatal(err)
				}
				defer nextLease.Release()
				nextRef, nextOK := nextLease.Account()
				assertRef(nextRef, nextOK, next)
				t.Logf("next actual endpoint lease source=%s default=%d profile=%s/%d epoch=%d", next.Source(), next.DefaultRevision(), next.Profile().ID, next.Profile().Revision, nextRef.CredentialEpoch)
				if err := fetch(t.Context(), nextLease); err != nil {
					t.Fatal(err)
				}
				nextLease.Release()
				f.assertDrained(t)
				f.startsMu.Lock()
				for id := range f.starts {
					if _, first := paired[id]; !first {
						paired[id] = next
						t.Logf("named models operation=next attempt=%s", id)
					}
				}
				f.startsMu.Unlock()
				if len(paired) != 2 {
					t.Fatalf("models independent operation attempts=%d want=2", len(paired))
				}
			}
			endpointModelsAudit(t, f, account, paired, outcome)
		}
	}
	metadata, err := f.secrets.Inspect(t.Context(), account.Account.SecretRef)
	if err != nil {
		t.Fatal(err)
	}
	retainedSecret := readSecret()
	defer clear(retainedSecret)
	if !bytes.Equal(retainedSecret, initialSecret) || metadata.Revision != 1 {
		t.Fatal("model discovery changed physical credential/header material or epoch")
	}
	want := endpointModelsCounts{epoch: 1}
	if edge == "send_edit" || edge == "held_edit" {
		want.sends, want.gets = 2, 2
		switch choice {
		case "AP":
			want.dialA, want.connectA, want.dns = 2, 2, 2
		case "AD":
			want.direct = 2
		case "G":
			want.dialB, want.connectB, want.direct, want.dns = 1, 1, 1, 1
		case "B":
			want.direct, want.dialA, want.connectA, want.dns = 1, 1, 1, 1
		}
		if edge == "held_edit" {
			want.dialB++
			want.connectB++
			want.dns++
		}
	} else if edge == "proxy_down" {
		want.sends, want.dns = 1, 1
		if choice == "AP" {
			want.dialA = 1
		} else {
			want.dialB = 1
		}
	} else if edge == "tls_failure" {
		want.sends, want.direct = 1, 1
	} else if edge == "failed_probe_cancel" {
		want.dialB, want.dns = 1, 1
	}
	got := endpointModelsCounts{f.network.a.Load(), f.network.b.Load(), f.network.direct.Load(), f.callsA.Load(), f.callsB.Load(), f.wire.sends.Load(), gets.Load(), f.network.dns.Load(), f.secrets.creates.Load(), f.secrets.rotations.Load(), int64(metadata.Revision)}
	if got != want || bad.Load() != 0 || f.network.denied.Load() != 0 {
		t.Fatalf("models literal counts got=%+v want=%+v badHTTP=%d denied=%d", got, want, bad.Load(), f.network.denied.Load())
	}
	t.Logf("models literal counts=%+v denied=%d badHTTP=%d; physical secret/header bytes exactly retained", got, f.network.denied.Load(), bad.Load())
	f.assertDrained(t)
	rejections := 0
	if edge == "tls_failure" {
		rejections = 1
	}
	f.assertServerDiagnostics(t, rejections)
}
