package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
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
	authority nativetool.AuthorityRepository
	checks    nativeFixtureChecks
	client    *http.Client
	origin    string
	session   servercontrol.WebSession
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
	fixture := &nativeManagementFixture{authority: host.Runtime().NativeToolRepository(), checks: t, origin: "https://" + host.Status().ListenAddress, client: &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
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

func (fixture *nativeManagementFixture) rejectPending(ctx context.Context, capture captureidentity.Reference, classified ...bool) {
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
			known := len(classified) > 0 && classified[0]
			if approval.State != "pending" || approval.CaptureKey != capture.Key() || !known && (approval.PreviewState != "unavailable" || approval.Preview != nil) || known && (approval.PreviewState != "available" || approval.Preview == nil) {
				fixture.checks.Fatal("unknown production profile fabricated an available preview")
			}
			if !known {
				status, _ := fixture.request(ctx, "POST", path+"/"+approval.ID+"/actions/decide", fixture.session.WriteToken, desktopcontrol.NativeDecisionDTO{Decision: nativetool.DecisionAllowOnce, BindingDigest: approval.BindingDigest}, map[string]string{"If-Match": strconv.FormatUint(approval.Revision, 10), "Idempotency-Key": "native-unclassified-allow-" + approval.ID})
				if status != http.StatusConflict {
					fixture.checks.Fatal("unknown profile bypassed unavailable-preview Allow denial")
				}
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
// decision. Its tool call uses the tagged exact synthetic profile; this does
// not establish a production classification for another tool or definition.
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
			id, parseErr := nativetool.NewID(approval.OperationID)
			operation, operationErr := fixture.authority.Get(ctx, id)
			fixture.checks.Logf("stream pending diagnostic kind=%s preview=%s sourceReadValid=%t", operation.Kind, approval.PreviewState, parseErr == nil && operationErr == nil)
			status, data = fixture.request(ctx, "POST", path+"/"+approval.ID+"/actions/decide", fixture.session.WriteToken, desktopcontrol.NativeDecisionDTO{Decision: nativetool.DecisionAllowOnce, BindingDigest: approval.BindingDigest}, map[string]string{"If-Match": strconv.FormatUint(approval.Revision, 10), "Idempotency-Key": "native-stream-allow-" + approval.ID})
			var decided desktopcontrol.NativeApprovalDTO
			decodeErr := json.Unmarshal(data, &decided)
			if status != 200 || decodeErr != nil || decided.State != "allowed" {
				var problem struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				_ = json.Unmarshal(data, &problem)
				fixture.checks.Logf("stream Allow diagnostic status=%d state=%q decode=%v code=%q approvalRevision=%d", status, decided.State, decodeErr, problem.Error.Code, approval.Revision)
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

func (fixture *nativeManagementFixture) allowSyntheticPending(ctx context.Context, capture captureidentity.Reference) {
	path := "/api/v1/captures/" + capture.Key() + "/native-tools/approvals"
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		status, data := fixture.request(ctx, "GET", path, fixture.session.ReadToken, nil, nil)
		var page desktopcontrol.NativeApprovalPageDTO
		if status != 200 || json.Unmarshal(data, &page) != nil {
			fixture.checks.Fatal("synthetic approval page unavailable")
		}
		for _, approval := range page.Items {
			if approval.State != "pending" {
				continue
			}
			if approval.CaptureKey != capture.Key() || approval.PreviewState != "available" || approval.Preview == nil || len(approval.Preview.Intents) != 1 {
				fixture.checks.Fatal("ordinary synthetic Review preview unavailable")
			}
			intent := approval.Preview.Intents[0]
			if intent.ToolName != "synthetic_read" || intent.Action != "tool_call" || intent.Target != "https://chatgpt.com" || string(intent.Arguments) != `{"query":"synthetic"}` || intent.Truncated {
				fixture.checks.Fatal("synthetic Review preview differs from fixed intent")
			}
			status, current := fixture.request(ctx, "GET", path+"/"+approval.ID, fixture.session.ReadToken, nil, nil)
			var reread desktopcontrol.NativeApprovalDTO
			if status != 200 || json.Unmarshal(current, &reread) != nil || reread.OperationID != approval.OperationID || reread.BindingDigest != approval.BindingDigest || reread.Revision != approval.Revision {
				fixture.checks.Fatal("synthetic approval changed before decision")
			}
			for _, wrong := range []struct {
				revision uint64
				digest   string
			}{{approval.Revision + 1, approval.BindingDigest}, {approval.Revision, strings.Repeat("0", 64)}} {
				status, _ := fixture.request(ctx, "POST", path+"/"+approval.ID+"/actions/decide", fixture.session.WriteToken, desktopcontrol.NativeDecisionDTO{Decision: nativetool.DecisionAllowOnce, BindingDigest: wrong.digest}, map[string]string{"If-Match": strconv.FormatUint(wrong.revision, 10), "Idempotency-Key": "native-stale-allow-" + approval.ID})
				if status != http.StatusConflict {
					fixture.checks.Fatal("stale synthetic approval granted Allow")
				}
			}
			status, data = fixture.request(ctx, "POST", path+"/"+approval.ID+"/actions/decide", fixture.session.WriteToken, desktopcontrol.NativeDecisionDTO{Decision: nativetool.DecisionAllowOnce, BindingDigest: approval.BindingDigest}, map[string]string{"If-Match": strconv.FormatUint(approval.Revision, 10), "Idempotency-Key": "synthetic-profile-allow-" + approval.ID})
			var decided desktopcontrol.NativeApprovalDTO
			if status != 200 || json.Unmarshal(data, &decided) != nil || decided.State != "allowed" {
				fixture.checks.Fatal("synthetic authenticated Allow failed")
			}
			// Retrying the exact decision is idempotent; it cannot create a
			// second tool request or consume a second once entry.
			status, _ = fixture.request(ctx, "POST", path+"/"+approval.ID+"/actions/decide", fixture.session.WriteToken, desktopcontrol.NativeDecisionDTO{Decision: nativetool.DecisionAllowOnce, BindingDigest: approval.BindingDigest}, map[string]string{"If-Match": strconv.FormatUint(approval.Revision, 10), "Idempotency-Key": "synthetic-profile-allow-" + approval.ID})
			if status != 200 {
				fixture.checks.Fatal("Allow retry lost the idempotent decision")
			}
			return
		}
		select {
		case <-ctx.Done():
			fixture.checks.Fatal("synthetic approval canceled")
		case <-time.After(10 * time.Millisecond):
		}
	}
	fixture.checks.Fatal("synthetic approval missing")
}

func (fixture *nativeManagementFixture) verifySyntheticFull(ctx context.Context, observed nativetool.InspectionOperation, probe nativeProfileProbe) {
	op := observed.Operation
	path := "/api/v1/captures/" + op.Capture.Key() + "/native-tools/operations/" + op.ID.String()
	status, data := fixture.request(ctx, "GET", path, fixture.session.ReadToken, nil, nil)
	var view desktopcontrol.NativeOperationDTO
	if status != 200 || json.Unmarshal(data, &view) != nil || view.Content.Mode != environment.ContentRecordingFull || view.OperationID != op.ID.String() || view.CaptureKey != op.Capture.Key() || view.Environment.Digest != op.EnvironmentDigest.String() {
		fixture.checks.Fatal("Full reader lost real operation ownership")
	}
	for _, part := range []struct {
		name       string
		retained   bool
		descriptor desktopcontrol.NativePartDescriptor
		stored     *nativetool.InspectionContentDescriptor
	}{
		{"tool_arguments", probe.argumentsRetained, view.Content.Arguments, observed.Arguments}, {"tool_result", probe.resultRetained, view.Content.Result, observed.Result},
	} {
		if !part.retained {
			if part.stored != nil || part.descriptor.State != "unavailable" || part.descriptor.Digest != "" {
				fixture.checks.Fatal("unclassified/sensitive Full part exposed a content hash")
			}
			status, _ = fixture.request(ctx, "GET", path+"/content?part="+part.name, fixture.session.ReadToken, nil, nil)
			if status != http.StatusGone {
				fixture.checks.Fatal("suppressed Full content was readable")
			}
			continue
		}
		if part.stored == nil || part.descriptor.State != "available" || part.descriptor.Digest != part.stored.Digest.String() {
			fixture.checks.Fatal("classified Full descriptor unavailable")
		}
		cursor := ""
		var content []byte
		pages := 0
		for {
			uri := path + "/content?part=" + part.name
			if cursor != "" {
				uri += "&cursor=" + url.QueryEscape(cursor)
			}
			status, data = fixture.request(ctx, "GET", uri, fixture.session.ReadToken, nil, nil)
			var page desktopcontrol.NativeContentPageDTO
			if status != 200 || len(data) > 128<<10 || json.Unmarshal(data, &page) != nil || page.OperationID != op.ID.String() || page.CaptureKey != op.Capture.Key() || page.BodyDigest != part.descriptor.Digest || page.Part != part.name {
				fixture.checks.Fatal("bounded Full content page invalid")
			}
			for _, block := range page.Blocks {
				if block.Offset != len(content) {
					fixture.checks.Fatal("Full content page offset regressed")
				}
				if block.ImageData != nil {
					decoded, err := base64.StdEncoding.DecodeString(*block.ImageData)
					if err != nil || block.MIMEType != "image/png" {
						fixture.checks.Fatal("Full image page invalid")
					}
					content = append(content, decoded...)
				} else if block.Text != nil {
					content = append(content, []byte(*block.Text)...)
				} else {
					fixture.checks.Fatal("Full page omitted block")
				}
			}
			pages++
			if page.NextCursor == "" {
				break
			}
			if page.NextCursor == cursor || pages >= 128 {
				fixture.checks.Fatal("Full pagination did not advance within bound")
			}
			cursor = page.NextCursor
		}
		var expected []byte
		if part.name == "tool_arguments" {
			expected = []byte(`{"query":"synthetic"}`)
		} else if probe.image != "" {
			expected, _ = base64.StdEncoding.DecodeString(probe.image)
		} else {
			expected = []byte(probe.text)
		}
		if !bytes.Equal(content, expected) || len(expected) > 2<<20 && pages < 2 {
			fixture.checks.Fatal("Full reader content differs from exact synthetic bytes")
		}
		fixture.checks.Logf("Full reader part=%s bytes=%d pages=%d", part.name, len(content), pages)
	}
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
