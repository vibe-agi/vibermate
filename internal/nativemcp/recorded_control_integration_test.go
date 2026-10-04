package nativemcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeRecordedContentHTTP(t *testing.T) {
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	nativemcp.ExerciseNativeRecordedControl(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}, `{"content":[{"type":"text","text":"actual retained text"}],"structuredContent":{"answer":42}}`, func(store *runtimepersistence.Store, manuals manualcapture.Controller, capture captureidentity.Reference, op nativetool.Operation) {
		app, err := desktopcontrol.New(desktopcontrol.Options{Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime, Clock: desktopcontrol.SystemClock{}, ManualCaptures: manuals, NativeTools: store.NativeToolRepository()})
		if err != nil {
			t.Fatal(err)
		}
		pageRequest := httptest.NewRequest(http.MethodGet, "/api/v1/captures/"+capture.Key()+"/native-tools/operations?afterSequence=999999", nil)
		pageRequest = pageRequest.WithContext(desktopcontrol.WithNativeManagementAuthority(context.Background(), desktopcontrol.ScopeRead, manualcapture.NewLocalOwnerScope()))
		pageResponse := httptest.NewRecorder()
		app.ServeHTTP(pageResponse, pageRequest)
		if pageResponse.Code != 200 || !bytes.Contains(pageResponse.Body.Bytes(), []byte(`"nextSequence":999999`)) {
			t.Fatalf("terminal native page %d: %s", pageResponse.Code, pageResponse.Body.String())
		}
		request := httptest.NewRequest(http.MethodGet, "/api/v1/captures/"+capture.Key()+"/native-tools/operations/"+op.ID.String()+"/content?part=tool_result", nil)
		request = request.WithContext(desktopcontrol.WithNativeManagementAuthority(context.Background(), desktopcontrol.ScopeRead, manualcapture.NewLocalOwnerScope()))
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("deferred content status %d want200: %s", response.Code, response.Body.String())
		}
		var body struct {
			Blocks []struct{ Kind, Text string } `json:"blocks"`
		}
		if err = json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Blocks) != 2 || body.Blocks[0].Kind != "text" || body.Blocks[0].Text != "actual retained text" || body.Blocks[1].Kind != "json" || body.Blocks[1].Text != `{"answer":42}` {
			t.Fatalf("real content lost %+v", body)
		}
	})
}
