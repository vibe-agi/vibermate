package capturecontrol_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturegrant"
)

func TestCaptureRunPOSTResponsesMatchTheClosedOpenAPIWire(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	defer fixture.Close(t)
	create := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs",
		fixture.controlCredential,
		"",
		capturecontrol.CreateRequest{
			EnvironmentID:  testEnvironmentID,
			CWD:            fixture.workspace,
			Command:        []string{"claude"},
			ExecutablePath: fixture.executable,
		},
	)
	if create.Code != http.StatusCreated ||
		create.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.Bytes())
	}
	grantObject := exactJSONObject(t, create.Body.Bytes(), []string{
		"adapter",
		"catalogRevision",
		"executablePath",
		"launchRecipe",
		"managedCredentialAuthorities",
		"protectedAuthorities",
		"proxyAddress",
		"proxyDelivery",
		"proxyToken",
		"recognition",
		"rootPemPath",
		"run",
		"runCapability",
	})
	runKeys := []string{
		"catalogRevision",
		"clientAdapter",
		"clientAdapterState",
		"clientRecognition",
		"createdAt",
		"cwd",
		"executableLabel",
		"expiresAt",
		"id",
	}
	runObject := exactJSONObject(t, grantObject["run"], runKeys)
	runAdapterKeys := []string{
		"catalogRevision",
		"id",
		"installShape",
		"launchRecipe",
		"revision",
		"source",
		"version",
	}
	launchAdapterKeys := append(
		append([]string{}, runAdapterKeys...),
		"streamingFallbackPolicy",
	)
	exactJSONObject(t, grantObject["adapter"], launchAdapterKeys)
	exactJSONObject(t, runObject["clientAdapter"], runAdapterKeys)
	for _, forbidden := range [][]byte{
		[]byte(`"releaseSha256"`),
		[]byte(`"features"`),
		[]byte(`"proxyOrigin"`),
		[]byte(`"proxyCapability"`),
		[]byte(`"state"`),
		[]byte(`"observation"`),
	} {
		if bytes.Contains(create.Body.Bytes(), forbidden) {
			t.Fatalf("create response leaked forbidden key %s: %s", forbidden, create.Body.Bytes())
		}
	}
	var grant capturecontrol.LaunchGrant
	decodeRecorder(t, create, &grant)
	if grant.LaunchRecipe != "node_env_proxy" ||
		grant.Run.ClientAdapterState != "verified" ||
		grant.Run.ClientRecognition != "verified" ||
		grant.Run.CatalogRevision != grant.CatalogRevision ||
		grant.Adapter == nil ||
		grant.Adapter.Source !=
			capturecontrol.ClientAdapterSourcePrelaunchDigestCatalog ||
		grant.Adapter.StreamingFallbackPolicy != "core_owned" {
		t.Fatalf("verified launch grant = %+v", grant)
	}

	attach := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs/"+grant.Run.ID+"/actions/attach-process",
		"",
		grant.RunCapability,
		capturecontrol.AttachRequest{ProcessID: 744},
	)
	if attach.Code != http.StatusOK {
		t.Fatalf("attach status=%d body=%s", attach.Code, attach.Body.Bytes())
	}
	exactJSONObject(t, attach.Body.Bytes(), append(runKeys, "processId"))

	heartbeat := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs/"+grant.Run.ID+"/actions/heartbeat",
		"",
		grant.RunCapability,
		nil,
	)
	if heartbeat.Code != http.StatusOK {
		t.Fatalf("heartbeat status=%d body=%s", heartbeat.Code, heartbeat.Body.Bytes())
	}
	exactJSONObject(t, heartbeat.Body.Bytes(), append(runKeys, "processId"))
}

func TestGenericCaptureRunGrantUsesTheContractedRecipeAndShape(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	defer fixture.Close(t)
	if err := os.WriteFile(
		fixture.executable,
		[]byte("#!/bin/sh\nexit 9\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	response := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs",
		fixture.controlCredential,
		"",
		capturecontrol.CreateRequest{
			EnvironmentID:  testEnvironmentID,
			CWD:            fixture.workspace,
			Command:        []string{"claude"},
			ExecutablePath: fixture.executable,
		},
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.Bytes())
	}
	grantObject := exactJSONObject(t, response.Body.Bytes(), []string{
		"catalogRevision",
		"executablePath",
		"launchRecipe",
		"managedCredentialAuthorities",
		"protectedAuthorities",
		"proxyAddress",
		"proxyDelivery",
		"proxyToken",
		"recognition",
		"run",
		"runCapability",
	})
	exactJSONObject(t, grantObject["run"], []string{
		"catalogRevision",
		"clientAdapterState",
		"clientRecognition",
		"createdAt",
		"cwd",
		"executableLabel",
		"expiresAt",
		"id",
	})
	var grant capturecontrol.LaunchGrant
	decodeRecorder(t, response, &grant)
	if grant.LaunchRecipe != "generic_http_proxy" ||
		grant.Run.ClientAdapterState != "generic" ||
		grant.Adapter != nil || grant.Run.ClientAdapter != nil {
		t.Fatalf("generic launch grant = %+v", grant)
	}
}

func TestRecognizedCaptureRunGrantProjectsExactSignerEvidence(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t, func(options *capturegrant.Options) {
		options.Verifier = recognizingVerifier{}
		options.ClientRootApprovals = fixedApprover{allow: true}
	})
	defer fixture.Close(t)
	response := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs",
		fixture.controlCredential,
		"",
		capturecontrol.CreateRequest{
			EnvironmentID:  testEnvironmentID,
			CWD:            fixture.workspace,
			Command:        []string{"claude"},
			ExecutablePath: fixture.executable,
		},
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.Bytes())
	}
	grantObject := exactJSONObject(t, response.Body.Bytes(), []string{
		"catalogRevision",
		"executablePath",
		"launchRecipe",
		"managedCredentialAuthorities",
		"protectedAuthorities",
		"proxyAddress",
		"proxyDelivery",
		"proxyToken",
		"recognition",
		"rootPemPath",
		"run",
		"runCapability",
		"signer",
	})
	exactJSONObject(t, grantObject["run"], []string{
		"catalogRevision",
		"clientAdapterState",
		"clientRecognition",
		"createdAt",
		"cwd",
		"executableLabel",
		"expiresAt",
		"id",
	})
	exactJSONObject(t, grantObject["signer"], []string{
		"catalogRevision",
		"id",
		"installShape",
		"launchRecipe",
		"revision",
		"signedPath",
	})
	var grant capturecontrol.LaunchGrant
	decodeRecorder(t, response, &grant)
	if grant.Recognition != "recognized" ||
		grant.Run.ClientRecognition != "recognized" ||
		grant.Run.ClientAdapterState != "generic" ||
		grant.Signer == nil || grant.Adapter != nil ||
		grant.Run.ClientAdapter != nil {
		t.Fatalf("recognized launch grant = %+v", grant)
	}
}

func TestCaptureRunProblemMatchesTheClosedOpenAPIWire(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	defer fixture.Close(t)
	response := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs",
		fixture.controlCredential,
		"",
		map[string]any{"cwd": fixture.workspace},
	)
	if response.Code != http.StatusUnprocessableEntity ||
		response.Header().Get("Content-Type") != "application/problem+json" ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf(
			"problem status=%d headers=%v body=%s",
			response.Code,
			response.Header(),
			response.Body.Bytes(),
		)
	}
	problem := exactJSONObject(t, response.Body.Bytes(), []string{
		"code",
		"status",
		"title",
		"type",
	})
	if string(problem["code"]) != `"invalid_capture_run"` ||
		string(problem["status"]) != "422" ||
		string(problem["title"]) != `"Unprocessable Entity"` ||
		string(problem["type"]) !=
			`"urn:vibermate:error:invalid-capture-run"` {
		t.Fatalf("problem body=%s", response.Body.Bytes())
	}
}

func exactJSONObject(
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
