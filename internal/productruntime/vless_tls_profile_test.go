package productruntime

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/vlesscore"
	"github.com/vibe-agi/vibermate/internal/vlessprofile"
)

// The file-backed host secret and SQL revisions must both survive Runtime reopen.
func TestRuntimeTLSTrustPersistsAcrossReopen(t *testing.T) {
	paths, err := NewRuntimePaths(filepath.Join(t.TempDir(), "runtime-data"))
	if err != nil {
		t.Fatal(err)
	}
	options := testOptionsWithPaths(t, paths, hostcontract.Desktop(), &coordinatorDouble{})
	first := startTestRuntime(t, options)
	t.Cleanup(func() { shutdownRuntime(t, first) })
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	c := vlessprofile.ImportCommand{ID: "profile.persist-tls", DisplayName: "TLS persistence", URI: "vless://11111111-2222-4333-8444-555555555555@node.fixture:443?security=tls&encryption=none&sni=tls.fixture&fp=firefox", TLS: &vlesscore.TLSOptions{Mode: vlesscore.TrustCustomCA, CAPEM: cert}, Resolver: egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverSystem, Transport: egressnetwork.ResolverTransportDirect}}
	one, err := first.VLESSProfiles().Import(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	readSecret := func(store secretstore.Store, policy egressnetwork.ProxyPolicy) []byte {
		t.Helper()
		ref, err := secretstore.ParseReference(policy.CredentialRef)
		if err != nil {
			t.Fatal(err)
		}
		value, err := store.ReadAtRevision(t.Context(), ref, secretstore.Revision(policy.CredentialRevision))
		if err != nil {
			t.Fatal(err)
		}
		defer value.Destroy()
		data, err := value.CopyBytes()
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	original := readSecret(options.Secrets, one.Policy.Proxy)
	defer clear(original)
	publicData, _ := json.Marshal(one)
	originalDigest := sha256.Sum256(publicData)
	c.URI = ""
	c.ExpectedRevision = 1
	c.TLS = &vlesscore.TLSOptions{Mode: vlesscore.TrustSystem}
	two, err := first.VLESSProfiles().Import(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if two.Policy.Proxy.CredentialRef == one.Policy.Proxy.CredentialRef {
		t.Fatal("trust replacement reused old secret")
	}
	shutdownRuntime(t, first)
	reopenedOptions := testOptionsWithPaths(t, paths, hostcontract.Desktop(), &coordinatorDouble{})
	second := startTestRuntime(t, reopenedOptions)
	defer shutdownRuntime(t, second)
	for revision, want := range map[uint64]vlesscore.TrustMode{1: vlesscore.TrustCustomCA, 2: vlesscore.TrustSystem} {
		d, err := second.VLESSProfiles().Describe(t.Context(), one.ID, egressprofile.Revision(revision))
		if err != nil || d.Security != vlesscore.SecurityTLS || d.TrustMode != want {
			t.Fatal("reopened private trust unavailable")
		}
	}
	historical, err := second.EgressProfiles().GetRevision(t.Context(), one.ID, 1)
	if err != nil || !historical.Equal(one) {
		t.Fatal("SQL historical profile changed")
	}
	historicalData, _ := json.Marshal(historical)
	if sha256.Sum256(historicalData) != originalDigest {
		t.Fatal("old profile digest changed")
	}
	restored := readSecret(reopenedOptions.Secrets, historical.Policy.Proxy)
	defer clear(restored)
	if !bytes.Equal(restored, original) || !bytes.Contains(restored, []byte(`"caPem"`)) {
		t.Fatal("host-secret lost original private CA")
	}
	newest := readSecret(reopenedOptions.Secrets, two.Policy.Proxy)
	defer clear(newest)
	var saved struct {
		Host, UUID, SNI, Fingerprint, Flow string
		Port                               uint16
		TLS                                struct {
			Mode  string
			CAPEM string `json:"caPem"`
		}
	}
	if json.Unmarshal(newest, &saved) != nil || saved.Host != "node.fixture" || saved.UUID != "11111111-2222-4333-8444-555555555555" || saved.SNI != "tls.fixture" || saved.Fingerprint != "firefox" || saved.Flow != "" || saved.Port != 443 || saved.TLS.Mode != "system" || saved.TLS.CAPEM != "" {
		t.Fatal("reopened trust edit lost identity or retained CA")
	}
	t.Log("actual temporary SQL and file-backed host-secret reopened; old custom CA and new System revision retained")
}
