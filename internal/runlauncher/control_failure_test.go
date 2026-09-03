package runlauncher

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"syscall"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
)

func TestClassifyCreateFailurePreservesTypedEnvironmentSelection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		reason capturecontrol.ReasonCode
		status int
		want   error
	}{
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
			got := classifyCreateFailure(failure)
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

	timedOut := classifyCreateFailure(context.DeadlineExceeded)
	if !errors.Is(timedOut, ErrCapturePreparationTimedOut) ||
		!errors.Is(timedOut, context.DeadlineExceeded) {
		t.Fatalf("deadline classification = %v", timedOut)
	}

	refused := classifyCreateFailure(&url.Error{
		Op:  http.MethodPost,
		URL: "http://127.0.0.1:43123/api/v1/capture-runs",
		Err: syscall.ECONNREFUSED,
	})
	if !errors.Is(refused, ErrRuntimeUnavailable) {
		t.Fatalf("connection refusal classification = %v", refused)
	}

	canceled := classifyCreateFailure(context.Canceled)
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
	if got := classifyCreateFailure(failure); got != failure {
		t.Fatalf("classifyCreateFailure()=%v want original failure", got)
	}
}
