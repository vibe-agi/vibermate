package desktopcontrol_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/desktoptrust"
	"github.com/vibe-agi/vibermate/internal/systemtrust"
)

type fakeRootTrustController struct {
	status     desktoptrust.Status
	material   desktoptrust.Material
	replace    int
	replaceErr error
}

func (controller *fakeRootTrustController) Material(context.Context) (desktoptrust.Material, error) {
	material := controller.material
	material.CertificateDER = bytes.Clone(material.CertificateDER)
	return material, nil
}

func (controller *fakeRootTrustController) Status(context.Context) (desktoptrust.Status, error) {
	return controller.status, nil
}

func (controller *fakeRootTrustController) Replace(context.Context) (desktoptrust.ActionResult, error) {
	controller.replace++
	if controller.replaceErr != nil {
		return desktoptrust.ActionResult{}, controller.replaceErr
	}
	return desktoptrust.ActionResult{
		Status:          controller.status,
		ResultStatus:    "applied",
		Reason:          "applied",
		Completed:       false,
		RestartRequired: true,
	}, nil
}

func (*fakeRootTrustController) Shutdown(context.Context) error { return nil }

func TestRootCATrustRoutesExposeStatusAndRequireWriteHeaders(t *testing.T) {
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	rootTrust := &fakeRootTrustController{
		status: desktoptrust.Status{
			RootRevision:       1,
			Fingerprint:        "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Algorithm:          "ecdsa-p256",
			NotBefore:          time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			NotAfter:           time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC),
			RootValid:          true,
			CertificatePresent: systemtrust.ExactPresenceAbsent,
			TrustDecision:      systemtrust.TrustDecisionUntrusted,
			Available:          true,
		},
		material: desktoptrust.Material{
			RootRevision:   1,
			Fingerprint:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			CertificateDER: []byte("exact public Root DER"),
		},
	}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness:    readyState(true),
		Status:       runtime,
		Environments: runtime.Environments(),
		Assignments:  runtime.CaptureAssignments(),
		Clock:        desktopcontrol.SystemClock{},
		Activities:   runtime.Activities(),
		Contents:     runtime.ExchangeContents(),
		Connections:  runtime.ConnectionEvents(),
		Egress:       runtime.EgressAttempts(),
		Approvals:    runtime.ToolApprovals(),
		Endpoints:    runtime.UpstreamEndpoints(),
		Accounts:     runtime.ProviderAccounts(),
		Offline:      runtime,
		RootTrust:    rootTrust,
	})
	if err != nil {
		t.Fatal(err)
	}
	const authority = "127.0.0.1:43141"
	authenticator, err := desktopcontrol.NewAuthenticator(
		desktopcontrol.CapabilityGrant{
			ReadToken: capability(0x71), WriteToken: capability(0x72),
			ExpiresAt: time.Now().Add(time.Hour),
		},
		desktopcontrol.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{
		Authority: authority, AllowedOrigins: []string{"vibermate://desktop"},
		Authenticator: authenticator, Application: application,
		Bootstrap: emptyBootstrap(), CLIControl: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		ManualCaptures: rejectingManualCaptureHandler{}, DesktopPrincipal: desktopManualPrincipal(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	statusResponse := doRequest(t, router, authority, http.MethodGet, "/api/v1/platform/root-ca", capability(0x71), nil)
	if statusResponse.Code != http.StatusOK ||
		statusResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status code = %d, body=%s", statusResponse.Code, statusResponse.Body)
	}
	var status map[string]any
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["fingerprint"] != rootTrust.status.Fingerprint {
		t.Fatalf("status did not expose the current fingerprint: %v", status)
	}
	materialResponse := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/platform/root-ca/material",
		capability(0x71),
		nil,
	)
	if materialResponse.Code != http.StatusOK ||
		materialResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("material code=%d body=%s", materialResponse.Code, materialResponse.Body)
	}
	var material map[string]any
	if err := json.Unmarshal(materialResponse.Body.Bytes(), &material); err != nil {
		t.Fatal(err)
	}
	encoded, _ := material["certificateDerBase64"].(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !bytes.Equal(decoded, rootTrust.material.CertificateDER) ||
		material["fingerprint"] != rootTrust.status.Fingerprint {
		t.Fatalf("material response=%v decode=%v", material, err)
	}
	for _, forbidden := range []string{
		"certificate", "certificatePem", "certificatePath", "privateKey", "command", "stderr", "stdout",
	} {
		if _, exposed := status[forbidden]; exposed {
			t.Fatalf("Root status exposed %q: %v", forbidden, status)
		}
	}
	for _, retired := range []string{"install", "remove"} {
		response := doRequest(
			t,
			router,
			authority,
			http.MethodPost,
			"/api/v1/platform/root-ca/actions/"+retired,
			capability(0x72),
			nil,
		)
		if response.Code != http.StatusNotFound {
			t.Fatalf("retired %s route code=%d body=%s", retired, response.Code, response.Body)
		}
	}
	readOnlyMutation := doMutation(
		t,
		router,
		authority,
		"/api/v1/platform/root-ca/actions/replace",
		capability(0x71),
		1,
		capability(0x70),
		nil,
	)
	if readOnlyMutation.Code != http.StatusUnauthorized || rootTrust.replace != 0 {
		t.Fatalf("read capability scheduled Root replacement: code=%d calls=%d", readOnlyMutation.Code, rootTrust.replace)
	}
	missingHeaders := doRequest(t, router, authority, http.MethodPost, "/api/v1/platform/root-ca/actions/replace", capability(0x72), nil)
	if missingHeaders.Code != http.StatusUnprocessableEntity || rootTrust.replace != 0 {
		t.Fatalf("replace without revision/idempotency was accepted: code=%d calls=%d", missingHeaders.Code, rootTrust.replace)
	}
	replaced := doMutation(t, router, authority, "/api/v1/platform/root-ca/actions/replace", capability(0x72), 1, capability(0x73), nil)
	if replaced.Code != http.StatusOK || rootTrust.replace != 1 {
		t.Fatalf("replace route failed: code=%d calls=%d body=%s", replaced.Code, rootTrust.replace, replaced.Body)
	}
	stale := doMutation(t, router, authority, "/api/v1/platform/root-ca/actions/replace", capability(0x72), 2, capability(0x74), nil)
	if stale.Code != http.StatusConflict || rootTrust.replace != 1 {
		t.Fatalf("stale Root revision was accepted: code=%d calls=%d", stale.Code, rootTrust.replace)
	}
	rootTrust.replaceErr = desktoptrust.ErrRootResetRequiresRemoval
	requiresRemoval := doMutation(t, router, authority, "/api/v1/platform/root-ca/actions/replace", capability(0x72), 1, capability(0x75), nil)
	if requiresRemoval.Code != http.StatusConflict ||
		!bytes.Contains(requiresRemoval.Body.Bytes(), []byte(`"code":"root_reset_requires_removal"`)) {
		t.Fatalf("manual removal prerequisite was not explicit: code=%d body=%s", requiresRemoval.Code, requiresRemoval.Body)
	}
}
