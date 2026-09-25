package desktopcontrol_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestPublishedEnvironmentDryRunExplainsOneSyntheticRequestWithoutRetainingIt(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime,
		Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(),
		DryRun:     runtime.DryRun,
		Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(),
		Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(),
		Accounts: runtime.ProviderAccounts(), CodeLibrary: runtime.CodeLibrary(), Offline: runtime,
		Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	const credential = "synthetic-dry-run-credential-do-not-return"
	account := environmentRequest(t, application, http.MethodPost,
		"/api/v1/provider-accounts", 0, "dry-run-account-create-0001",
		[]byte(`{"id":"account.dry-run","displayName":"Synthetic","upstreamEndpointId":"target.claude.official","kind":"anthropic_api_key","secret":"`+credential+`"}`))
	if account.Code != http.StatusCreated {
		t.Fatalf("account status=%d body=%s", account.Code, account.Body.Bytes())
	}
	draft := environmentRequest(t, application, http.MethodPut,
		"/api/v1/environments/preview-work/draft", 0, "dry-run-draft-0001", []byte(`{
  "expectedDraftRevision":0,"name":"Preview work","state":"active",
  "clientEndpoints":[{"id":"endpoint.preview","revision":1,"clientOrigin":"https://api.anthropic.com",
    "protocolPlans":[{"id":"plan.preview","revision":1,"clientProtocol":"anthropic_messages",
      "clientAdapterPolicy":{"id":"adapter.preview","revision":1},
      "destination":{"kind":"upstream","upstream":{
        "routes":[{"id":"route.preview","revision":1,
          "providerTarget":{"id":"target.claude.official","revision":1,"origin":"https://api.anthropic.com","realmId":"anthropic.official","capabilities":["messages","streaming","tool_calls"]},
          "backendProtocol":"anthropic_messages",
          "accountPolicy":{"revision":1,"mode":"fixed","fixedAccountId":"account.dry-run","accounts":[]},
          "modelPolicy":{"revision":1,"mode":"passthrough","mappings":[]},
          "wireProfileRef":"follow-client","pluginBindings":[]}],
        "defaultRouteId":"route.preview","routeSet":{"id":"routes.preview","revision":1,"candidateRouteIds":["route.preview"]}}},
      "egressProfile":{"id":"profile.direct","revision":1,"displayName":"Direct · System DNS","policy":{"proxy":{"kind":"direct"},"resolver":{"kind":"system","transport":"direct"}},"publishedAt":"1970-01-01T00:00:00Z"},
      "transforms":[],"pluginBindings":[]}] }],
  "pluginBindings":[],"budgetPolicy":{"id":"","revision":0},
  "contentRecording":{"mode":"full","retentionDays":30}
}`))
	if draft.Code != http.StatusOK {
		t.Fatalf("draft status=%d body=%s", draft.Code, draft.Body.Bytes())
	}
	published := environmentRequest(t, application, http.MethodPost,
		"/api/v1/environments/preview-work/draft/actions/publish", 1,
		"dry-run-publish-0001", nil)
	if published.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", published.Code, published.Body.Bytes())
	}
	const privateBody = "SYNTHETIC_PRIVATE_BODY_DO_NOT_RETURN"
	input, err := json.Marshal(map[string]any{
		"source": "published", "revision": 1,
		"clientOrigin": "https://api.anthropic.com", "method": "POST", "path": "/v1/messages",
		"clientProtocol": "http/1.1",
		"body":           `{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"` + privateBody + `"}]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	activitiesBefore := environmentRequest(t, application, http.MethodGet,
		"/api/v1/activities?kind=exchange&environmentId=preview-work&limit=100", 0, "", nil)
	if activitiesBefore.Code != http.StatusOK {
		t.Fatalf("activities before dry run status=%d", activitiesBefore.Code)
	}
	result := environmentRequest(t, application, http.MethodPost,
		"/api/v1/environments/preview-work/actions/dry-run", 0, "", input)
	if result.Code != http.StatusOK {
		t.Fatalf("dry run status=%d body=%s", result.Code, result.Body.Bytes())
	}
	var view struct {
		Source string `json:"source"`
		Result struct {
			EnvironmentID       string   `json:"environmentId"`
			EnvironmentRevision uint64   `json:"environmentRevision"`
			RouteID             string   `json:"routeId"`
			AccountID           string   `json:"accountId"`
			RequestedModel      string   `json:"requestedModel"`
			EffectiveModel      string   `json:"effectiveModel"`
			NetworkExitID       string   `json:"networkExitId"`
			Unverified          []string `json:"unverified"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Source != "published" || view.Result.EnvironmentID != "preview-work" ||
		view.Result.EnvironmentRevision != 1 || view.Result.RouteID != "route.preview" ||
		view.Result.AccountID != "account.dry-run" ||
		view.Result.RequestedModel != "claude-sonnet-4-5" ||
		view.Result.EffectiveModel != "claude-sonnet-4-5" ||
		view.Result.NetworkExitID != "profile.direct" || len(view.Result.Unverified) == 0 {
		t.Fatalf("unexpected dry run result: %+v", view)
	}
	if bytes.Contains(result.Body.Bytes(), []byte(credential)) ||
		bytes.Contains(result.Body.Bytes(), []byte(privateBody)) {
		t.Fatalf("dry run returned credential or request content: %s", result.Body.Bytes())
	}
	var unmatchedInput map[string]any
	if err := json.Unmarshal(input, &unmatchedInput); err != nil {
		t.Fatal(err)
	}
	unmatchedInput["path"] = "/v1/not-catalogued"
	unmatchedBody, err := json.Marshal(unmatchedInput)
	if err != nil {
		t.Fatal(err)
	}
	unmatched := environmentRequest(t, application, http.MethodPost,
		"/api/v1/environments/preview-work/actions/dry-run", 0, "", unmatchedBody)
	if unmatched.Code != http.StatusUnprocessableEntity ||
		!bytes.Contains(unmatched.Body.Bytes(), []byte(`"code":"dry_run_flow_not_matched"`)) ||
		bytes.Contains(unmatched.Body.Bytes(), []byte(privateBody)) {
		t.Fatalf("unmatched flow response=%d body=%s", unmatched.Code, unmatched.Body.Bytes())
	}
	invalidRequestInput := map[string]any{}
	for key, value := range unmatchedInput {
		invalidRequestInput[key] = value
	}
	invalidRequestInput["path"] = "/v1/messages"
	invalidRequestInput["body"] = `{"model":"claude-sonnet-4-5"}`
	invalidRequestBody, err := json.Marshal(invalidRequestInput)
	if err != nil {
		t.Fatal(err)
	}
	invalidRequest := environmentRequest(t, application, http.MethodPost,
		"/api/v1/environments/preview-work/actions/dry-run", 0, "", invalidRequestBody)
	if invalidRequest.Code != http.StatusUnprocessableEntity ||
		!bytes.Contains(invalidRequest.Body.Bytes(), []byte(`"code":"dry_run_input_invalid"`)) {
		t.Fatalf("invalid synthetic request status=%d body=%s", invalidRequest.Code, invalidRequest.Body.Bytes())
	}
	secretHeaderInput := map[string]any{}
	for key, value := range invalidRequestInput {
		secretHeaderInput[key] = value
	}
	secretHeaderInput["headers"] = map[string]string{"Authorization": "Bearer " + credential}
	secretHeaderBody, err := json.Marshal(secretHeaderInput)
	if err != nil {
		t.Fatal(err)
	}
	secretHeader := environmentRequest(t, application, http.MethodPost,
		"/api/v1/environments/preview-work/actions/dry-run", 0, "", secretHeaderBody)
	if secretHeader.Code != http.StatusUnprocessableEntity ||
		bytes.Contains(secretHeader.Body.Bytes(), []byte(credential)) {
		t.Fatalf("credential-shaped preview input status=%d body=%s", secretHeader.Code, secretHeader.Body.Bytes())
	}

	var committed desktopcontrol.EnvironmentPublishResponse
	if err := json.Unmarshal(published.Body.Bytes(), &committed); err != nil {
		t.Fatal(err)
	}
	current := committed.Environment
	current.ClientEndpoints[0].Revision++
	plan := &current.ClientEndpoints[0].ProtocolPlans[0]
	plan.Revision++
	route := &plan.Destination.Upstream.Routes[0]
	route.Revision++
	route.ModelPolicy.Revision++
	route.ModelPolicy.Mode = "map"
	route.ModelPolicy.Mappings = []environment.ModelMapping{{
		RequestedModel: "claude-sonnet-4-5", UpstreamModel: "synthetic-draft-model",
	}}
	draftInput, err := json.Marshal(desktopcontrol.EnvironmentDraftInput{
		ExpectedDraftRevision: 0, Name: current.Name, State: current.State,
		ClientEndpoints: current.ClientEndpoints, PluginBindings: current.PluginBindings,
		BudgetPolicy: current.BudgetPolicy, ContentRecording: current.ContentRecording,
		LaunchEnvironment: current.LaunchEnvironment, PolicySet: &current.PolicySet,
	})
	if err != nil {
		t.Fatal(err)
	}
	nextDraft := environmentRequest(t, application, http.MethodPut,
		"/api/v1/environments/preview-work/draft", 1, "dry-run-draft-0002", draftInput)
	if nextDraft.Code != http.StatusOK {
		t.Fatalf("next draft status=%d body=%s", nextDraft.Code, nextDraft.Body.Bytes())
	}
	var saved desktopcontrol.EnvironmentDraftResponse
	if err := json.Unmarshal(nextDraft.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	var dryRunInput map[string]any
	if err := json.Unmarshal(input, &dryRunInput); err != nil {
		t.Fatal(err)
	}
	dryRunInput["source"] = "draft"
	dryRunInput["revision"] = saved.DraftRevision
	draftBody, err := json.Marshal(dryRunInput)
	if err != nil {
		t.Fatal(err)
	}
	draftResult := environmentRequest(t, application, http.MethodPost,
		"/api/v1/environments/preview-work/actions/dry-run", 0, "", draftBody)
	if draftResult.Code != http.StatusOK {
		t.Fatalf("draft dry run status=%d body=%s", draftResult.Code, draftResult.Body.Bytes())
	}
	var draftView struct {
		Source            string `json:"source"`
		PublishedRevision uint64 `json:"publishedRevision"`
		DraftRevision     uint64 `json:"draftRevision"`
		Result            struct {
			EnvironmentRevision uint64 `json:"environmentRevision"`
			EffectiveModel      string `json:"effectiveModel"`
			ModelMapped         bool   `json:"modelMapped"`
		} `json:"result"`
	}
	if err := json.Unmarshal(draftResult.Body.Bytes(), &draftView); err != nil {
		t.Fatal(err)
	}
	if draftView.Source != "draft" || draftView.PublishedRevision != 1 ||
		draftView.DraftRevision != uint64(saved.DraftRevision) ||
		draftView.Result.EnvironmentRevision != 2 ||
		draftView.Result.EffectiveModel != "synthetic-draft-model" ||
		!draftView.Result.ModelMapped ||
		bytes.Contains(draftResult.Body.Bytes(), []byte(privateBody)) {
		t.Fatalf("draft dry run did not explain unpublished mapping: %+v", draftView)
	}
	unmappedInput := map[string]any{}
	for key, value := range dryRunInput {
		unmappedInput[key] = value
	}
	unmappedInput["body"] = `{"model":"unmapped-synthetic","max_tokens":16,"messages":[{"role":"user","content":"synthetic"}]}`
	unmappedBody, err := json.Marshal(unmappedInput)
	if err != nil {
		t.Fatal(err)
	}
	unmapped := environmentRequest(t, application, http.MethodPost,
		"/api/v1/environments/preview-work/actions/dry-run", 0, "", unmappedBody)
	if unmapped.Code != http.StatusOK ||
		!bytes.Contains(unmapped.Body.Bytes(), []byte(`"effectiveModel":"unmapped-synthetic"`)) ||
		!bytes.Contains(unmapped.Body.Bytes(), []byte(`"modelMapped":false`)) {
		t.Fatalf("unmapped model status=%d body=%s", unmapped.Code, unmapped.Body.Bytes())
	}
	staleInput := map[string]any{}
	for key, value := range dryRunInput {
		staleInput[key] = value
	}
	staleInput["revision"] = 1
	staleBody, err := json.Marshal(staleInput)
	if err != nil {
		t.Fatal(err)
	}
	stale := environmentRequest(t, application, http.MethodPost,
		"/api/v1/environments/preview-work/actions/dry-run", 0, "", staleBody)
	if stale.Code != http.StatusConflict ||
		!bytes.Contains(stale.Body.Bytes(), []byte(`"code":"revision_conflict"`)) {
		t.Fatalf("stale draft status=%d body=%s", stale.Code, stale.Body.Bytes())
	}

	collection := environmentRequest(t, application, http.MethodPost,
		"/api/v1/code-library/collections", 0, "dry-run-transform-collection-0001",
		[]byte(`{"id":"dry-run-tests","displayName":"Dry run tests"}`))
	if collection.Code != http.StatusCreated {
		t.Fatalf("transform collection status=%d body=%s", collection.Code, collection.Body.Bytes())
	}
	const privateScript = "PRIVATE_SCRIPT_FAILURE_DO_NOT_RETURN"
	transform := environmentRequest(t, application, http.MethodPut,
		"/api/v1/code-library/transforms/failing-preview", 0,
		"dry-run-transform-publish-0001", []byte(`{
  "collectionId":"dry-run-tests","displayName":"Failing preview",
  "policy":{"requestJavaScript":"throw new Error('`+privateScript+`');","responseJavaScript":""}
}`))
	if transform.Code != http.StatusOK {
		t.Fatalf("transform status=%d body=%s", transform.Code, transform.Body.Bytes())
	}
	var publishedTransform codelibrary.TransformRevision
	if err := json.Unmarshal(transform.Body.Bytes(), &publishedTransform); err != nil {
		t.Fatal(err)
	}
	failingCandidate := saved.Candidate
	failingCandidate.ClientEndpoints[0].ProtocolPlans[0].Transforms = []codelibrary.TransformRevision{publishedTransform}
	failingInput, err := json.Marshal(desktopcontrol.EnvironmentDraftInput{
		ExpectedDraftRevision: saved.DraftRevision,
		Name:                  failingCandidate.Name, State: failingCandidate.State,
		ClientEndpoints:   failingCandidate.ClientEndpoints,
		PluginBindings:    failingCandidate.PluginBindings,
		BudgetPolicy:      failingCandidate.BudgetPolicy,
		ContentRecording:  failingCandidate.ContentRecording,
		LaunchEnvironment: failingCandidate.LaunchEnvironment,
		PolicySet:         &failingCandidate.PolicySet,
	})
	if err != nil {
		t.Fatal(err)
	}
	failingDraft := environmentRequest(t, application, http.MethodPut,
		"/api/v1/environments/preview-work/draft", 1, "dry-run-draft-0003", failingInput)
	if failingDraft.Code != http.StatusOK {
		t.Fatalf("failing draft status=%d body=%s", failingDraft.Code, failingDraft.Body.Bytes())
	}
	var failingSaved desktopcontrol.EnvironmentDraftResponse
	if err := json.Unmarshal(failingDraft.Body.Bytes(), &failingSaved); err != nil {
		t.Fatal(err)
	}
	dryRunInput["revision"] = failingSaved.DraftRevision
	failingBody, err := json.Marshal(dryRunInput)
	if err != nil {
		t.Fatal(err)
	}
	failure := environmentRequest(t, application, http.MethodPost,
		"/api/v1/environments/preview-work/actions/dry-run", 0, "", failingBody)
	if failure.Code != http.StatusUnprocessableEntity ||
		!bytes.Contains(failure.Body.Bytes(), []byte(`"code":"dry_run_transform_failed"`)) ||
		bytes.Contains(failure.Body.Bytes(), []byte(privateScript)) ||
		bytes.Contains(failure.Body.Bytes(), []byte(privateBody)) {
		t.Fatalf("transform failure status=%d body=%s", failure.Code, failure.Body.Bytes())
	}
	working := environmentRequest(t, application, http.MethodPut,
		"/api/v1/code-library/transforms/failing-preview", 1,
		"dry-run-transform-publish-0002", []byte(`{
  "collectionId":"dry-run-tests","displayName":"Visible preview",
  "policy":{"requestJavaScript":"const value = JSON.parse(request.body); value.dry_run_marker = 'synthetic'; request.body = JSON.stringify(value); request.headers['x-dry-run'] = 'yes';","responseJavaScript":""}
}`))
	if working.Code != http.StatusOK {
		t.Fatalf("working transform status=%d body=%s", working.Code, working.Body.Bytes())
	}
	var workingTransform codelibrary.TransformRevision
	if err := json.Unmarshal(working.Body.Bytes(), &workingTransform); err != nil {
		t.Fatal(err)
	}
	workingCandidate := failingSaved.Candidate
	workingCandidate.ClientEndpoints[0].ProtocolPlans[0].Transforms = []codelibrary.TransformRevision{workingTransform}
	workingInput, err := json.Marshal(desktopcontrol.EnvironmentDraftInput{
		ExpectedDraftRevision: failingSaved.DraftRevision,
		Name:                  workingCandidate.Name, State: workingCandidate.State,
		ClientEndpoints:   workingCandidate.ClientEndpoints,
		PluginBindings:    workingCandidate.PluginBindings,
		BudgetPolicy:      workingCandidate.BudgetPolicy,
		ContentRecording:  workingCandidate.ContentRecording,
		LaunchEnvironment: workingCandidate.LaunchEnvironment,
		PolicySet:         &workingCandidate.PolicySet,
	})
	if err != nil {
		t.Fatal(err)
	}
	workingDraft := environmentRequest(t, application, http.MethodPut,
		"/api/v1/environments/preview-work/draft", 1, "dry-run-draft-0004", workingInput)
	if workingDraft.Code != http.StatusOK {
		t.Fatalf("working draft status=%d body=%s", workingDraft.Code, workingDraft.Body.Bytes())
	}
	var workingSaved desktopcontrol.EnvironmentDraftResponse
	if err := json.Unmarshal(workingDraft.Body.Bytes(), &workingSaved); err != nil {
		t.Fatal(err)
	}
	dryRunInput["revision"] = workingSaved.DraftRevision
	workingBody, err := json.Marshal(dryRunInput)
	if err != nil {
		t.Fatal(err)
	}
	workingResult := environmentRequest(t, application, http.MethodPost,
		"/api/v1/environments/preview-work/actions/dry-run", 0, "", workingBody)
	if workingResult.Code != http.StatusOK {
		t.Fatalf("working transform dry run status=%d body=%s", workingResult.Code, workingResult.Body.Bytes())
	}
	var changes struct {
		Result struct {
			ChangedHeaderNames    []string `json:"changedHeaderNames"`
			ChangedTopLevelFields []string `json:"changedTopLevelFields"`
		} `json:"result"`
	}
	if err := json.Unmarshal(workingResult.Body.Bytes(), &changes); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(changes.Result.ChangedHeaderNames, "X-Dry-Run") ||
		!slices.Contains(changes.Result.ChangedTopLevelFields, "dry_run_marker") ||
		bytes.Contains(workingResult.Body.Bytes(), []byte(privateBody)) {
		t.Fatalf("request transform changes = %+v", changes)
	}
	selector := environmentRequest(t, application, http.MethodPut,
		"/api/v1/code-library/account-selectors/dry-run-choice", 0,
		"dry-run-selector-publish-0001", []byte(`{
  "collectionId":"dry-run-tests","displayName":"Synthetic choice",
  "policy":{"javaScript":"selection.accountId = accounts[0].id;"}
}`))
	if selector.Code != http.StatusOK {
		t.Fatalf("selector status=%d body=%s", selector.Code, selector.Body.Bytes())
	}
	var selectorRevision codelibrary.AccountSelectorRevision
	if err := json.Unmarshal(selector.Body.Bytes(), &selectorRevision); err != nil {
		t.Fatal(err)
	}
	selectorCandidate := workingSaved.Candidate
	selectorPlan := &selectorCandidate.ClientEndpoints[0].ProtocolPlans[0]
	selectorPlan.Transforms = nil
	selectorPolicy := &selectorPlan.Destination.Upstream.Routes[0].AccountPolicy
	selectorPolicy.Revision++
	selectorPolicy.Mode = environment.AccountSelectionJavaScript
	selectorPolicy.FixedAccountID = ""
	selectorPolicy.Selector = &selectorRevision
	selectorPolicy.Accounts = nil
	selectorInput, err := json.Marshal(desktopcontrol.EnvironmentDraftInput{
		ExpectedDraftRevision: workingSaved.DraftRevision,
		Name:                  selectorCandidate.Name, State: selectorCandidate.State,
		ClientEndpoints:   selectorCandidate.ClientEndpoints,
		PluginBindings:    selectorCandidate.PluginBindings,
		BudgetPolicy:      selectorCandidate.BudgetPolicy,
		ContentRecording:  selectorCandidate.ContentRecording,
		LaunchEnvironment: selectorCandidate.LaunchEnvironment,
		PolicySet:         &selectorCandidate.PolicySet,
	})
	if err != nil {
		t.Fatal(err)
	}
	selectorDraft := environmentRequest(t, application, http.MethodPut,
		"/api/v1/environments/preview-work/draft", 1, "dry-run-draft-0005", selectorInput)
	if selectorDraft.Code != http.StatusOK {
		t.Fatalf("selector draft status=%d body=%s", selectorDraft.Code, selectorDraft.Body.Bytes())
	}
	var selectorSaved desktopcontrol.EnvironmentDraftResponse
	if err := json.Unmarshal(selectorDraft.Body.Bytes(), &selectorSaved); err != nil {
		t.Fatal(err)
	}
	dryRunInput["revision"] = selectorSaved.DraftRevision
	selectorBody, err := json.Marshal(dryRunInput)
	if err != nil {
		t.Fatal(err)
	}
	selected := environmentRequest(t, application, http.MethodPost,
		"/api/v1/environments/preview-work/actions/dry-run", 0, "", selectorBody)
	if selected.Code != http.StatusOK ||
		!bytes.Contains(selected.Body.Bytes(), []byte(`"accountId":"account.dry-run"`)) {
		t.Fatalf("selector dry run status=%d body=%s", selected.Code, selected.Body.Bytes())
	}
	badSelector := environmentRequest(t, application, http.MethodPut,
		"/api/v1/code-library/account-selectors/dry-run-choice", 1,
		"dry-run-selector-publish-0002", []byte(`{
  "collectionId":"dry-run-tests","displayName":"Out-of-set choice",
  "policy":{"javaScript":"selection.accountId = 'unlinked-account';"}
}`))
	if badSelector.Code != http.StatusOK {
		t.Fatalf("bad selector status=%d body=%s", badSelector.Code, badSelector.Body.Bytes())
	}
	if err := json.Unmarshal(badSelector.Body.Bytes(), &selectorRevision); err != nil {
		t.Fatal(err)
	}
	outOfSetCandidate := selectorSaved.Candidate
	outOfSetCandidate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].AccountPolicy.Selector = &selectorRevision
	outOfSetInput, err := json.Marshal(desktopcontrol.EnvironmentDraftInput{
		ExpectedDraftRevision: selectorSaved.DraftRevision,
		Name:                  outOfSetCandidate.Name, State: outOfSetCandidate.State,
		ClientEndpoints:   outOfSetCandidate.ClientEndpoints,
		PluginBindings:    outOfSetCandidate.PluginBindings,
		BudgetPolicy:      outOfSetCandidate.BudgetPolicy,
		ContentRecording:  outOfSetCandidate.ContentRecording,
		LaunchEnvironment: outOfSetCandidate.LaunchEnvironment,
		PolicySet:         &outOfSetCandidate.PolicySet,
	})
	if err != nil {
		t.Fatal(err)
	}
	outOfSetDraft := environmentRequest(t, application, http.MethodPut,
		"/api/v1/environments/preview-work/draft", 1, "dry-run-draft-0006", outOfSetInput)
	if outOfSetDraft.Code != http.StatusOK {
		t.Fatalf("out-of-set draft status=%d body=%s", outOfSetDraft.Code, outOfSetDraft.Body.Bytes())
	}
	var outOfSetSaved desktopcontrol.EnvironmentDraftResponse
	if err := json.Unmarshal(outOfSetDraft.Body.Bytes(), &outOfSetSaved); err != nil {
		t.Fatal(err)
	}
	dryRunInput["revision"] = outOfSetSaved.DraftRevision
	outOfSetBody, err := json.Marshal(dryRunInput)
	if err != nil {
		t.Fatal(err)
	}
	outOfSet := environmentRequest(t, application, http.MethodPost,
		"/api/v1/environments/preview-work/actions/dry-run", 0, "", outOfSetBody)
	if outOfSet.Code != http.StatusUnprocessableEntity ||
		!bytes.Contains(outOfSet.Body.Bytes(), []byte(`"code":"dry_run_selector_failed"`)) ||
		bytes.Contains(outOfSet.Body.Bytes(), []byte(privateBody)) {
		t.Fatalf("out-of-set selector status=%d body=%s", outOfSet.Code, outOfSet.Body.Bytes())
	}
	disabledCandidate := committed.Environment
	disabledInput, err := json.Marshal(desktopcontrol.EnvironmentDraftInput{
		ExpectedDraftRevision: outOfSetSaved.DraftRevision,
		Name:                  disabledCandidate.Name, State: environment.StateDisabled,
		ClientEndpoints:   disabledCandidate.ClientEndpoints,
		PluginBindings:    disabledCandidate.PluginBindings,
		BudgetPolicy:      disabledCandidate.BudgetPolicy,
		ContentRecording:  disabledCandidate.ContentRecording,
		LaunchEnvironment: disabledCandidate.LaunchEnvironment,
		PolicySet:         &disabledCandidate.PolicySet,
	})
	if err != nil {
		t.Fatal(err)
	}
	disabledDraft := environmentRequest(t, application, http.MethodPut,
		"/api/v1/environments/preview-work/draft", 1, "dry-run-draft-0007", disabledInput)
	if disabledDraft.Code != http.StatusOK {
		t.Fatalf("disabled draft status=%d body=%s", disabledDraft.Code, disabledDraft.Body.Bytes())
	}
	var disabledSaved desktopcontrol.EnvironmentDraftResponse
	if err := json.Unmarshal(disabledDraft.Body.Bytes(), &disabledSaved); err != nil {
		t.Fatal(err)
	}
	dryRunInput["revision"] = disabledSaved.DraftRevision
	disabledBody, err := json.Marshal(dryRunInput)
	if err != nil {
		t.Fatal(err)
	}
	disabled := environmentRequest(t, application, http.MethodPost,
		"/api/v1/environments/preview-work/actions/dry-run", 0, "", disabledBody)
	if disabled.Code != http.StatusUnprocessableEntity ||
		!bytes.Contains(disabled.Body.Bytes(), []byte(`"code":"dry_run_environment_disabled"`)) {
		t.Fatalf("disabled draft dry run status=%d body=%s", disabled.Code, disabled.Body.Bytes())
	}
	activitiesAfter := environmentRequest(t, application, http.MethodGet,
		"/api/v1/activities?kind=exchange&environmentId=preview-work&limit=100", 0, "", nil)
	if activitiesAfter.Code != http.StatusOK ||
		!bytes.Equal(activitiesBefore.Body.Bytes(), activitiesAfter.Body.Bytes()) {
		t.Fatal("dry run created or changed retained Activity evidence")
	}
}

func TestOriginalDestinationDryRunDoesNotInventRouteOrAccount(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime,
		Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(),
		DryRun:     runtime.DryRun,
		Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(),
		Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(),
		Accounts: runtime.ProviderAccounts(), Offline: runtime,
		Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := []byte(`{
  "source":"published","revision":1,"clientOrigin":"https://api.anthropic.com",
  "method":"POST","path":"/v1/messages","clientProtocol":"http/1.1",
  "body":"{\"model\":\"claude-sonnet-4-5\",\"max_tokens\":16,\"messages\":[{\"role\":\"user\",\"content\":\"synthetic\"}]}"
}`)
	response := environmentRequest(t, application, http.MethodPost,
		"/api/v1/environments/system_transparent/actions/dry-run", 0, "", input)
	if response.Code != http.StatusOK {
		t.Fatalf("original dry run status=%d body=%s", response.Code, response.Body.Bytes())
	}
	var view struct {
		Result struct {
			DestinationKind string   `json:"destinationKind"`
			RouteID         string   `json:"routeId"`
			AccountID       string   `json:"accountId"`
			RequestedModel  string   `json:"requestedModel"`
			EffectiveModel  string   `json:"effectiveModel"`
			Unverified      []string `json:"unverified"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Result.DestinationKind != "original" || view.Result.RouteID != "" ||
		view.Result.AccountID != "" || view.Result.RequestedModel != "claude-sonnet-4-5" ||
		view.Result.EffectiveModel != "claude-sonnet-4-5" ||
		!slices.Contains(view.Result.Unverified, "client_authentication") {
		t.Fatalf("original dry run invented authority: %+v", view)
	}
}
