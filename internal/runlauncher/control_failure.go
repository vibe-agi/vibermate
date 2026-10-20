package runlauncher

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"strings"
	"syscall"

	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

// ControlCallFailure retains the cause of a transport or response failure for
// errors.Is/As while exposing only closed, locally chosen diagnostic values.
// HTTP rejections remain ControlFailure so their retry semantics stay distinct.
type ControlCallFailure struct {
	stage  string
	reason string
	cause  error
}

func (failure *ControlCallFailure) Unwrap() error { return failure.cause }
func (failure *ControlCallFailure) Error() string { return failure.SafeSummary() }
func (failure *ControlCallFailure) SafeSummary() string {
	return "stage=" + safeControlStage(failure.stage) + " reason=" + safeControlCause(failure.reason)
}

func newControlCallFailure(stage, fallback string, cause error) *ControlCallFailure {
	return &ControlCallFailure{stage: stage, reason: classifyControlCause(cause, fallback), cause: cause}
}

func safeControlStage(stage string) string {
	switch stage {
	case "control", "session_check", "capture_create", "process_attach",
		"capture_heartbeat", "capture_finish", "acp_start", "acp_publish":
		return stage
	default:
		return "control"
	}
}

func safeControlCause(reason string) string {
	switch reason {
	case "timeout", "canceled", "dns", "connection_refused", "connection_reset",
		"tls", "response_read", "invalid_response", "network":
		return reason
	default:
		return "network"
	}
}

func classifyControlCause(err error, fallback string) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout() {
		return "timeout"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection_refused"
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return "connection_reset"
	}
	var verification *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	var alert tls.AlertError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificate x509.CertificateInvalidError
	if errors.As(err, &verification) || errors.As(err, &record) || errors.As(err, &alert) ||
		errors.As(err, &authority) || errors.As(err, &hostname) || errors.As(err, &certificate) {
		return "tls"
	}
	return safeControlCause(fallback)
}

// Only locally constructed methods and paths select a stage. Run identifiers
// and unrecognized paths never enter the diagnostic output.
func controlRequestStage(method, path string) string {
	path, _, _ = strings.Cut(path, "?")
	if method == http.MethodGet && path == servercontrol.RuntimeUserCurrentSessionPath {
		return "session_check"
	}
	if method != http.MethodPost {
		return "control"
	}
	if path == "/api/v1/capture-runs" {
		return "capture_create"
	}
	rest, ok := strings.CutPrefix(path, "/api/v1/capture-runs/")
	if !ok {
		return "control"
	}
	runID, action, ok := strings.Cut(rest, "/actions/")
	if !ok || runID == "" || strings.Contains(runID, "/") {
		return "control"
	}
	switch action {
	case "attach-process":
		return "process_attach"
	case "heartbeat":
		return "capture_heartbeat"
	case "finish":
		return "capture_finish"
	case "start-acp":
		return "acp_start"
	case "observe-acp":
		return "acp_publish"
	default:
		return "control"
	}
}
