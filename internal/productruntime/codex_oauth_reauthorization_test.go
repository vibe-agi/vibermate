package productruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type reauthorizationHTTP func(*http.Request, providerauth.AccountRef) (*http.Response, error)

// This fixture reloads accounts to model a disabled state; use the database's
// millisecond timestamp precision so reloading does not change the incarnation.
type reauthorizationAccountClock struct{}

func (reauthorizationAccountClock) Now() time.Time {
	return time.Now().UTC().Truncate(time.Millisecond)
}

func (f reauthorizationHTTP) Do(r *http.Request, scope providerauth.AccountRef) (*http.Response, error) {
	return f(r, scope)
}

func reauthorizationCredential(t *testing.T, workspace, user string, expired bool) *codexoauth.Credential {
	t.Helper()
	expiry := time.Now().Add(time.Hour)
	if expired {
		expiry = time.Now().Add(-time.Hour)
	}
	claims, _ := json.Marshal(map[string]any{"email": "same@example.com", "exp": expiry.Unix(),
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": workspace, "chatgpt_user_id": user}})
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic"
	wire, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{
		"id_token": token, "access_token": token, "refresh_token": "synthetic-refresh", "account_id": workspace},
		"last_refresh": time.Now().UTC().Format(time.RFC3339Nano)})
	defer clear(wire)
	credential, err := codexoauth.ImportAuthJSON(wire)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(credential.Destroy)
	return credential
}

func reauthorizationValue(t *testing.T, credential *codexoauth.Credential) *secretstore.Value {
	t.Helper()
	encoded, err := credential.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	material, err := providerauth.NewMaterial(string(encoded), map[string]string{"User-Agent": "preserved-agent"}, []string{"X-Remove"})
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	payload, err := material.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(payload)
	value, err := secretstore.NewValue(payload)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(value.Destroy)
	return value
}

type reauthorizationFixture struct {
	accounts  *provideraccount.Manager
	secrets   *reauthorizationSecrets
	oauth     *codexoauth.Manager
	original  provideraccount.View
	database  *runtimepersistence.Store
	endpoints *upstreamendpoint.Manager
}

type reauthorizationSecrets struct {
	secretstore.Store
	failReplace bool
}

func (s *reauthorizationSecrets) Replace(ctx context.Context, cmd secretstore.ReplaceCommand) (secretstore.Metadata, error) {
	if s.failReplace {
		return secretstore.Metadata{}, secretstore.ErrUnavailable
	}
	return s.Store.Replace(ctx, cmd)
}

type reauthorizationDeletionGuard struct{}

func (reauthorizationDeletionGuard) GuardAccountAssociationRemoval(_ context.Context, _ string, _ string, remove func() error) ([]environment.AccountReference, error) {
	return nil, remove()
}
func (reauthorizationDeletionGuard) GuardAccountDeletion(_ context.Context, _ string, remove func() error) ([]environment.AccountReference, error) {
	return nil, remove()
}

func reauthorizationStore(t *testing.T) reauthorizationFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{DatabasePath: filepath.Join(dir, "test.sqlite"),
		BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Shutdown(ctx) })
	factory, err := hostsecret.NewDevelopmentFileFactory(filepath.Join(dir, "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	physical, err := factory.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secrets := &reauthorizationSecrets{Store: physical}
	builtins, err := upstreamendpoint.BuiltInCommands()
	if err != nil {
		t.Fatal(err)
	}
	endpoints, err := upstreamendpoint.NewManager(ctx, store.UpstreamEndpointRepository(), builtins, SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = endpoints.Shutdown(ctx) })
	accounts, err := provideraccount.NewManager(ctx, store.ProviderAccountRepository(), secrets, endpoints, provideraccount.BuiltInRealms(), reauthorizationAccountClock{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accounts.Shutdown(ctx) })
	if err := accounts.BindDeletionGuard(reauthorizationDeletionGuard{}); err != nil {
		t.Fatal(err)
	}
	oauth, err := codexoauth.NewManager(codexoauth.Options{Secrets: secrets, Clock: SystemClock{}, Client: reauthorizationHTTP(func(*http.Request, providerauth.AccountRef) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_grant"}`))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = oauth.Shutdown(ctx) })
	if err := accounts.BindCredentialPreparer(oauth); err != nil {
		t.Fatal(err)
	}
	view, err := accounts.Create(ctx, provideraccount.CreateCommand{ID: "account.codex.existing", DisplayName: "Keep my name",
		UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.CodexOAuthDriverRef(),
		Secret: reauthorizationValue(t, reauthorizationCredential(t, "workspace-original", "user-original", true))})
	if err != nil {
		t.Fatal(err)
	}
	view, err = accounts.SetNote(ctx, provideraccount.NoteCommand{ID: view.Account.ID, ExpectedRevision: 0, Note: "Keep my note"})
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := egressprofile.NewManager(store.EgressProfileRepository(), SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	policy := egressnetwork.DefaultPolicy()
	policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:1080"}
	profile, err := profiles.Publish(ctx, egressprofile.PublishCommand{ID: "profile.reauthorize", DisplayName: "Selected exit", Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.BindEgressProfiles(profiles); err != nil {
		t.Fatal(err)
	}
	view, err = accounts.SetSettings(ctx, provideraccount.SettingsCommand{ID: view.Account.ID, ExpectedRevision: 1, EgressProfile: profile, AutomaticRefresh: false})
	if err != nil {
		t.Fatal(err)
	}
	return reauthorizationFixture{accounts, secrets, oauth, view, store, endpoints}
}

func TestPersistCodexReauthorizationReplacesOnlyCredentialAndClearsReconnect(t *testing.T) {
	ctx := context.Background()
	f := reauthorizationStore(t)
	accounts, secrets, oauth, original := f.accounts, f.secrets, f.oauth, f.original
	scope := providerauth.AccountRef{ID: original.Account.ID.String(), Revision: original.Account.Revision,
		CredentialEpoch: original.Health.CredentialEpoch, RealmID: original.Account.RealmID,
		SettingsRevision: original.Account.SettingsRevision, EgressProfile: egressprofile.Direct()}
	_, _ = oauth.Prepare(ctx, original.Account.Driver, original.Account.SecretRef, scope, true)
	original, _ = accounts.Get(ctx, original.Account.ID)
	before, err := oauth.Inspect(ctx, original.Account.SecretRef, secretstore.Revision(original.Health.CredentialEpoch))
	if err != nil || before.State != codexoauth.StateReconnectRequired {
		t.Fatalf("expected reconnect fixture: %s %v", before.State, err)
	}
	target := codexoauth.LoginAccount{Mode: "reauthorize", ID: original.Account.ID.String(), Reauthorization: &codexoauth.LoginReauthorization{
		SecretRef: original.Account.SecretRef, CredentialEpoch: original.Health.CredentialEpoch, CreatedAt: original.Account.CreatedAt,
		Profile: before.Profile, Scope: original.Account.CredentialScope(original.Account.RealmID, original.Health.CredentialEpoch, egressprofile.ProfileRevision{})}}
	id, err := persistCodexLogin(accounts, secrets)(ctx, target, reauthorizationCredential(t, "workspace-original", "user-original", false))
	if err != nil {
		t.Fatalf("existing account credential-only commit failed: %v", err)
	}
	current, err := accounts.Get(ctx, original.Account.ID)
	if err != nil || id != original.Account.ID.String() || current.Account != original.Account || current.Health.CredentialEpoch != original.Health.CredentialEpoch+1 {
		t.Fatalf("account changed or epoch not advanced: epoch=%d err=%v", current.Health.CredentialEpoch, err)
	}
	all, err := accounts.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatal("reauthorization created another account")
	}
	value, err := secrets.ReadAtRevision(ctx, current.Account.SecretRef, secretstore.Revision(current.Health.CredentialEpoch))
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	payload, _ := value.CopyBytes()
	defer clear(payload)
	material, err := providerauth.ParseMaterial(payload)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	if !reflect.DeepEqual(material.HeaderPolicy(), providerauth.HeaderPolicy{Set: []providerauth.HeaderAssignment{{Name: "User-Agent", Value: "preserved-agent"}}, Delete: []string{"X-Remove"}}) {
		t.Fatal("request header policy lost")
	}
	after, err := oauth.Inspect(ctx, current.Account.SecretRef, secretstore.Revision(current.Health.CredentialEpoch))
	if err != nil || after.State != codexoauth.StateReady {
		t.Fatalf("new credential not ready: %s %v", after.State, err)
	}
}

type reauthorizationController struct {
	provideraccount.Controller
	beforeReplace func()
}

func (c reauthorizationController) ReplaceSecret(ctx context.Context, cmd provideraccount.ReplaceSecretCommand) (provideraccount.View, error) {
	if c.beforeReplace != nil {
		c.beforeReplace()
	}
	return c.Controller.ReplaceSecret(ctx, cmd)
}

func TestCodexReauthorizationCompletionUsesFrozenScopeAndPreservesAccount(t *testing.T) {
	for _, scenario := range []string{"success", "wrong workspace", "wrong known user", "missing known user", "cancel", "deny", "foreign owner", "exchange failure", "storage failure", "credential update", "deleted", "recreated", "recreated at commit", "disabled while pending", "note changed while pending"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			f := reauthorizationStore(t)
			identity, err := f.oauth.Inspect(ctx, f.original.Account.SecretRef, 1)
			if err != nil {
				t.Fatal(err)
			}
			scope := f.original.Account.CredentialScope(f.original.Account.RealmID, 1, egressprofile.ProfileRevision{})
			target := codexoauth.LoginAccount{Mode: "reauthorize", ID: f.original.Account.ID.String(), Reauthorization: &codexoauth.LoginReauthorization{
				SecretRef: f.original.Account.SecretRef, CredentialEpoch: 1, CreatedAt: f.original.Account.CreatedAt, Profile: identity.Profile, Scope: scope}}
			workspace, user := "workspace-original", "user-original"
			if scenario == "wrong workspace" {
				workspace = "workspace-other"
			}
			if scenario == "wrong known user" {
				user = "user-other"
			}
			if scenario == "missing known user" {
				user = ""
			}
			fresh := reauthorizationCredential(t, workspace, user, false)
			encoded, _ := fresh.MarshalBinary()
			defer clear(encoded)
			var wire map[string]json.RawMessage
			_ = json.Unmarshal(encoded, &wire)
			response, _ := json.Marshal(map[string]json.RawMessage{"id_token": wire["idToken"], "access_token": wire["accessToken"], "refresh_token": wire["refreshToken"]})
			defer clear(response)
			calls := 0
			wantAccount := f.original.Account
			wantEpoch := secretstore.Revision(1)
			var controller provideraccount.Controller = f.accounts
			recreate := func() {
				deleted, err := f.accounts.Delete(ctx, provideraccount.DeleteCommand{ID: f.original.Account.ID, ExpectedCredentialEpoch: 1})
				if err != nil || !deleted.Deleted {
					t.Fatalf("fixture delete failed: %v", err)
				}
				if scenario == "deleted" {
					return
				}
				view, err := f.accounts.Create(ctx, provideraccount.CreateCommand{ID: f.original.Account.ID, DisplayName: "New incarnation",
					UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.CodexOAuthDriverRef(), Secret: reauthorizationValue(t, fresh)})
				if err != nil {
					t.Fatal(err)
				}
				wantAccount = view.Account
			}
			if scenario == "recreated at commit" {
				controller = reauthorizationController{Controller: f.accounts, beforeReplace: recreate}
			}
			manager, err := codexoauth.NewLoginManager(codexoauth.LoginOptions{Clock: SystemClock{},
				Client: reauthorizationHTTP(func(r *http.Request, got providerauth.AccountRef) (*http.Response, error) {
					calls++
					if got != scope || got.EgressProfile.ID != "profile.reauthorize" || got.EgressProfile.Policy.Proxy.Endpoint != "127.0.0.1:1080" {
						t.Error("authorization exchange lost selected account exit or frozen metadata")
						return nil, errors.New("closed routing fixture rejected request")
					}
					body, _ := io.ReadAll(r.Body)
					defer clear(body)
					form, _ := url.ParseQuery(string(body))
					if r.Header.Get("Authorization") != "" || form.Get("grant_type") != "authorization_code" || form.Get("refresh_token") != "" {
						t.Error("exchange attached old authentication material")
						return nil, errors.New("closed token fixture rejected request")
					}
					if scenario == "exchange failure" {
						return nil, errors.New("synthetic transport failure")
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(response)))}, nil
				}), Persist: func(ctx context.Context, target codexoauth.LoginAccount, credential *codexoauth.Credential) (string, error) {
					return persistCodexLogin(controller, f.secrets)(ctx, target, credential)
				}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Shutdown(ctx) })
			view, err := manager.Start(ctx, "owner", target, "manual")
			if err != nil {
				t.Fatal(err)
			}
			// The caller cannot mutate the pending target after admission.
			target.Reauthorization.Scope.EgressProfile = egressprofile.Direct()
			target.Reauthorization.Profile.UserID = "forged-user"
			parsed, _ := url.Parse(view.AuthorizationURL)
			callback := parsed.Query().Get("redirect_uri") + "?" + url.Values{"state": {parsed.Query().Get("state")}, "code": {"synthetic-code"}}.Encode()
			wantState, wantReason := "failed", ""
			switch scenario {
			case "success":
				wantState = "completed"
				wantEpoch = 2
			case "wrong workspace", "wrong known user", "missing known user":
				wantReason = "login_identity_mismatch"
			case "cancel":
				if err := manager.Cancel(ctx, "owner", view.ID); err != nil {
					t.Fatal(err)
				}
				wantState = "cancelled"
			case "deny":
				callback = parsed.Query().Get("redirect_uri") + "?" + url.Values{"state": {parsed.Query().Get("state")}, "error": {"access_denied"}}.Encode()
				wantReason = "login_denied"
			case "foreign owner":
				if _, err := manager.Complete(ctx, "another-owner", view.ID, callback); !errors.Is(err, codexoauth.ErrLoginNotFound) {
					t.Fatal("foreign owner completed login")
				}
				if err := manager.Cancel(ctx, "owner", view.ID); err != nil {
					t.Fatal(err)
				}
				wantState = "cancelled"
			case "exchange failure":
				wantReason = "login_exchange_failed"
			case "storage failure":
				f.secrets.failReplace = true
				wantReason = "login_account_save_failed"
			case "credential update":
				_, err := f.accounts.ReplaceSecret(ctx, provideraccount.ReplaceSecretCommand{ID: f.original.Account.ID, ExpectedCredentialEpoch: 1, Secret: reauthorizationValue(t, fresh)})
				if err != nil {
					t.Fatal(err)
				}
				wantEpoch = 2
				wantReason = "login_account_changed"
			case "deleted", "recreated":
				recreate()
				wantReason = "login_account_changed"
			case "recreated at commit":
				wantReason = "login_account_changed"
			case "disabled while pending":
				candidate := f.original.Account
				candidate.State = provideraccount.StateDisabled
				candidate.Revision++
				candidate.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
				result, err := f.database.ProviderAccountRepository().Write(ctx, 1, candidate)
				if err != nil || result.Outcome != provideraccount.CommitCommitted {
					t.Fatalf("disable fixture: %v", err)
				}
				_ = f.accounts.Shutdown(ctx)
				f.accounts, err = provideraccount.NewManager(ctx, f.database.ProviderAccountRepository(), f.secrets, f.endpoints, provideraccount.BuiltInRealms(), SystemClock{})
				if err != nil {
					t.Fatal(err)
				}
				defer f.accounts.Shutdown(ctx)
				if err := f.accounts.BindCredentialPreparer(f.oauth); err != nil {
					t.Fatal(err)
				}
				controller = f.accounts
				wantAccount = candidate
				wantState = "completed"
				wantEpoch = 2
			case "note changed while pending":
				changed, err := f.accounts.SetNote(ctx, provideraccount.NoteCommand{ID: f.original.Account.ID, ExpectedRevision: 1, Note: "New note during login"})
				if err != nil {
					t.Fatal(err)
				}
				wantAccount = changed.Account
				wantState = "completed"
				wantEpoch = 2
			}
			var oldPayload []byte
			if scenario != "deleted" {
				old, err := f.secrets.ReadAtRevision(ctx, f.original.Account.SecretRef, wantEpoch)
				// Successful paths have not committed their next epoch yet.
				if wantState == "completed" {
					old, err = f.secrets.ReadAtRevision(ctx, f.original.Account.SecretRef, 1)
				}
				if err != nil {
					t.Fatal(err)
				}
				oldPayload, _ = old.CopyBytes()
				old.Destroy()
				defer clear(oldPayload)
			}
			completed, err := manager.Complete(ctx, "owner", view.ID, callback)
			if err != nil || completed.State != wantState || completed.Reason != wantReason {
				t.Fatalf("completion state=%s reason=%s want=%s/%s err=%v", completed.State, completed.Reason, wantState, wantReason, err)
			}
			if wantState == "completed" && completed.AccountID != f.original.Account.ID.String() {
				t.Fatal("completion changed account destination")
			}
			again, err := manager.Complete(ctx, "owner", view.ID, callback)
			if err != nil || again != completed {
				t.Fatal("duplicate callback changed terminal state")
			}
			wantCalls := 1
			if scenario == "cancel" || scenario == "deny" || scenario == "foreign owner" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("exchange count=%d want=%d", calls, wantCalls)
			}
			current, err := f.accounts.Get(ctx, f.original.Account.ID)
			if scenario == "deleted" {
				if !errors.Is(err, provideraccount.ErrAccountNotFound) {
					t.Fatal("deleted account resurrected")
				}
				return
			}
			if err != nil || current.Account != wantAccount {
				t.Fatalf("account fields changed: %v", err)
			}
			metadata, _ := f.secrets.Inspect(ctx, current.Account.SecretRef)
			if metadata.Revision != wantEpoch {
				t.Fatalf("credential epoch=%d want=%d", metadata.Revision, wantEpoch)
			}
			all, err := f.accounts.List(ctx)
			if err != nil || len(all) != 1 {
				t.Fatal("completion allocated another account")
			}
			if wantState != "completed" && scenario != "recreated at commit" {
				value, err := f.secrets.ReadAtRevision(ctx, current.Account.SecretRef, wantEpoch)
				if err != nil {
					t.Fatal(err)
				}
				payload, _ := value.CopyBytes()
				value.Destroy()
				defer clear(payload)
				if !reflect.DeepEqual(payload, oldPayload) {
					t.Fatal("failed authorization altered old material")
				}
			}
		})
	}
}
