package desktopcontrol_test

import (
	"net/http"
	"testing"
)

func TestNativeReaderDirectHandlerRequiresManagementAuthority(t *testing.T) {
	app := unifiedCaptureApplication(t, newAssignmentFixture())
	for _, path := range []string{
		"/api/v1/captures/manual_capture:same-id/native-tools/operations",
		"/api/v1/captures/manual_capture:same-id/native-tools/operations/00000000000000000000000000000001",
		"/api/v1/captures/manual_capture:same-id/native-tools/operations/00000000000000000000000000000001/content?part=tool_result",
	} {
		got := environmentRequest(t, app, http.MethodGet, path, 0, "", nil)
		if got.Code != 401 {
			t.Fatalf("unauthenticated reader%d: %s", got.Code, got.Body.String())
		}
	}
}
