package desktopcontrol_test

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

func TestWorkspaceEnvironmentDefaultHTTPContract(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application := newWorkspaceDefaultApplication(t, runtime)
	draftBody := []byte(`{
  "expectedDraftRevision": 0,
  "name": "Work",
  "state": "active",
  "clientEndpoints": [],
  "pluginBindings": [],
  "budgetPolicy": {"id":"","revision":0},
  "egressPolicy": {"id":"","revision":0,"mode":""}
}`)
	for _, action := range []struct {
		method, path, key string
		revision          uint64
		body              []byte
	}{
		{http.MethodPut, "/api/v1/environments/work/draft", "workspace-default-draft-01", 0, draftBody},
		{http.MethodPost, "/api/v1/environments/work/draft/actions/preview", "workspace-default-preview-01", 1, nil},
		{http.MethodPost, "/api/v1/environments/work/draft/actions/publish", "workspace-default-publish-01", 1, nil},
	} {
		response := environmentRequest(t, application, action.method, action.path, action.revision, action.key, action.body)
		if response.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", action.method, action.path, response.Code, response.Body.Bytes())
		}
	}
	encode := func(value byte) string {
		return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32))
	}
	path := "/api/v1/machines/" + encode(7) + "/workspaces/" + encode(8) + "/environment-default"
	missing := environmentRequest(t, application, http.MethodGet, path, 0, "", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.Bytes())
	}
	created := environmentRequest(
		t, application, http.MethodPut, path, 0, "workspace-default-set-01",
		[]byte(`{"environmentId":"work"}`),
	)
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.Bytes())
	}
	assertJSONString(t, created.Body.Bytes(), "environmentId", "work")
	assertJSONString(t, created.Body.Bytes(), "environmentName", "Work")
	assertJSONNumber(t, created.Body.Bytes(), "revision", 1)
	loaded := environmentRequest(t, application, http.MethodGet, path, 0, "", nil)
	if loaded.Code != http.StatusOK || loaded.Header().Get("ETag") != `"revision-1"` {
		t.Fatalf("get status=%d etag=%q body=%s", loaded.Code, loaded.Header().Get("ETag"), loaded.Body.Bytes())
	}
	deleted := environmentRequest(t, application, http.MethodDelete, path, 1, "workspace-default-delete-01", nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.Bytes())
	}
}

func newWorkspaceDefaultApplication(t *testing.T, runtime *productruntime.Runtime) http.Handler {
	t.Helper()
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(),
		Approvals: runtime.ToolApprovals(), Accounts: runtime.ProviderAccounts(),
		Offline: runtime, ManualCaptures: runtime.ManualCaptures(),
		WorkspaceDefaults: runtime.WorkspaceDefaults(), Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return application
}
