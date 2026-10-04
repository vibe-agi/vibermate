package desktopcontrol_test

import (
	"net/http"
	"testing"
)

// A native read must not inherit authority merely because callers can invoke
// the application handler directly (as model-only contract tests do).
func TestNativeApprovalDirectHandlerRequiresManagementAuthority(t *testing.T) {
	app := unifiedCaptureApplication(t, newAssignmentFixture())
	got := environmentRequest(t, app, http.MethodGet, "/api/v1/captures/manual_capture:same-id/native-tools/approvals", 0, "", nil)
	if got.Code != http.StatusUnauthorized {
		t.Fatalf("native read without authenticated context = %d, want 401: %s", got.Code, got.Body.String())
	}
}
