package provideraccount

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestOAuthRefreshRecoversRotatedCredentialAfterStorageUnlock(t *testing.T) {
	for _, recovery := range []string{"manual refresh", "automatic lease", "manual-only lease", "concurrent leases", "expired recovery", "expired manual recovery", "expired manual-only lease", "shutdown", "owner replacement", "owner replaces during rotation"} {
		t.Run(recovery, func(t *testing.T) {
			testOAuthRefreshRecovery(t, recovery)
		})
	}
}

func testOAuthRefreshRecovery(t *testing.T, recovery string) {
	t.Helper()
	ctx := t.Context()
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	clock := &fixedClock{now: now}
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"workspace-recovery"}}`, now.Add(time.Hour).Unix()))) + ".synthetic"
	credential, err := codexoauth.ImportAuthJSON([]byte(fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"id_token":%q,"refresh_token":"synthetic-old","account_id":"workspace-recovery"},"last_refresh":%q}`, token, token, now.Format(time.RFC3339Nano))))
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	encoded, err := credential.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	value, err := newTestCredentialValue(t, string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	secrets := &writeLockedSecrets{Store: newMemorySecrets()}
	manager, err := NewManager(ctx, &memoryRepository{accounts: map[ID]Account{}}, secrets, testEndpoints(t), BuiltInRealms(), clock)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(context.Background())
	wire := &rotatingOAuthHTTP{refresh: "synthetic-old", token: token}
	oauth, err := codexoauth.NewManager(codexoauth.Options{Secrets: secrets, Client: wire, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.BindCredentialPreparer(oauth); err != nil {
		t.Fatal(err)
	}
	view, err := manager.Create(ctx, CreateCommand{ID: "oauth-recovery", DisplayName: "Recovery", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.CodexOAuthDriverRef(), AutomaticRefresh: recovery == "automatic lease" || recovery == "expired recovery" || recovery == "owner replaces during rotation", Secret: value})
	if err != nil {
		t.Fatal(err)
	}
	if recovery == "owner replaces during rotation" {
		clock.now = now.Add(56 * time.Minute)
		wire.started, wire.release = make(chan struct{}), make(chan struct{})
		endpoint := testEndpoints(t)[upstreamendpoint.ChatGPTOfficialID]
		finished := make(chan struct{})
		go func() {
			lease, _ := manager.AcquireEndpointCredential(ctx, view.Account.ID, endpoint)
			if lease != nil {
				lease.Release()
			}
			close(finished)
		}()
		select {
		case <-wire.started:
		case <-time.After(time.Second):
			t.Fatal("automatic rotation did not reach the synthetic provider")
		}
		_, replaceErr := manager.ReplaceSecret(ctx, ReplaceSecretCommand{ID: view.Account.ID, ExpectedCredentialEpoch: 1, Secret: value})
		secrets.locked.Store(true)
		close(wire.release)
		<-finished
		if replaceErr != nil {
			t.Fatal(replaceErr)
		}
		if err := oauth.Shutdown(ctx); err != nil {
			t.Fatalf("superseded in-flight rotation remained pending: %v", err)
		}
		return
	}
	secrets.locked.Store(true)
	if _, err := manager.RefreshCredential(ctx, view.Account.ID, 1); !errors.Is(err, secretstore.ErrLocked) || errors.Is(err, codexoauth.ErrReconnectRequired) {
		t.Fatalf("storage failure was hidden or became a permanent authentication failure: %v", err)
	}
	state, err := oauth.Inspect(ctx, view.Account.SecretRef, 1)
	if err != nil || state.State != codexoauth.StateRefreshDue {
		t.Fatalf("uncommitted rotation was reported as ready: state=%s err=%v", state.State, err)
	}
	if lease, err := manager.AcquireEndpointCredential(ctx, view.Account.ID, testEndpoints(t)[upstreamendpoint.ChatGPTOfficialID]); !errors.Is(err, secretstore.ErrLocked) {
		if lease != nil {
			lease.Release()
		}
		t.Fatalf("uncommitted rotation admitted a stale credential lease: %v", err)
	}
	if recovery == "shutdown" {
		if err := oauth.Shutdown(ctx); !errors.Is(err, secretstore.ErrLocked) {
			t.Fatalf("shutdown hid an uncommitted rotation: %v", err)
		}
	}
	expectedEpoch := uint64(2)
	if strings.HasPrefix(recovery, "expired ") {
		clock.now = now.Add(2 * time.Hour)
		wire.token = "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"workspace-recovery"}}`, now.Add(3*time.Hour).Unix()))) + ".synthetic"
		if recovery != "expired manual-only lease" {
			expectedEpoch = 3
		}
	}
	secrets.locked.Store(false)
	if recovery == "owner replacement" {
		if _, err := manager.ReplaceSecret(ctx, ReplaceSecretCommand{ID: view.Account.ID, ExpectedCredentialEpoch: 1, Secret: value}); err != nil {
			t.Fatal(err)
		}
		secrets.locked.Store(true)
		if err := oauth.Shutdown(ctx); err != nil {
			t.Fatalf("owner replacement left an obsolete rotation pending: %v", err)
		}
		return
	}
	if recovery == "shutdown" {
		if err := oauth.Shutdown(ctx); err != nil {
			t.Fatalf("shutdown discarded the recoverable credential: %v", err)
		}
		stored, err := manager.Get(ctx, view.Account.ID)
		if err != nil || stored.Health.CredentialEpoch != 2 {
			t.Fatalf("shutdown failed to save rotation: %+v, %v", stored.Health, err)
		}
		if _, err := manager.RefreshCredential(ctx, view.Account.ID, 2); !errors.Is(err, codexoauth.ErrManagerClosing) {
			t.Fatalf("shutdown admitted another token rotation: %v", err)
		}
		return
	}
	if recovery == "manual refresh" || recovery == "expired manual recovery" {
		recovered, err := manager.RefreshCredential(ctx, view.Account.ID, 1)
		if err != nil || recovered.Health.CredentialEpoch != expectedEpoch {
			t.Fatalf("unlock did not recover the already-rotated credential: epoch=%d err=%v", recovered.Health.CredentialEpoch, err)
		}
	} else if recovery == "concurrent leases" {
		start := make(chan struct{})
		results := make(chan error, 32)
		for range 32 {
			go func() {
				<-start
				lease, err := manager.AcquireEndpointCredential(ctx, view.Account.ID, testEndpoints(t)[upstreamendpoint.ChatGPTOfficialID])
				if err == nil {
					scope, _ := lease.Account()
					lease.Release()
					if scope.CredentialEpoch != 2 {
						err = fmt.Errorf("recovery admitted epoch %d instead of 2", scope.CredentialEpoch)
					}
				}
				results <- err
			}()
		}
		close(start)
		for range 32 {
			if err := <-results; err != nil {
				t.Error(err)
			}
		}
	} else {
		lease, err := manager.AcquireEndpointCredential(ctx, view.Account.ID, testEndpoints(t)[upstreamendpoint.ChatGPTOfficialID])
		if err != nil {
			t.Fatal(err)
		}
		scope, _ := lease.Account()
		lease.Release()
		if scope.CredentialEpoch != expectedEpoch {
			t.Fatalf("new request bypassed pending credential recovery: epoch=%d", scope.CredentialEpoch)
		}
	}
	// The next real rotation must use the retained refresh token; accepting a
	// stale access token or merely returning success cannot satisfy this check.
	after, err := manager.RefreshCredential(ctx, view.Account.ID, expectedEpoch)
	if err != nil || after.Health.CredentialEpoch != expectedEpoch+1 {
		t.Fatalf("subsequent rotation lost the recovered token: epoch=%d err=%v", after.Health.CredentialEpoch, err)
	}
}

type writeLockedSecrets struct {
	secretstore.Store
	locked atomic.Bool
}

func (store *writeLockedSecrets) Replace(ctx context.Context, command secretstore.ReplaceCommand) (secretstore.Metadata, error) {
	if store.locked.Load() {
		return secretstore.Metadata{}, secretstore.ErrLocked
	}
	return store.Store.Replace(ctx, command)
}

type rotatingOAuthHTTP struct {
	mu      sync.Mutex
	refresh string
	token   string
	started chan struct{}
	release chan struct{}
}

func (client *rotatingOAuthHTTP) Do(request *http.Request, _ providerauth.AccountRef) (*http.Response, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.started != nil {
		close(client.started)
		select {
		case <-client.release:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	}
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if request.URL.String() != codexoauth.TokenURL || request.Method != http.MethodPost || json.NewDecoder(request.Body).Decode(&body) != nil {
		return nil, errors.New("unexpected synthetic OAuth request")
	}
	status, response := http.StatusUnauthorized, `{"error":"invalid_grant"}`
	if body.RefreshToken == client.refresh {
		client.refresh += "-rotated"
		status, response = http.StatusOK, fmt.Sprintf(`{"access_token":%q,"refresh_token":%q}`, client.token, client.refresh)
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
}

type heldOAuthHTTP struct {
	started chan struct{}
	release chan struct{}
	token   string
	status  int
}

func (client *heldOAuthHTTP) Do(request *http.Request, _ providerauth.AccountRef) (*http.Response, error) {
	if request.URL.String() != codexoauth.TokenURL || request.Method != http.MethodPost {
		return nil, errors.New("unexpected synthetic OAuth request")
	}
	close(client.started)
	select {
	case <-request.Context().Done():
		return nil, request.Context().Err()
	case <-client.release:
	}
	return &http.Response{StatusCode: client.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"access_token":%q,"refresh_token":"synthetic-new"}`, client.token)))}, nil
}

func TestManualOAuthRefreshWaitsBeforeFreezingNewAccountLeases(t *testing.T) {
	for _, test := range []struct {
		automatic, cancelRefresh, shutdown bool
		status                             int
	}{
		{false, false, false, 200}, {true, false, false, 200},
		{false, true, false, 200}, {true, true, false, 200},
		{false, false, true, 200}, {false, false, false, 503}, {true, false, false, 503},
	} {
		t.Run(fmt.Sprintf("automatic=%t/cancel=%t/shutdown=%t/status=%d", test.automatic, test.cancelRefresh, test.shutdown, test.status), func(t *testing.T) {
			ctx := t.Context()
			now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
			token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"workspace-test"}}`, now.Add(time.Hour).Unix()))) + ".synthetic"
			credential, err := codexoauth.ImportAuthJSON([]byte(fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"id_token":%q,"refresh_token":"synthetic-old","account_id":"workspace-test"},"last_refresh":%q}`, token, token, now.Format(time.RFC3339Nano))))
			if err != nil {
				t.Fatal(err)
			}
			defer credential.Destroy()
			encoded, err := credential.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			defer clear(encoded)
			value, err := newTestCredentialValue(t, string(encoded))
			if err != nil {
				t.Fatal(err)
			}
			defer value.Destroy()
			secrets, endpoints := newMemorySecrets(), testEndpoints(t)
			manager, err := NewManager(ctx, &memoryRepository{accounts: map[ID]Account{}}, secrets, endpoints, BuiltInRealms(), fixedClock{now: now})
			if err != nil {
				t.Fatal(err)
			}
			wire := &heldOAuthHTTP{started: make(chan struct{}), release: make(chan struct{}), token: token, status: test.status}
			defer func() { close(wire.release); _ = manager.Shutdown(context.Background()) }()
			oauth, err := codexoauth.NewManager(codexoauth.Options{Secrets: secrets, Client: wire, Clock: fixedClock{now: now}})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.BindCredentialPreparer(oauth); err != nil {
				t.Fatal(err)
			}
			view, err := manager.Create(ctx, CreateCommand{ID: "oauth", DisplayName: "OAuth", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.CodexOAuthDriverRef(), AutomaticRefresh: test.automatic, Secret: value})
			if err != nil {
				t.Fatal(err)
			}
			refreshed := make(chan error, 1)
			refreshContext, cancelRefresh := context.WithCancel(ctx)
			defer cancelRefresh()
			go func() { _, err := manager.RefreshCredential(refreshContext, view.Account.ID, 1); refreshed <- err }()
			select {
			case <-wire.started:
			case <-time.After(time.Second):
				t.Fatal("manual refresh did not reach the synthetic token endpoint")
			}
			if test.cancelRefresh {
				cancelRefresh() // Leaving the page cannot thaw an in-progress rotation.
			}
			waiting, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			lease, err := manager.AcquireEndpointCredential(waiting, view.Account.ID, endpoints[upstreamendpoint.ChatGPTOfficialID])
			if lease != nil {
				lease.Release()
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("refresh became an immediate credential failure instead of a cancelable wait: %v", err)
			}
			other, err := manager.Create(ctx, CreateCommand{ID: "other", DisplayName: "Other", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.CodexOAuthDriverRef(), Secret: value})
			if err != nil {
				t.Fatal(err)
			}
			unrelated, err := manager.AcquireEndpointCredential(ctx, other.Account.ID, endpoints[upstreamendpoint.ChatGPTOfficialID])
			if err != nil {
				t.Fatalf("refresh blocked another account: %v", err)
			}
			unrelated.Release()
			type result struct {
				account providerauth.AccountRef
				err     error
			}
			acquired := make(chan result, 1)
			go func() {
				lease, err := manager.AcquireEndpointCredential(ctx, view.Account.ID, endpoints[upstreamendpoint.ChatGPTOfficialID])
				var account providerauth.AccountRef
				if lease != nil {
					account, _ = lease.Account()
					lease.Release()
				}
				acquired <- result{account: account, err: err}
			}()
			select {
			case got := <-acquired:
				t.Fatalf("new request did not wait for the refresh: %+v", got)
			case <-time.After(20 * time.Millisecond):
			}
			if test.shutdown {
				closing, cancel := context.WithCancel(ctx)
				cancel()
				if err := manager.Shutdown(closing); !errors.Is(err, context.Canceled) {
					t.Fatalf("shutdown lost the in-flight refresh: %v", err)
				}
				select {
				case got := <-acquired:
					if !errors.Is(got.err, ErrManagerClosing) {
						t.Fatalf("closing manager admitted a waiting request: %+v", got)
					}
				case <-time.After(time.Second):
					t.Fatal("shutdown did not release the account waiter")
				}
				return
			}
			wire.release <- struct{}{}
			err = <-refreshed
			wantEpoch := uint64(2)
			if test.status == 503 {
				wantEpoch = 1 // A transient manual failure leaves the still-valid token unchanged.
				if !errors.Is(err, codexoauth.ErrRefreshUnavailable) {
					t.Fatalf("manual refresh lost its transient failure: %v", err)
				}
			} else if err != nil && !(test.cancelRefresh && errors.Is(err, context.Canceled)) {
				t.Fatal(err)
			}
			select {
			case got := <-acquired:
				if got.err != nil || got.account.CredentialEpoch != wantEpoch || got.account.ID != "oauth" {
					t.Fatalf("new request froze an old/different account: %+v", got)
				}
			case <-time.After(time.Second):
				t.Fatal("completed refresh did not release the account waiter")
			}
		})
	}
}

func (preparer *rotatingCredentialPreparer) Refresh(ctx context.Context, driver providerauth.DriverRef, ref secretstore.Reference, scope providerauth.AccountRef) (secretstore.Revision, error) {
	return preparer.Prepare(ctx, driver, ref, scope, true)
}

func TestExplicitRefreshChecksEpochAndDoesNotRetargetIdentity(t *testing.T) {
	ctx := context.Background()
	secrets := newMemorySecrets()
	manager, err := NewManager(ctx, &memoryRepository{accounts: make(map[ID]Account)}, secrets, testEndpoints(t), BuiltInRealms(), nil)
	if err != nil {
		t.Fatal(err)
	}
	material, err := providerauth.NewMaterial("synthetic-oauth", map[string]string{"User-Agent": "synthetic-agent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	encoded, err := material.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	value, err := secretstore.NewValue(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	before, err := manager.Create(ctx, CreateCommand{ID: "oauth", DisplayName: "OAuth", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.CodexOAuthDriverRef(), Secret: value})
	if err != nil {
		t.Fatal(err)
	}
	preparer := &rotatingCredentialPreparer{secrets: secrets, replacement: value}
	if err := manager.BindCredentialPreparer(preparer); err != nil {
		t.Fatal(err)
	}
	after, err := manager.RefreshCredential(ctx, before.Account.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if after.Account != before.Account || after.Health.CredentialEpoch != 2 || len(after.SetHeaderNames) != 1 {
		t.Fatal("refresh changed account identity or lost header policy")
	}
	if _, err := manager.RefreshCredential(ctx, before.Account.ID, 1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale epoch error=%v", err)
	}
	if _, err := manager.RefreshCredential(ctx, before.Account.ID, 0); !errors.Is(err, ErrInvalidAccount) {
		t.Fatalf("zero epoch error=%v", err)
	}
	if preparer.calls != 1 {
		t.Fatal("stale click started a second exchange")
	}
}
