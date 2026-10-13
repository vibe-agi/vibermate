package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/proxyclient"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

// Catches native authority surviving real remote login/Capture mutations.
func TestNativeManagedRunIdentityAndCaptureHooks(t *testing.T) {
	for _, action := range []string{"logout", "disable", "password", "finish", "revoke", "recover", "expired-login", "logout-race", "disable-race"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			var users *runtimeuser.Manager
			var user runtimeuser.User
			var token string
			var runID string
			store, id, rev := nativeFixtureWithCapture(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, true, func(store *Store, now time.Time) captureidentity.Reference {
				var err error
				users, err = runtimeuser.New(runtimeuser.Options{Repository: store.RuntimeUserRepository(), Clock: runtimeUserTestClock{now: now}, Random: rand.Reader, SessionLifetime: time.Hour})
				if err != nil {
					t.Fatal(err)
				}
				user, err = users.Create(ctx, runtimeuser.CreateCommand{Username: "native-user", Password: []byte("test-only-password")})
				if err != nil {
					t.Fatal(err)
				}
				machine, err := workspaceidentity.ParseMachineID(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)))
				if err != nil {
					t.Fatal(err)
				}
				login, err := users.Login(ctx, runtimeuser.LoginCommand{Username: user.Username, Password: []byte("test-only-password"), MachineID: machine, DeviceName: "test editor"})
				if err != nil {
					t.Fatal(err)
				}
				token = login.Token.Value()
				runs, err := capturerun.NewManager(ctx, capturerun.DefaultOptions(store.CaptureRunRepository()))
				if err != nil {
					t.Fatal(err)
				}
				grant, err := runs.Create(ctx, capturerun.CreateCommand{CWD: "/workspace", CanonicalExecutablePath: "/bin/test-agent", ExecutableLabel: "test-agent", CatalogRevision: 1, Lifetime: 10 * time.Minute, RuntimeUserID: user.ID, RuntimeUsername: user.Username, LoginSessionID: login.ID, DeviceName: "test editor"})
				if err != nil {
					t.Fatal(err)
				}
				runID = grant.Run.ID
				return captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: runID}
			})
			repo := nativeFullRepository(t, store)
			now := time.Now().UTC().Truncate(time.Millisecond)
			nativeAllow(t, repo, id, now)
			var receipt nativetool.StartReceipt
			var err error
			if action != "logout-race" && action != "disable-race" {
				receipt, err = repo.Start(ctx, id, "identity-start", rev, now)
				if err != nil {
					t.Fatal(err)
				}
			}
			switch action {
			case "logout":
				err = users.Logout(ctx, token)
			case "disable":
				_, err = users.Disable(ctx, user.ID)
			case "password":
				_, err = users.ReplacePassword(ctx, user.ID, []byte("new-test-only-password"))
			case "finish":
				var digest capturerun.CapabilityDigest
				var raw []byte
				if err := store.database.QueryRow(`SELECT control_capability_hash FROM capture_runs WHERE run_id=?`, runID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				copy(digest[:], raw)
				err = store.CaptureRunRepository().Finish(ctx, runID, digest, now)
			case "revoke":
				_, err = store.CaptureRunRepository().RevokeActive(ctx, now)
			case "recover":
				_, err = store.CaptureRunRepository().Recover(ctx, now.Add(time.Hour))
			case "expired-login":
				_, err = store.database.Exec(`UPDATE runtime_user_login_sessions SET expires_at_unix_ms=? WHERE user_id=?`, now.UnixMilli(), user.ID)
			case "logout-race", "disable-race":
				ready := make(chan struct{})
				done := make(chan error, 1)
				started := make(chan struct {
					receipt nativetool.StartReceipt
					err     error
				}, 1)
				go func() {
					<-ready
					r, err := repo.Start(ctx, id, "identity-start", rev, now)
					started <- struct {
						receipt nativetool.StartReceipt
						err     error
					}{r, err}
				}()
				go func() {
					<-ready
					if action == "logout-race" {
						done <- users.Logout(ctx, token)
					} else {
						_, err := users.Disable(ctx, user.ID)
						done <- err
					}
				}()
				close(ready)
				result := <-started
				err = <-done
				receipt = result.receipt
				if result.err != nil && receipt.OperationID() != "" {
					t.Fatal("failed racing start issued receipt")
				}
				var count int
				if err := store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, id).Scan(&count); err != nil || count > 1 {
					t.Fatalf("racing login once: %d %v", count, err)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
				t.Fatal("revoked identity receipt accepted")
			}
			if action != "expired-login" {
				var state string
				if err := store.database.QueryRow(`SELECT state FROM native_tool_scopes WHERE capture_kind='managed_run'`).Scan(&state); err != nil || state != "revoked" {
					t.Fatalf("native durable scope: %s %v", state, err)
				}
			}
		})
	}
}

func TestNativeProxyBindingAndManualRecoveryRevokeAtomically(t *testing.T) {
	for _, action := range []string{"proxy-binding", "manual-recover"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			var bindingID proxyclient.BindingID
			store, id, rev := nativeFixtureWithCapture(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, true, func(store *Store, now time.Time) captureidentity.Reference {
				owner := manualcapture.NewLocalOwnerScope()
				if action == "proxy-binding" {
					manager := newProxyClientManager(t, store.ProxyClientRepository(), &proxyClientClock{now: now}, &proxyClientRandom{})
					binding, _ := createBindingAndEnrollment(t, manager, time.Hour)
					var err error
					bindingID, err = proxyclient.ParseBindingID(binding.ID)
					if err != nil {
						t.Fatal(err)
					}
					owner, err = manualcapture.NewProxyClientOwnerScope(binding.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
				captures := newManualCaptureManager(t, store, &manualCaptureClock{now: now}, &manualCaptureRandom{next: 87})
				grant, err := captures.Create(ctx, manualcapture.CreateCommand{Owner: owner, DisplayName: "Native hook fixture", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
				if err != nil {
					t.Fatal(err)
				}
				return captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: grant.Capture.ID}
			})
			repo := nativeFullRepository(t, store)
			now := time.Now().UTC().Truncate(time.Millisecond)
			nativeAllow(t, repo, id, now)
			receipt, err := repo.Start(ctx, id, "binding-start", rev, now)
			if err != nil {
				t.Fatal(err)
			}
			op, err := repo.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if action == "proxy-binding" {
				_, err = store.ProxyClientRepository().RevokeBinding(ctx, bindingID, 1, now)
			} else {
				_, err = store.database.Exec(`UPDATE manual_captures SET lifetime='temporary',expires_at_unix_ms=? WHERE capture_id=?`, now.Add(time.Minute).UnixMilli(), op.Capture.ID)
				if err == nil {
					_, err = store.ManualCaptureRepository().Recover(ctx, now.Add(time.Hour))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
				t.Fatal("revoked proxy/manual authority accepted")
			}
			var state string
			if err := store.database.QueryRow(`SELECT state FROM native_tool_scopes`).Scan(&state); err != nil || state != "revoked" {
				t.Fatalf("native closure: %s %v", state, err)
			}
		})
	}
}
