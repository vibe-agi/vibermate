package providertransport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

// Use the real SOCKS, TLS, authentication and provider clients, but permit
// sockets only to the two loopback fixtures. No public DNS or credentials.
func TestAccountEgressThroughSOCKSForEverySupportedOperation(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"chatgpt.com", "auth.openai.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan *http.Request, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case observed <- r.Clone(context.Background()):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	proxy, proxyCalls := newAccountSOCKSFixture(t, server.Listener.Addr().String())
	network := &accountLoopbackNetwork{proxy: proxy.Addr().String(), upstream: server.Listener.Addr().String()}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	dialers, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network})
	if err != nil {
		t.Fatal(err)
	}
	transport, err := newStrictTransportWithDialers(roots, dialers, DefaultTransportTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	accessToken := fakeChatGPTToken(`{"exp":1999988400,"https://api.openai.com/auth":{"chatgpt_account_id":"fixture-account"}}`)
	authJSON, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt", "tokens": map[string]string{
			"id_token": accessToken, "access_token": accessToken,
			"refresh_token": "fixture-only", "account_id": "fixture-account",
		}, "last_refresh": time.Now().UTC().Format(time.RFC3339Nano),
	})
	stored, err := codexoauth.ImportAuthJSON(authJSON)
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Destroy()
	encoded, err := stored.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	auth, err := NewCodexOAuthAuthenticator(testSecretReaderWithPolicy(t, string(encoded), nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	gate := newStartedGate(t)
	audit := &runtimeAuditRecorder{}
	client, err := NewClient(ClientOptions{
		Coordinator: gate, Authenticator: auth, Transport: transport,
		Audit: audit, InstanceIDs: &sequentialInstanceIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownClient(t, client) })
	endpoint := testDiscoveryEndpoint(t, "https://chatgpt.com")
	credential := accountCodexEgressCredential{testRuntimeDiscoveryCredential(t, endpoint.RealmID).(*runtimeDiscoveryCredential)}
	credential.account.EgressProfile = testAccountEgress()
	credential.account.EgressProfile.Policy.Proxy.Endpoint = proxy.Addr().String()
	credential.account.SettingsRevision = 4
	for _, unavailable := range []bool{false, true} {
		if unavailable {
			_ = proxy.Close()
		}
		for _, operation := range []string{"model", "oauth", "quota", "history", "models", "reset"} {
			name := operation
			if unavailable {
				name += "/proxy-unavailable"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				beforeProxy, beforeDials := proxyCalls.Load(), network.calls.Load()
				var response *http.Response
				var requestErr error
				switch operation {
				case "model":
					options := validRequestOptions(t)
					action, err := gate.BeginAction(ctx, offlinehold.ActionRequest{ActionID: "account-model-" + name})
					if err != nil {
						t.Fatal(err)
					}
					defer action.Release()
					options.Action = action
					options.Target, _ = NewTarget(endpoint.Origin)
					options.RelativePath = "backend-api/codex/responses"
					options.AccountRef = credential.account
					options.AuthDriverRef = credential.Driver()
					options.SecretRef = credential.Secret()
					options.ClientHello = captureTransportClientHello(t, nil)
					request, err := NewRequest(options)
					if err != nil {
						t.Fatal(err)
					}
					response, _, requestErr = client.Do(ctx, request)
				case "oauth":
					request, _ := http.NewRequestWithContext(ctx, http.MethodPost, codexoauth.TokenURL, strings.NewReader(`{"grant_type":"refresh_token","refresh_token":"fixture-only"}`))
					request.Header.Set("Content-Type", "application/json")
					response, requestErr = client.DoCodexOAuthTokenRequest(request, credential.account)
				case "models":
					response, requestErr = client.FetchEndpointModels(ctx, endpoint, credential)
				case "reset":
					id, err := ResetRequestID(credential.account.ID, "fixture-credit")
					if err != nil {
						t.Fatal(err)
					}
					command, err := NewOwnedResetRedemption(endpoint.Origin, "fixture-credit", id, credential)
					if err != nil {
						t.Fatal(err)
					}
					response, requestErr = client.ConsumeResetCredit(ctx, command)
				default:
					op := upstreamservice.CodexRateLimits
					if operation == "history" {
						op = upstreamservice.CodexUsageHistory
					}
					read, err := NewOwnedAccountRead(endpoint.Origin, op, credential)
					if err != nil {
						t.Fatal(err)
					}
					response, requestErr = client.ReadAccount(ctx, read)
				}
				if response != nil {
					_, readErr := io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					if readErr != nil {
						t.Fatal(readErr)
					}
				}
				if unavailable {
					if requestErr == nil || proxyCalls.Load() != beforeProxy {
						t.Fatalf("unavailable proxy did not fail closed: %v", requestErr)
					}
				} else {
					if requestErr != nil || proxyCalls.Load() != beforeProxy+1 {
						t.Fatalf("account operation missed SOCKS: %v", requestErr)
					}
					request := <-observed
					if operation == "oauth" {
						if request.Host != "auth.openai.com" || request.Header.Get("Authorization") != "" {
							t.Fatal("token request changed authority or leaked an access token")
						}
					} else if request.Host != "chatgpt.com" || request.Header.Get("Authorization") != "Bearer "+accessToken {
						t.Fatal("account operation lost its target or authentication")
					}
				}
				if network.calls.Load() != beforeDials+1 || network.direct.Load() != 0 {
					t.Fatal("account operation retried or bypassed its explicit proxy")
				}
				started, terminal := audit.attempts()
				if started.Decision().ProxyID != credential.account.EgressProfile.ID.String() || started.Decision().ProxyRevision != 3 || started.Decision().AccountSettingsRevision != 4 || started.Decision().AccountID != credential.account.ID || !terminal.Terminal() || terminal.Decision() != started.Decision() {
					t.Fatal("account operation omitted effective egress or terminal evidence")
				}
			})
		}
	}
}

type accountCodexEgressCredential struct{ *runtimeDiscoveryCredential }

func (accountCodexEgressCredential) Driver() providerauth.DriverRef {
	return providerauth.CodexOAuthDriverRef()
}

type accountLoopbackNetwork struct {
	proxy, upstream string
	calls, direct   atomic.Int64
}

func (*accountLoopbackNetwork) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func (network *accountLoopbackNetwork) DialContext(ctx context.Context, kind, address string) (net.Conn, error) {
	network.calls.Add(1)
	if address != network.proxy {
		network.direct.Add(1)
		if address != "chatgpt.com:443" && address != "auth.openai.com:443" && address != "127.0.0.1:443" {
			return nil, errors.New("test refused a non-fixture address")
		}
		address = network.upstream
	}
	return (&net.Dialer{}).DialContext(ctx, kind, address)
}

func newAccountSOCKSFixture(t *testing.T, upstream string) (net.Listener, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
				// The production dialer resolves to the fixture's IPv4 address.
				greeting := make([]byte, 3)
				if _, err := io.ReadFull(connection, greeting); err != nil || string(greeting) != "\x05\x01\x00" {
					return
				}
				if _, err := connection.Write([]byte{5, 0}); err != nil {
					return
				}
				request := make([]byte, 10)
				if _, err := io.ReadFull(connection, request); err != nil || string(request) != "\x05\x01\x00\x01\x7f\x00\x00\x01\x01\xbb" {
					return
				}
				peer, err := net.DialTimeout("tcp4", upstream, time.Second)
				if err != nil {
					return
				}
				defer peer.Close()
				_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
				calls.Add(1)
				if _, err := connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
					return
				}
				done := make(chan struct{})
				go func() {
					_, _ = io.Copy(peer, connection)
					_ = peer.Close()
					close(done)
				}()
				_, _ = io.Copy(connection, peer)
				_ = connection.Close()
				<-done
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); workers.Wait() })
	return listener, &calls
}
