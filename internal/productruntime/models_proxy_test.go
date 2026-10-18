package productruntime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

const managedCodexCatalogFixture = `{"models":[{"slug":"gpt-native","display_name":"Native","available_access_programs":{"cyber":["standard","daybreak_blue","daybreak_red"]},"service_tiers":[{"id":"priority"}],"use_responses_lite":true,"max_context_window":272000,"future_model_field":{"keep":true}}],"future_catalog_field":"keep"}`

func TestManagedCodexModelsThroughCONNECT(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		name := "http1"
		if http2 {
			name = "http2"
		}
		t.Run(name, func(t *testing.T) {
			fault := "unavailable"
			if http2 {
				fault = "corrupt"
			}
			f := unavailableAccountReadFixture(t, providerauth.CodexOAuthDriverRef(), fault)
			proxyURL := f.serveProxy(t)
			roots := x509.NewCertPool()
			roots.AppendCertsFromPEM(f.runtime.LocalRootCertificate().CertificatePEM())
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, ForceAttemptHTTP2: http2}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
			send := func(method, query string) (int, string, http.Header) {
				request, _ := http.NewRequest(method, "https://chatgpt.com/backend-api/codex/models"+query, nil)
				request.Header.Set("Authorization", "Bearer original-A")
				request.Header.Set("Chatgpt-Account-Id", "workspace-A")
				request.Header.Set("Cookie", "original-A-cookie")
				request.Header.Set("If-None-Match", `"catalog-global"`)
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if http2 && response.ProtoMajor != 2 {
					t.Fatal("model read did not use HTTP/2")
				}
				return response.StatusCode, string(body), response.Header
			}
			status, body, headers := send("GET", "?client_version=0.163.0")
			if status != 200 || body != managedCodexCatalogFixture {
				t.Fatalf("native model metadata lost: status=%d body=%s", status, body)
			}
			if headers.Get("Cache-Control") != "no-store" || !strings.HasPrefix(headers.Get("Etag"), `"vibermate-models-v1-`) || headers.Get("Set-Cookie") != "" {
				t.Fatalf("unsafe model cache headers: %v", headers)
			}
			f.wire.mu.Lock()
			requests := append([]*http.Request(nil), f.wire.requests...)
			f.wire.mu.Unlock()
			if len(requests) != 1 {
				t.Fatalf("model reads=%d", len(requests))
			}
			upstream := requests[0]
			if upstream.Header.Get("Authorization") != "Bearer "+f.token || upstream.Header.Get("Chatgpt-Account-Id") != "workspace-B" || upstream.Header.Get("Cookie") != "" || upstream.Header.Get("If-None-Match") != "" || upstream.URL.RawQuery != "client_version=0.163.0" {
				t.Fatal("models did not use the frozen managed account and closed query")
			}
			for _, invalid := range []struct{ method, query string }{{"GET", "?account_id=other"}, {"GET", "?client_version=invalid"}, {"GET", "?client_version=0.163.0&client_version=0.163.1"}, {"POST", ""}} {
				status, _, _ := send(invalid.method, invalid.query)
				if status != http.StatusUnprocessableEntity {
					t.Fatalf("unexpected query accepted: %s %s status=%d", invalid.method, invalid.query, status)
				}
			}
			f.wire.mu.Lock()
			defer f.wire.mu.Unlock()
			if len(f.wire.requests) != 1 {
				t.Fatal("invalid model read escaped to transport")
			}
		})
	}
}

func TestCodexModelsFollowManualAccountSwitchOnExistingConnection(t *testing.T) {
	f := unavailableAccountReadFixture(t, providerauth.StaticHeaderDriverRef())
	ctx := context.Background()
	tokenC := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"workspace-C"}}`, time.Now().Add(time.Hour).Unix()))) + ".synthetic"
	material, err := providerauth.NewMaterial(tokenC, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	encoded, err := material.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	secret, err := secretstore.NewValue(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	accountC, err := f.runtime.accounts.Create(ctx, provideraccount.CreateCommand{ID: "managed-c", DisplayName: "Managed C", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.StaticHeaderDriverRef(), Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	refC := environment.RouteAccountReference{ID: accountC.Account.ID.String(), Revision: environment.Revision(accountC.Account.Revision), DisplayName: accountC.Account.DisplayName}
	route := &f.aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
	route.AccountPolicy.Accounts = append(route.AccountPolicy.Accounts, refC)
	route.ModelPolicy = environment.ModelPolicy{Revision: 1, Mode: environment.ModelModeMap, Mappings: []environment.ModelMapping{{RequestedModel: "client-alias", UpstreamModel: "gpt-native"}}}
	proxyURL := f.serveProxy(t)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(f.runtime.LocalRootCertificate().CertificatePEM())
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	read := func(previous string) string {
		t.Helper()
		request, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/models", nil)
		request.Header.Set("Authorization", "Bearer original-A")
		request.Header.Set("If-None-Match", previous)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != 200 {
			t.Fatalf("catalog response: %d %s %v", response.StatusCode, body, err)
		}
		var catalog struct {
			Models []map[string]json.RawMessage `json:"models"`
		}
		if json.Unmarshal(body, &catalog) != nil || len(catalog.Models) != 2 {
			t.Fatalf("alias missing from native catalog: %s", body)
		}
		alias := catalog.Models[1]
		if string(alias["slug"]) != `"client-alias"` || string(alias["use_responses_lite"]) != "true" || string(alias["future_model_field"]) != `{"keep":true}` {
			t.Fatalf("mapped alias lost target metadata: %s", body)
		}
		return response.Header.Get("Etag")
	}
	first := read("")
	current, err := f.runtime.environments.Get(ctx, f.aggregate.ID)
	if err != nil {
		t.Fatal(err)
	}
	candidate, changed, err := environment.ActivateRouteAccount(current.Aggregate(), route.ID, refC)
	if err != nil || !changed {
		t.Fatalf("manual account switch: %v", err)
	}
	draft, err := f.runtime.environments.SaveDraft(ctx, environment.DraftCommand{ExpectedBaseRevision: current.Revision(), Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.runtime.environments.Preview(ctx, candidate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.runtime.environments.Publish(ctx, preview); err != nil || result.Outcome != environment.CommitOutcomeCommitted {
		t.Fatalf("publish switched account: %v", err)
	}
	second := read(first)
	if first == "" || second == "" || first == second {
		t.Fatal("same upstream ETag was reused after account switch")
	}
	f.wire.mu.Lock()
	defer f.wire.mu.Unlock()
	if len(f.wire.requests) != 2 {
		t.Fatalf("model reads=%d", len(f.wire.requests))
	}
	for i, want := range []string{f.token, tokenC} {
		r := f.wire.requests[i]
		if r.Header.Get("Authorization") != "Bearer "+want || r.Header.Get("If-None-Match") != "" || r.URL.RawQuery != "client_version=0.163.0" {
			t.Fatalf("request %d did not follow active manual account", i)
		}
	}
}
