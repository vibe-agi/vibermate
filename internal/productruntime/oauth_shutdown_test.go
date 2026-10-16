package productruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

// Only the external token peer and physical store availability are synthetic.
// Startup, the provider client, credential manager, account manager and shutdown
// all use their production implementations and the existing provider builder.
type shutdownTokenPeer struct {
	response  string
	rotations atomic.Int32
	started   chan struct{}
	release   <-chan struct{}
}

func (p *shutdownTokenPeer) RoundTrip(r *http.Request, _ providertransport.TransportDispatch) (*http.Response, transportprofile.Evidence, error) {
	var payload map[string]string
	if r.Method != http.MethodPost || r.URL.String() != codexoauth.TokenURL ||
		r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "" ||
		json.NewDecoder(r.Body).Decode(&payload) != nil || len(payload) != 3 ||
		payload["grant_type"] != "refresh_token" || payload["refresh_token"] != "synthetic-refresh" || payload["client_id"] != codexoauth.ClientID {
		return nil, transportprofile.Evidence{}, errors.New("closed token peer rejected unexpected request")
	}
	p.rotations.Add(1)
	if p.started != nil {
		close(p.started)
		select {
		case <-p.release:
		case <-r.Context().Done():
			return nil, transportprofile.Evidence{}, r.Context().Err()
		}
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(p.response))}, transportprofile.Evidence{}, nil
}

type shutdownSecrets struct {
	secretstore.Store
	locked atomic.Bool
}

func (s *shutdownSecrets) Replace(ctx context.Context, command secretstore.ReplaceCommand) (secretstore.Metadata, error) {
	if s.locked.Load() {
		return secretstore.Metadata{}, secretstore.ErrLocked
	}
	return s.Store.Replace(ctx, command)
}

func newOAuthShutdownRuntime(t *testing.T) (*Runtime, *shutdownSecrets, *shutdownTokenPeer, provideraccount.View) {
	t.Helper()
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions(t, hostcontract.Desktop(), gate)
	secrets := &shutdownSecrets{Store: options.Secrets}
	options.Secrets = secrets
	credential := reauthorizationCredential(t, "workspace-shutdown", "user-shutdown", false)
	encoded, err := credential.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	response, err := json.Marshal(map[string]json.RawMessage{
		"access_token": wire["accessToken"], "id_token": wire["idToken"], "refresh_token": json.RawMessage(`"synthetic-rotated"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	peer := &shutdownTokenPeer{response: string(response)}
	r, err := startWithBuilders(t.Context(), options, runtimeBuildOverrides{provider: func(request providerBuildRequest) (providerRuntime, error) {
		auth, err := providertransport.NewCodexOAuthAuthenticator(request.secrets)
		if err != nil {
			return nil, err
		}
		return providertransport.NewClient(providertransport.ClientOptions{
			EgressDefaults: request.egressDefaults, Coordinator: request.coordinator,
			Authenticators: []providertransport.Authenticator{auth}, Transport: peer,
			InstanceIDs: request.instanceIDs, Audit: request.audit, RawEvidence: request.rawEvidence,
		})
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		secrets.locked.Store(false)
		// Also release retained synthetic credentials on an assertion failure.
		_ = r.codexOAuth.Shutdown(context.Background())
		_ = r.Shutdown(context.Background())
	})
	account, err := r.ProviderAccounts().Create(t.Context(), provideraccount.CreateCommand{
		ID: "account.shutdown", DisplayName: "Synthetic shutdown", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID,
		Driver: providerauth.CodexOAuthDriverRef(), Secret: reauthorizationValue(t, credential),
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, secrets, peer, account
}

// Removing the credential manager from successful-start cleanup must fail both
// cases: a consumed refresh token must be saved, or its save failure reported.
func TestRuntimeShutdownSettlesPendingOAuthRotation(t *testing.T) {
	for _, recovered := range []bool{true, false} {
		name := "storage_still_locked"
		if recovered {
			name = "storage_recovered"
		}
		t.Run(name, func(t *testing.T) {
			r, secrets, peer, account := newOAuthShutdownRuntime(t)
			secrets.locked.Store(true)
			if _, err := r.ProviderAccounts().RefreshCredential(t.Context(), account.Account.ID, 1); !errors.Is(err, secretstore.ErrLocked) {
				t.Fatalf("rotation must leave a pending CAS: %v", err)
			}
			secrets.locked.Store(!recovered)
			err := r.Shutdown(t.Context())
			metadata, inspectErr := secrets.Inspect(t.Context(), account.Account.SecretRef)
			if recovered {
				if err != nil || inspectErr != nil || metadata.Revision != 2 {
					t.Errorf("shutdown must save retained rotation: error=%v inspect=%v revision=%d", err, inspectErr, metadata.Revision)
				}
			} else if !errors.Is(err, secretstore.ErrLocked) || inspectErr != nil || metadata.Revision != 1 {
				t.Errorf("shutdown must surface failed save: error=%v inspect=%v revision=%d", err, inspectErr, metadata.Revision)
			}
			if got := peer.rotations.Load(); got != 1 {
				t.Errorf("token rotations=%d; want 1", got)
			}
		})
	}
}

// Closing the provider before its credential manager would cancel this
// admitted token exchange instead of settling its rotated secret.
func TestRuntimeShutdownDrainsOAuthBeforeProviderAndClosesRefreshAdmission(t *testing.T) {
	r, secrets, peer, account := newOAuthShutdownRuntime(t)
	release := make(chan struct{})
	peer.started, peer.release = make(chan struct{}), release
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	resolved, err := r.EgressDefaults().Resolve(t.Context(), egressprofile.Inherit(), egressprofile.Inherit())
	if err != nil {
		t.Fatal(err)
	}
	probeScope := account.Account.CredentialScope(account.Account.RealmID, 2, egressprofile.Direct())
	probeScope.ResolvedEgress = resolved
	refreshed := make(chan error, 1)
	go func() {
		_, err := r.ProviderAccounts().RefreshCredential(t.Context(), account.Account.ID, 1)
		refreshed <- err
	}()
	select {
	case <-peer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("token exchange did not start")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- r.Shutdown(t.Context()) }()
	// A nonrotating preparation of a different epoch observes admission without
	// joining the blocked rotation. Only the public closing error is inspected.
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := r.codexOAuth.Prepare(t.Context(), account.Account.Driver, account.Account.SecretRef, probeScope, false)
		if errors.Is(err, codexoauth.ErrManagerClosing) {
			break
		}
		select {
		case err := <-stopped:
			t.Fatalf("shutdown escaped active token exchange: %v", err)
		case <-deadline.C:
			t.Fatalf("credential refresh admission did not close: %v", err)
		case <-ticker.C:
		}
	}
	select {
	case err := <-stopped:
		t.Fatalf("shutdown did not wait for active rotation: %v", err)
	default:
	}
	metadata, err := secrets.Inspect(t.Context(), account.Account.SecretRef)
	if err != nil || metadata.Revision != 1 {
		t.Fatalf("blocked rotation changed revision: %d %v", metadata.Revision, err)
	}
	close(release)
	released = true
	if err := <-refreshed; err != nil {
		t.Fatalf("admitted rotation was interrupted: %v", err)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	metadata, err = secrets.Inspect(t.Context(), account.Account.SecretRef)
	if err != nil || metadata.Revision != 2 {
		t.Fatalf("drained rotation revision=%d error=%v", metadata.Revision, err)
	}
	if _, err := r.ProviderAccounts().RefreshCredential(t.Context(), account.Account.ID, 2); !errors.Is(err, provideraccount.ErrManagerClosing) {
		t.Fatalf("account refresh admission remained open: %v", err)
	}
	if got := peer.rotations.Load(); got != 1 {
		t.Fatalf("token rotations=%d; want 1", got)
	}
}
