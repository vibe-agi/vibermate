//go:build darwin

package main

import (
	"context"
	"crypto/x509"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestModelDefaultPluginDenialPredicatesAndCaps(t *testing.T) {
	rows := []struct{ path, query, class string }{{"/ps/plugins/list", "scope=GLOBAL&limit=200", "list"}, {"/ps/plugins/installed", "limit=200", "installed_metadata"}, {"/ps/plugins/installed", "limit=200&includeDownloadUrls=true", "installed_bundle_metadata"}, {"/plugins/featured", "platform=codex", "featured"}, {"/ps/plugins/suggested/codex", "scope=GLOBAL", "suggested"}, {"/codex/models", "client_version=0.160.0", "models"}, {"/wham/settings/user", "", "settings"}}
	for _, row := range rows {
		t.Run(row.class, func(t *testing.T) {
			for _, mutation := range []string{"exact", "extra", "duplicate", "malformed", "wrongvalue", "body", "TE", "auth", "duplicateauth", "host", "path", "method", "rawalias", "userinfo", "scheme"} {
				t.Run(mutation, func(t *testing.T) {
					address := "https://chatgpt.com/backend-api" + row.path
					if row.query != "" {
						address += "?" + row.query
					}
					r := httptest.NewRequest("GET", address, nil)
					r.Header.Set("Authorization", "Bearer synthetic-original-login")
					switch mutation {
					case "extra":
						r.URL.RawQuery += "&extra=x"
					case "duplicate":
						r.URL.RawQuery += "&" + row.query
						if row.query == "" {
							r.URL.RawQuery = "&"
						}
					case "malformed":
						r.URL.RawQuery += "&bad=%zz"
					case "wrongvalue":
						r.URL.RawQuery = strings.ReplaceAll(row.query, "200", "201")
						r.URL.RawQuery = strings.ReplaceAll(r.URL.RawQuery, "GLOBAL", "USER")
						r.URL.RawQuery = strings.ReplaceAll(r.URL.RawQuery, "codex", "other")
						r.URL.RawQuery = strings.ReplaceAll(r.URL.RawQuery, "0.160.0", "0.159.2")
						if row.query == "" {
							r.URL.RawQuery = "?"
						}
					case "body":
						r.Body = io.NopCloser(strings.NewReader("x"))
						r.ContentLength = 1
					case "TE":
						r.TransferEncoding = []string{"chunked"}
					case "auth":
						r.Header.Set("Authorization", "Bearer wrong")
					case "duplicateauth":
						r.Header.Add("Authorization", "Bearer synthetic-original-login")
					case "host":
						r.URL.Host = "other.invalid"
						r.Host = r.URL.Host
					case "path":
						r.URL.Path += "/more"
					case "method":
						r.Method = "POST"
					case "rawalias":
						r.URL.RawPath = "/backend-api/%70s/plugins/list"
					case "userinfo":
						r.URL.User = httpProxySyntheticUser()
					case "scheme":
						r.URL.Scheme = "http"
					}
					class := nativeModelAuxiliaryClass(r)
					if (class == row.class) != (mutation == "exact") {
						t.Fatalf("closed auxiliary predicate class=%s mutation=%s", class, mutation)
					}
				})
			}
		})
	}
	guard := &nativeModelAuxiliaryGuard{}
	for i := 0; i < 8; i++ {
		if !guard.deny("list") {
			t.Fatal("per-row cap rejected in-bound request")
		}
	}
	if guard.deny("list") {
		t.Fatal("per-row overflow accepted")
	}
	if _, _, failed := guard.snapshot(); !failed {
		t.Fatal("overflow was silently dropped")
	}
	guard = &nativeModelAuxiliaryGuard{}
	for _, row := range rows[:4] {
		for i := 0; i < 8; i++ {
			if !guard.deny(row.class) {
				t.Fatal("total cap rejected in-bound request")
			}
		}
	}
	if guard.deny("models") {
		t.Fatal("total overflow accepted")
	}
	guard = &nativeModelAuxiliaryGuard{}
	if guard.deny("unknown") {
		t.Fatal("unknown auxiliary accepted")
	}
}

func httpProxySyntheticUser() *url.Userinfo { return url.User("synthetic") }

func TestModelDefaultPluginTransportKeepsVerifiedPrivateTLS(t *testing.T) {
	for _, scenario := range []string{"exact", "untrusted_root", "redirect", "extra_query", "foreign_host"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			server, roots := nativeTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Host != "chatgpt.com" || r.TLS == nil || r.TLS.ServerName != "chatgpt.com" {
					t.Error("canonical model auxiliary TLS changed")
				}
				if scenario == "redirect" {
					w.Header().Set("Location", "https://other.invalid/")
					w.WriteHeader(302)
					return
				}
				w.WriteHeader(403)
			}))
			if scenario == "untrusted_root" {
				roots = x509.NewCertPool()
			}
			transport, err := newNativeFixtureTransport(server.Listener.Addr().String(), roots)
			if err != nil {
				t.Fatal(err)
			}
			defer transport.transport.CloseIdleConnections()
			transport.modelAuxiliary = true
			guard := &nativeModelAuxiliaryGuard{}
			transport.modelGuard = guard
			transport.modelCaseActive.Store(true)
			r, _ := http.NewRequest("GET", "https://chatgpt.com/backend-api/ps/plugins/list?scope=GLOBAL&limit=200", nil)
			r.Header.Set("Authorization", "Bearer synthetic-original-login")
			if scenario == "extra_query" {
				r.URL.RawQuery += "&extra=x"
			}
			if scenario == "foreign_host" {
				r.URL.Host = "other.invalid"
			}
			response, err := transport.RoundTrip(r, egressnetwork.DefaultPolicy())
			if response != nil {
				response.Body.Close()
			}
			if scenario == "exact" {
				if err != nil || response == nil || response.StatusCode != 403 || calls != 1 {
					t.Fatal("exact private auxiliary denial unavailable")
				}
			} else if err == nil {
				t.Fatal("auxiliary guard escaped")
			}
			if (scenario == "extra_query" || scenario == "foreign_host" || scenario == "untrusted_root") && calls != 0 {
				t.Fatal("denied auxiliary reached handler")
			}
			if scenario != "exact" {
				if _, _, failed := guard.snapshot(); !failed {
					t.Fatal("unknown active auxiliary form was silently denied without failing case")
				}
			}
		})
	}
}

func TestModelSSEDefaultPluginsOnHeldStream(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary, node := modelIdleVerifiedClient(t, parent, t.TempDir())
	trace, client, guard := &modelIdleTrace{}, &modelIdleClientObservation{}, &nativeModelAuxiliaryGuard{}
	options := &nativeModelProbeOptions{Profile: nativeModelProbePluginsOn, Auxiliary: guard, Handler: modelIdleProvider(trace, "managed_long_arguments"), ThreadParameters: func(dir string) map[string]any { return modelPinnedThreadParams(dir, "0.160.0", 20000) }, ClientObservation: client, ExpectedModelRequests: 2,
		ObserveTerminal: func(ctx context.Context, actual nativeModelProbeObservation) {
			evidence := collectModelLifecycleEvidence(t, ctx, actual)
			counts, total, failed := guard.snapshot()
			trace.mu.Lock()
			requests, acks, failure := trace.requests, trace.acks, trace.failure
			trace.mu.Unlock()
			client.mu.Lock()
			plans, idle := client.plans, client.idle
			client.mu.Unlock()
			t.Logf("defaultPluginsON completed=%t requests=%d plans=%d ack=%d generatedDATA=%d auxiliaryDenials=%v total=%d guardFailed=%t", actual.Completed, requests, plans, acks, evidence.generatedData, counts, total, failed)
			if !actual.Completed || requests != 2 || plans != 1 || acks != 1 || idle || failure != "" || failed || evidence.generatedData != 2 || evidence.toolBatches != 1 {
				t.Error("default pluginsON held stream not proved")
			}
		}}
	runNativeFixtureSliceWithModelProbe(modelIdleManagedChecks{T: t, ctx: parent}, binary, options, node)
}
