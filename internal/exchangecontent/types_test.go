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
