//go:build darwin

package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"errors"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/clienttarget"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func modelSyntheticKeyEvidenceMatches(meta rawevidence.EnvelopeMetadata, headers []rawevidence.HeaderField, salt []byte) bool {
	if meta.Layer != rawevidence.LayerClientIngress || meta.Method != "POST" || meta.Scheme != "https" || meta.Authority != "chatgpt.com" && meta.Authority != "chatgpt.com:443" || meta.Path != "/v1/responses" || meta.RawQuery != "" || len(salt) != rawevidence.RedactionSaltBytes {
		return false
	}
	mac := hmac.New(sha256.New, salt)
	_, _ = mac.Write([]byte("Bearer synthetic-original-model-api-key"))
	want := hex.EncodeToString(mac.Sum(nil))
	found := false
	for _, field := range headers {
		if strings.EqualFold(field.Name, "ChatGPT-Account-ID") {
			return false
		}
		if strings.EqualFold(field.Name, "Authorization") {
			if found || len(field.Values) != 0 || len(field.Redacted) != 1 || field.Redacted[0].Bytes != len("Bearer synthetic-original-model-api-key") || !hmac.Equal([]byte(field.Redacted[0].Digest), []byte(want)) {
				return false
			}
			found = true
		}
	}
	return found
}

func TestModelSyntheticAPIKeyIngressEvidenceIsClosed(t *testing.T) {
	salt := make([]byte, 32)
	mac := hmac.New(sha256.New, salt)
	_, _ = mac.Write([]byte("Bearer synthetic-original-model-api-key"))
	good := rawevidence.HeaderField{Name: "Authorization", Redacted: []rawevidence.RedactedValue{{Digest: hex.EncodeToString(mac.Sum(nil)), Bytes: len("Bearer synthetic-original-model-api-key")}}}
	for _, test := range []struct {
		name string
		edit func(*rawevidence.EnvelopeMetadata, []rawevidence.HeaderField) []rawevidence.HeaderField
	}{
		{"exact", func(_ *rawevidence.EnvelopeMetadata, h []rawevidence.HeaderField) []rawevidence.HeaderField { return h }},
		{"native_path", func(m *rawevidence.EnvelopeMetadata, h []rawevidence.HeaderField) []rawevidence.HeaderField {
			m.Path = "/backend-api/codex/responses"
			return h
		}},
		{"foreign_host", func(m *rawevidence.EnvelopeMetadata, h []rawevidence.HeaderField) []rawevidence.HeaderField {
			m.Authority = "api.openai.com"
			return h
		}},
		{"query", func(m *rawevidence.EnvelopeMetadata, h []rawevidence.HeaderField) []rawevidence.HeaderField {
			m.RawQuery = "extra=x"
			return h
		}},
		{"wrong_method", func(m *rawevidence.EnvelopeMetadata, h []rawevidence.HeaderField) []rawevidence.HeaderField {
			m.Method = "GET"
			return h
		}},
		{"wrong_scheme", func(m *rawevidence.EnvelopeMetadata, h []rawevidence.HeaderField) []rawevidence.HeaderField {
			m.Scheme = "http"
			return h
		}},
		{"missing", func(_ *rawevidence.EnvelopeMetadata, _ []rawevidence.HeaderField) []rawevidence.HeaderField {
			return nil
		}},
		{"wrong_key", func(_ *rawevidence.EnvelopeMetadata, h []rawevidence.HeaderField) []rawevidence.HeaderField {
			h[0].Redacted[0].Digest = strings.Repeat("0", 64)
			return h
		}},
		{"empty_key", func(_ *rawevidence.EnvelopeMetadata, h []rawevidence.HeaderField) []rawevidence.HeaderField {
			h[0].Redacted[0].Bytes = 0
			return h
		}},
		{"duplicate_field", func(_ *rawevidence.EnvelopeMetadata, h []rawevidence.HeaderField) []rawevidence.HeaderField {
			return append(h, h[0])
		}},
		{"duplicate_value", func(_ *rawevidence.EnvelopeMetadata, h []rawevidence.HeaderField) []rawevidence.HeaderField {
			h[0].Redacted = append(h[0].Redacted, h[0].Redacted[0])
			return h
		}},
		{"header_override", func(_ *rawevidence.EnvelopeMetadata, h []rawevidence.HeaderField) []rawevidence.HeaderField {
			h[0].Values = []string{"unexpected"}
			return h
		}},
		{"account_header", func(_ *rawevidence.EnvelopeMetadata, h []rawevidence.HeaderField) []rawevidence.HeaderField {
			return append(h, rawevidence.HeaderField{Name: "ChatGPT-Account-ID"})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			meta := rawevidence.EnvelopeMetadata{Layer: rawevidence.LayerClientIngress, Method: "POST", Scheme: "https", Authority: "chatgpt.com", Path: "/v1/responses"}
			header := good
			header.Redacted = append([]rawevidence.RedactedValue(nil), good.Redacted...)
			headers := test.edit(&meta, []rawevidence.HeaderField{header})
			if modelSyntheticKeyEvidenceMatches(meta, headers, salt) != (test.name == "exact") {
				t.Fatal("closed synthetic ingress predicate mismatch")
			}
		})
	}
}

func TestModelSSEEachPinnedGenericAPIKeyHeldStream(t *testing.T) {
	version := os.Getenv("VIBERMATE_NATIVE_VERSION")
	if version == "" {
		t.Skip("requires exact pin; not acceptance evidence")
	}
	parent, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary, node := modelIdleVerifiedClient(t, parent, t.TempDir(), version)
	trace, client := &modelIdleTrace{}, &modelIdleClientObservation{}
	options := &nativeModelProbeOptions{Profile: nativeModelProbeAPIKey, Handler: modelIdleProvider(trace, "managed_long_arguments"), ThreadParameters: func(dir string) map[string]any {
		params := modelPinnedThreadParams(dir, version, 20000)
		provider := params["config"].(map[string]any)["model_providers.synthetic_idle"].(map[string]any)
		provider["base_url"] = "https://chatgpt.com/v1"
		provider["env_key"] = "VIBERMATE_SYNTHETIC_MODEL_API_KEY"
		provider["requires_openai_auth"] = false
		return params
	}, ClientObservation: client, ExpectedModelRequests: 2,
		ObserveTerminal: func(ctx context.Context, actual nativeModelProbeObservation) {
			client.mu.Lock()
			t.Logf("generic client errorClass=%s missingEnvFlag=%t authenticationFlag=%t pathUnsupported=%t targetUnproven=%t operationNotCatalogued=%t helperFailure=%s", client.errorClass, client.missingEnv, client.authenticationFailure, client.pathUnsupported, client.targetUnproven, client.operationNotCatalogued, actual.Failure)
			client.mu.Unlock()
			target := actual.Assignment.ClientTarget
			t.Logf("actual synthetic launch target actual=%s canonical=%s basePath=%s containsGenericPath=%t", target.ActualOrigin().String(), target.CanonicalOrigin().String(), target.ActualOrigin().BasePath(), target.ContainsPath("/v1/responses"))
			if target.ActualOrigin().String() != "https://chatgpt.com/v1" || target.CanonicalOrigin().String() != "https://api.openai.com" || !target.ContainsPath("/v1/responses") {
				t.Fatal("actual generic prelaunch target mismatch")
			}
			connections, err := actual.Runtime.ConnectionEvents().List(ctx, connectionevent.PageRequest{IngressID: "capture-run/" + actual.Capture.ID, LatestPerConnection: true, Limit: 20})
			if err != nil {
				t.Fatal("generic capture connection evidence unavailable")
			}
			for _, event := range connections.Items {
				t.Logf("generic actual captured connection phase=%s errorClass=%s", event.Phase, event.ErrorClass)
			}
			evidence := collectModelLifecycleEvidence(t, ctx, actual)
			reader, err := openExchangeAuditReader(ctx, actual.DataDirectory)
			if err != nil {
				t.Fatal("synthetic raw commitment reader unavailable")
			}
			defer reader.Close()
			var salt []byte
			if err = reader.connection.QueryRowContext(ctx, `SELECT salt FROM runtime_raw_evidence_redaction WHERE singleton=1`).Scan(&salt); err != nil {
				t.Fatal("synthetic raw commitment salt unavailable")
			}
			defer clear(salt)
			ingresses := 0
			for _, record := range evidence.records {
				if record.Status != activity.StatusSucceeded {
					t.Error("generic client lacked successful Activity")
				}
				metadata, err := actual.Runtime.RawEvidence().ListExchange(ctx, record.SubjectID)
				if err != nil {
					t.Fatal(err)
				}
				for _, meta := range metadata {
					if meta.Layer != rawevidence.LayerClientIngress {
						continue
					}
					raw, err := actual.Runtime.RawEvidence().Reveal(ctx, rawevidence.RevealRequest{EnvelopeID: meta.EnvelopeID, ActorID: "synthetic-apikey-proof"})
					if err != nil {
						t.Fatal("synthetic ingress evidence unavailable")
					}
					if meta.ExchangeID != record.SubjectID || meta.ScopeID != actual.Capture.ID || !modelSyntheticKeyEvidenceMatches(meta, raw.Payload.Headers, salt) {
						t.Fatal("actual generic ingress operation/auth/account-header mismatch")
					}
					ingresses++
				}
			}
			trace.mu.Lock()
			requests, acks, failure := trace.requests, trace.acks, trace.failure
			trace.mu.Unlock()
			client.mu.Lock()
			plans, idle := client.plans, client.idle
			client.mu.Unlock()
			t.Logf("actual generic key MODEL version=%s ingressExact=%d completed=%t requests=%d plan=%d ack=%d generatedDATA=%d args=%d idle=%t", version, ingresses, actual.Completed, requests, plans, acks, evidence.generatedData, evidence.argumentEvents, idle)
			if !actual.Completed || ingresses != 2 || requests != 2 || acks != 1 || plans != 1 || idle || failure != "" || evidence.generatedData != 2 || evidence.toolBatches != 1 || evidence.argumentEvents != 20 {
				t.Error("exact generic API-key held stream not proved")
			}
		}}
	runNativeFixtureSliceWithModelProbe(modelIdleManagedChecks{T: t, ctx: parent, version: version}, binary, options, node)
}

func TestModelGenericPrelaunchFactsRequireCanonicalFlow(t *testing.T) {
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	id, _ := protocolspec.NewCodecPairID("synthetic.prelaunch.responses")
	protocols, err := protocolspec.NewCatalog(operations.Definitions(), []protocolspec.CodecPairDefinition{{ID: id, Revision: 1, ClientDialect: protocolspec.DialectOpenAIResponses, ProviderDialect: protocolspec.DialectOpenAIResponses, ClientOperationIDs: operations.SemanticOperationIDs(protocolspec.DialectOpenAIResponses), RequiredCapabilities: []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages, protocolspec.ProviderCapabilityStreaming, protocolspec.ProviderCapabilityToolCalls}}})
	if err != nil {
		t.Fatal(err)
	}
	wires, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := environment.NewCompiler(nil, nil, protocols, wires)
	if err != nil {
		t.Fatal(err)
	}
	for _, withFlow := range []bool{false, true} {
		origin, _ := originidentity.ParseClientOrigin("https://chatgpt.com")
		if withFlow {
			origin, _ = originidentity.ParseClientOrigin("https://api.openai.com")
		}
		e := environment.Environment{ID: "synthetic.prelaunch", Name: "Synthetic prelaunch", Revision: 1, State: environment.StateActive, ContentRecording: environment.DefaultContentRecordingPolicy(), ClientEndpoints: []environment.ClientEndpoint{{ID: "synthetic.client", Revision: 1, ClientOrigin: origin, ProtocolPlans: []environment.ClientProtocolPlan{{ID: "synthetic.protocol", Revision: 1, ClientProtocol: environment.ClientProtocolOpenAIResponses, ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "synthetic.adapter", Revision: 1}, Destination: environment.DestinationPlan{Kind: environment.DestinationKindOriginal}, EgressProfile: egressprofile.Direct()}}}}}
		snapshot, err := compiler.Compile(e)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := clienttarget.NewProfile("codex-cli", clienttarget.FromEnvironment([]string{"OPENAI_BASE_URL=https://chatgpt.com/v1", "VIBERMATE_SYNTHETIC_MODEL_API_KEY=synthetic-original-model-api-key"}))
		if err != nil {
			t.Fatal(err)
		}
		_, target, err := captureassignment.LaunchAuthorityForClient(snapshot, profile)
		if !withFlow {
			if !errors.Is(err, captureassignment.ErrClientTargetNotConfigured) {
				t.Fatal("missing canonical flow granted generic target")
			}
			continue
		}
		if err != nil || target.ActualOrigin().String() != "https://chatgpt.com/v1" || target.CanonicalOrigin().String() != "https://api.openai.com" || !target.ContainsPath("/v1/responses") || target.ContainsPath("/backend-api/codex/responses") {
			t.Fatal("generic target was not frozen from explicit prelaunch facts")
		}
	}
	late, _ := clienttarget.NewProfile("codex-cli", clienttarget.FromEnvironment([]string{"VIBERMATE_SYNTHETIC_MODEL_API_KEY=synthetic-original-model-api-key"}))
	target, ok, err := late.Resolve(nil, nil)
	if err != nil || !ok || target.ContainsPath("/v1/responses") {
		t.Fatal("postlaunch provider setting implicitly widened native target")
	}
}

func TestModelSSEEachPinMissingAPIKeyFailsClosed(t *testing.T) {
	version := os.Getenv("VIBERMATE_NATIVE_VERSION")
	if version == "" {
		t.Skip("requires exact pin; no evidence")
	}
	for _, profile := range []nativeModelProbeProfile{nativeModelProbeAPIKeyMissing, nativeModelProbeAPIKeyEmpty} {
		name := "missing"
		if profile == nativeModelProbeAPIKeyEmpty {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			parent, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			binary, node := modelIdleVerifiedClient(t, parent, t.TempDir(), version)
			trace, client := &modelIdleTrace{}, &modelIdleClientObservation{}
			options := &nativeModelProbeOptions{Profile: profile, Handler: modelIdleProvider(trace, "managed_arguments"), ThreadParameters: func(dir string) map[string]any {
				params := modelPinnedThreadParams(dir, version, 20000)
				provider := params["config"].(map[string]any)["model_providers.synthetic_idle"].(map[string]any)
				provider["base_url"] = "https://chatgpt.com/v1"
				provider["env_key"] = "VIBERMATE_SYNTHETIC_MODEL_API_KEY"
				provider["requires_openai_auth"] = false
				return params
			}, ClientObservation: client, ExpectedModelRequests: 0,
				ObserveTerminal: func(ctx context.Context, actual nativeModelProbeObservation) {
					evidence := collectModelLifecycleEvidence(t, ctx, actual)
					client.mu.Lock()
					missing, plans, idle := client.missingEnv, client.plans, client.idle
					client.mu.Unlock()
					trace.mu.Lock()
					requests := trace.requests
					trace.mu.Unlock()
					t.Logf("missing-key closed profile version=%s empty=%t missingEnv=%t completed=%t modelRequests=%d Activity=%d", version, profile == nativeModelProbeAPIKeyEmpty, missing, actual.Completed, requests, len(evidence.records))
					if actual.Completed || !missing || plans != 0 || idle || requests != 0 || len(evidence.records) != 0 {
						t.Error("missing/empty env_key fell back to synthetic ChatGPT login")
					}
				}}
			runNativeFixtureSliceWithModelProbe(modelIdleManagedChecks{T: t, ctx: parent, version: version}, binary, options, node)
		})
	}
}
