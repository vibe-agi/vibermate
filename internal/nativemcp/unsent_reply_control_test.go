package nativemcp_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestUnsentFormAndURLRepliesUseAuthenticatedMetadataApproval(t *testing.T) {
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	for _, kind := range []string{"form", "url"} {
		for _, ordering := range []string{"open", "before_verify", "finish_race"} {
			t.Run(kind+"/"+ordering, func(t *testing.T) {
				nativemcp.ExerciseUnsentReply(t, kind, func(executor nativemcp.Executor, store *runtimepersistence.Store, manuals manualcapture.Controller, capture captureidentity.Reference, a nativetool.Approval) {
					app, err := desktopcontrol.New(desktopcontrol.Options{Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime, Clock: &decisionClock{time.Now().UTC()}, ManualCaptures: manuals, NativeTools: store.NativeToolRepository(), NativePreviews: executor})
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
					unused := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
					router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{Authority: "127.0.0.1:43129", AllowedOrigins: []string{"vibermate://desktop"}, Authenticator: auth, Application: app, Bootstrap: unused, CLIControl: unused, ManualCaptures: manualHandler{}, DesktopPrincipal: principal})
					if err != nil {
						t.Fatal(err)
					}
					path := "/api/v1/captures/" + capture.Key() + "/native-tools/approvals/" + a.ID.String()
					request := func(method, suffix, token, body string) *httptest.ResponseRecorder {
						r := httptest.NewRequest(method, "http://127.0.0.1:43129"+path+suffix, strings.NewReader(body))
						r.RemoteAddr = "127.0.0.1:50000"
						r.Header.Set("Origin", "vibermate://desktop")
						r.Header.Set("Authorization", "Bearer "+token)
						r.Header.Set("Content-Type", "application/json")
						r.Header.Set("If-Match", fmt.Sprint(a.Revision))
						r.Header.Set("Idempotency-Key", "unsent-reply-"+kind)
						w := httptest.NewRecorder()
						router.ServeHTTP(w, r)
						return w
					}
					got := request("GET", "", read, "")
					var dto desktopcontrol.NativeApprovalDTO
					if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &dto) != nil || dto.Preview == nil || len(dto.Preview.Intents) != 1 || dto.Preview.Intents[0].Action != kind+"_reply" || len(dto.Preview.Intents[0].Arguments) != 0 {
						t.Fatal("real registered-parent metadata preview unavailable")
					}
					body := fmt.Sprintf(`{"decision":"allow-once","bindingDigest":%q}`, a.BindingDigest.String())
					if denied := request("POST", "/actions/decide", read, body); denied.Code != 401 {
						t.Fatal("read-only credential authorized a reply")
					}
					if allowed := request("POST", "/actions/decide", write, body); allowed.Code != 200 {
						t.Fatalf("actual authenticated Allow failed status%d", allowed.Code)
					}
				}, ordering != "open", ordering == "finish_race")
			})
		}
	}
}
