package servercontrol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/serveridentity"
)

const (
	ServerCertificatePath        = "/api/v1/server/certificate"
	ServerCertificateSchema      = "vibermate-server-certificate-v1"
	ServerCertificateStagePath   = ServerCertificatePath + "/stage"
	ServerCertificateApplyPath   = ServerCertificatePath + "/apply"
	ServerCertificateStageSchema = "vibermate-server-certificate-stage-v1"
	ServerCertificateApplySchema = "vibermate-server-certificate-apply-v1"
)

// ServerCertificate contains only the public identity of the active HTTPS
// listener and the shared public Root. It never contains private keys.
type ServerCertificate struct {
	Schema         string                      `json:"schema"`
	Available      bool                        `json:"available"`
	Mode           string                      `json:"mode"`
	CertificatePEM string                      `json:"certificatePem,omitempty"`
	Fingerprint    string                      `json:"fingerprint,omitempty"`
	DNSNames       []string                    `json:"dnsNames,omitempty"`
	IPAddresses    []string                    `json:"ipAddresses,omitempty"`
	NotBefore      string                      `json:"notBefore,omitempty"`
	NotAfter       string                      `json:"notAfter,omitempty"`
	Managed        bool                        `json:"managed,omitempty"`
	Pending        *ServerCertificate          `json:"pending,omitempty"`
	CA             *ServerCertificateAuthority `json:"ca,omitempty"`
	IssuedByCA     bool                        `json:"issuedByCA,omitempty"`
}

// ServerCertificateAuthority exports the Runtime's single Root CA certificate,
// shared by managed HTTPS and authorized AI-traffic interception, never its key.
type ServerCertificateAuthority struct {
	Schema         string `json:"schema"`
	CertificatePEM string `json:"certificatePem"`
	Fingerprint    string `json:"fingerprint"`
	NotBefore      string `json:"notBefore"`
	NotAfter       string `json:"notAfter"`
}

type ServerCertificateHandler struct {
	certificate ServerCertificate
	manager     *serveridentity.Manager
}

func ServerCertificateRoute(path string) bool {
	return path == ServerCertificatePath || path == ServerCertificateStagePath || path == ServerCertificateApplyPath
}

func NewServerCertificate(mode string, identity serveridentity.Identity) (*ServerCertificateHandler, error) {
	view, err := publicServerCertificate(mode, identity)
	if err != nil {
		return nil, err
	}
	return &ServerCertificateHandler{certificate: view}, nil
}

func NewManagedServerCertificate(manager *serveridentity.Manager) *ServerCertificateHandler {
	return &ServerCertificateHandler{manager: manager}
}

func publicServerCertificate(mode string, identity serveridentity.Identity) (ServerCertificate, error) {
	view := ServerCertificate{Schema: ServerCertificateSchema, Mode: mode}
	if mode == "http" && !identity.Valid() {
		return view, nil
	}
	if (mode != "self_signed_tls" && mode != "tls_files") || !identity.Valid() {
		return ServerCertificate{}, errors.New("Runtime Server certificate transport is invalid")
	}
	certificate, err := identity.Certificate()
	if err != nil || certificate.Leaf == nil {
		return ServerCertificate{}, errors.New("Runtime Server public certificate is unavailable")
	}
	view.Available = true
	view.CertificatePEM = string(identity.CertificatePEM())
	view.Fingerprint = identity.Fingerprint()
	view.DNSNames = slices.Clone(certificate.Leaf.DNSNames)
	for _, address := range certificate.Leaf.IPAddresses {
		view.IPAddresses = append(view.IPAddresses, address.String())
	}
	view.NotBefore = certificate.Leaf.NotBefore.UTC().Format(time.RFC3339)
	view.NotAfter = certificate.Leaf.NotAfter.UTC().Format(time.RFC3339)
	return view, nil
}

func (handler *ServerCertificateHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler == nil || request == nil || request.URL.RawQuery != "" ||
		!ServerCertificateRoute(request.URL.Path) {
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
		return
	}
	if request.URL.Path == ServerCertificatePath && request.Method == http.MethodGet {
		handler.writeSnapshot(writer)
		return
	}
	if request.Method != http.MethodPost || request.URL.Path == ServerCertificatePath {
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
		return
	}
	if handler.manager == nil {
		writeProblem(writer, http.StatusConflict, "server_certificate_not_managed")
		return
	}
	var err error
	switch request.URL.Path {
	case ServerCertificateStagePath:
		var input struct {
			Schema                     string   `json:"schema"`
			Hosts                      []string `json:"hosts"`
			ExpectedFingerprint        string   `json:"expectedFingerprint"`
			ExpectedPendingFingerprint string   `json:"expectedPendingFingerprint"`
		}
		if !decodeCertificateMutation(request, &input) || input.Schema != ServerCertificateStageSchema ||
			!validCertificateFingerprint(input.ExpectedFingerprint) ||
			(input.ExpectedPendingFingerprint != "" && !validCertificateFingerprint(input.ExpectedPendingFingerprint)) || input.Hosts == nil {
			writeProblem(writer, http.StatusUnprocessableEntity, "invalid_server_certificate_request")
			return
		}
		_, err = handler.manager.Stage(request.Context(), input.Hosts, input.ExpectedFingerprint, input.ExpectedPendingFingerprint)
	case ServerCertificateApplyPath:
		var input struct {
			Schema              string `json:"schema"`
			ExpectedFingerprint string `json:"expectedFingerprint"`
			PendingFingerprint  string `json:"pendingFingerprint"`
		}
		if !decodeCertificateMutation(request, &input) || input.Schema != ServerCertificateApplySchema ||
			!validCertificateFingerprint(input.ExpectedFingerprint) || !validCertificateFingerprint(input.PendingFingerprint) {
			writeProblem(writer, http.StatusUnprocessableEntity, "invalid_server_certificate_request")
			return
		}
		accessHost := request.Host
		if host, _, splitErr := net.SplitHostPort(accessHost); splitErr == nil {
			accessHost = host
		}
		accessHost = strings.Trim(accessHost, "[]")
		var applied serveridentity.Identity
		applied, err = handler.manager.Apply(request.Context(), input.ExpectedFingerprint, input.PendingFingerprint, accessHost)
		if err == nil {
			log.Printf("server_tls_certificate_applied tlsFingerprint=%s", applied.Fingerprint())
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, serveridentity.ErrCertificateConflict):
			writeProblem(writer, http.StatusConflict, "server_certificate_conflict")
		case errors.Is(err, serveridentity.ErrInvalidHosts):
			writeProblem(writer, http.StatusUnprocessableEntity, "invalid_server_certificate_hosts")
		case errors.Is(err, serveridentity.ErrAccessHostMissing):
			writeProblem(writer, http.StatusUnprocessableEntity, "server_certificate_access_host_missing")
		case errors.Is(err, serveridentity.ErrCertificateCAChanged):
			writeProblem(writer, http.StatusConflict, "server_certificate_ca_changed")
		default:
			writeProblem(writer, http.StatusServiceUnavailable, "server_certificate_update_unavailable")
		}
		return
	}
	// This response uses the already-established TLS connection. It includes
	// the new public identity; callers need not reconnect to obtain the file.
	handler.writeSnapshot(writer)
}

func (handler *ServerCertificateHandler) writeSnapshot(writer http.ResponseWriter) {
	view := handler.certificate
	if handler.manager != nil {
		active, pending := handler.manager.Snapshot()
		var err error
		view, err = publicServerCertificate("self_signed_tls", active)
		if err != nil {
			writeProblem(writer, http.StatusServiceUnavailable, "server_certificate_unavailable")
			return
		}
		view.Managed = true
		ca := handler.manager.Authority()
		view.CA = &ServerCertificateAuthority{
			Schema: "vibermate-runtime-root-ca-v1", CertificatePEM: string(ca.CertificatePEM),
			Fingerprint: ca.Fingerprint,
			NotBefore:   ca.NotBefore.UTC().Format(time.RFC3339),
			NotAfter:    ca.NotAfter.UTC().Format(time.RFC3339),
		}
		view.IssuedByCA = handler.manager.IssuedByAuthority(active)
		if pending.Valid() {
			candidate, err := publicServerCertificate("self_signed_tls", pending)
			if err != nil {
				writeProblem(writer, http.StatusServiceUnavailable, "server_certificate_unavailable")
				return
			}
			view.Pending = &candidate
			candidate.IssuedByCA = handler.manager.IssuedByAuthority(pending)
		}
	}
	writeServerJSON(writer, http.StatusOK, view)
}

func decodeCertificateMutation(request *http.Request, input any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return false
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, (16<<10)+1))
	if err != nil || len(payload) == 0 || len(payload) > 16<<10 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		return false
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func validCertificateFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}
