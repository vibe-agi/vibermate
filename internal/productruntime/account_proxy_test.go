package productruntime

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clienttarget"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/providerauth"
)

// Uses the production proxy, CA, capture assignment and account runtime, with
// synthetic accounts and the same external provider boundary as the reader test.
// Use the launcher's ClientProfile too: a bare Capture omits the client's model
// API base-path scope and cannot catch failures in native /status or /usage.
func (f accountReadFixture) serveProxy(t *testing.T) *url.URL {
	t.Helper()
	ctx := context.Background()
	draft, err := f.runtime.environments.SaveDraft(ctx, environment.DraftCommand{Candidate: f.aggregate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.runtime.environments.Preview(ctx, f.aggregate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.runtime.environments.Publish(ctx, preview); err != nil || result.Outcome != environment.CommitOutcomeCommitted {
		t.Fatalf("publish account fixture: %v", err)
	}
	dir := t.TempDir()
	grant, err := f.runtime.captureRuns.Create(ctx, capturerun.CreateCommand{
		CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "codex"), ExecutableLabel: "codex",
		Lifetime: 5 * time.Minute, CatalogRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	capture, err := captureidentity.New(captureidentity.KindManagedRun, grant.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := clienttarget.NewProfile("codex-cli", clienttarget.EnvironmentFacts{})
	if err != nil {
		t.Fatal(err)
	}
	assignment, _, err := f.runtime.assignments.CreateForLaunch(ctx, captureassignment.CreateCommand{
		Capture: capture, EnvironmentID: f.aggregate.ID, Source: captureassignment.SourceLaunch, ClientProfile: profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if assignment.ClientTarget.ActualOrigin().String() != "https://chatgpt.com/backend-api/codex" {
		t.Fatal("fixture lost the native Codex launch target and its model API base path")
	}
	admissions, err := captureadmission.NewAuthorizer(f.runtime.captureRuns, f.runtime.manualCaptures)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := (connectionpolicy.Snapshot{Revision: 1, Mode: connectionpolicy.ModeDenyUnknown,
		Rules: []connectionpolicy.Rule{{ID: "fixture.chatgpt", Priority: 1, Decision: connectionpolicy.DecisionAllow,
			Match: connectionpolicy.MatchExactHostPort("chatgpt.com", 443)}}}).Compile()
	if err != nil {
		t.Fatal(err)
	}
	blind, err := newBlindTunnelDialer(f.runtime.offlineHold)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := buildProxy(proxyBuildRequest{
		ownerContext: ctx, admissions: admissions, assignments: f.runtime.assignments,
		exchanges: f.runtime.exchanges, original: accountFixtureOriginal{}, accountReads: f.reader,
		certificates: f.runtime.localCA, connections: f.runtime.connections,
		policy: connectionpolicy.NewLive(policy), approvals: f.runtime.approvals, blindTunnels: blind,
		egressAudit: f.runtime.egressCompletion, random: rand.Reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(proxy)
	t.Cleanup(func() {
		server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := proxy.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	target, _ := url.Parse(server.URL)
	target.User = url.UserPassword("capture", grant.ProxyCapability.Value())
	return target
}

// Native CLI startup may ask for unrelated model catalogs. Do not send its
// synthetic credential to a real origin during this acceptance test.
type accountFixtureOriginal struct{}

func (accountFixtureOriginal) Do(context.Context, originaltransport.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"error":"unrelated operation disabled in synthetic acceptance"}`))}, nil
}

func TestManagedAccountQueriesThroughCONNECT(t *testing.T) {
	for _, test := range []struct {
		name           string
		historyAllowed bool
		http2          bool
		oauth          bool
	}{
		{name: "history_denied"},
		{name: "history_allowed", historyAllowed: true},
		{name: "http2_history_denied", http2: true},
		{name: "http2_history_allowed", historyAllowed: true, http2: true},
		{name: "oauth_history_denied", oauth: true},
		{name: "oauth_history_allowed", historyAllowed: true, oauth: true},
		{name: "http2_oauth_history_denied", http2: true, oauth: true},
		{name: "http2_oauth_history_allowed", historyAllowed: true, http2: true, oauth: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			historyAllowed := test.historyAllowed
			driver := providerauth.StaticHeaderDriverRef()
			if test.oauth {
				driver = providerauth.CodexOAuthDriverRef()
			}
			f := newAccountReadFixtureWithDriver(t, driver)
			f.aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].AllowAccountHistory = historyAllowed
			proxyURL := f.serveProxy(t)
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(f.runtime.LocalRootCertificate().CertificatePEM()) {
				t.Fatal("fixture CA invalid")
			}
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, ForceAttemptHTTP2: test.http2}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
			send := func(method, path string) (int, string, http.Header) {
				request, _ := http.NewRequest(method, "https://chatgpt.com"+path, nil)
				request.Header.Set("Authorization", "Bearer original-A")
				request.Header.Set("ChatGPT-Account-Id", "workspace-A")
				request.Header.Set("Cookie", "original-A-cookie")
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if test.http2 && response.ProtoMajor != 2 {
					t.Fatal("account query did not negotiate HTTP/2")
				}
				body, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				return response.StatusCode, string(body), response.Header
			}
			status, body, headers := send("GET", "/backend-api/wham/usage")
			if status != 200 || !strings.Contains(body, `"account_id":"workspace-B"`) || headers.Get("Cache-Control") != "no-store" {
				t.Fatalf("managed quota query failed: status %d", status)
			}
			f.wire.mu.Lock()
			request := f.wire.requests[0]
			f.wire.mu.Unlock()
			if request.Header.Get("Authorization") != "Bearer "+f.token || request.Header.Get("Chatgpt-Account-Id") != "workspace-B" || request.Header.Get("Cookie") != "" {
				t.Fatal("original account credential or cookie reached the managed provider")
			}
			status, body, _ = send("GET", "/backend-api/wham/profiles/me")
			expected, attempts := 403, 1
			if historyAllowed {
				expected, attempts = 200, 2
			}
			if status != expected || historyAllowed && !strings.Contains(body, "1200") {
				t.Fatalf("history scope: status %d", status)
			}
			for _, rejected := range []struct{ method, path string }{
				{"POST", "/backend-api/wham/rate-limit-reset-credits/consume"},
				{"GET", "/backend-api/wham/rate-limit-reset-credits"},
				{"GET", "/backend-api/wham/usage/other"},
				{"GET", "/backend-api/wham/usage?account_id=other"},
				{"POST", "/v1/responses"},
			} {
				status, body, _ = send(rejected.method, rejected.path)
				if status != http.StatusUnprocessableEntity || !strings.Contains(body, "client_target_path_not_allowed") {
					t.Fatalf("client target scope did not reject %s %s precisely: status %d", rejected.method, rejected.path, status)
				}
			}
			f.wire.mu.Lock()
			defer f.wire.mu.Unlock()
			if len(f.wire.requests) != attempts {
				t.Fatal("denied operation escaped to provider")
			}
		})
	}
}
