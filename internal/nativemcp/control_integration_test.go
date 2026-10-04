package nativemcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type manualHandler struct{}
type decisionClock struct{ now time.Time }

func (c *decisionClock) Now() time.Time { return c.now }
func (manualHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, p controlprincipal.Principal) {
	w.WriteHeader(404)
}
func TestNativeApprovalRequiresAuthenticatedCaptureAndExactIntent(t *testing.T) {
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	for _, scenario := range []struct {
		name string
		run  func(*testing.T, func(nativemcp.Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval))
	}{{"callback", nativemcp.ExerciseNativePendingControl}, {"ordinary", nativemcp.ExerciseNativeToolControl}, {"expired", nativemcp.ExerciseNativeExpiredControl}, {"unavailable", nativemcp.ExerciseNativeUnavailableControl}} {
		t.Run(scenario.name, func(t *testing.T) {
			scenario.run(t, func(executor nativemcp.Executor, store *runtimepersistence.Store, manuals manualcapture.Controller, capture captureidentity.Reference, a nativetool.Approval) {
				clock := &decisionClock{time.Now().UTC()}
				app, err := desktopcontrol.New(desktopcontrol.Options{Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime, Clock: clock, ManualCaptures: manuals, NativeTools: store.NativeToolRepository(), NativePreviews: executor})
				if err != nil {
					t.Fatal(err)
				}
				read, write := capability(3), capability(4)
				auth, err := desktopcontrol.NewAuthenticator(desktopcontrol.CapabilityGrant{ReadToken: read, WriteToken: write, ExpiresAt: time.Now().Add(time.Hour)}, desktopcontrol.SystemClock{})
				if err != nil {
					t.Fatal(err)
				}
				principal, err := controlprincipal.New(controlprincipal.Attributes{ID: "desktop:test", Kind: controlprincipal.KindDesktopApp, CredentialRevision: 1, AllowedGrantKinds: []controlprincipal.GrantKind{controlprincipal.GrantManualCapture}})
				if err != nil {
					t.Fatal(err)
				}
				unused := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
				router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{Authority: "127.0.0.1:43129", AllowedOrigins: []string{"vibermate://desktop"}, Authenticator: auth, Application: app, Bootstrap: unused, CLIControl: unused, ManualCaptures: manualHandler{}, DesktopPrincipal: principal})
				if err != nil {
					t.Fatal(err)
				}
				path := "/api/v1/captures/" + capture.Key() + "/native-tools/approvals/" + a.ID.String()
				request := func(method, path, token, body, revision, key string) *httptest.ResponseRecorder {
					r := httptest.NewRequest(method, "http://127.0.0.1:43129"+path, strings.NewReader(body))
					r.RemoteAddr = "127.0.0.1:50000"
					r.Header.Set("Origin", "vibermate://desktop")
					r.Header.Set("Authorization", "Bearer "+token)
					r.Header.Set("Content-Type", "application/json")
					r.Header.Set("If-Match", revision)
					r.Header.Set("Idempotency-Key", key)
					w := httptest.NewRecorder()
					router.ServeHTTP(w, r)
					return w
				}
				check := func(w *httptest.ResponseRecorder, status int) {
					t.Helper()
					if w.Code != status {
						t.Fatalf("status %d want%d: %s", w.Code, status, w.Body.String())
					}
					for _, secret := range []string{"callback-private-sentinel", "form-proof-private-sentinel", "synthetic-original-login", "content", "requestedSchema"} {
						if bytes.Contains(w.Body.Bytes(), []byte(secret)) {
							t.Fatalf("private field leaked %s", secret)
						}
					}
				}
				check(request("GET", path, "", "", "", ""), 401)
				got := request("GET", path, read, "", "", "")
				check(got, 200)
				if got.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("cache allowed")
				}
				collection := strings.TrimSuffix(path, "/"+a.ID.String())
				first := request("GET", collection+"?limit=1", read, "", "", "")
				check(first, 200)
				var page desktopcontrol.NativeApprovalPageDTO
				if err = json.Unmarshal(first.Body.Bytes(), &page); err != nil || len(page.Items) != 0 || page.NextSequence <= 0 {
					t.Fatalf("filtered page did not advance %+v %v", page, err)
				}
				end := request("GET", collection+"?afterSequence=999999", read, "", "", "")
				check(end, 200)
				if err = json.Unmarshal(end.Body.Bytes(), &page); err != nil || len(page.Items) != 0 || page.NextSequence != 999999 {
					t.Fatalf("terminal page cursor moved backwards %+v %v", page, err)
				}
				for _, query := range []string{"?limit=0", "?limit=200", "?limit=1&limit=2", "?afterSequence=0&afterSequence=1", "?unknown=1", "?afterSequence=-1"} {
					check(request("GET", collection+query, read, "", "", ""), 422)
				}
				var dto desktopcontrol.NativeApprovalDTO
				if err = json.Unmarshal(got.Body.Bytes(), &dto); err != nil {
					t.Fatal(err)
				}
				if scenario.name == "unavailable" {
					if dto.Preview != nil || dto.PreviewState != "unavailable" {
						t.Fatal("unknown arguments exposed")
					}
					body := fmt.Sprintf(`{"decision":"allow-once","bindingDigest":%q}`, a.BindingDigest.String())
					check(request("POST", path+"/actions/decide", write, body, "1", "native-unavailable-allow"), 409)
					check(request("POST", path+"/actions/decide", write, strings.Replace(body, "allow-once", "deny", 1), "1", "native-unavailable-deny"), 200)
					return
				}
				if dto.Preview == nil || len(dto.Preview.Intents) != 1 {
					t.Fatalf("missing bound preview %+v", dto)
				}
				intent := dto.Preview.Intents[0]
				if scenario.name == "callback" {
					if intent.Action != "form_reply" || len(intent.Arguments) != 0 {
						t.Fatalf("safe registered-parent preview %+v", dto)
					}
				} else if intent.ToolName != "read_file" || intent.Action != "tool_call" || string(intent.Arguments) != `{"path":"frozen.txt"}` {
					t.Fatalf("ordinary frozen args %+v", dto)
				}
				wrong, err := manuals.Create(context.Background(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Other", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeTemporary, ExpiresIn: time.Hour})
				if err != nil {
					t.Fatal(err)
				}
				check(request("GET", strings.Replace(path, capture.ID, wrong.Capture.ID, 1), read, "", "", ""), 404)
				body := fmt.Sprintf(`{"decision":"allow-once","bindingDigest":%q}`, a.BindingDigest.String())
				if scenario.name == "expired" {
					clock.now = a.ExpiresAt.Add(time.Millisecond)
					check(request("POST", path+"/actions/decide", write, strings.Replace(body, "allow-once", "deny", 1), "1", "native-expired-deny"), 409)
					return
				}
				for _, bad := range []string{`{"decision":"deny","bindingDigest":"x"}`, `{"decision":"deny","bindingDigest":null}`, `{"approved":true}`, `{"safe":true}`, `{"decision":"allow-once","scope":"remember"}`, `{"Decision":"deny","bindingDigest":"x"}`, `{"decision":"deny","decision":"allow-once","bindingDigest":"x"}`} {
					check(request("POST", path+"/actions/decide", write, bad, "1", "native-invalid-shape"), 422)
				}
				check(request("POST", path+"/actions/decide", read, body, "1", "native-read-token"), 401)
				check(request("POST", path+"/actions/decide", write, body, "999", "native-stale-revision"), 409)
				check(request("POST", path+"/actions/decide", write, strings.Replace(body, a.BindingDigest.String(), strings.Repeat("0", 64), 1), "1", "native-stale-binding"), 409)
				check(request("POST", path+"/actions/decide", write, body, "1", "native-allow-once-key"), 200)
				check(request("POST", path+"/actions/decide", write, body, "1", "native-allow-once-key"), 200)
				resolved := request("GET", path, read, "", "", "")
				check(resolved, 200)
				if bytes.Contains(resolved.Body.Bytes(), []byte(`"preview":`)) {
					t.Fatal("decided preview survived")
				}
			})
		})
	}
}
