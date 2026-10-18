package runlauncher

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

func TestHeartbeatRetriesOnlyTemporaryControlFailures(t *testing.T) {
	for _, test := range []struct {
		err   error
		retry bool
	}{
		{context.DeadlineExceeded, true},
		{io.ErrUnexpectedEOF, true},
		{&url.Error{Op: "POST", Err: syscall.ECONNREFUSED}, true},
		{decodeControlFailure(503, []byte("<h1>Unavailable</h1>")), true},
		{decodeControlFailure(429, nil), true},
		{decodeControlFailure(408, nil), true},
		{decodeControlFailure(500, nil), true},
		{decodeControlFailure(599, nil), true},
		{decodeControlFailure(600, nil), false},
		{decodeControlFailure(403, nil), false},
		{decodeControlFailure(401, nil), false},
		{decodeControlFailure(404, nil), false},
		{errors.New("invalid control contract"), false},
		{context.Canceled, false},
		{nil, false},
	} {
		if got := retryableControlFailure(test.err); got != test.retry {
			t.Errorf("retry(%v)=%t, want %t", test.err, got, test.retry)
		}
	}
}

func TestClassifyCreateFailurePreservesTypedEnvironmentSelection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		reason capturecontrol.ReasonCode
		status int
		want   error
	}{
		{
			name:   "client destination missing",
			reason: capturecontrol.ReasonClientTargetNotConfigured,
			status: http.StatusUnprocessableEntity,
			want:   ErrClientTargetNotConfigured,
		},
		{
			name:   "invalid client base URL",
			reason: capturecontrol.ReasonClientTargetInvalid,
			status: http.StatusUnprocessableEntity,
			want:   ErrClientTargetInvalid,
		},
		{
			name:   "missing",
			reason: capturecontrol.ReasonEnvironmentNotFound,
			status: http.StatusNotFound,
			want:   ErrEnvironmentNotFound,
		},
		{
			name:   "disabled",
			reason: capturecontrol.ReasonEnvironmentUnavailable,
			status: http.StatusConflict,
			want:   ErrEnvironmentUnavailable,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			failure := &ControlFailure{Status: test.status, ReasonCode: test.reason}
			got := classifyCreateFailure(failure, false)
			if !errors.Is(got, test.want) || !errors.As(got, &failure) {
				t.Fatalf("classifyCreateFailure()=%v", got)
			}
		})
	}
}

func TestClassifyCreateFailureSeparatesPreparationTimeoutFromDeadRuntime(
	t *testing.T,
) {
	t.Parallel()

	timedOut := classifyCreateFailure(context.DeadlineExceeded, false)
	if !errors.Is(timedOut, ErrCapturePreparationTimedOut) ||
		!errors.Is(timedOut, context.DeadlineExceeded) {
		t.Fatalf("deadline classification = %v", timedOut)
	}

	refused := classifyCreateFailure(&url.Error{
		Op:  http.MethodPost,
		URL: "http://127.0.0.1:43123/api/v1/capture-runs",
		Err: syscall.ECONNREFUSED,
	}, false)
	if !errors.Is(refused, ErrRuntimeUnavailable) {
		t.Fatalf("connection refusal classification = %v", refused)
	}

	canceled := classifyCreateFailure(context.Canceled, false)
	if !errors.Is(canceled, context.Canceled) ||
		errors.Is(canceled, ErrCapturePreparationTimedOut) {
		t.Fatalf("caller cancellation classification = %v", canceled)
	}
}

func TestClassifyCreateFailureLeavesOtherProblemsIntact(t *testing.T) {
	t.Parallel()
	failure := &ControlFailure{
		Status:     http.StatusUnprocessableEntity,
		ReasonCode: capturecontrol.ReasonCaptureRunCreate,
	}
	if got := classifyCreateFailure(failure, false); got != failure {
		t.Fatalf("classifyCreateFailure()=%v want original failure", got)
	}
}

// These checks catch losing safe metadata or the original typed transport cause
// when wrapping requests, consuming a response body, or rejecting invalid JSON.
func TestControlCallFailurePreservesSafeCause(t *testing.T) {
	sentinel := "https://user:FAKE_CREDENTIAL@private.invalid/run-secret"
	for _, test := range []struct {
		name  string
		cause error
		want  string
		retry bool
	}{
		{"deadline", context.DeadlineExceeded, "timeout", true},
		{"canceled", context.Canceled, "canceled", true},
		{"temporary dns", &net.DNSError{Err: sentinel, Name: sentinel, IsTemporary: true}, "dns", true},
		{"refused", syscall.ECONNREFUSED, "connection_refused", true},
		{"reset", syscall.ECONNRESET, "connection_reset", true},
		{"tls", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, "tls", true},
		{"network", errors.New(sentinel), "network", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := &url.Error{Op: "POST", URL: sentinel, Err: test.cause}
			client := &controlClient{origin: "http://127.0.0.1", httpClient: controlFailureDoer(func(*http.Request) (*http.Response, error) { return nil, original })}
			err := client.attach(context.Background(), capturecontrol.LaunchGrant{Run: capturecontrol.CaptureRunView{ID: sentinel}}, 123)
			err = errors.Join(ErrCaptureSupervisionFailed, fmt.Errorf("attach captured process: %w", err))
			assertControlFailureSummary(t, err, "stage=process_attach reason="+test.want)
			var call *ControlCallFailure
			var network *url.Error
			if !errors.As(err, &call) || !errors.Is(err, test.cause) || !errors.As(err, &network) || network != original {
				t.Fatalf("original cause lost: %v", err)
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Fatalf("unsafe error: %v", err)
			}
			if retryableControlFailure(err) != test.retry {
				t.Fatalf("retry changed: %v", err)
			}
		})
	}
}

func TestControlResponseFailureAfterHeaders(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		stall bool
		want  string
	}{
		{"body timeout", "{", true, "timeout"},
		{"invalid JSON", "not-json-FAKE_CREDENTIAL", false, "invalid_response"},
		{"trailing JSON", "{} {}", false, "invalid_response"},
		{"oversize", strings.Repeat("x", maxControlResponseBytes+1), false, "invalid_response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				_, _ = io.WriteString(w, test.body)
				if test.stall {
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			client := &controlClient{origin: server.URL, httpClient: server.Client()}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := client.attach(ctx, capturecontrol.LaunchGrant{Run: capturecontrol.CaptureRunView{ID: "private-run"}}, 123)
			assertControlFailureSummary(t, err, "stage=process_attach reason="+test.want)
			if test.stall && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline lost: %v", err)
			}
			if test.name == "invalid JSON" {
				var syntax *json.SyntaxError
				if !errors.As(err, &syntax) {
					t.Fatalf("syntax cause lost: %v", err)
				}
			}
			if strings.Contains(err.Error(), "FAKE_CREDENTIAL") {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}

func TestControlReadFailurePreservesCause(t *testing.T) {
	cause := errors.New("https://FAKE_CREDENTIAL@private.invalid")
	client := &controlClient{origin: "http://127.0.0.1", httpClient: controlFailureDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: controlFailureBody{cause}}, nil
	})}
	err := client.heartbeat(context.Background(), capturecontrol.LaunchGrant{Run: capturecontrol.CaptureRunView{ID: "private-run"}})
	assertControlFailureSummary(t, err, "stage=capture_heartbeat reason=response_read")
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "FAKE_CREDENTIAL") {
		t.Fatalf("unsafe or lost cause: %v", err)
	}
}

func TestControlHTTPFailureUsesLocalStage(t *testing.T) {
	for _, test := range []struct{ method, path, stage string }{
		{http.MethodGet, servercontrol.RuntimeUserCurrentSessionPath, "session_check"},
		{http.MethodPost, "/api/v1/capture-runs", "capture_create"},
		{http.MethodPost, "/api/v1/capture-runs/private-run/actions/attach-process", "process_attach"},
		{http.MethodPost, "/api/v1/capture-runs/private-run/actions/heartbeat", "capture_heartbeat"},
		{http.MethodPost, "/api/v1/capture-runs/private-run/actions/finish", "capture_finish"},
		{http.MethodPost, "/api/v1/capture-runs/private-run/actions/start-acp", "acp_start"},
		{http.MethodPost, "/api/v1/capture-runs/private-run/actions/observe-acp", "acp_publish"},
		{http.MethodGet, "/api/v1/capture-runs/private-run/actions/attach-process", "control"},
		{http.MethodPost, "/api/v1/capture-runs/private-run/actions/FAKE_CREDENTIAL", "control"},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			client := &controlClient{origin: "http://127.0.0.1", httpClient: controlFailureDoer(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("FAKE_CREDENTIAL"))}, nil
			})}
			err := client.jsonRequest(context.Background(), test.method, test.path, "", "", nil, http.StatusOK, nil)
			assertControlFailureSummary(t, err, "stage="+test.stage+" status=403 reason=unknown")
			var rejected *ControlFailure
			if !errors.As(fmt.Errorf("joined: %w", err), &rejected) || retryableControlFailure(err) {
				t.Fatalf("HTTP rejection changed: %v", err)
			}
		})
	}
}

type controlFailureDoer func(*http.Request) (*http.Response, error)

func (do controlFailureDoer) Do(r *http.Request) (*http.Response, error) { return do(r) }

type controlFailureBody struct{ cause error }

func (body controlFailureBody) Read([]byte) (int, error) { return 0, body.cause }
func (body controlFailureBody) Close() error             { return nil }
func assertControlFailureSummary(t *testing.T, err error, want string) {
	t.Helper()
	var failure interface {
		error
		SafeSummary() string
	}
	if !errors.As(err, &failure) {
		t.Fatalf("missing safe control failure: %v", err)
	}
	if got := failure.SafeSummary(); got != want {
		t.Fatalf("summary=%q, want %q", got, want)
	}
}

func TestControlFailureSummariesRejectUnknownValues(t *testing.T) {
	const sentinel = "https://FAKE_CREDENTIAL@private.invalid"
	call := &ControlCallFailure{stage: sentinel, reason: sentinel, cause: errors.New(sentinel)}
	if got := call.Error(); got != "stage=control reason=network" {
		t.Fatalf("unsafe call summary: %q", got)
	}
	rejection := &ControlFailure{Stage: sentinel, Status: 1000, ReasonCode: capturecontrol.ReasonCode(sentinel)}
	if got := rejection.SafeSummary(); got != "stage=control status=0 reason=unknown" {
		t.Fatalf("unsafe rejection summary: %q", got)
	}
}

func TestControlInspectionFailurePreservesCause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := InspectLocal(ctx, catalogDiscovery{url: server.URL}, time.Second)
	var call *ControlCallFailure
	if !errors.Is(err, ErrRuntimeUnavailable) || !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &call) {
		t.Fatalf("inspection lost typed control cause or availability classification: %v", err)
	}
	if call.SafeSummary() != "stage=control reason=timeout" {
		t.Fatalf("inspection summary=%q", call.SafeSummary())
	}
}
