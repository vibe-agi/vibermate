package capturecontrol_test

import (
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/launchsnapshot"
)

func TestInventoryRecordedOnlyAfterAuthorizedSuccessfulGrant(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.Close(t)
	inventory := launchsnapshot.Collect([]string{"GH_TOKEN=synthetic-private", "PATH=/synthetic"})
	create := capturecontrol.CreateRequest{EnvironmentID: testEnvironmentID, CWD: fixture.workspace,
		Command: []string{"claude"}, ExecutablePath: fixture.executable, EnvironmentInventory: &inventory}
	response := fixture.DoJSON(t, http.MethodPost, "/api/v1/capture-runs", "", "", create)
	if response.Code != http.StatusUnauthorized || len(fixture.snapshots.List()) != 0 {
		t.Fatal("unauthorized snapshot recorded")
	}
	create.CWD = "relative-path-not-allowed"
	response = fixture.DoJSON(t, http.MethodPost, "/api/v1/capture-runs", fixture.controlCredential, "", create)
	if response.Code == http.StatusCreated || len(fixture.snapshots.List()) != 0 {
		t.Fatal("failed grant snapshot recorded")
	}
	create.CWD = fixture.workspace
	response = fixture.DoJSON(t, http.MethodPost, "/api/v1/capture-runs", fixture.controlCredential, "", create)
	if response.Code != http.StatusCreated || len(fixture.snapshots.List()) != 1 {
		t.Fatalf("authorized inventory missing: %d %s", response.Code, response.Body)
	}
	for _, body := range []any{
		map[string]any{"environmentInventory": map[string]any{"names": []string{"GH_TOKEN=secret"}, "truncated": false}},
		map[string]any{"environmentInventory": map[string]any{"names": []string{"GH_TOKEN"}, "values": map[string]string{"GH_TOKEN": "secret"}, "truncated": false}},
	} {
		response = fixture.DoJSON(t, http.MethodPost, "/api/v1/capture-runs", fixture.controlCredential, "", body)
		if response.Code != http.StatusUnprocessableEntity || len(fixture.snapshots.List()) != 1 {
			t.Fatal("value-bearing input accepted")
		}
	}
}
