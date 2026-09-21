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

func TestRuntimeRootCAExportsOnlyTheUnifiedPublicRoot(t *testing.T) {
	t.Parallel()
	root := testCertificateAuthority(t)
	handler := NewRuntimeRootCA(root.CertificatePEM)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RuntimeRootCAPath, nil))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Root CA status=%d headers=%v", response.Code, response.Header())
	}
	var view RuntimeRootCA
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Schema != RuntimeRootCASchema || view.CertificatePEM != string(root.CertificatePEM()) {
		t.Fatal("export is not the exact Runtime/traffic Root")
	}
	block, rest := pem.Decode([]byte(view.CertificatePEM))
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatal("export is not one public certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !certificate.IsCA || certificate.CheckSignatureFrom(certificate) != nil {
		t.Fatal("export is not a self-signed Root CA")
	}
	digest := sha256.Sum256(block.Bytes)
	if view.Fingerprint != hex.EncodeToString(digest[:]) || view.Fingerprint != root.Identity().Fingerprint() ||
		view.NotBefore != certificate.NotBefore.UTC().Format(time.RFC3339) || view.NotAfter != certificate.NotAfter.UTC().Format(time.RFC3339) {
		t.Fatal("Root CA metadata is inconsistent")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil || len(fields) != 5 {
		t.Fatal("Root CA export contains unexpected fields")
	}
	for _, forbidden := range []string{"PRIVATE", "privateKey", "pending", "managed", "dnsNames", "ipAddresses", "issuedByCA"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("export contains forbidden material: %s", forbidden)
		}
	}
}

func TestRuntimeRootCAIsReadOnlyAndRejectsRemovedRoutes(t *testing.T) {
	t.Parallel()
	calls := 0
	handler := NewRuntimeRootCA(func() []byte { calls++; return nil })
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead} {
		for _, path := range []string{RuntimeRootCAPath, RuntimeRootCAPath + "?", RuntimeRootCAPath + "?key=1", RuntimeRootCAPath + "/private-key",
			"/api/v1/server/certificate", "/api/v1/server/certificate/stage", "/api/v1/server/certificate/apply"} {
			if method == http.MethodGet && path == RuntimeRootCAPath {
				continue
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("unexpected route accepted: %s %s (%d)", method, path, response.Code)
			}
		}
	}
	for _, request := range []*http.Request{nil, {Method: http.MethodGet}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatal("invalid request accepted")
		}
	}
	if calls != 0 {
		t.Fatal("rejected request accessed certificate material")
	}
}

func TestRuntimeRootCARejectsInvalidOrPrivateMaterial(t *testing.T) {
	t.Parallel()
	root := testCertificateAuthority(t).CertificatePEM()
	leaf, err := serveridentity.Open(context.Background(), t.TempDir(), rand.Reader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for name, encoded := range map[string][]byte{
		"empty":         nil,
		"oversized":     []byte(strings.Repeat(" ", 64*1024+1)),
		"malformed":     []byte("-----BEGIN CERTIFICATE-----\nAA==\n-----END CERTIFICATE-----\n"),
		"leaf":          leaf.CertificatePEM(),
		"bundle":        append(append([]byte{}, root...), root...),
		"trailing-key":  append(append([]byte{}, root...), []byte("-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n")...),
		"leading-key":   append([]byte("-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n"), root...),
		"leading-junk":  append([]byte("not a certificate\n"), root...),
		"trailing-junk": append(append([]byte{}, root...), []byte("not a certificate")...),
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			NewRuntimeRootCA(func() []byte { return encoded }).ServeHTTP(response, httptest.NewRequest(http.MethodGet, RuntimeRootCAPath, nil))
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "certificatePem") || strings.Contains(response.Body.String(), "PRIVATE") {
				t.Fatal("invalid material was exported")
			}
		})
	}
	for _, handler := range []*RuntimeRootCAHandler{nil, NewRuntimeRootCA(nil)} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RuntimeRootCAPath, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatal("missing provider accepted")
		}
	}
}

func TestRuntimeRootCAReadsCurrentPublicMaterialOnEveryDownload(t *testing.T) {
	t.Parallel()
	first, second := testCertificateAuthority(t), testCertificateAuthority(t)
	current := first
	handler := NewRuntimeRootCA(func() []byte { return current.CertificatePEM() })
	for _, authority := range []*localca.Authority{first, second, second} {
		current = authority
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RuntimeRootCAPath, nil))
		var view RuntimeRootCA
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || view.Fingerprint != current.Identity().Fingerprint() {
			t.Fatal("download returned a stale Root CA")
		}
	}
}
