package capturecontrol

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
)

type failedLifecycle struct {
	capturerun.Controller
	err error
}

func (f failedLifecycle) Heartbeat(context.Context, string, capturerun.ControlCapability, time.Duration) (capturerun.View, error) {
	return capturerun.View{}, f.err
}
func (f failedLifecycle) Attach(context.Context, string, capturerun.ControlCapability, int) (capturerun.View, error) {
	return capturerun.View{}, f.err
}
func (f failedLifecycle) Finish(context.Context, string, capturerun.ControlCapability) error {
	return f.err
}

func TestLifecycleDistinguishesUnavailabilityFromRevocation(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{context.DeadlineExceeded, http.StatusServiceUnavailable},
		{errors.New("store is temporarily busy"), http.StatusServiceUnavailable},
		{capturerun.ErrRuntimeStopping, http.StatusServiceUnavailable},
		{capturerun.ErrCapabilityRejected, http.StatusForbidden},
		{capturerun.ErrNotFound, http.StatusForbidden},
	} {
		handler := &Handler{runs: failedLifecycle{err: test.err}}
		for _, action := range []string{"heartbeat", "attach-process", "finish"} {
			body := ""
			if action == "attach-process" {
				body = `{"processId":42}`
			}
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			request.SetPathValue("runId", "run.test")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(RunCapabilityHeader, strings.Repeat("A", 43))
			response := httptest.NewRecorder()
			switch action {
			case "heartbeat":
				handler.heartbeat(response, request)
			case "attach-process":
				handler.attach(response, request)
			case "finish":
				handler.finish(response, request)
			}
			if response.Code != test.status {
				t.Errorf("%s(%v) status=%d, want %d", action, test.err, response.Code, test.status)
			}
			if strings.Contains(response.Body.String(), "store is temporarily busy") {
				t.Fatal("internal error details leaked")
			}
		}
	}
}
