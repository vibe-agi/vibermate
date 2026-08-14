package exchangecontent

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestFullEvidenceRedactsSecretsAndHomePathsBeforeEncoding(t *testing.T) {
	t.Parallel()
	request, response := evidenceFixture(t)
	record, err := NewRecord(
		"exchange-1",
		frozenFixture(),
		environment.DefaultContentRecordingPolicy(),
		time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC),
		request,
		&response,
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := CanonicalJSON(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{
		[]byte("/Users/alice/project"),
		[]byte("sk-ant-secretfixture"),
		[]byte("Bearer secretfixture"),
	} {
		if bytes.Contains(encoded, forbidden) {
			t.Fatalf("encoded evidence retained %q: %s", forbidden, encoded)
		}
	}
	for _, wanted := range [][]byte{
		[]byte(`~/project`),
		[]byte(`"api_key":"[redacted]"`),
		[]byte(`Bearer [redacted]`),
	} {
		if !bytes.Contains(encoded, wanted) {
			t.Fatalf("encoded evidence omitted %q: %s", wanted, encoded)
		}
	}
	recovered, err := DecodeCanonicalJSON(encoded)
	if err != nil || recovered.ExchangeID != record.ExchangeID {
		t.Fatalf("DecodeCanonicalJSON() = %+v, %v", recovered, err)
	}
	encoded[0] = '['
	if recovered.ExchangeID != "exchange-1" {
		t.Fatal("decoded record aliases input bytes")
	}
}

func TestMetadataOnlyRetainsShapeUsageAndNoContent(t *testing.T) {
	t.Parallel()
	request, response := evidenceFixture(t)
	policy := environment.ContentRecordingPolicy{
		Mode: environment.ContentRecordingMetadataOnly, RetentionDays: 7,
	}
	record, err := NewRecord(
		"exchange-2", frozenFixture(), policy,
		time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC), request, &response,
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := CanonicalJSON(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"please inspect", "secretfixture", "/Users/alice"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("metadata evidence retained %q: %s", forbidden, encoded)
		}
	}
	if len(record.Request.Messages) != 1 ||
		record.Request.Messages[0].Blocks[0].Availability != AvailabilityOmitted ||
		record.Response == nil || !record.Response.Usage.Output.Known ||
		record.Response.Usage.Output.Tokens != 5 {
		t.Fatalf("metadata projection = %+v", record)
	}
}

func TestKnownZeroUsageRemainsDistinctFromUnknownOnTheWire(t *testing.T) {
	t.Parallel()
	request, response := evidenceFixture(t)
	response.Usage.CacheRead = protocolcore.UsageValue{
		Known: true, Tokens: 0, Source: "provider",
	}
	record, err := NewRecord(
		"exchange-known-zero", frozenFixture(),
		environment.DefaultContentRecordingPolicy(),
		time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC), request, &response,
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := CanonicalJSON(record)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"cacheRead":{"known":true,"tokens":0,"source":"provider"}`)) ||
		!bytes.Contains(encoded, []byte(`"reasoning":{"known":false}`)) {
		t.Fatalf("usage truth was not preserved: %s", encoded)
	}
	decoded, err := DecodeCanonicalJSON(encoded)
	if err != nil || decoded.Response == nil ||
		!decoded.Response.Usage.CacheRead.Known ||
		decoded.Response.Usage.CacheRead.Tokens != 0 {
		t.Fatalf("DecodeCanonicalJSON() = %+v, %v", decoded.Response, err)
	}

	missingZero := bytes.Replace(
		encoded,
		[]byte(`"cacheRead":{"known":true,"tokens":0,"source":"provider"}`),
		[]byte(`"cacheRead":{"known":true,"source":"provider"}`),
		1,
	)
	if _, err := DecodeCanonicalJSON(missingZero); err == nil {
		t.Fatal("known usage without an explicit zero token count was accepted")
	}
}

func TestProviderThinkingHistoryNeverEntersContentEvidence(t *testing.T) {
	t.Parallel()

	request, response := evidenceFixture(t)
	extension, err := protocolcore.NewProviderExtension(
		protocolcore.ProviderExtensionSourceAnthropicMessages,
		protocolcore.ProviderExtensionThinking,
		"$.messages[1].content[0]",
		[][]byte{[]byte(`{"type":"thinking","thinking":"private-provider-reasoning","signature":"opaque-signature"}`)},
	)
	if err != nil {
		t.Fatal(err)
	}
	block, err := protocolcore.NewProviderExtensionBlock(extension)
	if err != nil {
		t.Fatal(err)
	}
	request.Messages = append(request.Messages, protocolcore.Message{
		Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{block},
	})
	record, err := NewRecord(
		"exchange-provider-history",
		frozenFixture(),
		environment.DefaultContentRecordingPolicy(),
		time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC),
		request,
		&response,
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := CanonicalJSON(record)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("private-provider-reasoning")) ||
		bytes.Contains(encoded, []byte("opaque-signature")) {
		t.Fatalf("content evidence retained provider-private history: %s", encoded)
	}
	projected := record.Request.Messages[1].Blocks[0]
	if projected.Kind != string(protocolcore.BlockProviderExtension) ||
		projected.Availability != AvailabilityOmitted || projected.OriginalSize == 0 {
		t.Fatalf("provider extension projection = %+v", projected)
	}
}

func TestProviderAgentOutputPersistsProvenanceAndHashesOpaqueContent(t *testing.T) {
	t.Parallel()

	request, _ := evidenceFixture(t)
	callKey, err := protocolcore.NewCallKey("openai-responses-multi-agent-call", "call-agent-1")
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := protocolcore.NewJSONObject([]byte(`{"task_name":"reviewer"}`), protocolcore.MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	callBlock, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{
		Kind: protocolcore.ToolKindFunction, Key: callKey,
		Namespace: "multi_agent", Name: "spawn_agent", Arguments: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	callBlock.Agent = &protocolcore.AgentMessageContext{AgentName: "root"}
	extension, err := protocolcore.NewProviderExtension(
		protocolcore.ProviderExtensionSourceOpenAIResponses,
		protocolcore.ProviderExtensionAgentMessageEncryptedContent,
		"$.output[0].content[0]",
		[][]byte{[]byte(`{"type":"encrypted_content","encrypted_content":"opaque-secret"}`)},
	)
	if err != nil {
		t.Fatal(err)
	}
	opaqueBlock, err := protocolcore.NewProviderExtensionBlock(extension)
	if err != nil {
		t.Fatal(err)
	}
	opaqueBlock.Agent = &protocolcore.AgentMessageContext{
		AgentName: "root", Author: "root", Recipient: "reviewer",
	}
	response := protocolcore.Response{
		ID: "response-agent-1", RequestedModel: request.RequestedModel,
		EffectiveModel: request.EffectiveModel, ReportedModel: request.EffectiveModel,
		Blocks:     []protocolcore.ContentBlock{opaqueBlock, callBlock},
		StopReason: protocolcore.StopReasonToolUse,
	}
	record, err := NewRecord(
		"exchange-agent-output", frozenFixture(), environment.DefaultContentRecordingPolicy(),
		time.Date(2026, time.August, 9, 1, 2, 3, 0, time.UTC),
		request, &response,
	)
	if err != nil {
		t.Fatal(err)
	}
	if record.Response == nil || len(record.Response.Blocks) != 2 {
		t.Fatalf("response projection = %#v", record.Response)
	}
	opaque := record.Response.Blocks[0]
	if opaque.Agent == nil || opaque.Agent.Recipient != "reviewer" ||
		opaque.Text != "" || !strings.HasPrefix(opaque.Fingerprint, "sha256:") {
		t.Fatalf("opaque agent evidence = %#v", opaque)
	}
	call := record.Response.Blocks[1]
	if call.Agent == nil || call.Agent.AgentName != "root" ||
		call.ToolNamespace != "multi_agent" || call.ToolName != "spawn_agent" {
		t.Fatalf("agent call evidence = %#v", call)
	}
	encoded, err := CanonicalJSON(record)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("opaque-secret")) {
		t.Fatal("opaque agent content leaked into stored evidence")
	}
}

func TestProviderThinkingResponseRecordsReadableTextWithoutOpaqueState(t *testing.T) {
	t.Parallel()

	request, response := evidenceFixture(t)
	thinking, err := protocolcore.NewProviderExtension(
		protocolcore.ProviderExtensionSourceAnthropicMessages,
		protocolcore.ProviderExtensionThinking,
		"$.content[0]",
		[][]byte{
			[]byte(`{"type":"thinking","thinking":"","signature":"must-not-persist"}`),
			[]byte(`{"type":"thinking_delta","thinking":"inspect the repository"}`),
			[]byte(`{"type":"signature_delta","signature":"also-must-not-persist"}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	redacted, err := protocolcore.NewProviderExtension(
		protocolcore.ProviderExtensionSourceAnthropicMessages,
		protocolcore.ProviderExtensionRedactedThinking,
		"$.content[1]",
		[][]byte{[]byte(`{"type":"redacted_thinking","data":"opaque-redacted-state"}`)},
	)
	if err != nil {
		t.Fatal(err)
	}
	response.ProviderExtensions = []protocolcore.ProviderExtension{thinking, redacted}
	record, err := NewRecord(
		"exchange-provider-response",
		frozenFixture(),
		environment.DefaultContentRecordingPolicy(),
		time.Date(2026, 8, 10, 1, 2, 3, 0, time.UTC),
		request,
		&response,
	)
	if err != nil {
		t.Fatal(err)
	}
	if record.Response == nil || len(record.Response.Blocks) != 4 {
		t.Fatalf("response projection = %+v", record.Response)
	}
	readable := record.Response.Blocks[1]
	if readable.Kind != BlockKindReasoning || readable.Availability != AvailabilityRecorded ||
		readable.Text != "inspect the repository" {
		t.Fatalf("thinking projection = %+v", readable)
	}
	signatureView := record.Response.Blocks[2]
	if signatureView.Kind != string(protocolcore.BlockProviderExtension) ||
		signatureView.ProviderKind != string(protocolcore.ProviderExtensionThinking) ||
		signatureView.ProviderSource != string(protocolcore.ProviderExtensionSourceAnthropicMessages) ||
		!strings.HasPrefix(signatureView.Fingerprint, "sha256:") {
		t.Fatalf("thinking signature projection = %+v", signatureView)
	}
	redactedView := record.Response.Blocks[3]
	if redactedView.Kind != string(protocolcore.BlockProviderExtension) ||
		redactedView.Availability != AvailabilityOmitted || redactedView.OriginalSize == 0 ||
		redactedView.ProviderKind != string(protocolcore.ProviderExtensionRedactedThinking) ||
		!strings.HasPrefix(redactedView.Fingerprint, "sha256:") {
		t.Fatalf("redacted thinking projection = %+v", redactedView)
	}
	encoded, err := CanonicalJSON(record)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte("inspect the repository")) {
		t.Fatalf("readable thinking was not recorded: %s", encoded)
	}
	for _, forbidden := range [][]byte{
		[]byte("must-not-persist"),
		[]byte("also-must-not-persist"),
		[]byte("opaque-redacted-state"),
	} {
		if bytes.Contains(encoded, forbidden) {
			t.Fatalf("opaque provider state %q entered evidence: %s", forbidden, encoded)
		}
	}
}

func TestDisabledRecordingCannotCreateAHiddenContentRecord(t *testing.T) {
	t.Parallel()
	request, response := evidenceFixture(t)
	_, err := NewRecord(
		"exchange-3", frozenFixture(),
		environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff},
		time.Now(), request, &response,
	)
	if err == nil {
		t.Fatal("disabled recording created content evidence")
	}
}

func evidenceFixture(t *testing.T) (protocolcore.Request, protocolcore.Response) {
	t.Helper()
	arguments, err := protocolcore.NewJSONObject(
		[]byte(`{"path":"/Users/alice/project","api_key":"sk-ant-secretfixture"}`),
		protocolcore.MaxToolJSONBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	callKey, err := protocolcore.NewCallKey("anthropic", "tool-1")
	if err != nil {
		t.Fatal(err)
	}
	user, err := protocolcore.NewTextBlock(
		"please inspect /Users/alice/project with Bearer secretfixture",
	)
	if err != nil {
		t.Fatal(err)
	}
	toolCall, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{
		Key: callKey, Name: "read_file", Arguments: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := protocolcore.Request{
		RequestedModel: "claude", EffectiveModel: "claude",
		MaxOutputTokens: 128, Stream: true,
		Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{user}}},
	}
	response := protocolcore.Response{
		ID: "response-1", RequestedModel: "claude", EffectiveModel: "claude",
		ReportedModel: "claude", Blocks: []protocolcore.ContentBlock{toolCall},
		StopReason: protocolcore.StopReasonToolUse,
		Usage: protocolcore.Usage{
			InputUncached: protocolcore.UsageValue{Known: true, Tokens: 12, Source: "provider"},
			Output:        protocolcore.UsageValue{Known: true, Tokens: 5, Source: "provider"},
		},
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
	return request, response
}

func frozenFixture() FrozenRef {
	return FrozenRef{
		EnvironmentID: "work", EnvironmentRevision: 2,
		EnvironmentDigest: strings.Repeat("a", 64),
		ClientEndpointID:  "endpoint.claude", ClientEndpointRevision: 2,
		ProtocolPlanID: "plan.claude", ProtocolPlanRevision: 2,
		RouteID: "route.claude", RouteRevision: 2,
	}
}

func TestCanonicalDecoderRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	request, response := evidenceFixture(t)
	record, err := NewRecord(
		"exchange-4", frozenFixture(), environment.DefaultContentRecordingPolicy(),
		time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC), request, &response,
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := CanonicalJSON(record)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if json.Unmarshal(encoded, &object) != nil {
		t.Fatal("fixture is not JSON")
	}
	object["secret"] = "must-not-be-accepted"
	tampered, _ := json.Marshal(object)
	if _, err := DecodeCanonicalJSON(tampered); err == nil {
		t.Fatal("unknown field was accepted")
	}
}
