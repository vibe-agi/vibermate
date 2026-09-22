package desktopcontrol

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/launchsnapshot"
)

func TestLaunchSnapshotsAreOnDemandNamesOnly(t *testing.T) {
	var store launchsnapshot.Store
	store.Record(capturerun.View{ID: "run-1", MachineID: "terminal-1"}, launchsnapshot.Collect([]string{"GH_TOKEN=do-not-serialize"}))
	handler := Handler{launchSnapshots: &store}
	response := httptest.NewRecorder()
	handler.listLaunchSnapshots(response, httptest.NewRequest(http.MethodGet, "/api/v1/launch-environment/snapshots", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), "GH_TOKEN") || strings.Contains(response.Body.String(), "do-not-serialize") {
		t.Fatalf("unsafe response: %d %s", response.Code, response.Body)
	}
	for _, query := range []string{"?includeValues=true", "?source=server"} {
		response := httptest.NewRecorder()
		handler.listLaunchSnapshots(response, httptest.NewRequest(http.MethodGet, "/api/v1/launch-environment/snapshots"+query, nil))
		if response.Code != 422 {
			t.Fatal("snapshot query accepted")
		}
	}
	handler.launchSnapshots = nil
	response = httptest.NewRecorder()
	handler.listLaunchSnapshots(response, httptest.NewRequest(http.MethodGet, "/api/v1/launch-environment/snapshots", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatal("empty registry must not guess server environment")
	}
}
