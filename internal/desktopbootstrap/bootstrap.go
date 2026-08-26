// Package desktopbootstrap owns the one-shot native-shell exchange that keeps
// App capabilities out of argv, URLs, local control discovery, and logs.
package desktopbootstrap

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	DescriptorSchema = "vibermate-daemon-bootstrap-v1"
	FailureSchema    = "vibermate-daemon-failure-v1"
	ProgressSchema   = "vibermate-daemon-progress-v1"
	SessionSchema    = "vibermate-app-session-v1"
	nonceDomain      = "vibermate:desktop-bootstrap:v1:"
	capabilityBytes  = 32
)

type ProgressPhase string

const ProgressRuntimeStarting ProgressPhase = "runtime_starting"

type FailureReason string

const (
	FailureRuntimeUnavailable     FailureReason = "runtime_unavailable"
	FailureRuntimeAlreadyActive   FailureReason = "runtime_already_active"
	FailureSecretStoreUnavailable FailureReason = "secret_store_unavailable"
	FailureStorageUnavailable     FailureReason = "storage_unavailable"
)

// Failure is the only startup diagnosis that crosses the native pipe. It is a
// closed reason code and deliberately carries no error text, paths, or secret
// material.
type Failure struct {
	Schema string        `json:"schema"`
	Reason FailureReason `json:"reason"`
}

func StartupFailure(reason FailureReason) Failure {
	return Failure{Schema: FailureSchema, Reason: reason}
}

func (failure Failure) Validate() error {
	if failure.Schema != FailureSchema {
		return errors.New("Desktop bootstrap failure is invalid")
	}
	switch failure.Reason {
	case FailureRuntimeUnavailable,
		FailureRuntimeAlreadyActive,
		FailureSecretStoreUnavailable,
		FailureStorageUnavailable:
		return nil
	default:
		return errors.New("Desktop bootstrap failure is invalid")
	}
}

// Progress is a bounded, capability-free native bootstrap frame. It lets the
// shell distinguish a live process entering storage/runtime initialization
// from a process that never reached its own startup path.
type Progress struct {
	Schema string        `json:"schema"`
	Phase  ProgressPhase `json:"phase"`
}

func RuntimeStartingProgress() Progress {
	return Progress{
		Schema: ProgressSchema,
		Phase:  ProgressRuntimeStarting,
	}
}

func (progress Progress) Validate() error {
	if progress.Schema != ProgressSchema ||
		progress.Phase != ProgressRuntimeStarting {
		return errors.New("Desktop bootstrap progress is invalid")
	}
	return nil
}

type Clock interface {
	Now() time.Time
}

// Descriptor crosses only the inherited native-shell pipe.
type Descriptor struct {
	Schema         string   `json:"schema"`
	InstanceID     string   `json:"instanceId"`
	ProcessID      int      `json:"pid"`
	BaseURL        string   `json:"baseUrl"`
	APIVersions    []string `json:"apiVersions"`
	EventVersions  []string `json:"eventVersions"`
	BootstrapNonce string   `json:"bootstrapNonce"`
}

func (descriptor Descriptor) Clone() Descriptor {
	descriptor.APIVersions = cloneStrings(descriptor.APIVersions)
	descriptor.EventVersions = cloneStrings(descriptor.EventVersions)
	return descriptor
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

// Session is the bounded App control session. Event and report-host
// capabilities are not issued until their corresponding routes exist.
type Session struct {
	Schema     string    `json:"schema"`
	BaseURL    string    `json:"baseUrl"`
	ReadToken  string    `json:"readToken"`
	WriteToken string    `json:"writeToken"`
	InstanceID string    `json:"instanceId"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type Grant struct {
	Nonce     string
	ExpiresAt time.Time
	Session   Session
}

type Authority struct {
	mu sync.Mutex

	clock     Clock
	digest    [sha256.Size]byte
	expiresAt time.Time
	session   Session
	used      bool
	revoked   bool
}

func New(grant Grant, clock Clock) (*Authority, error) {
	if clock == nil ||
		grant.ExpiresAt.IsZero() ||
		!clock.Now().UTC().Before(grant.ExpiresAt.UTC()) ||
		!validCapability(grant.Nonce) ||
		!validCapability(grant.Session.ReadToken) ||
		!validCapability(grant.Session.WriteToken) ||
		grant.Nonce == grant.Session.ReadToken ||
		grant.Nonce == grant.Session.WriteToken ||
		grant.Session.ReadToken == grant.Session.WriteToken ||
		grant.Session.Schema != SessionSchema ||
		grant.Session.BaseURL == "" ||
		grant.Session.InstanceID == "" ||
		grant.Session.ExpiresAt.IsZero() ||
		!clock.Now().UTC().Before(grant.Session.ExpiresAt.UTC()) {
		return nil, errors.New("Desktop bootstrap grant is invalid")
	}
	return &Authority{
		clock:     clock,
		digest:    bootstrapDigest(grant.Nonce),
		expiresAt: grant.ExpiresAt.UTC(),
		session:   grant.Session,
	}, nil
}

func (authority *Authority) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if authority == nil ||
		request == nil ||
		request.Method != http.MethodPost ||
		request.URL.Path != "/api/v1/auth/sessions" ||
		!emptyBody(request.Body) {
		writeProblem(writer, http.StatusUnauthorized, "bootstrap_unauthorized")
		return
	}
	values := request.Header.Values("Authorization")
	request.Header.Del("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bootstrap ") {
		writeProblem(writer, http.StatusUnauthorized, "bootstrap_unauthorized")
		return
	}
	nonce := strings.TrimPrefix(values[0], "Bootstrap ")
	if !validCapability(nonce) {
		writeProblem(writer, http.StatusUnauthorized, "bootstrap_unauthorized")
		return
	}
	digest := bootstrapDigest(nonce)
	authority.mu.Lock()
	authorized := !authority.used &&
		!authority.revoked &&
		authority.clock.Now().UTC().Before(authority.expiresAt) &&
		subtle.ConstantTimeCompare(digest[:], authority.digest[:]) == 1
	var session Session
	if authorized {
		authority.used = true
		session = authority.session
	}
	authority.mu.Unlock()
	if !authorized {
		writeProblem(writer, http.StatusUnauthorized, "bootstrap_unauthorized")
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(writer).Encode(session)
}

func (authority *Authority) Revoke() {
	if authority == nil {
		return
	}
	authority.mu.Lock()
	authority.revoked = true
	authority.digest = [sha256.Size]byte{}
	authority.session = Session{}
	authority.mu.Unlock()
}

func validCapability(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == capabilityBytes
}

func bootstrapDigest(value string) [sha256.Size]byte {
	return sha256.Sum256([]byte(nonceDomain + value))
}

func emptyBody(body io.ReadCloser) bool {
	if body == nil {
		return true
	}
	value, err := io.ReadAll(io.LimitReader(body, 1))
	return err == nil && len(value) == 0
}

func writeProblem(writer http.ResponseWriter, status int, reasonCode string) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Code   string `json:"code"`
	}{
		Type:   "urn:vibermate:error:" + strings.ReplaceAll(reasonCode, "_", "-"),
		Title:  http.StatusText(status),
		Status: status,
		Code:   reasonCode,
	})
}
