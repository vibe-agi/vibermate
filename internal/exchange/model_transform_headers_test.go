package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

func TestNativeResponseTransformPreservesNormalizedModelHeaders(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, reported := range []string{"mapped-model", "restricted-model"} {
			t.Run(modelHeaderCaseName(streaming, reported), func(t *testing.T) {
				baseline := runNativeModelHeaderResponse(t, streaming, false, reported, `"provider-catalog"`, "")
				wantModel := reported
				if reported == "mapped-model" {
					wantModel = "codex-client-alias"
				}
				if baseline.Get("Openai-Model") != wantModel || !strings.HasPrefix(baseline.Get("X-Models-Etag"), `"vibermate-models-v1-`) {
					t.Fatalf("ordinary response did not expose native model metadata: %v", baseline)
				}
				transformed := runNativeModelHeaderResponse(t, streaming, false, reported, `"provider-catalog"`, `response.headers["x-transform-marker"] = "ran";`)
				if transformed.Get("Openai-Model") != wantModel {
					t.Errorf("transformed model = %q, want %q", transformed.Get("Openai-Model"), wantModel)
				}
				// The transform path must use the same once-scoped validator as the
				// ordinary path, rather than a raw tag or a hash of the scoped tag.
				if transformed.Get("X-Models-Etag") != baseline.Get("X-Models-Etag") {
					t.Errorf("transformed validator = %q, want %q", transformed.Get("X-Models-Etag"), baseline.Get("X-Models-Etag"))
				}
				if transformed.Get("X-Transform-Marker") != "ran" {
					t.Fatal("response script did not execute")
				}
			})
		}
	}
}

func TestManagedResponseTransformCannotInventCatalogValidator(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, test := range []struct{ name, upstream, script string }{
			{"delete", `"provider-catalog"`, `delete response.headers["x-models-etag"];`},
			{"change", `"provider-catalog"`, `response.headers["x-models-etag"] = "script-validator";`},
			{"invent", "", `response.headers["x-models-etag"] = "script-validator";`},
		} {
			t.Run(modelHeaderCaseName(streaming, test.name), func(t *testing.T) {
				headers := runNativeModelHeaderResponse(t, streaming, false, "mapped-model", test.upstream, test.script)
				if headers.Get("X-Models-Etag") != "" {
					t.Fatalf("non-provider catalog validator escaped: %v", headers)
				}
			})
		}
	}
}

func TestOriginalAccountResponseTransformKeepsUnscopedCatalogMetadata(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, test := range []struct{ name, script, want string }{
			{"ordinary", "", `"provider-catalog"`},
			{"unchanged", `response.headers["x-marker"] = "ran";`, `"provider-catalog"`},
			{"edited", `response.headers["x-models-etag"] = "script-validator";`, "script-validator"},
		} {
			t.Run(modelHeaderCaseName(streaming, test.name), func(t *testing.T) {
				headers := runNativeModelHeaderResponse(t, streaming, true, "mapped-model", `"provider-catalog"`, test.script)
				if headers.Get("X-Models-Etag") != test.want {
					t.Fatalf("original-account catalog metadata changed: %v", headers)
				}
			})
		}
	}
}

func modelHeaderCaseName(streaming bool, name string) string {
	if streaming {
		return "SSE/" + name
	}
	return "JSON/" + name
}

func runNativeModelHeaderResponse(t *testing.T, streaming, originalAccount bool, reported, tag, script string) http.Header {
	t.Helper()
	account := testAccount{id: "account.model-headers", revision: 3, epoch: 7}
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
		destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com",
		backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModeMap, mappedModel: "mapped-model",
		originalAccount: originalAccount, accounts: []testAccount{account}, preferred: account.id,
		transform: messagetransform.Policy{ResponseJavaScript: script},
	})
	providerBody := completeResponsesProviderResponse(reported)
	response := jsonResponse(http.StatusOK, providerBody)
	if streaming {
		data, err := json.Marshal(map[string]any{"type": "response.completed", "response": json.RawMessage(providerBody)})
		if err != nil {
			t.Fatal(err)
		}
		response = streamResponse(http.StatusOK, strings.NewReader("event: response.completed\ndata: "+string(data)+"\n\n"))
	}
	response.Header.Set("Openai-Model", reported)
	response.Header.Set("X-Request-Id", "trace-original")
	if tag != "" {
		response.Header.Set("X-Models-Etag", tag)
	}
	provider := &providerDouble{results: []providerResult{{response: response}}}
	content := &contentObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(t, newAccountAuthority(t, account), provider, approvedDecisions(), &attemptObserverDouble{}, content)
	raw := &rawObserverDouble{}
	pipeline.rawEvidence = raw
	defer shutdownPipeline(t, pipeline)
	body, err := json.Marshal(map[string]any{"model": "codex-client-alias", "stream": streaming, "input": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	downstream := &downstreamRecorder{}
	_, err = pipeline.Execute(context.Background(), mustClientRequestWithOptions(t, "response-model-headers", plan, body,
		WithOriginalHeaders(http.Header{"Authorization": {"Bearer synthetic-client"}})), downstream)
	if err != nil {
		t.Fatal(err)
	}
	envelopes := downstream.envelopesSnapshot()
	if len(envelopes) != 1 {
		t.Fatalf("expected one response envelope before any Hold: %d", len(envelopes))
	}
	if response.Header.Get("Openai-Model") != reported || response.Header.Get("X-Models-Etag") != tag {
		t.Fatal("upstream response header evidence was changed")
	}
	observation, ok := content.latest()
	if !ok || observation.Response == nil || observation.Response.ReportedModel != reported {
		t.Fatalf("upstream reported model evidence changed: %+v", observation)
	}
	if script != "" {
		found := false
		for _, item := range raw.snapshot() {
			if item.Layer == rawevidence.LayerTransformResponseInput {
				found = true
				if item.Headers.Get("Openai-Model") != reported || item.Headers.Get("X-Models-Etag") != tag || !bytes.Contains(item.Body, []byte(`"model":"`+reported+`"`)) {
					t.Fatalf("transform input audit lost upstream identity: headers=%v body=%s", item.Headers, item.Body)
				}
			}
		}
		if !found {
			t.Fatal("response transform input was not recorded")
		}
	}
	return envelopes[0].Headers()
}
