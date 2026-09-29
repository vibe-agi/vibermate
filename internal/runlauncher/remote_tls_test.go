package runlauncher

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serveridentity"
	"github.com/vibe-agi/vibermate/internal/servertransport"
)

func TestRemoteLoginSurvivesPrivateCALeafRenewal(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	clock := &remoteTLSClock{}
	clock.unix.Store(time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC).Unix())
	options := localca.DefaultOptions(filepath.Join(t.TempDir(), "root"), ctx)
	options.Clock = clock
	root, err := localca.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Shutdown(context.Background()) })
	identityDirectory := filepath.Join(t.TempDir(), "server")
	manager, err := serveridentity.OpenManager(ctx, identityDirectory, rand.Reader, clock.Now, root, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	firstIdentity := manager.Current()
	firstCertificate, err := firstIdentity.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	var active atomic.Pointer[tls.Certificate]
	active.Store(&firstCertificate)
	var logins atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != servercontrol.RuntimeUserSessionPath {
			http.NotFound(writer, request)
			return
		}
		logins.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(servercontrol.RuntimeUserSession{
			Schema: servercontrol.RuntimeUserSessionSchema, InstanceID: "instance.test",
			APIVersion: "v1", ProductBuild: "test-build",
			SessionID: "login.test", SessionToken: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x45}, 32)),
			User:      servercontrol.RuntimeUserView{ID: "user.test", Username: "alice"},
			ExpiresAt: clock.Now().Add(8 * time.Hour),
		})
	}))
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS13,
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{*active.Load()}}, nil
		},
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	target, err := serverconnection.ParseTarget(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	request := RemoteLoginRequest{
		Config: RemoteConfig{
			Target: target, StateDirectory: filepath.Join(t.TempDir(), "client"),
			DisplayName: "renewal-test", Clock: clock, Random: rand.Reader,
		},
		Username: "alice", Password: []byte("synthetic-password"),
	}
	first, err := LoginRemote(ctx, request)
	if err != nil || !first.FirstUse {
		t.Fatalf("initial private Server login = %+v, %v", first, err)
	}
	clock.unix.Store(firstCertificate.Leaf.NotAfter.Add(-20 * 24 * time.Hour).Unix())
	renewed, err := serveridentity.OpenManager(ctx, identityDirectory, rand.Reader, clock.Now, root, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Current().Fingerprint() == firstIdentity.Fingerprint() || renewed.Authority().Fingerprint != manager.Authority().Fingerprint {
		t.Fatal("fixture did not renew the Server leaf under the same CA")
	}
	renewedCertificate, err := renewed.Current().Certificate()
	if err != nil {
		t.Fatal(err)
	}
	active.Store(&renewedCertificate)
	second, err := LoginRemote(ctx, request)
	if err != nil {
		t.Fatalf("login after private Server leaf renewal: %v", err)
	}
	if second.FirstUse || second.TLSFingerprint != renewed.Current().Fingerprint() || logins.Load() != 2 {
		t.Fatalf("renewed login = %+v, received logins = %d", second, logins.Load())
	}
}

type remoteTLSClock struct{ unix atomic.Int64 }

func (clock *remoteTLSClock) Now() time.Time { return time.Unix(clock.unix.Load(), 0).UTC() }

func TestRemotePrivateCATrustRejectsInvalidPeersBeforeLogin(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	rootTemplate := x509.Certificate{
		Subject:   pkix.Name{CommonName: "synthetic private Server CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	root := issueRemoteTestCertificate(t, nil, rootTemplate)
	otherRoot := issueRemoteTestCertificate(t, nil, rootTemplate)
	leafTemplate := x509.Certificate{
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:   now.Add(-time.Hour), NotAfter: now.Add(12 * time.Hour),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	valid := issueRemoteTestCertificate(t, &root, leafTemplate)
	foreign := issueRemoteTestCertificate(t, &otherRoot, leafTemplate)
	wrongName := leafTemplate
	wrongName.IPAddresses, wrongName.DNSNames = nil, []string{"other.example.test"}
	expired := leafTemplate
	expired.NotAfter = now.Add(-time.Minute)
	future := leafTemplate
	future.NotBefore = now.Add(time.Hour)
	clientOnly := leafTemplate
	clientOnly.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	injectedRoot := foreign
	injectedRoot.Certificate = [][]byte{foreign.Certificate[0], root.Certificate[0]}
	missingChain := valid
	missingChain.Certificate = [][]byte{valid.Certificate[0]}
	brokenSignature := valid
	brokenSignature.Certificate = [][]byte{bytes.Clone(valid.Certificate[0]), root.Certificate[0]}
	brokenSignature.Certificate[0][len(brokenSignature.Certificate[0])-1] ^= 1
	digest := sha256.Sum256(root.Certificate[0])
	caFingerprint := hex.EncodeToString(digest[:])
	for _, test := range []struct {
		name string
		peer tls.Certificate
	}{
		{"different-ca", foreign},
		{"wrong-host", issueRemoteTestCertificate(t, &root, wrongName)},
		{"expired-leaf", issueRemoteTestCertificate(t, &root, expired)},
		{"not-yet-valid", issueRemoteTestCertificate(t, &root, future)},
		{"not-server-auth", issueRemoteTestCertificate(t, &root, clientOnly)},
		{"unrelated-ca-appended", injectedRoot},
		{"missing-ca", missingChain},
		{"invalid-signature", brokenSignature},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var active atomic.Pointer[tls.Certificate]
			active.Store(&valid)
			var logins atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost || request.URL.Path != servercontrol.RuntimeUserSessionPath {
					http.NotFound(writer, request)
					return
				}
				logins.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(writer).Encode(servercontrol.RuntimeUserSession{
					Schema: servercontrol.RuntimeUserSessionSchema, InstanceID: "instance.test",
					APIVersion: "v1", ProductBuild: "test-build", SessionID: "login.test",
					SessionToken: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x45}, 32)),
					User:         servercontrol.RuntimeUserView{ID: "user.test", Username: "alice"},
					ExpiresAt:    now.Add(8 * time.Hour),
				})
			}))
			server.TLS = &tls.Config{
				MinVersion: tls.VersionTLS13,
				GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
					return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{*active.Load()}}, nil
				},
			}
			server.StartTLS()
			t.Cleanup(server.Close)
			target, err := serverconnection.ParseTarget(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			request := RemoteLoginRequest{
				Config: RemoteConfig{
					Target: target, StateDirectory: filepath.Join(t.TempDir(), "client"),
					DisplayName: "trust-test", Clock: fixedRemoteClock{now: now}, Random: rand.Reader,
				},
				Username: "alice", Password: []byte("synthetic-password"),
			}
			if _, err := LoginRemote(ctx, request); err != nil {
				t.Fatal(err)
			}
			active.Store(&test.peer)
			if _, err := LoginRemote(ctx, request); err == nil {
				t.Fatal("login accepted an invalid replacement certificate")
			}
			transport, err := servertransport.Open(servertransport.Options{
				Target: target, TrustDirectory: filepath.Join(request.Config.StateDirectory, "trust"),
				Clock: request.Config.Clock, Timeout: 5 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer transport.Close()
			if connection, err := transport.Dial(ctx); err == nil {
				connection.Close()
				t.Fatal("proxy relay accepted an invalid replacement certificate")
			}
			if _, err := TrustRemote(ctx, target, request.Config.StateDirectory, request.Config.Clock, 5*time.Second, caFingerprint); err == nil {
				t.Fatal("explicit CA verification accepted an invalid peer")
			}
			if logins.Load() != 1 {
				t.Fatal("credentials reached an invalid TLS peer")
			}
			active.Store(&valid)
			if result, err := LoginRemote(ctx, request); err != nil || result.FirstUse {
				t.Fatalf("failed verification changed existing trust: %+v, %v", result, err)
			}
			connection, err := transport.Dial(ctx)
			if err != nil {
				t.Fatalf("proxy relay could not reconnect to valid Server: %v", err)
			}
			connection.Close()
		})
	}
}

func issueRemoteTestCertificate(t *testing.T, parent *tls.Certificate, template x509.Certificate) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template.SerialNumber = big.NewInt(1)
	issuer, signer := &template, key
	if parent != nil {
		issuer, signer = parent.Leaf, parent.PrivateKey.(*ecdsa.PrivateKey)
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, issuer, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	result := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	if parent != nil {
		result.Certificate = append(result.Certificate, parent.Certificate[0])
	}
	return result
}
