package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serverhost"
)

type nativeManagementFixture struct {
	checks  nativeFixtureChecks
	client  *http.Client
	origin  string
	session servercontrol.WebSession
}

func (fixture *nativeManagementFixture) createContainmentCapture(ctx context.Context, id environment.EnvironmentID) capturecontrol.ManualCaptureGrant {
	status, data := fixture.request(ctx, "GET", "/api/v1/manual-captures/context?environmentId="+url.QueryEscape(id.String()), fixture.session.ReadToken, nil, nil)
	var review capturecontrol.ManualCaptureContext
	if status != http.StatusOK || json.Unmarshal(data, &review) != nil || review.EnvironmentID != id.String() || review.ConfirmationToken == "" {
		fixture.checks.Fatal("private Capture review unavailable")
	}
	expires := int64(60)
	status, data = fixture.request(ctx, "POST", "/api/v1/manual-captures", fixture.session.WriteToken, capturecontrol.ManualCaptureCreateRequest{EnvironmentID: id.String(), DisplayName: "Private containment", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeTemporary, ExpiresInSeconds: &expires, ConfirmationToken: review.ConfirmationToken}, nil)
	var grant capturecontrol.ManualCaptureGrant
	if status != http.StatusCreated || json.Unmarshal(data, &grant) != nil || grant.EnvironmentID != id.String() || grant.AssignmentRevision == 0 || grant.ProxyUsername == "" || grant.ProxyPassword == "" {
		fixture.checks.Fatal("private authenticated Capture grant unavailable")
	}
	return grant
}

func newNativeManagementFixture(ctx context.Context, t nativeFixtureChecks, host *serverhost.Host) *nativeManagementFixture {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(host.Runtime().LocalRootCertificate().CertificatePEM()) {
		t.Fatal("private management root unavailable")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	t.Cleanup(transport.CloseIdleConnections)
	fixture := &nativeManagementFixture{checks: t, origin: "https://" + host.Status().ListenAddress, client: &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	key, err := os.ReadFile(host.Status().RecoveryKeyPath)
	if err != nil {
		t.Fatal("private recovery key unavailable")
	}
	status, data := fixture.request(ctx, "POST", servercontrol.WebSetupPath, "", servercontrol.WebSetup{Schema: servercontrol.WebSetupSchema, RecoveryKey: strings.TrimSpace(string(key)), Username: "native-owner", Password: "synthetic-private-owner-password"}, nil)
	clear(key)
	if status != 201 || json.Unmarshal(data, &fixture.session) != nil || fixture.session.Principal.Role != "owner" {
		t.Fatal("private authenticated management session unavailable")
	}
	return fixture
}

func (fixture *nativeManagementFixture) request(ctx context.Context, method, path, token string, body any, headers map[string]string) (int, []byte) {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			fixture.checks.Fatal("management request encoding failed")
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, fixture.origin+path, bytes.NewReader(encoded))
	if err != nil {
		fixture.checks.Fatal("management request unavailable")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := fixture.client.Do(request)
	if err != nil {
		fixture.checks.Fatal("private management request failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		fixture.checks.Fatal("private management response exceeds bound")
	}
	return response.StatusCode, data
}

func (fixture *nativeManagementFixture) rejectPending(ctx context.Context, capture captureidentity.Reference) {
	path := "/api/v1/captures/" + capture.Key() + "/native-tools/approvals"
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		status, data := fixture.request(ctx, "GET", path, fixture.session.ReadToken, nil, nil)
		var page desktopcontrol.NativeApprovalPageDTO
		if status != 200 || json.Unmarshal(data, &page) != nil {
			fixture.checks.Fatal("actual approval page unavailable")
		}
		if len(page.Items) > 0 {
			if len(page.Items) != 1 {
				fixture.checks.Fatal("unexpected concurrent native questions")
			}
			approval := page.Items[0]
			if approval.State != "pending" || approval.CaptureKey != capture.Key() || approval.PreviewState != "unavailable" || approval.Preview != nil {
				fixture.checks.Fatal("unknown production profile fabricated an available preview")
			}
			status, data = fixture.request(ctx, "POST", path+"/"+approval.ID+"/actions/decide", fixture.session.WriteToken, desktopcontrol.NativeDecisionDTO{Decision: nativetool.DecisionDeny, BindingDigest: approval.BindingDigest}, map[string]string{"If-Match": strconv.FormatUint(approval.Revision, 10), "Idempotency-Key": "native-private-reject-" + approval.ID})
			var decided desktopcontrol.NativeApprovalDTO
			if status != 200 || json.Unmarshal(data, &decided) != nil || decided.State != "denied" {
				fixture.checks.Fatal("actual authenticated Deny failed")
			}
			return
		}
		select {
		case <-ctx.Done():
			fixture.checks.Fatal("approval wait canceled")
		case <-time.After(10 * time.Millisecond):
		}
	}
	fixture.checks.Fatal("actual pending native approval missing")
}

// Only the private synthetic stream fixture calls this authenticated user
// decision. It establishes no production content-profile classification.
func (fixture *nativeManagementFixture) allowStreamPending(ctx context.Context, capture captureidentity.Reference) string {
	path := "/api/v1/captures/" + capture.Key() + "/native-tools/approvals"
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		status, data := fixture.request(ctx, "GET", path, fixture.session.ReadToken, nil, nil)
		var page desktopcontrol.NativeApprovalPageDTO
		if status != 200 || json.Unmarshal(data, &page) != nil {
			fixture.checks.Fatal("stream approval page unavailable")
		}
		for _, approval := range page.Items {
			if approval.State != "pending" {
				continue
			}
			if approval.CaptureKey != capture.Key() {
				fixture.checks.Fatal("stream approval Capture mismatch")
			}
			status, data = fixture.request(ctx, "POST", path+"/"+approval.ID+"/actions/decide", fixture.session.WriteToken, desktopcontrol.NativeDecisionDTO{Decision: nativetool.DecisionAllowOnce, BindingDigest: approval.BindingDigest}, map[string]string{"If-Match": strconv.FormatUint(approval.Revision, 10), "Idempotency-Key": "native-stream-allow-" + approval.ID})
			var decided desktopcontrol.NativeApprovalDTO
			if status != 200 || json.Unmarshal(data, &decided) != nil || decided.State != "allowed" {
				fixture.checks.Fatal("authenticated stream Allow failed")
			}
			return approval.ID
		}
		select {
		case <-ctx.Done():
			fixture.checks.Fatal("stream approval wait canceled")
		case <-time.After(10 * time.Millisecond):
		}
	}
	fixture.checks.Fatal("stream pending approval missing")
	return ""
}

func (fixture *nativeManagementFixture) verifyMetadata(ctx context.Context, operation nativetool.Operation) {
	path := "/api/v1/captures/" + operation.Capture.Key() + "/native-tools/operations"
	status, data := fixture.request(ctx, "GET", path, fixture.session.ReadToken, nil, nil)
	var page desktopcontrol.NativeOperationPageDTO
	if status != 200 || json.Unmarshal(data, &page) != nil {
		fixture.checks.Fatal("authenticated native operations page unavailable")
	}
	found := false
	for _, item := range page.Items {
		if item.OperationID == operation.ID.String() {
			found = true
		}
	}
	if !found {
		fixture.checks.Fatal("actual native operation absent from Capture reader")
	}
	status, data = fixture.request(ctx, "GET", path+"/"+operation.ID.String(), fixture.session.ReadToken, nil, nil)
	var value desktopcontrol.NativeOperationDTO
	if status != 200 || json.Unmarshal(data, &value) != nil || value.CaptureKey != operation.Capture.Key() || value.Environment.ID != operation.EnvironmentID.String() || value.Environment.Revision != uint64(operation.EnvironmentRevision) || value.Environment.Digest != operation.EnvironmentDigest.String() || value.State != operation.State || value.CredentialOwner != "original_client" || value.Content.Arguments.State != "off" || value.Content.Result.State != "off" {
		fixture.checks.Fatal("reader metadata changed actual native ownership or recording")
	}
	status, _ = fixture.request(ctx, "GET", path+"/"+operation.ID.String()+"/content?part=tool_result", fixture.session.ReadToken, nil, nil)
	if status != http.StatusGone {
		fixture.checks.Fatal("OFF content was exposed")
	}
}
