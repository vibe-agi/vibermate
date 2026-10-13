package loopbackproxy

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

type TargetObservation struct {
	Method, Scheme, Authority, KnownPath string
	PathSegments                         int
	HasQuery                             bool
	StatusCode                           int
	Reason                               string
}
type TargetObserver interface{ Observe(TargetObservation) }
type timedTargetObservation struct {
	value TargetObservation
	at    time.Time
}
type TargetObservationRing struct {
	mu      sync.Mutex
	clock   func() time.Time
	entries []timedTargetObservation
}

func NewTargetObservationRing(clock func() time.Time) *TargetObservationRing {
	if clock == nil {
		clock = time.Now
	}
	return &TargetObservationRing{clock: clock}
}
func (ring *TargetObservationRing) Observe(value TargetObservation) {
	value = sanitizeTargetObservation(value)
	ring.mu.Lock()
	defer ring.mu.Unlock()
	now := ring.clock()
	ring.expire(now)
	if len(ring.entries) == 64 {
		copy(ring.entries, ring.entries[1:])
		ring.entries = ring.entries[:63]
	}
	ring.entries = append(ring.entries, timedTargetObservation{value, now})
}
func (ring *TargetObservationRing) Observations() []TargetObservation {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	ring.expire(ring.clock())
	result := make([]TargetObservation, len(ring.entries))
	for i, entry := range ring.entries {
		result[i] = entry.value
	}
	return result
}
func (ring *TargetObservationRing) expire(now time.Time) {
	first := 0
	for first < len(ring.entries) && !ring.entries[first].at.Add(5*time.Minute).After(now) {
		first++
	}
	if first > 0 {
		copy(ring.entries, ring.entries[first:])
		size := len(ring.entries) - first
		clear(ring.entries[size:])
		ring.entries = ring.entries[:size]
	}
}
func sanitizeTargetObservation(value TargetObservation) TargetObservation {
	switch value.Method {
	case "GET", "POST", "DELETE", "PUT", "PATCH", "HEAD", "OPTIONS", "CONNECT":
	default:
		value.Method = "unrecognized"
	}
	if value.Scheme != "https" && value.Scheme != "http" {
		value.Scheme = ""
		value.Authority = ""
	}
	if value.Authority != "" {
		origin, err := originidentity.ParseProviderOrigin(value.Scheme + "://" + value.Authority)
		if err != nil || origin.HTTPAuthority() != value.Authority {
			value.Authority = ""
		}
	}
	switch value.KnownPath {
	case environment.ClientToolMCPPath, "/v1/responses", "/responses", "/v1/messages", "/backend-api/codex/responses":
	default:
		value.KnownPath = "unrecognized"
	}
	if value.PathSegments < 0 {
		value.PathSegments = 0
	}
	if value.PathSegments > 1024 {
		value.PathSegments = 1024
	}
	if value.StatusCode < 100 || value.StatusCode > 599 {
		value.StatusCode = 0
	}
	switch value.Reason {
	case "completed", "rejected", "permission_off", "target_unproven", "protocol_invalid", "session_invalid", "approval_rejected", "approval_expired", "revoked", "execution_uncertain", "budget_exceeded", "storage_unavailable", "request_authority_mismatch", "environment_changed", "native_executor_unavailable", "original_login_unavailable":
	default:
		value.Reason = "rejected"
	}
	return value
}
func observationOf(request *http.Request, origin originidentity.ProviderOrigin, status int, reason string) TargetObservation {
	value := TargetObservation{Scheme: origin.Scheme(), Authority: origin.HTTPAuthority(), StatusCode: status, Reason: reason}
	if request != nil {
		value.Method = request.Method
		if request.URL != nil {
			value.KnownPath = request.URL.Path
			value.HasQuery = request.URL.RawQuery != "" || request.URL.ForceQuery
			for _, segment := range strings.Split(request.URL.Path, "/") {
				if segment != "" {
					value.PathSegments++
				}
			}
		}
	}
	return sanitizeTargetObservation(value)
}

type observedResponseWriter struct {
	http.ResponseWriter
	status int
}

func (writer *observedResponseWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
	}
	writer.ResponseWriter.WriteHeader(status)
}
func (writer *observedResponseWriter) Write(data []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return writer.ResponseWriter.Write(data)
}
func (writer *observedResponseWriter) Flush() {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	_ = http.NewResponseController(writer.ResponseWriter).Flush()
}
func (writer *observedResponseWriter) Unwrap() http.ResponseWriter { return writer.ResponseWriter }
