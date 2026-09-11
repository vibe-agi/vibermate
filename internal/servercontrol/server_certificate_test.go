package servercontrol

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/localca"

	"github.com/vibe-agi/vibermate/internal/serveridentity"
)

func testCertificateAuthority(t *testing.T) *localca.Authority {
	t.Helper()
	root, err := localca.Open(context.Background(), localca.DefaultOptions(filepath.Join(t.TempDir(), "root"), context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return root
}

func TestServerCertificateExportsOnlyTheActivePublicIdentity(t *testing.T) {
	t.Parallel()
	identity, err := serveridentity.Open(context.Background(), t.TempDir(), rand.Reader, time.Now(), "192.168.1.20")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"self_signed_tls", "tls_files"} {
		handler, err := NewServerCertificate(mode, identity)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, ServerCertificatePath, nil))
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("certificate status=%d headers=%v", response.Code, response.Header())
		}
		if strings.Contains(response.Body.String(), "PRIVATE KEY") || strings.Contains(response.Body.String(), "privateKey") {
			t.Fatal("certificate endpoint exposed private key material")
		}
		var view ServerCertificate
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.CA != nil || view.IssuedByCA {
			t.Fatal("unmanaged certificate claimed a managed HTTPS CA")
		}
		block, rest := pem.Decode([]byte(view.CertificatePEM))
		if block == nil || len(rest) != 0 {
			t.Fatal("invalid exported certificate")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || certificate.IsCA || certificate.VerifyHostname("192.168.1.20") != nil {
			t.Fatal("download is not the configured Server leaf certificate")
		}
		digest := sha256.Sum256(block.Bytes)
		if !view.Available || view.Fingerprint != hex.EncodeToString(digest[:]) || view.Fingerprint != identity.Fingerprint() {
			t.Fatal("download fingerprint does not match the active identity")
		}
	}
}

func TestManagedCertificateCAExportAndLegacyStatus(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		directory := t.TempDir()
		if legacy {
			if _, err := serveridentity.Open(context.Background(), directory, rand.Reader, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		root := testCertificateAuthority(t)
		manager, err := serveridentity.OpenManager(context.Background(), directory, rand.Reader, time.Now, root)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		NewManagedServerCertificate(manager).ServeHTTP(response, httptest.NewRequest(http.MethodGet, ServerCertificatePath, nil))
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "PRIVATE KEY") || strings.Contains(response.Body.String(), "privateKey") {
			t.Fatal("CA response contains secrets or failed")
		}
		var view ServerCertificate
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.CA == nil || view.IssuedByCA == legacy || view.CA.Schema != "vibermate-runtime-root-ca-v1" {
			t.Fatal("CA migration status is incorrect")
		}
		if view.CA.CertificatePEM != string(root.CertificatePEM()) {
			t.Fatal("export is not the traffic Root CA")
		}
		block, rest := pem.Decode([]byte(view.CA.CertificatePEM))
		if block == nil || len(rest) != 0 {
			t.Fatal("invalid CA PEM export")
		}
		ca, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !ca.IsCA {
			t.Fatalf("exported a leaf as CA: %v", err)
		}
		digest := sha256.Sum256(block.Bytes)
		if view.CA.Fingerprint != hex.EncodeToString(digest[:]) || view.CA.Fingerprint != manager.Authority().Fingerprint {
			t.Fatal("CA fingerprint is inconsistent")
		}
	}
}

func TestServerCertificateHTTPModeAndInvalidRoutes(t *testing.T) {
	t.Parallel()
	handler, err := NewServerCertificate("http", serveridentity.Identity{})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, ServerCertificatePath, nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "certificatePem") ||
		!strings.Contains(response.Body.String(), `"available":false`) {
		t.Fatal("HTTP mode must not offer a certificate")
	}
	for _, path := range []string{ServerCertificatePath + "?key=1", ServerCertificatePath + "/private-key"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatal("unexpected certificate route")
		}
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, ServerCertificatePath, nil))
	if response.Code != http.StatusNotFound {
		t.Fatal("certificate read accepted mutation")
	}
}

func TestCertificateMutationsValidateBodiesAndExportPublicCandidates(t *testing.T) {
	manager, err := serveridentity.OpenManager(context.Background(), t.TempDir(), rand.Reader, time.Now, testCertificateAuthority(t), "current.example.test")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewManagedServerCertificate(manager)
	initial := manager.Current().Fingerprint()
	call := func(path, body, host string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Host = host
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	valid := map[string]any{"schema": ServerCertificateStageSchema, "hosts": []string{"192.168.1.20"}, "expectedFingerprint": initial, "expectedPendingFingerprint": ""}
	body, _ := json.Marshal(valid)
	for _, invalid := range []string{
		`{}`, string(body) + ` {}`, strings.Replace(string(body), `"hosts":["192.168.1.20"]`, `"hosts":null`, 1),
		strings.Replace(string(body), "192.168.1.20", "https://192.168.1.20", 1),
		strings.TrimSuffix(string(body), "}") + `,"privateKeyPem":"forbidden"}`, strings.Repeat(" ", 17<<10),
	} {
		response := call(ServerCertificateStagePath, invalid, "localhost")
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid body status: %d", response.Code)
		}
	}
	response := call(ServerCertificateStagePath, string(body), "localhost")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "PRIVATE KEY") || strings.Contains(response.Body.String(), "privateKey") {
		t.Fatal("candidate response invalid or contains secret material")
	}
	var view ServerCertificate
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || !view.Managed || view.Pending == nil {
		t.Fatalf("pending view: %v", err)
	}
	if view.CA == nil || !view.Pending.IssuedByCA || view.Pending.CA != nil {
		t.Fatal("pending certificate does not identify its HTTPS CA")
	}
	apply, _ := json.Marshal(map[string]any{"schema": ServerCertificateApplySchema, "expectedFingerprint": initial, "pendingFingerprint": view.Pending.Fingerprint})
	if response := call(ServerCertificateApplyPath, string(apply), "current.example.test:9667"); response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "server_certificate_access_host_missing") {
		t.Fatal("allowed loss of current access address")
	}
	if manager.Current().Fingerprint() != initial {
		t.Fatal("rejected apply changed current identity")
	}
	if response := call(ServerCertificateApplyPath, string(apply), "localhost:9667"); response.Code != http.StatusOK {
		t.Fatalf("apply: %d", response.Code)
	}
	for _, mode := range []string{"http", "tls_files"} {
		identity := manager.Current()
		if mode == "http" {
			identity = serveridentity.Identity{}
		}
		readOnly, err := NewServerCertificate(mode, identity)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, ServerCertificateStagePath, strings.NewReader(string(body)))
		response := httptest.NewRecorder()
		readOnly.ServeHTTP(response, request)
		if response.Code != http.StatusConflict {
			t.Fatalf("%s certificate was editable", mode)
		}
	}
}
