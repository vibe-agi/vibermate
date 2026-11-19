package desktopcontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// allApprovalsAuthority serves only the cross-Capture listing, keyset-paged
// like the store. It applies no visibility: that is the handler's job.
type allApprovalsAuthority struct {
	nativetool.AuthorityRepository
	mu       sync.Mutex
	items    []nativetool.PendingApprovalItem
	fail     error
	requests []nativetool.AllPendingApprovalRequest
}

func (f *allApprovalsAuthority) ListAllPendingApprovals(_ context.Context, in nativetool.AllPendingApprovalRequest) (nativetool.PendingApprovalPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, in)
	if f.fail != nil {
		return nativetool.PendingApprovalPage{}, f.fail
	}
	items := append([]nativetool.PendingApprovalItem(nil), f.items...)
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i].Approval, items[j].Approval
		return a.CreatedAt.Before(b.CreatedAt) || a.CreatedAt.Equal(b.CreatedAt) && a.ID < b.ID
	})
	page := nativetool.PendingApprovalPage{Items: []nativetool.PendingApprovalItem{}}
	for _, item := range items {
		a := item.Approval
		if !in.AfterCreatedAt.IsZero() && (a.CreatedAt.Before(in.AfterCreatedAt) || a.CreatedAt.Equal(in.AfterCreatedAt) && a.ID <= in.AfterID) {
			continue
		}
		if len(page.Items) == in.Limit {
			last := page.Items[len(page.Items)-1].Approval
			page.NextCreatedAt, page.NextID = last.CreatedAt, last.ID
			break
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

// ownedManualCaptures answers Get only for the local installation's own
// Captures, as the real manager does for another owner's Capture.
type ownedManualCaptures struct {
	manualCaptureFixture
	views   map[string]manualcapture.View
	failing string
}

func (f ownedManualCaptures) Get(_ context.Context, owner manualcapture.OwnerScope, id manualcapture.ID) (manualcapture.View, error) {
	if id.String() == f.failing {
		return manualcapture.View{}, errors.New("synthetic storage failure")
	}
	view, ok := f.views[id.String()]
	if !ok || owner.Kind() != manualcapture.OwnerLocalInstallation {
		return manualcapture.View{}, manualcapture.ErrNotFound
	}
	return view, nil
}

type allApprovalsClock struct{ now time.Time }

func (c allApprovalsClock) Now() time.Time { return c.now }

func pendingNativeItem(t *testing.T, capture string, serial int, created, expires time.Time) nativetool.PendingApprovalItem {
	t.Helper()
	ref, err := captureidentity.ParseKey(capture)
	if err != nil {
		t.Fatal(err)
	}
	approvalID := nativetool.ID(fmt.Sprintf("%032x", serial))
	operationID := nativetool.ID(fmt.Sprintf("%032x", 1000+serial))
	digest := nativetool.Digest{byte(serial)}
	return nativetool.PendingApprovalItem{
		Approval:  nativetool.Approval{ID: approvalID, OperationID: operationID, BindingDigest: digest, Revision: 1, State: "pending", CreatedAt: created, ExpiresAt: expires},
		Operation: nativetool.Operation{ID: operationID, Capture: ref, ApprovalID: &approvalID, BindingDigest: &digest, EnvironmentID: "native.synthetic", EnvironmentRevision: 3, CredentialOwner: "original_client", CreatedAt: created},
	}
}

func allApprovalsApplication(t *testing.T, authority *allApprovalsAuthority, now time.Time, previews ...desktopcontrol.PendingPreviewReader) http.Handler {
	t.Helper()
	runtime := startRuntime(t)
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	manual := manualcapture.View{
		ID: "same-id", DisplayName: "Manual app", ClientClass: manualcapture.ClientDesktopApp,
		Lifetime: manualcapture.LifetimeUntilRevoked, State: manualcapture.StateActive,
		CredentialRevision: 1, Observation: manualcapture.ObservationWaiting, CreatedAt: now, UpdatedAt: now,
	}
	options := desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: newAssignmentFixture(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime,
		CaptureRuns: captureReaderFixture{view: capturerun.View{
			ID: "same-id", ExecutableLabel: "claude", CWD: "/workspace", State: capturerun.StateAttached,
			Observation: capturerun.ObservationObserved, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour), CatalogRevision: 1,
		}},
		ManualCaptures:  ownedManualCaptures{views: map[string]manualcapture.View{"same-id": manual}, failing: "broken-id"},
		NativeAuthority: authority,
		Clock:           allApprovalsClock{now: now},
	}
	for _, reader := range previews {
		options.NativePreviews = reader
	}
	application, err := desktopcontrol.New(options)
	if err != nil {
		t.Fatal(err)
	}
	return application
}

func allApprovalsRequest(t *testing.T, handler http.Handler, path string, scope desktopcontrol.Scope) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if scope != "" {
		request = request.WithContext(desktopcontrol.WithNativeManagementAuthority(request.Context(), scope, manualcapture.NewLocalOwnerScope()))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%s may be cached: %v", path, response.Header())
	}
	return response
}

func decodeAllApprovals(t *testing.T, response *httptest.ResponseRecorder) desktopcontrol.NativeSessionApprovalPageDTO {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var page desktopcontrol.NativeSessionApprovalPageDTO
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestAllNativeApprovalsListsEverySessionOldestFirst(t *testing.T) {
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	later := now.Add(time.Minute)
	authority := &allApprovalsAuthority{items: []nativetool.PendingApprovalItem{
		pendingNativeItem(t, "managed_run:same-id", 4, now.Add(-3*time.Second), later),
		pendingNativeItem(t, "manual_capture:same-id", 2, now.Add(-4*time.Second), later),
		// Another owner's Capture: the per-session list answers 404 for it.
		pendingNativeItem(t, "manual_capture:foreign-id", 3, now.Add(-3*time.Second), later),
		pendingNativeItem(t, "managed_run:same-id", 1, now.Add(-5*time.Second), later),
		// Past its deadline, though storage still says pending.
		pendingNativeItem(t, "manual_capture:same-id", 5, now.Add(-2*time.Second), now),
	}}
	application := allApprovalsApplication(t, authority, now)

	first := decodeAllApprovals(t, allApprovalsRequest(t, application, desktopcontrol.NativeAllApprovalsPath+"?limit=2", desktopcontrol.ScopeRead))
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page %+v", first)
	}
	for i, want := range []struct{ id, capture, name string }{
		{fmt.Sprintf("%032x", 1), "managed_run:same-id", "claude"},
		{fmt.Sprintf("%032x", 2), "manual_capture:same-id", "Manual app"},
	} {
		got := first.Items[i]
		if got.Approval.ID != want.id || got.Approval.CaptureKey != want.capture || got.CaptureDisplayName != want.name || got.Approval.State != "pending" || got.Approval.CredentialOwner != "original_client" {
			t.Fatalf("item %d = %+v, want %+v", i, got, want)
		}
	}

	second := decodeAllApprovals(t, allApprovalsRequest(t, application, desktopcontrol.NativeAllApprovalsPath+"?limit=2&cursor="+url.QueryEscape(first.NextCursor), desktopcontrol.ScopeRead))
	if len(second.Items) != 1 || second.Items[0].Approval.ID != fmt.Sprintf("%032x", 4) || second.Items[0].CaptureDisplayName != "claude" || second.NextCursor == "" {
		t.Fatalf("second page skips only the hidden Capture %+v", second)
	}
	third := decodeAllApprovals(t, allApprovalsRequest(t, application, desktopcontrol.NativeAllApprovalsPath+"?limit=2&cursor="+url.QueryEscape(second.NextCursor), desktopcontrol.ScopeRead))
	if len(third.Items) != 0 || third.NextCursor != "" {
		t.Fatalf("expired question listed or traversal did not end %+v", third)
	}

	if len(authority.requests) != 3 {
		t.Fatalf("requests %+v", authority.requests)
	}
	for _, request := range authority.requests {
		if !request.ObservedAt.Equal(now) || request.Limit != 2 {
			t.Fatalf("request %+v lacks the handler's clock or limit", request)
		}
	}
	if resumed := authority.requests[1]; !resumed.AfterCreatedAt.Equal(now.Add(-4*time.Second)) || resumed.AfterID != nativetool.ID(fmt.Sprintf("%032x", 2)) {
		t.Fatalf("cursor did not resume after the last listed question: %+v", resumed)
	}
	if defaulted := decodeAllApprovals(t, allApprovalsRequest(t, application, desktopcontrol.NativeAllApprovalsPath, desktopcontrol.ScopeRead)); len(defaulted.Items) != 3 || defaulted.NextCursor != "" {
		t.Fatalf("default page %+v", defaulted)
	}
}

func TestAllNativeApprovalsKeepTheNativeReadBoundary(t *testing.T) {
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	authority := &allApprovalsAuthority{items: []nativetool.PendingApprovalItem{
		pendingNativeItem(t, "managed_run:same-id", 1, now.Add(-time.Second), now.Add(time.Minute)),
	}}
	application := allApprovalsApplication(t, authority, now)
	if got := allApprovalsRequest(t, application, desktopcontrol.NativeAllApprovalsPath, ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("read without native management authority = %d: %s", got.Code, got.Body.String())
	}
	if len(authority.requests) != 0 {
		t.Fatal("unauthenticated read reached storage")
	}
	if got := allApprovalsRequest(t, application, desktopcontrol.NativeAllApprovalsPath, desktopcontrol.ScopeWrite); got.Code != http.StatusOK {
		t.Fatalf("write scope read = %d", got.Code)
	}
	for _, query := range []string{"?limit=0", "?limit=200", "?limit=1&limit=2", "?unknown=1", "?cursor=", "?cursor=foreign", "?cursor=a&cursor=b"} {
		if got := allApprovalsRequest(t, application, desktopcontrol.NativeAllApprovalsPath+query, desktopcontrol.ScopeRead); got.Code != http.StatusUnprocessableEntity {
			t.Fatalf("query %q = %d", query, got.Code)
		}
	}
}

func TestAllNativeApprovalsFailClosedOnStorage(t *testing.T) {
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	authority := &allApprovalsAuthority{items: []nativetool.PendingApprovalItem{
		pendingNativeItem(t, "managed_run:same-id", 1, now.Add(-2*time.Second), now.Add(time.Minute)),
		pendingNativeItem(t, "manual_capture:broken-id", 2, now.Add(-time.Second), now.Add(time.Minute)),
	}}
	application := allApprovalsApplication(t, authority, now)
	// Whether broken-id is visible is unknown; dropping it would hide a
	// blocked agent, so the whole read fails.
	if got := allApprovalsRequest(t, application, desktopcontrol.NativeAllApprovalsPath, desktopcontrol.ScopeRead); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("visibility lookup failure = %d: %s", got.Code, got.Body.String())
	}
	authority.fail = errors.Join(nativetool.ErrUnavailable, errors.New("synthetic disk failure"))
	got := allApprovalsRequest(t, application, desktopcontrol.NativeAllApprovalsPath, desktopcontrol.ScopeRead)
	if got.Code != http.StatusServiceUnavailable {
		t.Fatalf("storage failure = %d: %s", got.Code, got.Body.String())
	}
	assertJSONString(t, got.Body.Bytes(), "code", string(desktopcontrol.ReasonRuntimeUnavailable))
}

// samplePreviews returns a frozen intent for the questions it knows.
type samplePreviews map[nativetool.ID][]nativemcp.NativeIntentPreview

func (p samplePreviews) PendingPreview(_ context.Context, _ captureidentity.Reference, operation nativetool.ID, digest nativetool.Digest) (nativemcp.PendingPreview, error) {
	intents, ok := p[operation]
	if !ok {
		return nativemcp.PendingPreview{}, nativetool.ErrNotFound
	}
	return nativemcp.PendingPreview{OperationID: operation, BindingDigest: digest, Revision: 1, Intents: intents, ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
}

// The app parses api/samples/native-approvals.json, so a change to what this
// list sends fails a test on both sides. VIBERMATE_UPDATE=1 rewrites it.
func TestAllNativeApprovalsSampleDescribesWhatTheRuntimeSends(t *testing.T) {
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	expires := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	managed := pendingNativeItem(t, "managed_run:same-id", 1, now.Add(-2*time.Second), expires)
	manual := pendingNativeItem(t, "manual_capture:same-id", 2, now.Add(-time.Second), expires)
	previews := samplePreviews{managed.Operation.ID: {{
		ToolName: "read_file", Action: "tool_call", Target: "https://chatgpt.com",
		Arguments: json.RawMessage(`{"path":"notes.txt"}`),
	}}}
	application := allApprovalsApplication(t, &allApprovalsAuthority{items: []nativetool.PendingApprovalItem{manual, managed}}, now, previews)
	response := allApprovalsRequest(t, application, desktopcontrol.NativeAllApprovalsPath, desktopcontrol.ScopeRead)
	if page := decodeAllApprovals(t, response); len(page.Items) != 2 || page.Items[0].Approval.PreviewState != "available" || page.Items[1].Approval.PreviewState != "unavailable" {
		t.Fatalf("sample must hold one question with and one without details: %+v", page)
	}
	var encoded bytes.Buffer
	if err := json.Indent(&encoded, bytes.TrimSpace(allApprovalsRequest(t, application, desktopcontrol.NativeAllApprovalsPath, desktopcontrol.ScopeRead).Body.Bytes()), "", "  "); err != nil {
		t.Fatal(err)
	}
	encoded.WriteByte('\n')
	path := filepath.Join("..", "..", "api", "samples", "native-approvals.json")
	if os.Getenv("VIBERMATE_UPDATE") == "1" {
		if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read native approval sample: %v", err)
	}
	if !bytes.Equal(current, encoded.Bytes()) {
		t.Fatalf("the native approval sample is stale; rerun with VIBERMATE_UPDATE=1\n--- stored\n%s\n--- current\n%s", current, encoded.Bytes())
	}
}
