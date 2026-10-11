package vlessprofile

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/vlesscore"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"
)

const tlsURI = "vless://11111111-2222-4333-8444-555555555555@node.fixture:443?security=tls&encryption=none&type=tcp"

func TestTLSImportAcceptsOrdinaryFlow(t *testing.T) {
	manager, _ := tlsManager(t)
	profile, err := manager.Import(t.Context(), tlsCommand())
	if err != nil {
		t.Fatalf("TLS profile rejected: %v", err)
	}
	if profile.Policy.Proxy.Kind != egressnetwork.ProxyVLESS {
		t.Fatal("not VLESS")
	}
	p, err := manager.loadPolicy(t.Context(), profile.Policy.Proxy)
	if err != nil {
		t.Fatal(err)
	}
	config, err := p.coreConfig()
	if err != nil || config.Security != vlesscore.SecurityTLS || config.TLS.Mode != vlesscore.TrustSystem || config.Flow != "" || config.ServerName != "node.fixture" {
		t.Fatal("ordinary TLS mapping lost trust, empty flow or host fallback")
	}
}

func tlsCertificate(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestTLSTrustEditsPreserveImmutableCredential(t *testing.T) {
	manager, store := tlsManager(t)
	cert := tlsCertificate(t)
	c := tlsCommand()
	c.URI += "&sni=tls.fixture&fp=firefox&flow=xtls-rprx-vision-udp443"
	c.TLS = &vlesscore.TLSOptions{Mode: vlesscore.TrustCustomCA, CAPEM: cert}
	first, err := manager.Import(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(store.items[first.Policy.Proxy.CredentialRef])
	defer clear(before)
	identity, err := manager.loadPolicy(t.Context(), first.Policy.Proxy)
	if err != nil {
		t.Fatal(err)
	}
	identity.TLS = nil
	c.URI = ""
	c.ExpectedRevision = 1
	c.TLS = nil
	c.DisplayName = "Edited public label"
	c.DirectDomains = "github.com"
	preserved, err := manager.Import(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, store.items[preserved.Policy.Proxy.CredentialRef]) || preserved.Policy.Proxy.CredentialRef == first.Policy.Proxy.CredentialRef || preserved.DisplayName != c.DisplayName || preserved.Policy.DirectDomains != "github.com" {
		t.Fatal("omitted TLS edit lost exact private bytes or public draft")
	}
	c.ExpectedRevision = 2
	c.TLS = &vlesscore.TLSOptions{Mode: vlesscore.TrustCustomCA, CAPEM: cert + cert}
	changed, err := manager.Import(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	p, err := manager.loadPolicy(t.Context(), changed.Policy.Proxy)
	if err != nil {
		t.Fatal(err)
	}
	if p.TLS.CAPEM != cert+cert {
		t.Fatal("new CA not saved")
	}
	p.TLS = nil
	if !reflect.DeepEqual(p, identity) {
		t.Fatal("CA edit changed node identity")
	}
	c.ExpectedRevision = 3
	c.TLS = &vlesscore.TLSOptions{Mode: vlesscore.TrustSystem}
	system, err := manager.Import(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	p, err = manager.loadPolicy(t.Context(), system.Policy.Proxy)
	if err != nil || p.TLS.CAPEM != "" {
		t.Fatal("explicit System retained CA")
	}
	p.TLS = nil
	if !reflect.DeepEqual(p, identity) {
		t.Fatal("System edit changed node identity")
	}
	historical, err := manager.profiles.GetRevision(t.Context(), first.ID, 1)
	if err != nil || !historical.Equal(first) || !bytes.Equal(before, store.items[first.Policy.Proxy.CredentialRef]) {
		t.Fatal("old profile or private bytes changed")
	}
	for rev, want := range map[egressprofile.Revision]vlesscore.TrustMode{1: vlesscore.TrustCustomCA, 2: vlesscore.TrustCustomCA, 3: vlesscore.TrustCustomCA, 4: vlesscore.TrustSystem} {
		d, err := manager.Describe(t.Context(), first.ID, rev)
		if err != nil || d.Security != vlesscore.SecurityTLS || d.TrustMode != want {
			t.Fatal("Describe did not read frozen revision")
		}
		data, _ := json.Marshal(d)
		if bytes.Contains(data, []byte("CERTIFICATE")) || bytes.Contains(data, []byte("fixture")) || len(strings.Split(string(data), ",")) != 2 {
			t.Fatal("Describe disclosed private material")
		}
	}
	c.ExpectedRevision = 1
	c.TLS = nil
	if _, err := manager.Import(t.Context(), c); !errors.Is(err, egressprofile.ErrRevisionConflict) {
		t.Fatal("stale edit did not conflict")
	}
	current, _ := manager.profiles.GetRevision(t.Context(), first.ID, 4)
	if !current.Equal(system) {
		t.Fatal("stale edit overwrote winner")
	}
	c.URI = tlsURI
	c.TLS = &vlesscore.TLSOptions{Mode: vlesscore.TrustCustomCA, CAPEM: cert}
	for _, value := range []any{c, identity, storedTLS{Mode: vlesscore.TrustCustomCA, CAPEM: cert}} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			s := fmt.Sprintf(format, value)
			if strings.Contains(s, "CERTIFICATE") || strings.Contains(s, "11111111") || strings.Contains(s, "node.fixture") {
				t.Fatal("formatter leaked private material")
			}
		}
	}
	data, _ := json.Marshal(c)
	if bytes.Contains(data, []byte("vless://")) || bytes.Contains(data, []byte("CERTIFICATE")) {
		t.Fatal("command JSON leaked private material")
	}
}

// Losing the original CAS after loading saved credentials would overwrite the winner.
func TestTLSTrustEditRaceKeepsWinningRevision(t *testing.T) {
	m, s := tlsManager(t)
	c := tlsCommand()
	cert := tlsCertificate(t)
	c.TLS = &vlesscore.TLSOptions{Mode: vlesscore.TrustCustomCA, CAPEM: cert}
	first, err := m.Import(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(s.items[first.Policy.Proxy.CredentialRef])
	defer clear(original)
	loser := tlsCommand()
	loser.URI = ""
	loser.ExpectedRevision = 1
	loser.TLS = &vlesscore.TLSOptions{Mode: vlesscore.TrustCustomCA, CAPEM: cert + cert}
	var winner egressprofile.ProfileRevision
	s.afterWrite = func() {
		s.afterWrite = nil
		command := loser
		command.TLS = &vlesscore.TLSOptions{Mode: vlesscore.TrustSystem}
		winner, err = m.Import(t.Context(), command)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Import(t.Context(), loser); !errors.Is(err, egressprofile.ErrRevisionConflict) {
		t.Fatal("concurrent trust edit lost CAS")
	}
	current, err := m.profiles.GetRevision(t.Context(), first.ID, 2)
	if err != nil || !current.Equal(winner) {
		t.Fatal("losing edit replaced winning public revision")
	}
	d, err := m.Describe(t.Context(), first.ID, 2)
	if err != nil || d.TrustMode != vlesscore.TrustSystem {
		t.Fatal("loser replaced winning trust")
	}
	if s.writes != 3 || !bytes.Equal(original, s.items[first.Policy.Proxy.CredentialRef]) {
		t.Fatal("failed CAS deleted possibly committed secret or changed original")
	}
}

func TestTLSReadUsesExactSecretRevisionAndValidatesSavedTrust(t *testing.T) {
	m, s := tlsManager(t)
	first, err := m.Import(t.Context(), tlsCommand())
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(s.items[first.Policy.Proxy.CredentialRef])
	defer clear(original)
	for _, trust := range []map[string]string{{"mode": "other"}, {"mode": "custom_ca", "caPem": "bad"}, {"mode": "system", "caPem": tlsCertificate(t)}} {
		var fields map[string]any
		json.Unmarshal(original, &fields)
		fields["tls"] = trust
		data, _ := json.Marshal(fields)
		s.items[first.Policy.Proxy.CredentialRef] = data
		if _, err := m.Describe(t.Context(), first.ID, 1); !errors.Is(err, ErrUnavailable) {
			t.Fatal("invalid saved trust silently defaulted")
		}
		edit := tlsCommand()
		edit.URI = ""
		edit.ExpectedRevision = 1
		if _, err := m.Import(t.Context(), edit); !errors.Is(err, ErrUnavailable) || s.writes != 1 {
			t.Fatal("invalid saved trust rewritten")
		}
	}
	s.items[first.Policy.Proxy.CredentialRef] = original
	// Latest secret exists, but the requested immutable private revision does not.
	policy := first.Policy.Proxy
	policy.CredentialRevision = 2
	if _, err := m.loadPolicy(t.Context(), policy); !errors.Is(err, ErrUnavailable) {
		t.Fatal("wrong secret revision fell back to latest")
	}
}

func TestTLSInvalidImportsWriteNothing(t *testing.T) {
	cert := tlsCertificate(t)
	malformed := "-----BEGIN CERTIFICATE-----\n%%%\n-----END CERTIFICATE-----\n"
	cases := map[string]ImportCommand{}
	for name, options := range map[string]vlesscore.TLSOptions{"empty-mode": {}, "unknown-mode": {Mode: "other"}, "system-ca": {Mode: vlesscore.TrustSystem, CAPEM: cert}, "empty-ca": {Mode: vlesscore.TrustCustomCA}, "partial-ca": {Mode: vlesscore.TrustCustomCA, CAPEM: cert + "junk"}, "malformed-ca": {Mode: vlesscore.TrustCustomCA, CAPEM: malformed + cert}, "mixed-ca": {Mode: vlesscore.TrustCustomCA, CAPEM: cert + string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1}}))}, "encoded-size": {Mode: vlesscore.TrustCustomCA, CAPEM: cert + strings.Repeat("\n", (64<<10)-len(cert))}} {
		c := tlsCommand()
		c.TLS = &options
		cases[name] = c
	}
	for name, suffix := range map[string]string{"key": "&pbk=", "sid": "&sid=", "unknown": "&allowInsecure=true", "duplicate": "&security=tls", "trust-query": "&caPem=secret", "bad-flow": "&flow=bad", "bad-fingerprint": "&fp=unsafe"} {
		c := tlsCommand()
		c.URI += suffix
		cases[name] = c
	}
	c := tlsCommand()
	c.URI = realityURI()
	c.TLS = &vlesscore.TLSOptions{Mode: vlesscore.TrustSystem}
	cases["reality-trust"] = c
	c = tlsCommand()
	c.URI = ""
	cases["edit-zero-revision"] = c
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m, s := tlsManager(t)
			if _, err := m.Import(t.Context(), c); err == nil {
				t.Fatal("invalid import accepted")
			}
			profiles, _ := m.profiles.List(t.Context())
			if s.writes != 0 || len(profiles) != 1 {
				t.Fatal("invalid import wrote secret or public profile")
			}
		})
	}
}

func realityURI() string {
	return "vless://11111111-2222-4333-8444-555555555555@node.fixture:443?security=reality&encryption=none&sni=tls.fixture&pbk=" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{47}, 32)) + "&sid=0123"
}
func TestTLSLegacyAndUnavailableReadback(t *testing.T) {
	m, s := tlsManager(t)
	c := tlsCommand()
	c.URI = realityURI()
	first, err := m.Import(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	json.Unmarshal(s.items[first.Policy.Proxy.CredentialRef], &legacy)
	delete(legacy, "security")
	data, _ := json.Marshal(legacy)
	s.items[first.Policy.Proxy.CredentialRef] = data
	d, err := m.Describe(t.Context(), first.ID, 1)
	if err != nil || d.Security != vlesscore.SecurityREALITY || d.TrustMode != "" {
		t.Fatal("legacy REALITY unavailable")
	}
	c.URI = ""
	c.ExpectedRevision = 1
	if _, err := m.Import(t.Context(), c); err != nil {
		t.Fatal("legacy credential preserving edit rejected")
	}
	c.ExpectedRevision = 2
	c.TLS = &vlesscore.TLSOptions{Mode: vlesscore.TrustSystem}
	if _, err := m.Import(t.Context(), c); !errors.Is(err, ErrInvalidURI) {
		t.Fatal("REALITY trust replacement accepted")
	}
	if _, err := m.Describe(t.Context(), egressprofile.DirectID, 1); !errors.Is(err, egressprofile.ErrInvalidProfile) {
		t.Fatal("non VLESS Describe accepted")
	}
	for _, test := range []struct {
		id   egressprofile.ID
		want error
	}{{egressprofile.DirectID, egressprofile.ErrInvalidProfile}, {"profile.missing", egressprofile.ErrProfileNotFound}} {
		edit := tlsCommand()
		edit.ID = test.id
		edit.ExpectedRevision = 1
		edit.URI = ""
		writes := s.writes
		if _, err := m.Import(t.Context(), edit); !errors.Is(err, test.want) || s.writes != writes {
			t.Fatal("missing/non-VLESS credential edit wrote private material or misclassified error")
		}
	}
	descriptionJSON, err := json.Marshal(d)
	if err != nil || string(descriptionJSON) != `{"security":"reality"}` {
		t.Fatal("REALITY description included private or TLS-only settings")
	}
	if _, err := m.Describe(t.Context(), first.ID, 99); !errors.Is(err, egressprofile.ErrProfileNotFound) {
		t.Fatal("missing revision misclassified")
	}
	for _, invalid := range []string{`{}`, `{"security":"tls","tls":{"mode":"custom_ca","caPem":"bad"}}`, string(data) + `{}`, `{"unexpected":"private"}`} {
		s.items[first.Policy.Proxy.CredentialRef] = []byte(invalid)
		if _, err := m.Describe(t.Context(), first.ID, 1); !errors.Is(err, ErrUnavailable) {
			t.Fatal("corrupt secret silently described")
		}
	}
	delete(s.items, first.Policy.Proxy.CredentialRef)
	if _, err := m.Describe(t.Context(), first.ID, 1); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing secret silently described")
	}
}

func tlsCommand() ImportCommand {
	return ImportCommand{ID: "profile.tls", DisplayName: "TLS fixture", URI: tlsURI, Resolver: egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverSystem, Transport: egressnetwork.ResolverTransportDirect}}
}
func tlsManager(t *testing.T) (*Manager, *tlsStore) {
	t.Helper()
	profiles, err := egressprofile.NewManager(&tlsRepository{revisions: map[egressprofile.Revision]egressprofile.ProfileRevision{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &tlsStore{items: map[string][]byte{}}
	manager, err := New(Options{Store: store, Profiles: profiles, Owner: t.Context()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	return manager, store
}

type tlsRepository struct {
	revisions map[egressprofile.Revision]egressprofile.ProfileRevision
	current   egressprofile.Revision
}

func (r *tlsRepository) Write(_ context.Context, expected egressprofile.Revision, p egressprofile.ProfileRevision) (egressprofile.CommitResult, error) {
	if expected != r.current {
		return egressprofile.CommitResult{Outcome: egressprofile.CommitConflict}, nil
	}
	r.revisions[p.Revision] = p
	r.current = p.Revision
	return egressprofile.CommitResult{Outcome: egressprofile.CommitCommitted, Revision: p}, nil
}
func (r *tlsRepository) LoadRevision(_ context.Context, id egressprofile.ID, rev egressprofile.Revision) (egressprofile.ProfileRevision, bool, error) {
	p, ok := r.revisions[rev]
	return p, ok && p.ID == id, nil
}
func (r *tlsRepository) LoadCurrent(context.Context) ([]egressprofile.ProfileRevision, error) {
	if r.current == 0 {
		return nil, nil
	}
	return []egressprofile.ProfileRevision{r.revisions[r.current]}, nil
}

type tlsStore struct {
	items      map[string][]byte
	writes     int
	afterWrite func()
}

func (s *tlsStore) Read(ctx context.Context, ref secretstore.Reference) (*secretstore.Value, error) {
	return s.ReadAtRevision(ctx, ref, 1)
}
func (s *tlsStore) ReadAtRevision(_ context.Context, ref secretstore.Reference, rev secretstore.Revision) (*secretstore.Value, error) {
	b, ok := s.items[ref.String()]
	if !ok || rev != 1 {
		return nil, secretstore.ErrNotFound
	}
	return secretstore.NewValue(b)
}
func (s *tlsStore) Inspect(_ context.Context, ref secretstore.Reference) (secretstore.Metadata, error) {
	if _, ok := s.items[ref.String()]; !ok {
		return secretstore.Metadata{State: secretstore.StateMissing}, nil
	}
	return secretstore.Metadata{State: secretstore.StateConfigured, Revision: 1}, nil
}
func (s *tlsStore) Replace(_ context.Context, c secretstore.ReplaceCommand) (secretstore.Metadata, error) {
	if _, ok := s.items[c.Reference.String()]; ok || c.ExpectedRevision != 0 {
		return secretstore.Metadata{}, secretstore.ErrRevisionConflict
	}
	b, err := c.Value.CopyBytes()
	if err != nil {
		return secretstore.Metadata{}, err
	}
	s.items[c.Reference.String()] = b
	s.writes++
	if s.afterWrite != nil {
		s.afterWrite()
	}
	return secretstore.Metadata{State: secretstore.StateConfigured, Revision: 1}, nil
}
func (s *tlsStore) Delete(_ context.Context, ref secretstore.Reference) error {
	delete(s.items, ref.String())
	return nil
}
