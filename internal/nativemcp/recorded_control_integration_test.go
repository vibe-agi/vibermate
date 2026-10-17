package nativemcp_test

import (
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
	"strings"
	"testing"
)

func TestNativeRecordedContentHTTP(t *testing.T) {
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	nativemcp.ExerciseNativeRecordedControl(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}, `{"content":[{"type":"text","text":"actual retained text"}],"structuredContent":{"answer":42}}`, func(store *runtimepersistence.Store, manuals manualcapture.Controller, capture captureidentity.Reference, op nativetool.Operation, reader nativetool.InspectionReader, invalidate func(bool)) {
		app, err := desktopcontrol.New(desktopcontrol.Options{Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime, Clock: desktopcontrol.SystemClock{}, ManualCaptures: manuals, NativeTools: store.NativeToolRepository(), NativeInspection: reader})
		if err != nil {
			t.Fatal(err)
		}
		// Sequence-only continuation is explicitly rejected by the new API.
		pageRequest := httptest.NewRequest(http.MethodGet, "/api/v1/captures/"+capture.Key()+"/native-tools/operations?afterSequence=999999", nil)
		pageRequest = pageRequest.WithContext(desktopcontrol.WithNativeManagementAuthority(context.Background(), desktopcontrol.ScopeRead, manualcapture.NewLocalOwnerScope()))
		pageResponse := httptest.NewRecorder()
		app.ServeHTTP(pageResponse, pageRequest)
		if pageResponse.Code != 422 {
			t.Fatalf("legacy native continuation accepted: %d", pageResponse.Code)
		}

		cursor := ""
		for i := 0; i < 2; i++ {
			path := "/api/v1/captures/" + capture.Key() + "/native-tools/operations"
			if cursor != "" {
				path += "?cursor=" + cursor
			}
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request = request.WithContext(desktopcontrol.WithNativeManagementAuthority(context.Background(), desktopcontrol.ScopeRead, manualcapture.NewLocalOwnerScope()))
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)
			if response.Code != 200 {
				t.Fatalf("native opaque page: %d %s", response.Code, response.Body.String())
			}
			var page desktopcontrol.NativeOperationPageDTO
			if err = json.Unmarshal(response.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if page.NextCursor == "" || len(page.InspectionVersion) != 64 || i == 1 && (len(page.Items) != 0 || page.NextCursor != cursor) {
				t.Fatal("opaque current tail reset")
			}
			cursor = page.NextCursor
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

func TestNativeRecordedContentOnlyHTTPUsesSeparateInspection(t *testing.T) {
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	nativemcp.ExerciseNativeContentOnlyControl(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}, `{"content":[{"type":"text","text":"body without snapshot"}]}`, func(store *runtimepersistence.Store, manuals manualcapture.Controller, capture captureidentity.Reference, op nativetool.Operation, reader nativetool.InspectionReader, _ func(bool)) {
		options := desktopcontrol.Options{Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime, Clock: desktopcontrol.SystemClock{}, ManualCaptures: manuals, NativeTools: store.NativeToolRepository(), NativeInspection: reader}
		app, err := desktopcontrol.New(options)
		if err != nil {
			t.Fatal(err)
		}
		base := "/api/v1/captures/" + capture.Key() + "/native-tools/operations/" + op.ID.String()
		read := func(handler http.Handler, path string) *httptest.ResponseRecorder {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request = request.WithContext(desktopcontrol.WithNativeManagementAuthority(context.Background(), desktopcontrol.ScopeRead, manualcapture.NewLocalOwnerScope()))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			return response
		}
		metadata := read(app, base)
		if metadata.Code != 200 {
			t.Fatal(metadata.Code, metadata.Body.String())
		}
		var dto desktopcontrol.NativeOperationDTO
		if err = json.Unmarshal(metadata.Body.Bytes(), &dto); err != nil {
			t.Fatal(err)
		}
		if dto.State != "unknown" || dto.Revision != 0 || dto.CompletedAt != nil || dto.Content.Result.State != "available" || dto.Content.Result.ObservedAt == nil {
			t.Fatalf("content-only lifecycle fabricated: %+v", dto)
		}
		content := read(app, base+"/content?part=tool_result")
		if content.Code != 200 || !strings.Contains(content.Body.String(), "body without snapshot") {
			t.Fatal("content-only body unavailable", content.Code)
		}
		options.NativeInspection = nil
		withoutOptional, err := desktopcontrol.New(options)
		if err != nil {
			t.Fatal(err)
		}
		unavailable := read(withoutOptional, base)
		if unavailable.Code != 503 {
			t.Fatal("inspection fell back to combined authority", unavailable.Code)
		}
	})
}
