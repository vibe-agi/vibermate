package servercontrol

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"time"
)

const (
	RuntimeRootCAPath   = "/api/v1/server/root-ca"
	RuntimeRootCASchema = "vibermate-runtime-root-ca-v1"
)

// RuntimeRootCA contains only the current Runtime's public trust anchor.
// HTTPS leaf identities, private keys and certificate mutations are not exposed.
type RuntimeRootCA struct {
	Schema         string `json:"schema"`
	CertificatePEM string `json:"certificatePem"`
	Fingerprint    string `json:"fingerprint"`
	NotBefore      string `json:"notBefore"`
	NotAfter       string `json:"notAfter"`
}

type RuntimeRootCAHandler struct {
	certificate func() []byte
}

// NewRuntimeRootCA reads the public certificate on every request so an
// explicitly replaced Runtime Root is never served from a stale export cache.
// Authentication is enforced by the enclosing Server or Desktop router.
func NewRuntimeRootCA(certificate func() []byte) *RuntimeRootCAHandler {
	return &RuntimeRootCAHandler{certificate: certificate}
}

func (handler *RuntimeRootCAHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request == nil || request.URL == nil || request.Method != http.MethodGet ||
		request.URL.Path != RuntimeRootCAPath || request.URL.RawQuery != "" || request.URL.ForceQuery {
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
		return
	}
	if handler == nil || handler.certificate == nil {
		writeProblem(writer, http.StatusServiceUnavailable, "runtime_root_ca_unavailable")
		return
	}
	encoded := handler.certificate()
	if len(encoded) == 0 || len(encoded) > 64*1024 ||
		!bytes.HasPrefix(bytes.TrimSpace(encoded), []byte("-----BEGIN CERTIFICATE-----")) {
		writeProblem(writer, http.StatusServiceUnavailable, "runtime_root_ca_unavailable")
		return
	}
	block, rest := pem.Decode(encoded)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		writeProblem(writer, http.StatusServiceUnavailable, "runtime_root_ca_unavailable")
		return
	}
	root, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !root.IsCA || !root.BasicConstraintsValid ||
		root.KeyUsage&x509.KeyUsageCertSign == 0 || len(root.UnhandledCriticalExtensions) != 0 ||
		!bytes.Equal(root.RawIssuer, root.RawSubject) || root.CheckSignatureFrom(root) != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "runtime_root_ca_unavailable")
		return
	}
	digest := sha256.Sum256(root.Raw)
	writeServerJSON(writer, http.StatusOK, RuntimeRootCA{
		Schema: RuntimeRootCASchema, CertificatePEM: string(pem.EncodeToMemory(block)),
		Fingerprint: hex.EncodeToString(digest[:]),
		NotBefore:   root.NotBefore.UTC().Format(time.RFC3339),
		NotAfter:    root.NotAfter.UTC().Format(time.RFC3339),
	})
}
