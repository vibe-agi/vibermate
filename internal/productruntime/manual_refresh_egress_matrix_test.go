package productruntime_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestActualRuntimeManualRefreshEgressMatrix(t *testing.T) {
	for _, choice := range []string{"AP", "AD", "G", "B"} {
		t.Run(choice, func(t *testing.T) { runManualAuxCase(t, choice, "send_edit") })
	}
}

func TestActualRuntimeManualRefreshFailureAndOwnership(t *testing.T) {
	for _, row := range [][2]string{
		{"G", "held_edit"}, {"G", "caller_cancel_owned_success"},
		{"G", "cancel_before_entry"}, {"G", "failed_probe_shutdown"},
		{"G", "held_shutdown"}, {"AP", "proxy_down"}, {"G", "proxy_down"},
		{"AD", "tls_failure"}, {"B", "tls_failure"}, {"G", "token_503"},
	} {
		t.Run(row[0]+"/"+row[1], func(t *testing.T) { runManualAuxCase(t, row[0], row[1]) })
	}
}

func auxExpected(t *testing.T, f *auxFixture, choice string, next bool) egressprofile.Resolved {
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
		t.Fatal("unknown auxiliary choice", choice)
	}
	resolved, err := egressprofile.RestoreResolved(profile, source, revision)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

type manualAuxCounts struct{ dialA, dialB, direct, connectA, connectB, sends, posts, creates, rotations, epoch int64 }

// Read only the fixture's physical configured secret; never infer rotation
// from a successful controller return or its safe health projection.
func auxCredentialBytes(t *testing.T, f *auxFixture, account provideraccount.View, epoch secretstore.Revision) []byte {
	t.Helper()
	value, err := f.secrets.ReadAtRevision(context.Background(), account.Account.SecretRef, epoch)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	wrapped, err := value.CopyBytes()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(wrapped)
	material, err := providerauth.ParseMaterial(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	return material.CredentialBytes()
}

func runManualAuxCase(t *testing.T, choice, edge string) {
	t.Helper()
	var posts, bad atomic.Int64
	token := auxToken()
	f := newAuxFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10+1))
		defer clear(body)
		var payload map[string]string
		valid := err == nil && r.Method == http.MethodPost && r.Host == "auth.openai.com" && r.URL.RequestURI() == "/oauth/token" &&
			r.Header.Get("Authorization") == "" && r.Header.Get("Content-Type") == "application/json" &&
			json.Unmarshal(body, &payload) == nil && len(payload) == 3 && payload["grant_type"] == "refresh_token" &&
			payload["refresh_token"] == "synthetic-refresh-old" && payload["client_id"] == codexoauth.ClientID
		if !valid {
			bad.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		posts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if edge == "token_503" {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"error":"synthetic-unavailable"}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": token, "refresh_token": "synthetic-refresh-new"})
	}))
	account := auxSetChoice(t, f, auxCreateAccount(t, f, "account.manual-aux", true), choice)
	expected := auxExpected(t, f, choice, false)
	f.secrets.creates.Store(0)
	f.secrets.rotations.Store(0)
	initialCredential := auxCredentialBytes(t, f, account, 1)
	defer clear(initialCredential)
	if account.Health.CredentialEpoch != 1 || account.Account.AutomaticRefresh {
		t.Fatalf("synthetic manual account setup=%+v", account)
	}
	assertEpoch := func(want int64) {
		t.Helper()
		metadata, err := f.secrets.Inspect(context.Background(), account.Account.SecretRef)
		if err != nil || int64(metadata.Revision) != want {
			t.Fatalf("physical secret metadata=%+v err=%v want epoch=%d", metadata, err, want)
		}
	}
	assertNoAudit := func() {
		t.Helper()
		page, err := f.runtime.EgressAttempts().List(context.Background(), egressaudit.PageRequest{Limit: 100})
		if err != nil || len(page.Items) != 0 {
			t.Fatalf("unexpected queued/canceled audit count=%d err=%v", len(page.Items), err)
		}
		f.startsMu.Lock()
		starts := len(f.starts)
		f.startsMu.Unlock()
		if starts != 0 {
			t.Fatalf("unexpected outgoing audit starts=%d", starts)
		}
		t.Log("outgoing SQL starts=0 terminals=0")
	}
	editDefault := func() {
		if choice == "B" {
			f.setDefault(t, 1, &f.profileA)
		} else {
			f.setDefault(t, 2, nil)
		}
	}
	if edge == "send_edit" {
		capture := f.wire.before
		f.wire.before = func() {
			capture()
			var ref *egressprofile.ProfileRef
			revision := uint64(2)
			if choice == "B" {
				revision = 1
				ref = &egressprofile.ProfileRef{ID: f.profileA.ID, Revision: f.profileA.Revision}
			}
			if _, err := f.runtime.EgressDefaults().SetDefault(context.Background(), egressprofile.SetDefaultCommand{ExpectedRevision: revision, Profile: ref}); err != nil {
				t.Error("wire default edit", err)
			}
		}
	}
	if edge == "proxy_down" {
		capture := f.wire.before
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
	held := edge == "held_edit" || edge == "caller_cancel_owned_success" || edge == "failed_probe_shutdown" || edge == "held_shutdown"
	if held {
		if _, err := f.runtime.EnterOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if edge == "cancel_before_entry" {
		cancel()
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.runtime.ProviderAccounts().RefreshCredential(ctx, account.Account.ID, 1)
		done <- err
	}()
	if held {
		auxWait(t, func() bool { return f.gate.Snapshot().QueuedRequests == 1 })
		targets := f.gate.PendingProbeTargets()
		want := offlinehold.ProbeTarget{Kind: offlinehold.EgressAuxiliary, Transport: offlinehold.ProbeTransportStrictTLS,
			TargetRef: "https://auth.openai.com", NetworkOrigin: "https://auth.openai.com", HTTPAuthority: "auth.openai.com",
			TLSServerName: "auth.openai.com", EgressPolicy: expected.Profile().Policy}
		if len(targets) != 1 || targets[0] != want {
			t.Fatalf("manual frozen queue got=%+v want=%+v", targets, want)
		}
		if f.network.a.Load()+f.network.b.Load()+f.network.direct.Load() != 0 || f.network.dns.Load() != 0 || f.network.denied.Load() != 0 || f.liveTLS.Load() != 0 || posts.Load() != 0 || f.wire.sends.Load() != 0 {
			t.Fatal("queued manual operation started a physical send")
		}
		assertEpoch(1)
		assertNoAudit()
		t.Logf("queued target=%+v gate=%+v sockets=0 POST=0 epoch=1", targets[0], f.gate.Snapshot())
		if _, err := f.runtime.ProviderAccounts().SetSettings(t.Context(), provideraccount.SettingsCommand{ID: account.Account.ID, ExpectedRevision: account.Account.SettingsRevision, EgressProfile: account.Account.EgressProfile, AutomaticRefresh: false}); !errors.Is(err, provideraccount.ErrOperationInProgress) {
			t.Fatalf("held account gate err=%v", err)
		}
		if edge == "caller_cancel_owned_success" {
			cancel()
			select {
			case err := <-done:
				t.Fatalf("caller cancellation released owned refresh: %v", err)
			default:
			}
			assertEpoch(1)
			if _, err := f.runtime.ProviderAccounts().SetSettings(t.Context(), provideraccount.SettingsCommand{ID: account.Account.ID, ExpectedRevision: account.Account.SettingsRevision, EgressProfile: account.Account.EgressProfile, AutomaticRefresh: false}); !errors.Is(err, provideraccount.ErrOperationInProgress) {
				t.Fatalf("caller canceled but lost owned account gate: %v", err)
			}
			t.Log("caller canceled; owned manual worker remains held; settings gate ErrOperationInProgress; epoch=1")
		}
		if edge != "held_shutdown" {
			editDefault()
		}
		if edge == "failed_probe_shutdown" {
			if err := f.proxyB.Close(); err != nil {
				t.Fatal(err)
			}
			_, err := f.runtime.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision)
			if err == nil || f.gate.Snapshot().State != offlinehold.StateHeld || f.gate.Snapshot().QueuedRequests != 1 || posts.Load() != 0 || f.secrets.rotations.Load() != 0 {
				t.Fatalf("failed concrete probe released queue: err=%v gate=%+v", err, f.gate.Snapshot())
			}
			assertNoAudit()
			t.Logf("failed real Resume err=%v gate=%+v POST=0 rotation=0", err, f.gate.Snapshot())
		}
		if edge == "held_shutdown" || edge == "failed_probe_shutdown" {
			assertNoAudit()
			f.shutdown(t)
		} else if _, err := f.runtime.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
			t.Fatal(err)
		}
	}
	var result error
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("manual worker did not join")
	}
	success := edge == "send_edit" || edge == "held_edit" || edge == "caller_cancel_owned_success"
	switch {
	case edge == "caller_cancel_owned_success" || edge == "cancel_before_entry":
		if !errors.Is(result, context.Canceled) {
			t.Fatalf("manual cancellation result=%v", result)
		}
	case edge == "token_503":
		if !errors.Is(result, codexoauth.ErrRefreshUnavailable) {
			t.Fatalf("manual 503 result=%v", result)
		}
	case success:
		if result != nil {
			t.Fatal(result)
		}
	default:
		if result == nil {
			t.Fatal("expected actual manual failure")
		}
	}
	f.assertDrained(t)
	rejections := 0
	if edge == "tls_failure" {
		rejections = 1
	}
	f.assertServerDiagnostics(t, rejections)
	want := manualAuxCounts{epoch: 1}
	if success {
		want.sends, want.posts, want.rotations, want.epoch = 1, 1, 1, 2
	}
	if edge == "proxy_down" || edge == "tls_failure" || edge == "token_503" {
		want.sends = 1
	}
	if edge == "token_503" {
		want.posts = 1
	}
	physical := success || edge == "proxy_down" || edge == "tls_failure" || edge == "token_503"
	if physical {
		switch choice {
		case "AP":
			want.dialA, want.connectA = 1, 1
		case "G":
			want.dialB, want.connectB = 1, 1
		case "AD", "B":
			want.direct = 1
		}
	}
	if held && edge != "held_shutdown" {
		want.dialB++
		if edge != "failed_probe_shutdown" {
			want.connectB++
		}
	}
	if edge == "proxy_down" {
		want.connectA, want.connectB = 0, 0
	}
	metadata, err := f.secrets.Inspect(context.Background(), account.Account.SecretRef)
	if err != nil {
		t.Fatal(err)
	}
	got := manualAuxCounts{f.network.a.Load(), f.network.b.Load(), f.network.direct.Load(), f.callsA.Load(), f.callsB.Load(), f.wire.sends.Load(), posts.Load(), f.secrets.creates.Load(), f.secrets.rotations.Load(), int64(metadata.Revision)}
	if got != want || bad.Load() != 0 || f.network.denied.Load() != 0 {
		t.Fatalf("manual literal counts got=%+v want=%+v bad=%d denied=%d", got, want, bad.Load(), f.network.denied.Load())
	}
	t.Logf("literal counts=%+v DNS=%d denied=%d badHTTP=%d worker=%v", got, f.network.dns.Load(), f.network.denied.Load(), bad.Load(), result)
	if physical {
		outcome := egressaudit.OutcomeCompleted
		if edge == "proxy_down" || edge == "tls_failure" {
			outcome = egressaudit.OutcomeFailed
		}
		auxAssertAudit(t, f, egressaudit.PurposeCredentialRefresh, "https://auth.openai.com", expected, account.Account.ID.String(), account.Account.SettingsRevision, outcome)
	} else if edge == "cancel_before_entry" {
		assertNoAudit()
	} else if edge == "held_shutdown" || edge == "failed_probe_shutdown" {
		// Runtime shutdown closes its reader. Inspect the same private persisted
		// database read-only after every worker has joined, proving no late audit.
		u := url.URL{Scheme: "file", Path: f.databasePath, RawQuery: "mode=ro"}
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM runtime_egress_attempts").Scan(&count); err != nil || count != 0 {
			t.Fatalf("shutdown persisted outgoing SQL count=%d err=%v", count, err)
		}
		t.Log("post-shutdown read-only persisted SQL starts=0 terminals=0; worker joined")
	}
	if !success {
		retained := auxCredentialBytes(t, f, account, 1)
		defer clear(retained)
		if !bytes.Equal(retained, initialCredential) {
			t.Fatal("failed/canceled manual refresh changed physical credential bytes")
		}
		t.Log("physical secret epoch=1 credential bytes exactly retained")
		return
	}
	view, err := f.runtime.ProviderAccounts().Get(t.Context(), account.Account.ID)
	if err != nil || view.Health.CredentialEpoch != 2 {
		t.Fatalf("manual account health=%+v err=%v", view.Health, err)
	}
	credential := auxCredentialBytes(t, f, account, 2)
	defer clear(credential)
	var content struct {
		Access  string `json:"accessToken"`
		Refresh string `json:"refreshToken"`
	}
	if err := json.Unmarshal(credential, &content); err != nil || content.Access != token || content.Refresh != "synthetic-refresh-new" {
		t.Fatalf("rotated physical credential content differs: err=%v", err)
	}
	t.Log("physical secret CAS epoch=2 access=handler-token refresh=synthetic-refresh-new account health epoch=2")
	endpoint, err := f.runtime.UpstreamEndpoints().Get(t.Context(), upstreamendpoint.ChatGPTOfficialID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.runtime.ProviderAccounts().AcquireEndpointCredential(t.Context(), account.Account.ID, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	ref, ok := lease.Account()
	next := auxExpected(t, f, choice, true)
	if !ok || ref.CredentialEpoch != 2 || !ref.EgressProfile.Equal(next.Profile()) || ref.ResolvedEgress.Source() != next.Source() || ref.ResolvedEgress.DefaultRevision() != next.DefaultRevision() {
		t.Fatalf("next endpoint lease account=%+v want=%+v", ref, next)
	}
	lease.Release()
	t.Logf("next endpoint lease source=%s default=%d profile=%s/%d epoch=%d; no next network send", next.Source(), next.DefaultRevision(), next.Profile().ID, next.Profile().Revision, ref.CredentialEpoch)
	f.assertDrained(t)
}
