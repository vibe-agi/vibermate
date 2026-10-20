package capturecontrol

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
)

type recoveryTestPrincipal struct{ principal controlprincipal.Principal }

func (p recoveryTestPrincipal) Authenticate(context.Context, string) (controlprincipal.Principal, bool) {
	return p.principal, true
}

type recoveryTestIssuer struct{ calls int }

func (i *recoveryTestIssuer) IssueCaptureRun(context.Context, controlprincipal.Principal, capturegrant.CaptureRunRequest) (capturegrant.CaptureRunGrant, error) {
	i.calls++
	return capturegrant.CaptureRunGrant{}, capturegrant.ErrPrincipalUnauthorized
}

func TestLocalRecoveryCreateRejectsInvalidNegotiationBeforeIssuer(t *testing.T) {
	for _, query := range []string{"localRecovery=0", "localRecovery=true", "localRecovery=1&localRecovery=1", "localRecovery=1&extra=1", "localRecovery=%", "other=1"} {
		t.Run(query, func(t *testing.T) {
			principal, err := controlprincipal.New(controlprincipal.Attributes{ID: "local:test", Kind: controlprincipal.KindLocalCLI, CredentialRevision: 1, AllowedGrantKinds: []controlprincipal.GrantKind{controlprincipal.GrantCaptureRun}})
			if err != nil {
				t.Fatal(err)
			}
			issuer := &recoveryTestIssuer{}
			handler := &Handler{principals: recoveryTestPrincipal{principal}, issuer: issuer}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/capture-runs?"+query, strings.NewReader(`{"cwd":"/tmp","command":["agent"],"executablePath":"/tmp/agent"}`))
			request.Header.Set("Authorization", "Bearer synthetic-control")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.create(response, request)
			if response.Code != http.StatusUnprocessableEntity || issuer.calls != 0 {
				t.Fatalf("invalid negotiation reached issuer: status=%d calls=%d body=%s", response.Code, issuer.calls, response.Body)
			}
		})
	}
}

func TestLocalRecoveryCreateRejectsNonLocalPrincipalBeforeIssuer(t *testing.T) {
	for _, attrs := range []controlprincipal.Attributes{
		{ID: "enrolled:test", Kind: controlprincipal.KindEnrolledClient, ProxyClientBindingID: "binding", MachineRegistrationID: "registration", MachineID: "machine", CredentialRevision: 1, AllowedGrantKinds: []controlprincipal.GrantKind{controlprincipal.GrantCaptureRun}},
		{ID: "local:denied", Kind: controlprincipal.KindLocalCLI, CredentialRevision: 1, AllowedGrantKinds: []controlprincipal.GrantKind{controlprincipal.GrantManualCapture}},
	} {
		t.Run(attrs.ID, func(t *testing.T) {
			principal, err := controlprincipal.New(attrs)
			if err != nil {
				t.Fatal(err)
			}
			issuer := &recoveryTestIssuer{}
			handler := &Handler{principals: recoveryTestPrincipal{principal}, issuer: issuer}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/capture-runs?localRecovery=1", strings.NewReader(`{"cwd":"/tmp","command":["agent"],"executablePath":"/tmp/agent"}`))
			request.Header.Set("Authorization", "Bearer synthetic-control")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.create(response, request)
			if response.Code != http.StatusForbidden || issuer.calls != 0 {
				t.Fatalf("unauthorized recovery negotiation reached issuer: status=%d calls=%d body=%s", response.Code, issuer.calls, response.Body)
			}
		})
	}
}

type recoveryTestRuns struct {
	capturerun.Controller
	attached bool
	err      error
}

func (r *recoveryTestRuns) Attach(_ context.Context, id string, _ capturerun.ControlCapability, pid int) (capturerun.View, error) {
	if r.err != nil {
		return capturerun.View{}, r.err
	}
	r.attached = true
	return capturerun.View{ID: id, State: capturerun.StateAttached, ProcessID: pid}, nil
}

func TestLocalRecoveryAttachCannotAcknowledgeMissingNativePreparer(t *testing.T) {
	runs := &recoveryTestRuns{}
	handler := &Handler{runs: runs}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/capture-runs/run.test/actions/attach-process?requireNativeReview=1", strings.NewReader(`{"processId":42}`))
	request.SetPathValue("runId", "run.test")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(RunCapabilityHeader, strings.Repeat("A", 43))
	response := httptest.NewRecorder()
	handler.attach(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing native barrier was acknowledged: %d %s", response.Code, response.Body)
	}
}

func TestLocalRecoveryAttachAcknowledgesOnlySuccessfulNativePreparation(t *testing.T) {
	for _, test := range []struct {
		name, query           string
		attachErr, prepareErr error
		status, calls         int
		attached              bool
	}{
		{name: "required failure", query: "?requireNativeReview=1", prepareErr: errors.New("private native preparation failure"), status: 503, calls: 1, attached: true},
		{name: "required success", query: "?requireNativeReview=1", status: 200, calls: 1, attached: true},
		{name: "ordinary attach", prepareErr: errors.New("must not run"), status: 200, attached: true},
		{name: "duplicate marker", query: "?requireNativeReview=1&requireNativeReview=1", status: 422},
		{name: "invalid marker", query: "?requireNativeReview=0", status: 422},
		{name: "wrong capability", query: "?requireNativeReview=1", attachErr: capturerun.ErrCapabilityRejected, status: 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			runs := &recoveryTestRuns{err: test.attachErr}
			calls := 0
			handler := &Handler{runs: runs, nativeRecoveryPreparer: func(_ context.Context, view capturerun.View) error {
				calls++
				if !runs.attached || view.ID != "run.test" || view.ProcessID != 42 || view.State != capturerun.StateAttached {
					t.Fatalf("preparer did not receive the actual attached process: %+v", view)
				}
				return test.prepareErr
			}}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/capture-runs/run.test/actions/attach-process"+test.query, strings.NewReader(`{"processId":42}`))
			request.SetPathValue("runId", "run.test")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(RunCapabilityHeader, strings.Repeat("A", 43))
			response := httptest.NewRecorder()
			handler.attach(response, request)
			if response.Code != test.status || calls != test.calls || runs.attached != test.attached {
				t.Fatalf("native preparation acknowledgement: status=%d calls=%d attached=%v body=%s", response.Code, calls, runs.attached, response.Body)
			}
			if strings.Contains(response.Body.String(), "private native preparation failure") {
				t.Fatal("native preparation leaked internal error details")
			}
		})
	}
}
