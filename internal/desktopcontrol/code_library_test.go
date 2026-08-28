package desktopcontrol_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

func TestCodeLibraryPublishesImmutableTransformRevisions(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(),
		Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(),
		CodeLibrary: runtime.CodeLibrary(), Offline: runtime, Clock: desktopcontrol.SystemClock{},
		ManualCaptures: runtime.ManualCaptures(),
	})
	if err != nil {
		t.Fatal(err)
	}

	created := environmentRequest(t, application, http.MethodPost,
		"/api/v1/code-library/collections", 0, "create-collection-0001",
		[]byte(`{"id":"privacy","displayName":"Privacy"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create collection status=%d body=%s", created.Code, created.Body.Bytes())
	}

	first := environmentRequest(t, application, http.MethodPut,
		"/api/v1/code-library/transforms/home-redaction", 0, "publish-transform-0001",
		[]byte(`{
			"collectionId":"privacy",
			"displayName":"Home redaction",
			"policy":{"requestJavaScript":"request.headers['x-revision'] = 'one';","responseJavaScript":""}
		}`))
	assertTransformRevision(t, first, 1, "request.headers['x-revision'] = 'one';")

	second := environmentRequest(t, application, http.MethodPut,
		"/api/v1/code-library/transforms/home-redaction", 1, "publish-transform-0002",
		[]byte(`{
			"collectionId":"privacy",
			"displayName":"Home redaction",
			"policy":{"requestJavaScript":"request.headers['x-revision'] = 'two';","responseJavaScript":"response.headers['x-restored'] = 'yes';"}
		}`))
	assertTransformRevision(t, second, 2, "request.headers['x-revision'] = 'two';")

	catalog := environmentRequest(t, application, http.MethodGet, "/api/v1/code-library", 0, "", nil)
	if catalog.Code != http.StatusOK {
		t.Fatalf("list catalog status=%d body=%s", catalog.Code, catalog.Body.Bytes())
	}
	var listed struct {
		Collections []struct {
			ID string `json:"id"`
		} `json:"collections"`
		Transforms []struct {
			ID       string `json:"id"`
			Revision uint64 `json:"revision"`
		} `json:"transforms"`
	}
	if err := json.Unmarshal(catalog.Body.Bytes(), &listed); err != nil ||
		len(listed.Collections) != 1 || listed.Collections[0].ID != "privacy" ||
		len(listed.Transforms) != 1 || listed.Transforms[0].ID != "home-redaction" ||
		listed.Transforms[0].Revision != 2 {
		t.Fatalf("catalog=%+v err=%v body=%s", listed, err, catalog.Body.Bytes())
	}

	historical := environmentRequest(t, application, http.MethodGet,
		"/api/v1/code-library/transforms/home-redaction/revisions/1", 0, "", nil)
	assertTransformRevision(t, historical, 1, "request.headers['x-revision'] = 'one';")
}

func assertTransformRevision(t *testing.T, response interface {
	Result() *http.Response
}, wantRevision uint64, wantRequestJavaScript string) {
	t.Helper()
	httpResponse := response.Result()
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusOK {
		var body any
		_ = json.NewDecoder(httpResponse.Body).Decode(&body)
		t.Fatalf("Transform status=%d body=%v", httpResponse.StatusCode, body)
	}
	var revision struct {
		Revision uint64 `json:"revision"`
		Policy   struct {
			RequestJavaScript string `json:"requestJavaScript"`
		} `json:"policy"`
	}
	if err := json.NewDecoder(httpResponse.Body).Decode(&revision); err != nil ||
		revision.Revision != wantRevision || revision.Policy.RequestJavaScript != wantRequestJavaScript {
		t.Fatalf("Transform=%+v want revision=%d request=%q err=%v", revision, wantRevision, wantRequestJavaScript, err)
	}
}
