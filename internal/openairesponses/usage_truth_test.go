package openairesponses

import (
	"encoding/json"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func knownUsageValue(tokens int64) protocolcore.UsageValue {
	return protocolcore.UsageValue{
		Tokens: tokens,
		Known:  true,
		Source: "test",
	}
}

// Responses requires cached_tokens when usage is present. A Chat backend that
// reports only aggregate prompt tokens is represented conservatively as fully
// uncached; buildResponseWire separately records that approximation.
func TestResponsesUsageTreatsUnknownCacheReadAsUncached(t *testing.T) {
	t.Parallel()

	wire, err := encodeResponseUsage(protocolcore.Usage{
		InputUncached: knownUsageValue(100),
		Output:        knownUsageValue(20),
	})
	if err != nil {
		t.Fatalf("unknown usage details were rejected: %v", err)
	}
	if wire.InputTokens != 100 || wire.OutputTokens != 20 ||
		wire.TotalTokens != 120 {
		t.Fatalf("usage wire = %+v", wire)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	details, _ := decoded["input_tokens_details"].(map[string]any)
	if value, found := details["cached_tokens"].(float64); !found || value != 0 {
		t.Fatalf("unknown cache read was not normalized conservatively: %s", encoded)
	}
	if _, found := details["cache_write_tokens"]; found {
		t.Fatalf("unknown cache write was reported: %s", encoded)
	}
}

func TestResponsesUsageReportsTheUnknownCacheApproximation(t *testing.T) {
	t.Parallel()

	request := streamingRequestFixture(t)
	response := completeResponseFixture(t)
	response.Usage.CacheRead = protocolcore.UsageValue{}
	_, report, err := buildResponseWire(request, response)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, notice := range report.Notices() {
		if notice.Code == protocolcore.NoticeCacheReadUsageAssumedUncached &&
			notice.Path == "$.usage.input_tokens_details.cached_tokens" {
			found = true
		}
	}
	if !found {
		t.Fatalf("translation report = %#v", report.Notices())
	}
}

func TestResponsesUsageReportsKnownDetails(t *testing.T) {
	t.Parallel()

	wire, err := encodeResponseUsage(protocolcore.Usage{
		InputUncached: knownUsageValue(60),
		CacheRead:     knownUsageValue(40),
		Output:        knownUsageValue(20),
	})
	if err != nil {
		t.Fatal(err)
	}
	if wire.InputTokens != 100 || wire.TotalTokens != 120 {
		t.Fatalf("usage wire = %+v", wire)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	details, _ := decoded["input_tokens_details"].(map[string]any)
	if value, _ := details["cached_tokens"].(float64); value != 40 {
		t.Fatalf("known cache read was not reported: %s", encoded)
	}
}

// The two fields the Responses wire always carries stay required.
func TestResponsesUsageStillRequiresInputAndOutput(t *testing.T) {
	t.Parallel()

	if _, err := encodeResponseUsage(protocolcore.Usage{
		Output: knownUsageValue(20),
	}); err == nil {
		t.Fatal("an unknown input was encoded")
	}
	if _, err := encodeResponseUsage(protocolcore.Usage{
		InputUncached: knownUsageValue(100),
	}); err == nil {
		t.Fatal("an unknown output was encoded")
	}
}
