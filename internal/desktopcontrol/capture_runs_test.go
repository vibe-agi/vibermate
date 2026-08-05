package desktopcontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

func TestCaptureRunAuditCollectionAdmitsOnlyItsWebviewPreflight(t *testing.T) {
	t.Parallel()

	fixture := newAuditFixture(t, fixedCaptureRunReader{})
	preflight := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(
			http.MethodOptions,
			"http://"+fixture.authority+path,
			nil,
		)
		request.Host = fixture.authority
		request.RemoteAddr = "127.0.0.1:50000"
		request.Header.Set("Origin", "tauri://localhost")
		request.Header.Set("Sec-Fetch-Site", "cross-site")
		request.Header.Set("Sec-Fetch-Mode", "cors")
		request.Header.Set("Sec-Fetch-Dest", "empty")
		request.Header.Set("Access-Control-Request-Method", http.MethodGet)
		request.Header.Set("Access-Control-Request-Headers", "authorization")
		recorder := httptest.NewRecorder()
		fixture.router.ServeHTTP(recorder, request)
		return recorder
	}

	collection := preflight("/api/v1/capture-runs?limit=20")
	if collection.Code != http.StatusNoContent ||
		collection.Header().Get("Access-Control-Allow-Origin") !=
			"tauri://localhost" ||
		collection.Header().Get("Access-Control-Allow-Methods") !=
			http.MethodGet {
		t.Fatalf(
			"capture audit preflight status=%d headers=%v body=%s",
			collection.Code,
			collection.Header(),
			collection.Body.Bytes(),
		)
	}

	controlPath := preflight("/api/v1/capture-runs/run-1/actions/attach")
	if controlPath.Code != http.StatusForbidden {
		t.Fatalf(
			"capture control preflight status=%d body=%s",
			controlPath.Code,
			controlPath.Body.Bytes(),
		)
	}
}

type fixedCaptureRunReader struct {
	page capturerun.Page
}

func (reader fixedCaptureRunReader) ListRuns(
	context.Context,
	capturerun.PageRequest,
) (capturerun.Page, error) {
	return reader.page, nil
}

// The Desktop-only GET keeps lifecycle/observation diagnostics while sharing
// the exact safe adapter projection used by the contracted launcher routes.
func TestCaptureRunsUseASeparateCapabilityFreeAuditProjection(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	fixture := newAuditFixture(t, fixedCaptureRunReader{
		page: capturerun.Page{Items: []capturerun.View{{
			ID:              "run-verified-sample",
			ExecutableLabel: "claude",
			CWD:             "/Users/example/project",
			ProcessID:       4242,
			State:           capturerun.StateAttached,
			Observation:     capturerun.ObservationObserved,
			Recognition:     clientadapter.RecognitionVerified,
			CatalogRevision: 4,
			Adapter: &clientadapter.Evidence{
				ID:              "claude-code",
				Revision:        1,
				Version:         "2.1.220",
				CatalogRevision: 4,
				InstallShape:    clientadapter.InstallNativeSingleBinary,
				ReleaseSHA256:   strings.Repeat("a", 64),
				LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
				Features: clientadapter.
					FeatureResponsesWebSocketHTTPFallback,
			},
			CreatedAt: createdAt,
			ExpiresAt: createdAt.Add(time.Hour),
		}}},
	})
	recorded := doRequest(
		t,
		fixture.router,
		fixture.authority,
		http.MethodGet,
		"/api/v1/capture-runs?limit=20",
		fixture.readToken,
		nil,
	)
	if recorded.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorded.Code, recorded.Body)
	}
	pageObject := exactCaptureObject(t, recorded.Body.Bytes(), []string{"items"})
	var items []json.RawMessage
	if err := json.Unmarshal(pageObject["items"], &items); err != nil || len(items) != 1 {
		t.Fatalf("capture items=%d err=%v body=%s", len(items), err, recorded.Body.Bytes())
	}
	runObject := exactCaptureObject(t, items[0], []string{
		"catalogRevision",
		"clientAdapter",
		"clientAdapterState",
		"clientRecognition",
		"createdAt",
		"cwd",
		"executableLabel",
		"expiresAt",
		"id",
		"observation",
		"processId",
		"recognition",
		"state",
	})
	exactCaptureObject(t, runObject["clientAdapter"], []string{
		"catalogRevision",
		"id",
		"installShape",
		"launchRecipe",
		"revision",
		"source",
		"version",
	})
	var page desktopcontrol.CaptureRunAuditPage
	if err := json.Unmarshal(recorded.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	run := page.Items[0]
	if run.State != capturerun.StateAttached ||
		run.Observation != capturerun.ObservationObserved ||
		run.Recognition != clientadapter.RecognitionVerified ||
		run.ClientAdapterState != clientadapter.StatusVerified ||
		run.ClientRecognition != clientadapter.RecognitionVerified ||
		run.CatalogRevision != 4 || run.ClientAdapter == nil {
		t.Fatalf("capture audit view = %+v", run)
	}
	for _, forbidden := range [][]byte{
		[]byte(`"proxyToken"`),
		[]byte(`"runCapability"`),
		[]byte(`"proxyCapability"`),
		[]byte(`"controlCapability"`),
		[]byte(`"capabilityHash"`),
		[]byte(`"releaseSha256"`),
		[]byte(`"features"`),
	} {
		if bytes.Contains(recorded.Body.Bytes(), forbidden) {
			t.Fatalf("the audit read leaked %s: %s", forbidden, recorded.Body.Bytes())
		}
	}
}

func exactCaptureObject(
	t *testing.T,
	payload []byte,
	wantKeys []string,
) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatalf("decode JSON object: %v; payload=%s", err, payload)
	}
	gotKeys := make([]string, 0, len(object))
	for key := range object {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	wantKeys = append([]string(nil), wantKeys...)
	sort.Strings(wantKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("JSON keys=%v want=%v payload=%s", gotKeys, wantKeys, payload)
	}
	return object
}
