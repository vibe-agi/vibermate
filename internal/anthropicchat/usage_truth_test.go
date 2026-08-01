package anthropicchat

import "testing"

// OpenAI Chat's usage object has no cache-write concept at all, so reporting a
// known zero asserts a fact the wire never stated and silently mis-prices any
// provider that bills cache writes. Design 08 gates on unknown usage never
// being displayed as zero.
func TestChatCacheWriteIsUnknownRatherThanAKnownZero(t *testing.T) {
	t.Parallel()

	usage, err := decodeUsage(&openAIUsageWire{
		PromptTokens:     100,
		CompletionTokens: 20,
		TotalTokens:      120,
		PromptTokensDetails: &openAIPromptUsageWire{
			CachedTokens: 40,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if usage.CacheWrite.Known || usage.CacheWrite.Tokens != 0 {
		t.Fatalf("cache write usage = %+v, want unknown", usage.CacheWrite)
	}
	if !usage.CacheRead.Known || usage.CacheRead.Tokens != 40 {
		t.Fatalf("cache read usage = %+v", usage.CacheRead)
	}
	if !usage.InputUncached.Known || usage.InputUncached.Tokens != 60 {
		t.Fatalf("uncached input usage = %+v", usage.InputUncached)
	}
}

// An absent prompt_tokens_details object means the provider did not report a
// cached split, not that the split was zero.
func TestChatCacheReadIsUnknownWhenDetailsAreAbsent(t *testing.T) {
	t.Parallel()

	usage, err := decodeUsage(&openAIUsageWire{
		PromptTokens:     100,
		CompletionTokens: 20,
		TotalTokens:      120,
	})
	if err != nil {
		t.Fatal(err)
	}
	if usage.CacheRead.Known || usage.CacheRead.Tokens != 0 {
		t.Fatalf("cache read usage = %+v, want unknown", usage.CacheRead)
	}
	if usage.CacheWrite.Known {
		t.Fatalf("cache write usage = %+v, want unknown", usage.CacheWrite)
	}
	if !usage.InputUncached.Known || usage.InputUncached.Tokens != 100 {
		t.Fatalf("uncached input usage = %+v", usage.InputUncached)
	}
	if !usage.Output.Known || usage.Output.Tokens != 20 {
		t.Fatalf("output usage = %+v", usage.Output)
	}
}

// A reported zero stays a reported zero: the provider did state it.
func TestChatReportedZeroCachedTokensStaysKnown(t *testing.T) {
	t.Parallel()

	usage, err := decodeUsage(&openAIUsageWire{
		PromptTokens:        100,
		CompletionTokens:    20,
		TotalTokens:         120,
		PromptTokensDetails: &openAIPromptUsageWire{CachedTokens: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !usage.CacheRead.Known || usage.CacheRead.Tokens != 0 {
		t.Fatalf("cache read usage = %+v, want a known zero", usage.CacheRead)
	}
}
