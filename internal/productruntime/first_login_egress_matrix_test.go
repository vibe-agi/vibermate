package productruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

// These cases catch reselecting the default after Start, invented account
// authority, token retries, and lost account/secret persistence.
func TestActualRuntimeFirstLoginEgressMatrix(t *testing.T) {
	for _, row := range [][2]string{{"G", "start_edit_complete"}, {"B", "start_edit_complete"},
		{"G", "held_edit"}, {"B", "held_edit"}, {"G", "proxy_down"}} {
		t.Run(row[0]+"/"+row[1], func(t *testing.T) { runFirstLoginAuxCase(t, row[0], row[1]) })
	}
}

// These cases catch abandoned Login-owned exchanges and a Runtime cleanup
// stack that closes provider/Store while retaining pending Login resources.
func TestActualRuntimeFirstLoginOwnershipAndDrain(t *testing.T) {
	for _, edge := range []string{"pending_cancel", "caller_cancel_owned_success", "failed_probe_shutdown",
		"held_shutdown", "pending_shutdown", "pending_loopback_shutdown"} {
		t.Run(edge, func(t *testing.T) { runFirstLoginAuxCase(t, "G", edge) })
	}
}

type firstLoginCounts struct {
	dialA, dialB, direct, connectA, connectB, sends, posts, creates, rotations int64
}

// Every independently named operation is paired with its own observed SQL ID.
func firstLoginAudit(t *testing.T, f *auxFixture, expected map[string]egressprofile.Resolved, outcome egressaudit.Outcome) {
	t.Helper()
	var page egressaudit.Page
	var err error
	if len(expected) == 0 {
		page, err = f.runtime.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 100})
	} else {
		page, err = productruntime.SettledEgressTestPage(t, f.runtime, egressaudit.PageRequest{Limit: 100})
	}
	if err != nil || len(page.Items) != len(expected) {
		t.Fatalf("login SQL terminals=%d want=%d err=%v", len(page.Items), len(expected), err)
	}
	authority, err := egressaudit.AuthorityForPurpose(egressaudit.PurposeCredentialRefresh)
	if err != nil {
		t.Fatal(err)
	}
	f.startsMu.Lock()
	defer f.startsMu.Unlock()
	if len(f.starts) != len(expected) || len(f.parents) != len(expected) {
		t.Fatalf("login SQL starts=%d parents=%d want=%d", len(f.starts), len(f.parents), len(expected))
	}
	parents := map[string]bool{}
	for _, record := range page.Items {
		attempt := record.Attempt
		resolved, exists := expected[attempt.ID()]
		if !exists {
			t.Fatalf("unpaired login SQL attempt=%s", attempt.ID())
		}
		want := egressaudit.BuiltInDirectDecision(authority)
		want.SelectionSource, want.DefaultRevision = resolved.Source(), resolved.DefaultRevision()
		want.ProxyID, want.ProxyRevision = resolved.Profile().ID.String(), uint64(resolved.Profile().Revision)
		if resolved.Source() == egressprofile.SelectionGlobal {
			want.PolicyID, want.PolicyRevision, want.RuleID = "runtime.egress-default", resolved.DefaultRevision(), "global.default"
		}
		start, startOK := f.starts[attempt.ID()]
		parent, parentOK := f.parents[attempt.ID()]
		if !startOK || !parentOK || start != want || attempt.Decision() != want || parent != attempt.Parent() ||
			!attempt.Terminal() || attempt.Outcome() != outcome || attempt.Purpose() != egressaudit.PurposeCredentialRefresh ||
			attempt.TargetOrigin() != "https://auth.openai.com" || attempt.PayloadClass() != egressaudit.PayloadRuntime ||
			attempt.Caller() != egressaudit.CallerCore || parent.Kind != egressaudit.ParentRuntimeAction || parent.ID == "" || parent.ExchangeID != "" || parents[parent.ID] {
			t.Fatalf("login immutable audit id=%s start=%+v terminal=%+v want=%+v parent=%+v outcome=%s", attempt.ID(), start, attempt.Decision(), want, parent, attempt.Outcome())
		}
		parents[parent.ID] = true
		t.Logf("login audit id=%s start=%+v terminal=%+v purpose=%s origin=%s parent=%+v outcome=%s", attempt.ID(), start, attempt.Decision(), attempt.Purpose(), attempt.TargetOrigin(), parent, attempt.Outcome())
	}
}

func runFirstLoginAuxCase(t *testing.T, choice, edge string) {
	t.Helper()
	var posts, bad atomic.Int64
	token, redirect := auxToken(), ""
	f := newAuxFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10+1))
		defer clear(body)
		form, parseErr := url.ParseQuery(string(body))
		valid := err == nil && parseErr == nil && len(body) <= 64<<10 && r.Method == http.MethodPost &&
			r.Host == "auth.openai.com" && r.URL.RequestURI() == "/oauth/token" && r.Header.Get("Authorization") == "" &&
			r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" && len(form) == 5 &&
			form.Get("grant_type") == "authorization_code" && form.Get("client_id") == codexoauth.ClientID &&
			form.Get("code") == "synthetic-code" && form.Get("redirect_uri") == redirect && form.Get("code_verifier") != ""
		for _, values := range form {
			valid = valid && len(values) == 1
		}
		if !valid {
			bad.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		posts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": token, "access_token": token, "refresh_token": "synthetic-login-refresh"})
	}))
	// Runtime bootstrap creates its unrelated annotation signing key. Measure
	// the Login operation from the established Runtime, before Start.
	initialAccounts, err := f.runtime.ProviderAccounts().List(t.Context())
	if err != nil || len(initialAccounts) != 0 {
		t.Fatalf("first Login requires empty Runtime accounts count=%d err=%v", len(initialAccounts), err)
	}
	t.Logf("established Runtime bootstrap secret effects creates=%d rotations=%d accounts=0; begin Login effect counters", f.secrets.creates.Load(), f.secrets.rotations.Load())
	f.secrets.creates.Store(0)
	f.secrets.rotations.Store(0)
	if choice == "G" {
		f.setDefault(t, 1, &f.profileB)
	}
	expected := auxExpected(t, f, choice, false)
	logins := f.runtime.CodexOAuthLogins()
	// Safety cleanup runs only after the actual Runtime assertions. On the RED
	// baseline it releases this fixture's pending timer/owned listener too.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if manager, ok := logins.(interface{ Shutdown(context.Context) error }); ok {
			if err := manager.Shutdown(ctx); err != nil {
				t.Error("fixture Login cleanup", err)
			}
		}
	})
	counts := func() firstLoginCounts {
		return firstLoginCounts{f.network.a.Load(), f.network.b.Load(), f.network.direct.Load(), f.callsA.Load(), f.callsB.Load(),
			f.wire.sends.Load(), posts.Load(), f.secrets.creates.Load(), f.secrets.rotations.Load()}
	}
	assertCounts := func(want firstLoginCounts) {
		t.Helper()
		got := counts()
		if got != want || bad.Load() != 0 || f.network.denied.Load() != 0 {
			t.Fatalf("login literal counts got=%+v want=%+v badHTTP=%d denied=%d", got, want, bad.Load(), f.network.denied.Load())
		}
		t.Logf("login literal counts=%+v DNS=%d denied=%d badHTTP=%d", got, f.network.dns.Load(), f.network.denied.Load(), bad.Load())
	}
	assertNoAccount := func() {
		t.Helper()
		_, err := f.runtime.ProviderAccounts().Get(t.Context(), "account.aux-login")
		list, listErr := f.runtime.ProviderAccounts().List(t.Context())
		if !errors.Is(err, provideraccount.ErrAccountNotFound) || listErr != nil || len(list) != 0 || f.secrets.creates.Load() != 0 || f.secrets.rotations.Load() != 0 {
			t.Fatalf("login account must be absent: get=%v list=%v count=%d creates=%d rotations=%d", err, listErr, len(list), f.secrets.creates.Load(), f.secrets.rotations.Load())
		}
		t.Log("actual Runtime accounts=0 physical secret creates=0 rotations=0")
	}
	assertNoAudit := func() {
		t.Helper()
		firstLoginAudit(t, f, map[string]egressprofile.Resolved{}, egressaudit.OutcomeCompleted)
		t.Log("login outgoing SQL starts=0 terminals=0")
	}
	start := func(id provideraccount.ID, mode string) codexoauth.LoginView {
		t.Helper()
		before := counts()
		view, err := logins.Start(t.Context(), "synthetic-owner", codexoauth.LoginAccount{Mode: "create", ID: id.String(),
			EndpointID: upstreamendpoint.ChatGPTOfficialID.String(), DisplayName: "Synthetic aux login"}, mode)
		if err != nil || view.State != "pending" || view.ID == "" {
			t.Fatalf("actual Login Start state=%s err=%v", view.State, err)
		}
		if counts() != before {
			t.Fatal("Login Start produced physical token/account/secret effects")
		}
		t.Logf("actual Login Start pending mode=%s; physical counters unchanged", view.CallbackMode)
		return view
	}
	mode := "manual"
	if edge == "pending_loopback_shutdown" {
		mode = "loopback"
	}
	view := start("account.aux-login", mode)
	callback := auxCallback(t, view)
	auth, err := url.Parse(view.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	redirect = auth.Query().Get("redirect_uri")
	assertCounts(firstLoginCounts{})
	assertNoAccount()
	assertNoAudit()
	editDefault := func() {
		if choice == "B" {
			f.setDefault(t, 1, &f.profileA)
		} else {
			f.setDefault(t, 2, nil)
		}
	}
	assertUnavailable := func() {
		t.Helper()
		status, err := logins.Status(context.Background(), "synthetic-owner", view.ID)
		if !errors.Is(err, codexoauth.ErrLoginNotFound) {
			t.Errorf("Runtime shutdown retained Login state=%s err=%v; want ErrLoginNotFound", status.State, err)
		} else {
			t.Log("retained actual Login controller unavailable after Runtime shutdown")
		}
	}
	if edge == "pending_shutdown" || edge == "pending_loopback_shutdown" {
		address := ""
		if mode == "loopback" {
			if view.CallbackMode != "loopback" {
				t.Log("UNVERIFIED pending_loopback_shutdown: both existing Login loopback ports unavailable; manual row remains required")
				f.shutdown(t)
				assertUnavailable()
				t.Skip("owned loopback listener unavailable; this listener row is not proved")
			}
			u, err := url.Parse(redirect)
			if err != nil || u.Scheme != "http" || u.Hostname() != "localhost" || (u.Port() != "1455" && u.Port() != "1457") || u.Path != "/auth/callback" {
				t.Fatal("unexpected trusted loopback redirect")
			}
			address = net.JoinHostPort("127.0.0.1", u.Port())
			conn, err := net.DialTimeout("tcp4", address, 200*time.Millisecond)
			if err != nil {
				t.Fatal("Login's newly owned listener unavailable", err)
			}
			_ = conn.Close()
			t.Logf("actual Login owns contained listener %s; bounded dial joined", address)
		}
		f.shutdown(t)
		assertUnavailable()
		if address != "" {
			until := time.Now().Add(2 * time.Second)
			for {
				conn, err := net.DialTimeout("tcp4", address, 100*time.Millisecond)
				if err != nil {
					t.Logf("owned listener %s refuses bounded dial after Runtime shutdown", address)
					break
				}
				_ = conn.Close()
				if time.Now().After(until) {
					t.Error("Runtime shutdown retained Login-owned loopback listener")
					break
				}
				time.Sleep(time.Millisecond)
			}
		}
		assertCounts(firstLoginCounts{})
		f.assertDrained(t)
		f.assertServerDiagnostics(t, 0)
		return
	}
	if edge == "pending_cancel" {
		if err := logins.Cancel(t.Context(), "synthetic-owner", view.ID); err != nil {
			t.Fatal(err)
		}
		status, err := logins.Status(t.Context(), "synthetic-owner", view.ID)
		terminal, completeErr := logins.Complete(t.Context(), "synthetic-owner", view.ID, callback)
		if err != nil || completeErr != nil || status.State != "cancelled" || terminal != status {
			t.Fatalf("pending Cancel status=%s terminal=%s err=%v/%v", status.State, terminal.State, err, completeErr)
		}
		assertNoAccount()
		assertNoAudit()
		assertCounts(firstLoginCounts{})
		f.assertDrained(t)
		f.assertServerDiagnostics(t, 0)
		return
	}
	held := edge == "held_edit" || edge == "caller_cancel_owned_success" || edge == "failed_probe_shutdown" || edge == "held_shutdown"
	if held {
		if _, err := f.runtime.EnterOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
			t.Fatal(err)
		}
	} else {
		editDefault()
	}
	if edge == "proxy_down" {
		if err := f.proxyB.Close(); err != nil {
			t.Fatal(err)
		}
	}
	type completion struct {
		view codexoauth.LoginView
		err  error
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan completion, 1)
	go func() {
		result, err := logins.Complete(ctx, "synthetic-owner", view.ID, callback)
		done <- completion{result, err}
	}()
	shutdown := edge == "held_shutdown" || edge == "failed_probe_shutdown"
	if held {
		auxWait(t, func() bool { return f.gate.Snapshot().QueuedRequests == 1 })
		status, err := logins.Status(t.Context(), "synthetic-owner", view.ID)
		targets := f.gate.PendingProbeTargets()
		want := offlinehold.ProbeTarget{Kind: offlinehold.EgressAuxiliary, Transport: offlinehold.ProbeTransportStrictTLS,
			TargetRef: "https://auth.openai.com", NetworkOrigin: "https://auth.openai.com", HTTPAuthority: "auth.openai.com",
			TLSServerName: "auth.openai.com", EgressPolicy: expected.Profile().Policy, TargetAccess: egressnetwork.TargetAccessOwnerConfigured}
		if err != nil || status.State != "exchanging" || len(targets) != 1 || targets[0] != want || f.network.dns.Load() != 0 {
			t.Fatalf("actual Login frozen queue status=%s err=%v targets=%+v want=%+v", status.State, err, targets, want)
		}
		assertNoAccount()
		assertNoAudit()
		assertCounts(firstLoginCounts{})
		t.Logf("actual Login exchanging queued target=%+v gate=%+v", targets[0], f.gate.Snapshot())
		if edge == "caller_cancel_owned_success" {
			cancel()
			if err := logins.Cancel(context.Background(), "synthetic-owner", view.ID); !errors.Is(err, codexoauth.ErrLoginBusy) {
				t.Fatalf("owned Login Cancel=%v want Busy", err)
			}
			if _, err := logins.Complete(context.Background(), "synthetic-owner", view.ID, callback); !errors.Is(err, codexoauth.ErrLoginBusy) {
				t.Fatalf("owned duplicate Complete=%v want Busy", err)
			}
			select {
			case <-done:
				t.Fatal("caller cancellation released Login-owned exchange")
			default:
			}
			assertNoAccount()
			t.Log("caller canceled; actual Login retains owned exchange; Cancel/duplicate Complete return Busy")
		}
		editDefault()
		if edge == "failed_probe_shutdown" {
			if err := f.proxyB.Close(); err != nil {
				t.Fatal(err)
			}
			_, err := f.runtime.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision)
			status, statusErr := logins.Status(t.Context(), "synthetic-owner", view.ID)
			if err == nil || statusErr != nil || status.State != "exchanging" || f.gate.Snapshot().State != offlinehold.StateHeld || f.gate.Snapshot().QueuedRequests != 1 {
				t.Fatalf("failed actual Resume err=%v status=%s/%v gate=%+v", err, status.State, statusErr, f.gate.Snapshot())
			}
			assertNoAccount()
			assertNoAudit()
			assertCounts(firstLoginCounts{dialB: 1})
			t.Logf("failed concrete Resume retains exchanging Login and queue err=%v", err)
		}
		if shutdown {
			assertNoAccount()
			assertNoAudit()
			f.shutdown(t)
		} else if _, err := f.runtime.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
			t.Fatal(err)
		}
	}
	var result completion
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("actual Login exchange did not join")
	}
	success := !shutdown && edge != "proxy_down"
	if result.err != nil || success && (result.view.State != "completed" || result.view.AccountID != "account.aux-login" || result.view.Reason != "") ||
		!success && (result.view.State != "failed" || result.view.Reason != "login_exchange_failed" || result.view.AccountID != "") {
		t.Fatalf("actual Login completion state=%s reason=%s err=%v", result.view.State, result.view.Reason, result.err)
	}
	t.Logf("joined actual Login completion state=%s reason=%s err=%v", result.view.State, result.view.Reason, result.err)
	f.assertDrained(t)
	f.assertServerDiagnostics(t, 0)
	if shutdown {
		assertUnavailable()
		want := firstLoginCounts{}
		if edge == "failed_probe_shutdown" {
			want.dialB = 1
		}
		assertCounts(want)
		return // Store is closed; joined worker and physical secret counters prove no late save.
	}
	if !success {
		assertNoAccount()
		assertCounts(firstLoginCounts{dialB: 1, sends: 1})
		auxAssertAudit(t, f, egressaudit.PurposeCredentialRefresh, "https://auth.openai.com", expected, "", 0, egressaudit.OutcomeFailed)
		return
	}
	assertAccount := func(id provideraccount.ID, total int) {
		t.Helper()
		account, err := f.runtime.ProviderAccounts().Get(t.Context(), id)
		list, listErr := f.runtime.ProviderAccounts().List(t.Context())
		if err != nil || listErr != nil || len(list) != total || account.Account.ID != id || account.Account.DisplayName != "Synthetic aux login" ||
			!account.Account.AutomaticRefresh || len(account.Account.Associations.IDs()) != 0 || account.Health.CredentialEpoch != 1 ||
			account.Account.Driver != providerauth.CodexOAuthDriverRef() || !upstreamendpoint.IsChatGPTCodexOrigin(account.Account.Origin) {
			t.Fatalf("actual Login persistence account=%+v get=%v list=%v count=%d want=%d", account, err, listErr, len(list), total)
		}
		credential := auxCredentialBytes(t, f, account, 1)
		defer clear(credential)
		var content struct {
			IDToken string `json:"idToken"`
			Access  string `json:"accessToken"`
			Refresh string `json:"refreshToken"`
		}
		if err := json.Unmarshal(credential, &content); err != nil || content.IDToken != token || content.Access != token || content.Refresh != "synthetic-login-refresh" {
			t.Fatal("physical Login credential differs from synthetic response")
		}
		t.Logf("actual Runtime persisted account=%s automatic=true associations=0 physical secret epoch=1 exact synthetic ID/access/refresh bytes verified accounts=%d", id, total)
	}
	assertAccount("account.aux-login", 1)
	want := firstLoginCounts{sends: 1, posts: 1, creates: 1}
	sockets := int64(1)
	if held {
		sockets = 2
	}
	if choice == "G" {
		want.dialB, want.connectB = sockets, sockets
	} else {
		want.direct = sockets
	}
	assertCounts(want)
	auxAssertAudit(t, f, egressaudit.PurposeCredentialRefresh, "https://auth.openai.com", expected, "", 0, egressaudit.OutcomeCompleted)
	before := counts()
	replayed, err := logins.Complete(context.Background(), "synthetic-owner", view.ID, callback)
	if err != nil || replayed != result.view || counts() != before {
		t.Fatal("terminal Login replay exchanged or saved again")
	}
	page, err := f.runtime.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 100})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("first Login audit pairing unavailable", err)
	}
	firstID := page.Items[0].Attempt.ID()
	next := start("account.aux-login-next", "manual")
	if next.ID == view.ID {
		t.Fatal("independent Login reused ID")
	}
	firstLoginAudit(t, f, map[string]egressprofile.Resolved{firstID: expected}, egressaudit.OutcomeCompleted)
	terminal, err := logins.Complete(t.Context(), "synthetic-owner", next.ID, auxCallback(t, next))
	if err != nil || terminal.State != "completed" || terminal.AccountID != "account.aux-login-next" {
		t.Fatalf("next actual Login state=%s err=%v", terminal.State, err)
	}
	assertAccount("account.aux-login-next", 2)
	want.sends, want.posts, want.creates = 2, 2, 2
	if choice == "G" {
		want.direct = 1
	} else {
		want.dialA, want.connectA = 1, 1
	}
	assertCounts(want)
	page, err = productruntime.SettledEgressTestPage(t, f.runtime, egressaudit.PageRequest{Limit: 100})
	if err != nil || len(page.Items) != 2 {
		t.Fatal("next Login audit pairing unavailable", err)
	}
	pairs := map[string]egressprofile.Resolved{firstID: expected}
	for _, record := range page.Items {
		if record.Attempt.ID() != firstID {
			pairs[record.Attempt.ID()] = auxExpected(t, f, choice, true)
		}
	}
	firstLoginAudit(t, f, pairs, egressaudit.OutcomeCompleted)
	f.assertDrained(t)
	f.assertServerDiagnostics(t, 0)
}
