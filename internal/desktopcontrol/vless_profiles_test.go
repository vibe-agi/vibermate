package desktopcontrol_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

// Dropping owner auth, URI cleanup/redaction, physical secret CAS, immutable
// credential versions or API idempotency must break consumer-visible results.
func TestOwnerControlImportsPrivateVLESSAndPreservesRevisions(t *testing.T) {
	secrets := newCredentialStoreFixture()
	runtime := startRuntimeWithSecrets(t, secrets)
	defer shutdownRuntime(t, runtime)
	app, err := desktopcontrol.New(desktopcontrol.Options{Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), CodeLibrary: runtime.CodeLibrary(), EgressProfiles: runtime.EgressProfiles(), VLESSProfiles: runtime.VLESSProfiles(), Offline: runtime, Clock: desktopcontrol.SystemClock{}, ManualCaptures: runtime.ManualCaptures()})
	if err != nil {
		t.Fatal(err)
	}
	readToken, writeToken := capability(0x41), capability(0x42)
	auth, err := desktopcontrol.NewAuthenticator(desktopcontrol.CapabilityGrant{ReadToken: readToken, WriteToken: writeToken, ExpiresAt: time.Now().Add(time.Minute)}, desktopcontrol.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	const authority = "127.0.0.1:43129"
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{Authority: authority, AllowedOrigins: []string{"vibermate://desktop"}, Authenticator: auth, Application: app, Bootstrap: emptyBootstrap(), CLIControl: http.NotFoundHandler(), ManualCaptures: rejectingManualCaptureHandler{}, DesktopPrincipal: desktopManualPrincipal(t)})
	if err != nil {
		t.Fatal(err)
	}
	const identity = "11111111-2222-4333-8444-555555555555"
	key := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{47}, 32))
	uri := "vless://" + identity + "@private-node.fixture:443?security=reality&encryption=none&type=tcp&flow=xtls-rprx-vision-udp443&sni=private-sni.fixture&pbk=" + key + "&sid=0123456789abcdef&fp=chrome#private-fragment"
	payload := func(uri string) []byte {
		data, err := json.Marshal(map[string]any{"id": "profile.imported", "displayName": "User node label", "uri": uri, "resolver": map[string]string{"kind": "system", "transport": "direct"}, "directDomains": "Github.com\n*.github.com\ngithub.com"})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	request := func(token string, expected uint64, idempotency string, body []byte) *httptest.ResponseRecorder {
		r := newRequest(http.MethodPost, authority, "/api/v1/egress-profiles/import-vless", token, body)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("If-Match", strconv.FormatUint(expected, 10))
		r.Header.Set("Idempotency-Key", idempotency)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		return response
	}
	assertPrivate := func(response *httptest.ResponseRecorder) {
		t.Helper()
		for _, private := range []string{uri, identity, key, "private-node.fixture", "private-sni.fixture", "private-fragment"} {
			if bytes.Contains(response.Body.Bytes(), []byte(private)) {
				t.Fatal("control response/error disclosed private VLESS material")
			}
		}
	}
	body := payload(uri)
	defer clear(body)
	denied := request(readToken, 0, "vless-read-denied-0001", body)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("read token imported private profile: status%d", denied.Code)
	}
	created := request(writeToken, 0, "vless-import-version-0001", body)
	assertPrivate(created)
	if created.Code != http.StatusOK {
		t.Fatalf("owner import status%d body%s", created.Code, created.Body.Bytes())
	}
	var one desktopcontrol.EgressProfileResponse
	if err := json.Unmarshal(created.Body.Bytes(), &one); err != nil || one.Revision != 1 || one.Policy.Proxy.Kind != egressnetwork.ProxyVLESS || one.Policy.Proxy.CredentialRevision != 1 || one.Policy.DirectDomains != "*.github.com\ngithub.com" {
		t.Fatal("owner import lost canonical public metadata")
	}
	firstRef, err := secretstore.ParseReference(one.Policy.Proxy.CredentialRef)
	if err != nil || firstRef.Namespace() != "egress-vless" {
		t.Fatal("wrong private reference namespace")
	}
	readPrivate := func(ref secretstore.Reference) []byte {
		t.Helper()
		value, err := secrets.ReadAtRevision(t.Context(), ref, 1)
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
	private := readPrivate(firstRef)
	defer clear(private)
	var stored struct {
		Host string `json:"host"`
		UUID string `json:"uuid"`
		Key  string `json:"key"`
		Flow string `json:"flow"`
	}
	if err := json.Unmarshal(private, &stored); err != nil || stored.Host != "private-node.fixture" || stored.UUID != identity || stored.Key != key || stored.Flow != "xtls-rprx-vision-udp443" {
		t.Fatal("private compact DTO did not retain exact node credential/flow")
	}
	stored = struct {
		Host string `json:"host"`
		UUID string `json:"uuid"`
		Key  string `json:"key"`
		Flow string `json:"flow"`
	}{}
	replay := request(writeToken, 0, "vless-import-version-0001", body)
	assertPrivate(replay)
	if replay.Code != http.StatusOK || replay.Body.String() != created.Body.String() {
		t.Fatal("import replay allocated another public revision")
	}
	second := request(writeToken, 1, "vless-import-version-0002", body)
	assertPrivate(second)
	var two desktopcontrol.EgressProfileResponse
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &two) != nil || two.Revision != 2 || two.Policy.Proxy.CredentialRef == one.Policy.Proxy.CredentialRef {
		t.Fatal("new import mutated prior credential identity")
	}
	historical, err := runtime.EgressProfiles().GetRevision(t.Context(), egressprofile.ID(one.ID), 1)
	if err != nil || historical.Policy != one.Policy {
		t.Fatal("earlier public profile lost its pinned private credential")
	}
	again := readPrivate(firstRef)
	defer clear(again)
	if !bytes.Equal(again, private) {
		t.Fatal("earlier secret bytes were replaced")
	}
	stale := request(writeToken, 0, "vless-import-stale-cas-0001", body)
	assertPrivate(stale)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale import status%d", stale.Code)
	}
	count := 0
	secrets.mu.Lock()
	for ref := range secrets.items {
		if strings.HasPrefix(ref, "secret://egress-vless/") {
			count++
		}
	}
	secrets.mu.Unlock()
	if count != 3 {
		t.Fatalf("expected immutable versions plus safely retained failed-CAS orphan, got%d", count)
	}
	unsupported := payload(strings.Replace(uri, "type=tcp", "type=ws", 1))
	defer clear(unsupported)
	rejected := request(writeToken, 2, "vless-import-unsupported-0001", unsupported)
	assertPrivate(rejected)
	if rejected.Code != http.StatusUnprocessableEntity || !bytes.Contains(rejected.Body.Bytes(), []byte("raw/TCP, TLS or REALITY")) {
		t.Fatal("unsupported URI did not return a clear sanitized error")
	}
	t.Log("authenticated owner import, strict public response, private physical revision, idempotent replay, immutable credential edits and failed-CAS orphan retention verified")
}

func TestOwnerControlTLSSaveEditAndSafeReadback(t *testing.T) {
	secrets := newCredentialStoreFixture()
	runtime := startRuntimeWithSecrets(t, secrets)
	defer shutdownRuntime(t, runtime)
	app, err := desktopcontrol.New(desktopcontrol.Options{Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), CodeLibrary: runtime.CodeLibrary(), EgressProfiles: runtime.EgressProfiles(), VLESSProfiles: runtime.VLESSProfiles(), Offline: runtime, Clock: desktopcontrol.SystemClock{}, ManualCaptures: runtime.ManualCaptures()})
	if err != nil {
		t.Fatal(err)
	}
	readToken, writeToken := capability(0x61), capability(0x62)
	auth, err := desktopcontrol.NewAuthenticator(desktopcontrol.CapabilityGrant{ReadToken: readToken, WriteToken: writeToken, ExpiresAt: time.Now().Add(time.Minute)}, desktopcontrol.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	const authority = "127.0.0.1:43129"
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{Authority: authority, AllowedOrigins: []string{"vibermate://desktop"}, Authenticator: auth, Application: app, Bootstrap: emptyBootstrap(), CLIControl: http.NotFoundHandler(), ManualCaptures: rejectingManualCaptureHandler{}, DesktopPrincipal: desktopManualPrincipal(t)})
	if err != nil {
		t.Fatal(err)
	}
	cert := controlTLSCertificate(t)
	const uri = "vless://11111111-2222-4333-8444-555555555555@private-node.fixture:443?security=tls&encryption=none&type=tcp&sni=private-sni.fixture&fp=firefox"
	body := func(link string, tls any) []byte {
		input := map[string]any{"id": "profile.tls-control", "displayName": "TLS node", "uri": link, "resolver": map[string]string{"kind": "system", "transport": "direct"}, "directDomains": "github.com"}
		if tls != nil {
			input["tls"] = tls
		}
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	request := func(method, path, token string, expected uint64, key string, data []byte) *httptest.ResponseRecorder {
		r := newRequest(method, authority, path, token, data)
		if method == http.MethodPost {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("If-Match", strconv.FormatUint(expected, 10))
			r.Header.Set("Idempotency-Key", "vless-control-"+key)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		for _, secret := range []string{uri, "11111111-2222-4333-8444-555555555555", "private-node.fixture", "private-sni.fixture", cert, "BEGIN CERTIFICATE"} {
			if bytes.Contains(w.Body.Bytes(), []byte(secret)) {
				t.Fatal("response leaked private TLS material")
			}
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("TLS response missing no-store")
		}
		return w
	}
	const post = "/api/v1/egress-profiles/import-vless"
	createBody := body(uri, map[string]string{"mode": "custom_ca", "caPem": cert})
	defer clear(createBody)
	if w := request(http.MethodPost, post, readToken, 0, "tls-read-denied-0001", createBody); w.Code != 401 {
		t.Fatal("read token wrote TLS profile")
	}
	created := request(http.MethodPost, post, writeToken, 0, "tls-import-0001", createBody)
	if created.Code != 200 {
		t.Fatalf("TLS wire import rejected: status%d body%s", created.Code, created.Body.String())
	}
	var first desktopcontrol.EgressProfileResponse
	json.Unmarshal(created.Body.Bytes(), &first)
	replay := request(http.MethodPost, post, writeToken, 0, "tls-import-0001", createBody)
	if replay.Code != 200 || replay.Body.String() != created.Body.String() {
		t.Fatal("TLS replay changed revision")
	}
	if w := request(http.MethodPost, post, writeToken, 0, "tls-import-0001", body(uri, map[string]string{"mode": "system"})); w.Code != 409 {
		t.Fatal("changed idempotency body accepted")
	}
	const prefix = "/api/v1/egress-profiles/profile.tls-control/revisions/"
	read := request(http.MethodGet, prefix+"1/vless-settings", readToken, 0, "", nil)
	var description map[string]string
	json.Unmarshal(read.Body.Bytes(), &description)
	if read.Code != 200 || len(description) != 2 || description["security"] != "tls" || description["trustMode"] != "custom_ca" {
		t.Fatal("saved TLS description not safe exact enums")
	}
	if w := request(http.MethodGet, prefix+"1/vless-settings", "", 0, "", nil); w.Code != 401 {
		t.Fatal("anonymous settings accessible")
	}
	preserved := request(http.MethodPost, post, writeToken, 1, "tls-preserve-0002", body("", nil))
	if preserved.Code != 200 {
		t.Fatal("empty URI omitted trust edit rejected")
	}
	system := request(http.MethodPost, post, writeToken, 2, "tls-system-0003", body("", map[string]string{"mode": "system"}))
	if system.Code != 200 {
		t.Fatal("empty URI trust edit rejected")
	}
	if w := request(http.MethodPost, post, writeToken, 1, "tls-stale-0004", body("", nil)); w.Code != 409 {
		t.Fatal("stale saved credential edit accepted")
	}
	old := request(http.MethodGet, prefix+"1/vless-settings", readToken, 0, "", nil)
	if old.Body.String() != read.Body.String() {
		t.Fatal("old settings changed")
	}
	current := request(http.MethodGet, prefix+"3/vless-settings", readToken, 0, "", nil)
	json.Unmarshal(current.Body.Bytes(), &description)
	if current.Code != 200 || description["trustMode"] != "system" {
		t.Fatal("new System settings unavailable")
	}
	for path, status := range map[string]int{prefix + "0/vless-settings": 422, prefix + "bad/vless-settings": 422, prefix + "999/vless-settings": 404, prefix + "1/vless-settings?x=1": 422, "/api/v1/egress-profiles/profile.direct/revisions/1/vless-settings": 422} {
		if w := request(http.MethodGet, path, readToken, 0, "", nil); w.Code != status {
			t.Fatalf("invalid settings path status%d want%d", w.Code, status)
		}
	}
	secrets.mu.Lock()
	beforeCount := len(secrets.items)
	secrets.mu.Unlock()
	for index, tls := range []any{map[string]string{}, map[string]string{"mode": "custom_ca", "caPem": "invalid"}, map[string]any{"mode": "system", "file": "/private"}, map[string]any{"mode": "system", "allowInsecure": true}, map[string]string{"mode": "system", "caPem": cert}} {
		w := request(http.MethodPost, post, writeToken, 3, "tls-invalid-"+strconv.Itoa(index), body("", tls))
		if w.Code != 422 {
			t.Fatal("invalid wire trust accepted")
		}
	}
	secrets.mu.Lock()
	afterCount := len(secrets.items)
	item := secrets.items[first.Policy.Proxy.CredentialRef]
	item.bytes = []byte(`{}`)
	secrets.items[first.Policy.Proxy.CredentialRef] = item
	secrets.mu.Unlock()
	if beforeCount != afterCount {
		t.Fatal("invalid wire request wrote secret")
	}
	if w := request(http.MethodGet, prefix+"1/vless-settings", readToken, 0, "", nil); w.Code != 503 {
		t.Fatal("corrupt settings silently defaulted")
	}
}

func controlTLSCertificate(t *testing.T) string {
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
