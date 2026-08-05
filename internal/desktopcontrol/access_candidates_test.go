package desktopcontrol_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accesscredential"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func TestAccessCandidateMutationStagesCredentialBeforeSelectingRoute(t *testing.T) {
	t.Parallel()

	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness:      readyState(true),
		Status:         runtime,
		Accesses:       runtime.AccessWriter(),
		AccessDeletion: runtime.AccessDeleter(),
		Clock:          desktopcontrol.SystemClock{},
		AccessCatalog:  runtime.AccessCatalog(),
		Resolver:       runtime.SnapshotResolver(),
		Credentials:    runtime.Credentials(),
		Activities:     runtime.Activities(),
		Connections:    runtime.ConnectionEvents(),
		Egress:         runtime.EgressAttempts(),
		Approvals:      runtime.ToolApprovals(),
		Offline:        runtime,
	})
	if err != nil {
		t.Fatal(err)
	}

	initialBody, err := json.Marshal(validApplyInput())
	if err != nil {
		t.Fatal(err)
	}
	initial := doMutation(
		t,
		application,
		"127.0.0.1:43141",
		"/api/v1/accesses/access-control/actions/apply",
		"unused",
		0,
		"candidate-initial-0001",
		initialBody,
	)
	if initial.Code != http.StatusOK {
		t.Fatalf("initial apply code=%d body=%s", initial.Code, initial.Body.Bytes())
	}

	accessID, err := access.NewAccessID("access-control")
	if err != nil {
		t.Fatal(err)
	}
	before, exists, err := runtime.AccessCatalog().ReadAccess(t.Context(), accessID)
	if err != nil || !exists {
		t.Fatalf("read initial aggregate exists=%t err=%v", exists, err)
	}
	oldSecretRef := before.AccountBindings[0].SecretRef
	addBody, err := json.Marshal(desktopcontrol.AddAccessCandidateInput{
		Name:          "Claude relay – work",
		Provider:      desktopcontrol.AccessCandidateProviderAnthropicCompatible,
		BaseURL:       "https://relay.example.test/anthropic",
		Model:         "claude-sonnet-4-5",
		AuthDriverRef: access.AuthDriverStaticHeaderValue,
	})
	if err != nil {
		t.Fatal(err)
	}
	added := doMutation(
		t,
		application,
		"127.0.0.1:43141",
		"/api/v1/accesses/access-control/actions/add-candidate",
		"unused",
		1,
		"candidate-add-0001",
		addBody,
	)
	if added.Code != http.StatusCreated {
		t.Fatalf("add candidate code=%d body=%s", added.Code, added.Body.Bytes())
	}
	addedWire := append([]byte(nil), added.Body.Bytes()...)
	if bytes.Contains(addedWire, []byte("secretRef")) ||
		bytes.Contains(addedWire, []byte("secret://")) {
		t.Fatalf("add candidate exposed SecretRef: %s", addedWire)
	}
	var addResult desktopcontrol.AccessAddCandidateResponse
	decodeResponse(t, added, &addResult)
	if addResult.Outcome != access.WriteOutcomeCommitted ||
		addResult.Revision != 2 ||
		addResult.ApplicationState != desktopcontrol.AccessApplicationStateActive ||
		addResult.Candidate.ProfileID == "" ||
		addResult.Candidate.CredentialID == "" ||
		len(addResult.PlanHash) != 64 {
		t.Fatalf("add candidate response = %+v", addResult)
	}

	replayedAdd := doMutation(
		t,
		application,
		"127.0.0.1:43141",
		"/api/v1/accesses/access-control/actions/add-candidate",
		"unused",
		1,
		"candidate-add-0001",
		addBody,
	)
	if replayedAdd.Code != added.Code ||
		!bytes.Equal(replayedAdd.Body.Bytes(), addedWire) {
		t.Fatalf(
			"add replay code=%d body=%s",
			replayedAdd.Code,
			replayedAdd.Body.Bytes(),
		)
	}

	staged, exists, err := runtime.AccessCatalog().ReadAccess(t.Context(), accessID)
	if err != nil || !exists {
		t.Fatalf("read staged aggregate exists=%t err=%v", exists, err)
	}
	if staged.Binding.Revision != 2 ||
		len(staged.Profiles) != 3 ||
		len(staged.ProviderTargets) != 3 ||
		len(staged.AccountBindings) != 2 ||
		staged.AccountBindings[0].SecretRef != oldSecretRef {
		t.Fatalf("staged aggregate changed existing resources: %+v", staged)
	}
	profileID, err := access.NewEndpointProfileID(addResult.Candidate.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	credentialID, err := access.NewAccountBindingID(addResult.Candidate.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	newProfile, newTarget, newAccount, found := candidateResources(
		staged,
		profileID,
		credentialID,
	)
	if !found ||
		newProfile.BackendDialect != access.DialectAnthropicMessages ||
		newProfile.DefaultModelPolicy.FixedModel.String() != "claude-sonnet-4-5" ||
		newTarget.Protocol != access.DialectAnthropicMessages ||
		newTarget.Origin.String() != "https://relay.example.test/anthropic" ||
		newAccount.AuthDriverRef != access.StaticHeaderAuthDriverRef() ||
		newAccount.SecretRef == oldSecretRef ||
		newAccount.Enabled {
		t.Fatalf(
			"staged resources profile=%+v target=%+v account=%+v found=%t",
			newProfile,
			newTarget,
			newAccount,
			found,
		)
	}
	if len(staged.RouteSets) != 1 ||
		len(staged.RouteSets[0].CandidateProfileIDs) != 2 ||
		staged.RouteSets[0].CandidateProfileIDs[0].String() != "access-control-profile" ||
		staged.RouteSets[0].CandidateProfileIDs[1] != access.OriginalPassthroughProfileID() {
		t.Fatalf("unconfigured candidate entered RouteSet: %+v", staged.RouteSets)
	}

	credentialPath := "/api/v1/accesses/access-control/profiles/" +
		addResult.Candidate.ProfileID + "/credentials/" +
		addResult.Candidate.CredentialID
	credentialResponse := doRequest(
		t,
		application,
		"127.0.0.1:43141",
		http.MethodGet,
		credentialPath,
		"unused",
		nil,
	)
	if credentialResponse.Code != http.StatusOK {
		t.Fatalf(
			"credential read code=%d body=%s",
			credentialResponse.Code,
			credentialResponse.Body.Bytes(),
		)
	}
	var credential accesscredential.View
	decodeResponse(t, credentialResponse, &credential)
	if credential.SecretState != secretstore.StateMissing || credential.SecretRevision != 0 {
		t.Fatalf("staged credential = %+v", credential)
	}

	selectPath := "/api/v1/accesses/access-control/profiles/" +
		addResult.Candidate.ProfileID + "/actions/select-candidate"
	unconfigured := doMutation(
		t,
		application,
		"127.0.0.1:43141",
		selectPath,
		"unused",
		2,
		"candidate-select-no-secret-0001",
		nil,
	)
	if unconfigured.Code != http.StatusUnprocessableEntity ||
		!bytes.Contains(unconfigured.Body.Bytes(), []byte("credential_not_configured")) {
		t.Fatalf(
			"unconfigured select code=%d body=%s",
			unconfigured.Code,
			unconfigured.Body.Bytes(),
		)
	}
	afterRejected, _, err := runtime.AccessCatalog().ReadAccess(t.Context(), accessID)
	if err != nil || afterRejected.Binding.Revision != 2 {
		t.Fatalf("rejected selection mutated Access: revision=%d err=%v", afterRejected.Binding.Revision, err)
	}

	secretBody, err := json.Marshal(desktopcontrol.CredentialSecretInput{
		Secret: "relay-secret-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	replaced := doMutation(
		t,
		application,
		"127.0.0.1:43141",
		credentialPath+"/actions/replace-secret",
		"unused",
		0,
		"candidate-secret-0001",
		secretBody,
	)
	if replaced.Code != http.StatusOK {
		t.Fatalf("replace secret code=%d body=%s", replaced.Code, replaced.Body.Bytes())
	}

	selected := doMutation(
		t,
		application,
		"127.0.0.1:43141",
		selectPath,
		"unused",
		2,
		"candidate-select-0001",
		nil,
	)
	if selected.Code != http.StatusOK {
		t.Fatalf("select candidate code=%d body=%s", selected.Code, selected.Body.Bytes())
	}
	selectedWire := append([]byte(nil), selected.Body.Bytes()...)
	var selectResult desktopcontrol.AccessApplyResponse
	decodeResponse(t, selected, &selectResult)
	if selectResult.Revision != 3 ||
		len(selectResult.PlanHash) != 64 {
		t.Fatalf("select response = %+v", selectResult)
	}
	if bytes.Contains(selectedWire, []byte("candidate")) ||
		bytes.Contains(selectedWire, []byte("credentialId")) {
		t.Fatalf("select response leaked staging coordinates: %s", selectedWire)
	}
	active, exists, err := runtime.AccessCatalog().ReadAccess(t.Context(), accessID)
	if err != nil || !exists {
		t.Fatalf("read selected aggregate exists=%t err=%v", exists, err)
	}
	if active.Binding.Revision != 3 ||
		len(active.RouteSets[0].CandidateProfileIDs) != 3 ||
		active.RouteSets[0].CandidateProfileIDs[0] != profileID ||
		active.RouteSets[0].CandidateProfileIDs[1].String() != "access-control-profile" ||
		active.RouteSets[0].CandidateProfileIDs[2] != access.OriginalPassthroughProfileID() ||
		active.RouteSets[0].FallbackMode() != access.FallbackDisabled {
		t.Fatalf("selected RouteSet = %+v", active.RouteSets[0])
	}
	_, _, selectedAccount, found := candidateResources(active, profileID, credentialID)
	if !found || !selectedAccount.Enabled || selectedAccount.Revision != 3 {
		t.Fatalf("selected account was not enabled atomically: %+v", selectedAccount)
	}
	snapshot, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatal(err)
	}
	primary, found := snapshot.Candidate(0)
	if !found || primary.ProfileID() != profileID {
		t.Fatalf("active candidate profile=%q found=%t", primary.ProfileID().String(), found)
	}

	replayedSelect := doMutation(
		t,
		application,
		"127.0.0.1:43141",
		selectPath,
		"unused",
		2,
		"candidate-select-0001",
		nil,
	)
	if replayedSelect.Code != selected.Code ||
		!bytes.Equal(replayedSelect.Body.Bytes(), selectedWire) {
		t.Fatalf(
			"select replay code=%d body=%s",
			replayedSelect.Code,
			replayedSelect.Body.Bytes(),
		)
	}

	originalPath := "/api/v1/accesses/access-control/profiles/" +
		access.OriginalPassthroughProfileID().String() +
		"/actions/select-candidate"
	originalSelected := doMutation(
		t,
		application,
		"127.0.0.1:43141",
		originalPath,
		"unused",
		3,
		"candidate-select-original-0001",
		nil,
	)
	if originalSelected.Code != http.StatusOK ||
		bytes.Contains(originalSelected.Body.Bytes(), []byte("credential")) {
		t.Fatalf(
			"select original code=%d body=%s",
			originalSelected.Code,
			originalSelected.Body.Bytes(),
		)
	}
	var originalResult desktopcontrol.AccessApplyResponse
	decodeResponse(t, originalSelected, &originalResult)
	if originalResult.Revision != 4 || len(originalResult.PlanHash) != 64 {
		t.Fatalf("select original response = %+v", originalResult)
	}
	originalSnapshot, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatal(err)
	}
	originalPrimary, found := originalSnapshot.Candidate(0)
	if !found ||
		originalPrimary.ProfileID() != access.OriginalPassthroughProfileID() ||
		originalPrimary.Kind() != access.EndpointProfileOriginalPassthrough {
		t.Fatalf(
			"selected original candidate=%+v found=%t",
			originalPrimary,
			found,
		)
	}
	originalAggregate, _, err := runtime.AccessCatalog().ReadAccess(t.Context(), accessID)
	if err != nil ||
		len(originalAggregate.RouteSets[0].CandidateProfileIDs) != 3 ||
		originalAggregate.RouteSets[0].CandidateProfileIDs[0] !=
			access.OriginalPassthroughProfileID() ||
		originalAggregate.RouteSets[0].CandidateProfileIDs[1] != profileID ||
		originalAggregate.RouteSets[0].CandidateProfileIDs[2].String() !=
			"access-control-profile" {
		t.Fatalf("original route choices = %+v err=%v", originalAggregate.RouteSets, err)
	}

	detail := doRequest(
		t,
		application,
		"127.0.0.1:43141",
		http.MethodGet,
		"/api/v1/accesses/access-control",
		"unused",
		nil,
	)
	if detail.Code != http.StatusOK ||
		bytes.Contains(detail.Body.Bytes(), []byte("secretRef")) ||
		bytes.Contains(detail.Body.Bytes(), []byte("secret://")) {
		t.Fatalf("Access detail exposed a SecretRef: %s", detail.Body.Bytes())
	}
}

func TestCodexAccessAddsConfiguresAndSelectsOpenAICandidate(t *testing.T) {
	t.Parallel()

	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness:      readyState(true),
		Status:         runtime,
		Accesses:       runtime.AccessWriter(),
		AccessDeletion: runtime.AccessDeleter(),
		Clock:          desktopcontrol.SystemClock{},
		AccessCatalog:  runtime.AccessCatalog(),
		Resolver:       runtime.SnapshotResolver(),
		Credentials:    runtime.Credentials(),
		Activities:     runtime.Activities(),
		Connections:    runtime.ConnectionEvents(),
		Egress:         runtime.EgressAttempts(),
		Approvals:      runtime.ToolApprovals(),
		Offline:        runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	initialInput := validApplyInput()
	initialInput.Access.Name = "Codex Access"
	initialInput.AgentEndpoint.ClientDialect = string(access.DialectOpenAIResponses)
	initialBody, err := json.Marshal(initialInput)
	if err != nil {
		t.Fatal(err)
	}
	initial := doMutation(
		t,
		application,
		"127.0.0.1:43143",
		"/api/v1/accesses/access-control/actions/apply",
		"unused",
		0,
		"codex-initial-0001",
		initialBody,
	)
	if initial.Code != http.StatusOK {
		t.Fatalf("Codex initial apply code=%d body=%s", initial.Code, initial.Body.Bytes())
	}

	addBody, err := json.Marshal(desktopcontrol.AddAccessCandidateInput{
		Name:     "Codex relay account",
		Provider: desktopcontrol.AccessCandidateProviderOpenAICompatible,
		// This is the complete endpoint shape people commonly paste. The
		// server stores /v1 because the OpenAI Chat codec appends its method.
		BaseURL: "https://codex-relay.example.test/v1/chat/completions",
		Model:   "gpt-5.2-codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	added := doMutation(
		t,
		application,
		"127.0.0.1:43143",
		"/api/v1/accesses/access-control/actions/add-candidate",
		"unused",
		1,
		"codex-candidate-add-0001",
		addBody,
	)
	if added.Code != http.StatusCreated {
		t.Fatalf("Codex add code=%d body=%s", added.Code, added.Body.Bytes())
	}
	var addResult desktopcontrol.AccessAddCandidateResponse
	decodeResponse(t, added, &addResult)
	profileID, err := access.NewEndpointProfileID(addResult.Candidate.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	credentialID, err := access.NewAccountBindingID(addResult.Candidate.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	accessID, _ := access.NewAccessID("access-control")
	staged, exists, err := runtime.AccessCatalog().ReadAccess(t.Context(), accessID)
	if err != nil || !exists {
		t.Fatalf("read Codex staged aggregate exists=%t err=%v", exists, err)
	}
	profile, target, account, found := candidateResources(
		staged,
		profileID,
		credentialID,
	)
	if !found ||
		profile.BackendDialect != access.DialectOpenAIChat ||
		target.Protocol != access.DialectOpenAIChat ||
		target.Origin.String() != "https://codex-relay.example.test/v1" ||
		account.AuthDriverRef != access.StaticHeaderAuthDriverRef() ||
		account.Enabled ||
		len(staged.RouteSets[0].CandidateProfileIDs) != 2 ||
		staged.RouteSets[0].CandidateProfileIDs[1] !=
			access.OriginalPassthroughProfileID() {
		t.Fatalf(
			"Codex staged resources profile=%+v target=%+v account=%+v routes=%+v",
			profile,
			target,
			account,
			staged.RouteSets,
		)
	}

	credentialPath := "/api/v1/accesses/access-control/profiles/" +
		addResult.Candidate.ProfileID + "/credentials/" +
		addResult.Candidate.CredentialID
	secretBody, err := json.Marshal(desktopcontrol.CredentialSecretInput{
		Secret: "codex-relay-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	replaced := doMutation(
		t,
		application,
		"127.0.0.1:43143",
		credentialPath+"/actions/replace-secret",
		"unused",
		0,
		"codex-secret-0001",
		secretBody,
	)
	if replaced.Code != http.StatusOK {
		t.Fatalf("Codex replace secret code=%d body=%s", replaced.Code, replaced.Body.Bytes())
	}
	selectPath := "/api/v1/accesses/access-control/profiles/" +
		addResult.Candidate.ProfileID + "/actions/select-candidate"
	selected := doMutation(
		t,
		application,
		"127.0.0.1:43143",
		selectPath,
		"unused",
		2,
		"codex-select-0001",
		nil,
	)
	if selected.Code != http.StatusOK {
		t.Fatalf("Codex select code=%d body=%s", selected.Code, selected.Body.Bytes())
	}
	snapshot, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatal(err)
	}
	primary, found := snapshot.Candidate(0)
	if !found ||
		primary.ProfileID() != profileID ||
		snapshot.CodecPlan().ClientDialect() != access.DialectOpenAIResponses ||
		snapshot.CodecPlan().ProviderDialect() != access.DialectOpenAIChat {
		t.Fatalf(
			"Codex selected candidate=%+v found=%t codec=%+v",
			primary,
			found,
			snapshot.CodecPlan(),
		)
	}
	active, _, err := runtime.AccessCatalog().ReadAccess(t.Context(), accessID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, activeAccount, found := candidateResources(active, profileID, credentialID)
	if !found || !activeAccount.Enabled ||
		active.RouteSets[0].CandidateProfileIDs[0] != profileID {
		t.Fatalf("Codex candidate was not activated: account=%+v routes=%+v", activeAccount, active.RouteSets)
	}
}

func TestAccessFullApplyPreservesExistingSecretRefServerSide(t *testing.T) {
	t.Parallel()

	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness:      readyState(true),
		Status:         runtime,
		Accesses:       runtime.AccessWriter(),
		AccessDeletion: runtime.AccessDeleter(),
		Clock:          desktopcontrol.SystemClock{},
		AccessCatalog:  runtime.AccessCatalog(),
		Resolver:       runtime.SnapshotResolver(),
		Credentials:    runtime.Credentials(),
		Activities:     runtime.Activities(),
		Connections:    runtime.ConnectionEvents(),
		Egress:         runtime.EgressAttempts(),
		Approvals:      runtime.ToolApprovals(),
		Offline:        runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	initialInput := validApplyInput()
	initialBody, err := json.Marshal(initialInput)
	if err != nil {
		t.Fatal(err)
	}
	initial := doMutation(
		t,
		application,
		"127.0.0.1:43142",
		"/api/v1/accesses/access-control/actions/apply",
		"unused",
		0,
		"preserve-initial-0001",
		initialBody,
	)
	if initial.Code != http.StatusOK {
		t.Fatalf("initial apply code=%d body=%s", initial.Code, initial.Body.Bytes())
	}
	accessID, _ := access.NewAccessID("access-control")
	before, _, err := runtime.AccessCatalog().ReadAccess(t.Context(), accessID)
	if err != nil {
		t.Fatal(err)
	}
	originalRef := before.AccountBindings[0].SecretRef

	edit := validApplyInput()
	edit.ExpectedRevision = 1
	edit.Access.Name = "Renamed without browser SecretRef"
	edit.AccountBindings[0].SecretRef = ""
	editBody, err := json.Marshal(edit)
	if err != nil {
		t.Fatal(err)
	}
	edited := doMutation(
		t,
		application,
		"127.0.0.1:43142",
		"/api/v1/accesses/access-control/actions/apply",
		"unused",
		1,
		"preserve-edit-0001",
		editBody,
	)
	if edited.Code != http.StatusOK {
		t.Fatalf("safe edit code=%d body=%s", edited.Code, edited.Body.Bytes())
	}
	after, _, err := runtime.AccessCatalog().ReadAccess(t.Context(), accessID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Binding.Revision != 2 ||
		after.Binding.Name != "Renamed without browser SecretRef" ||
		after.AccountBindings[0].SecretRef != originalRef {
		t.Fatalf("safe edit did not preserve SecretRef: %+v", after)
	}

	unsafeAppend := edit
	unsafeAppend.ExpectedRevision = 2
	unsafeAppend.AccountBindings = append(
		unsafeAppend.AccountBindings,
		unsafeAppend.AccountBindings[0],
	)
	unsafeAppend.AccountBindings[1].ID = "browser-selected-account"
	unsafeAppend.AccountBindings[1].SecretRef = "secret://browser/chosen-ref"
	unsafeBody, err := json.Marshal(unsafeAppend)
	if err != nil {
		t.Fatal(err)
	}
	rejected := doMutation(
		t,
		application,
		"127.0.0.1:43142",
		"/api/v1/accesses/access-control/actions/apply",
		"unused",
		2,
		"preserve-reject-0001",
		unsafeBody,
	)
	if rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsafe append code=%d body=%s", rejected.Code, rejected.Body.Bytes())
	}
	unchanged, _, err := runtime.AccessCatalog().ReadAccess(t.Context(), accessID)
	if err != nil || unchanged.Binding.Revision != 2 ||
		unchanged.AccountBindings[0].SecretRef != originalRef {
		t.Fatalf("unsafe append mutated Access: revision=%d err=%v", unchanged.Binding.Revision, err)
	}
}

func candidateResources(
	aggregate access.Aggregate,
	profileID access.EndpointProfileID,
	credentialID access.AccountBindingID,
) (
	access.EndpointProfile,
	access.ProviderTarget,
	access.ProviderAccountBinding,
	bool,
) {
	var profile access.EndpointProfile
	for _, candidate := range aggregate.Profiles {
		if candidate.ID == profileID {
			profile = candidate
			break
		}
	}
	var target access.ProviderTarget
	for _, candidate := range aggregate.ProviderTargets {
		if candidate.ProfileID == profileID {
			target = candidate
			break
		}
	}
	var account access.ProviderAccountBinding
	for _, candidate := range aggregate.AccountBindings {
		if candidate.ID == credentialID {
			account = candidate
			break
		}
	}
	return profile, target, account,
		profile.ID == profileID &&
			target.ProfileID == profileID &&
			account.ID == credentialID
}
