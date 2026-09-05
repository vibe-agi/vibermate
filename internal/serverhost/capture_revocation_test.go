package serverhost_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serverhost"
)

func TestRevokedMemberCannotKeepUsingCapture(t *testing.T) {
	for _, action := range []string{"disable", "reset-password", "logout"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			options := serverOptions(t, t.TempDir())
			options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
			host, err := serverhost.Start(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownServer(t, host)
			users := host.Runtime().RuntimeUsers()
			user, err := users.Create(ctx, runtimeuser.CreateCommand{Username: "review-member", Password: []byte("review-password")})
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Timeout: 10 * time.Second}
			base := "http://" + host.Status().ListenAddress
			login := postJSON(t, client, base+servercontrol.RuntimeUserSessionPath, "", servercontrol.RuntimeUserLogin{
				Schema: servercontrol.RuntimeUserLoginSchema, Username: user.Username, Password: "review-password",
				MachineID: "uRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6xQfEk", DeviceName: "Review device"})
			defer login.Body.Close()
			var session servercontrol.RuntimeUserSession
			if login.StatusCode != http.StatusCreated {
				t.Fatalf("login %d", login.StatusCode)
			}
			if err := json.NewDecoder(login.Body).Decode(&session); err != nil {
				t.Fatal(err)
			}
			catalog := clientadapter.BuiltInCatalog()
			created := postJSON(t, client, base+"/api/v1/capture-runs", session.SessionToken, capturecontrol.CreateRequest{
				EnvironmentID: environment.SystemTransparentID.String(), CWD: "/workspace/project", Command: []string{"custom-agent"}, ExecutablePath: "/opt/tools/custom-agent",
				Companion: &capturecontrol.CompanionAttestationInput{
					Detection: clientadapter.Detection{Status: clientadapter.StatusGeneric, Recognition: clientadapter.RecognitionUnknown, CatalogRevision: catalog.Revision(), CanonicalPath: "/opt/tools/custom-agent", ExecutableLabel: "custom-agent"},
					Workspace: capturecontrol.CompanionWorkspaceInput{MachineID: "uRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6xQfEk", WorkspaceID: "QfEkuRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6w", WorkspaceLabel: "project", RegistrationRevision: 1, DerivationRevision: 1}}})
			defer created.Body.Close()
			if created.StatusCode != http.StatusCreated {
				t.Fatalf("capture %d", created.StatusCode)
			}
			var grant capturecontrol.LaunchGrant
			if err := json.NewDecoder(created.Body).Decode(&grant); err != nil {
				t.Fatal(err)
			}
			runs := host.Runtime().CaptureRuns().(*capturerun.Manager)
			control, err := capturerun.NewControlCapability(grant.RunCapability)
			if err != nil {
				t.Fatal(err)
			}
			proxy, err := capturerun.NewProxyCapability(grant.ProxyToken)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runs.Attach(ctx, grant.Run.ID, control, 12345); err != nil {
				t.Fatal(err)
			}
			switch action {
			case "disable":
				_, err = users.Disable(ctx, user.ID)
			case "reset-password":
				_, err = users.ReplacePassword(ctx, user.ID, []byte("new-review-password"))
			case "logout":
				err = users.Logout(ctx, session.SessionToken)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runs.AuthorizeProxy(ctx, proxy); err == nil {
				t.Fatal("revoked login retained proxy authority")
			}
			_, heartbeatErr := runs.Heartbeat(ctx, grant.Run.ID, control, 5*time.Second)
			_, proxyErr := runs.AuthorizeProxy(ctx, proxy)
			if heartbeatErr == nil || proxyErr == nil {
				t.Fatalf("revoked member retains capture; heartbeat accepted=%t, new proxy authorization accepted=%t", heartbeatErr == nil, proxyErr == nil)
			}
			if action == "disable" {
				enabled, err := users.Enable(ctx, user.ID)
				if err != nil || enabled.ID != user.ID || enabled.State != runtimeuser.StateActive {
					t.Fatalf("re-enable = %v", err)
				}
				if _, err := users.Authenticate(ctx, session.SessionToken); err == nil {
					t.Fatal("re-enable revived old login")
				}
				if _, err := runs.AuthorizeProxy(ctx, proxy); err == nil {
					t.Fatal("re-enable revived old capture")
				}
				if _, err := users.VerifyCredentials(ctx, user.Username, []byte("review-password")); err != nil {
					t.Fatalf("enabled user cannot sign in again: %v", err)
				}
			}
		})
	}
}
