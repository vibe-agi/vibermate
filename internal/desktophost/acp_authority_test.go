package desktophost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

func TestACPControlAuthorityAndFrozenRecordingCannotBeUpgraded(t *testing.T) {
	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	host := startHost(t, hostOptions(t, paths, filepath.Join(root, "data")))
	defer shutdownHost(t, host)
	discovery, err := localdiscovery.NewFile(paths.DiscoveryPath(), productruntime.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	session, err := discovery.Load()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	post := func(path, bearer, capability string, input any, status int, result any) {
		t.Helper()
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodPost, session.BaseURL+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		if capability != "" {
			request.Header.Set(capturecontrol.RunCapabilityHeader, capability)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != status {
			t.Fatalf("%s: got %d want %d", path, response.StatusCode, status)
		}
		if result != nil && json.NewDecoder(response.Body).Decode(result) != nil {
			t.Fatal("response could not be decoded")
		}
	}
	create := func() capturecontrol.LaunchGrant {
		var grant capturecontrol.LaunchGrant
		post("/api/v1/capture-runs", session.ControlCredential, "", capturecontrol.CreateRequest{EnvironmentID: environment.SystemTransparentID.String(), CWD: root, ExecutablePath: executable, Command: []string{executable}}, http.StatusCreated, &grant)
		return grant
	}
	grant, other := create(), create()
	otherCapability, _ := capturerun.NewControlCapability(other.RunCapability)
	if _, err := host.Runtime().CaptureRuns().Attach(context.Background(), other.Run.ID, otherCapability, 744); err != nil {
		t.Fatal(err)
	}
	post("/api/v1/capture-runs/"+other.Run.ID+"/actions/start-acp", "", other.RunCapability, capturecontrol.StartACPRequest{}, http.StatusConflict, nil)
	path := "/api/v1/capture-runs/" + grant.Run.ID + "/actions/"
	for _, rejected := range []string{"", grant.ProxyToken, other.RunCapability, session.ControlCredential} {
		post(path+"start-acp", "", rejected, capturecontrol.StartACPRequest{}, http.StatusForbidden, nil)
	}
	before, err := host.Runtime().CaptureRunReader().GetRun(context.Background(), grant.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var record acpobservation.Record
	post(path+"start-acp", "", grant.RunCapability, capturecontrol.StartACPRequest{}, http.StatusOK, &record)
	after, err := host.Runtime().CaptureRunReader().GetRun(context.Background(), grant.Run.ID)
	if err != nil || after.State != capturerun.StateCreated || after.ProcessID != 0 || !after.ExpiresAt.Equal(before.ExpiresAt) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("registration altered process/lease evidence")
	}
	if record.Policy.Mode != environment.ContentRecordingMetadataOnly {
		t.Fatal("default retained content")
	}
	post(path+"start-acp", "", grant.RunCapability, capturecontrol.StartACPRequest{RecordContent: true}, http.StatusConflict, nil)
	malicious := acpobservation.Snapshot{Revision: 2, Sessions: []acpobservation.Session{{ID: "s", Operation: "new"}}, Prompts: []acpobservation.Prompt{{Sequence: 1, SessionID: "s", State: "pending", UserText: "private injected text"}}}
	post(path+"observe-acp", "", grant.RunCapability, malicious, http.StatusUnprocessableEntity, nil)
	for _, rejected := range []string{grant.ProxyToken, other.RunCapability} {
		post(path+"observe-acp", "", rejected, record.Snapshot, http.StatusForbidden, nil)
	}
	observer := acpobservation.NewObserver(false)
	observer.Finish(0, false)
	final := observer.Snapshot()
	post(path+"observe-acp", "", grant.RunCapability, final, http.StatusNoContent, nil)
	post(path+"observe-acp", "", grant.RunCapability, final, http.StatusNoContent, nil)
	final.Revision++
	post(path+"observe-acp", "", grant.RunCapability, final, http.StatusConflict, nil)
	app := host.AppSession()
	for _, token := range []string{"", grant.RunCapability, other.RunCapability} {
		response := controlRequest(t, app.BaseURL, http.MethodGet, "/api/v1/captures/managed_run:"+grant.Run.ID+"/acp", token, "vibermate://desktop")
		response.Body.Close()
		if response.StatusCode == http.StatusOK {
			t.Fatal("run credential gained management read authority")
		}
	}
	response := controlRequest(t, app.BaseURL, http.MethodGet, "/api/v1/captures", app.ReadToken, "vibermate://desktop")
	var page struct {
		Items []struct {
			ID        string `json:"id"`
			Transport string `json:"transport"`
		} `json:"items"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&page) != nil {
		t.Fatal("cannot list captures")
	}
	response.Body.Close()
	for _, item := range page.Items {
		if (item.Transport == "acp_stdio") != (item.ID == grant.Run.ID) {
			t.Fatal("ACP membership mislabeled HTTP capture")
		}
	}
	capability, _ := capturerun.NewControlCapability(grant.RunCapability)
	if err := host.Runtime().CaptureRuns().Finish(context.Background(), grant.Run.ID, capability); err != nil {
		t.Fatal(err)
	}
	post(path+"observe-acp", "", grant.RunCapability, final, http.StatusForbidden, nil)
	post(path+"start-acp", "", grant.RunCapability, capturecontrol.StartACPRequest{}, http.StatusForbidden, nil)
}
