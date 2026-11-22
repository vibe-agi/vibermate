package exchangecontent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// storedKinds is the closed set of stored block kinds the app's contract
// accepts (control_models.dart). Neutral blocks must project into it.
var storedKinds = map[string]bool{
	"text": true, "refusal": true, "tool_call": true, "tool_result": true,
	"reasoning": true, "provider_extension": true, "deferred": true,
}

func fingerprintOf(value string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value)))
}

// neutralFixture is a translated Claude turn: an image and a tool result with
// a PDF and a tool reference from the client, positioned reasoning with a
// Responses seal in the history, and reasoning in the response.
func neutralFixture(t *testing.T) (protocolcore.Request, protocolcore.Response) {
	t.Helper()
	request, response := evidenceFixture(t)
	image, err := protocolcore.NewMediaBlock(protocolcore.Media{Kind: protocolcore.MediaImage, MediaType: "image/png", Data: "iVBORw0KGgo-private-image"})
	if err != nil {
		t.Fatal(err)
	}
	history, err := protocolcore.NewReasoningBlock(protocolcore.Reasoning{
		Text: "history-reasoning-summary",
		Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_1", Model: "gpt-test", Binding: "binding", Opaque: "ENC-private-history"},
	})
	if err != nil {
		t.Fatal(err)
	}
	call, err := protocolcore.NewCallKey("anthropic-messages", "toolu_pdf")
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := protocolcore.NewJSONObject([]byte(`{"file_path":"x.pdf"}`), protocolcore.MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	result, err := protocolcore.NewToolResultBlock(protocolcore.ToolResult{
		Key: call, Content: "PDF text",
		Parts: &protocolcore.ToolResultParts{Items: []protocolcore.ToolResultPart{
			{Kind: protocolcore.ToolResultPartText, Text: "PDF text"},
			{Kind: protocolcore.ToolResultPartMedia, Media: &protocolcore.Media{Kind: protocolcore.MediaDocument, MediaType: "application/pdf", Data: "JVBERi0-private-pdf"}},
			{Kind: protocolcore.ToolResultPartToolReference, ToolName: "CronCreate"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request.Messages = append(request.Messages,
		protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "look"}, image}},
		protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{history, {Kind: protocolcore.BlockToolCall, ToolCall: protocolcore.ToolCall{Key: call, Name: "Read", Arguments: arguments}}}},
		protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{result}},
	)
	answer, err := protocolcore.NewReasoningBlock(protocolcore.Reasoning{
		Text: "response-raw-reasoning", TextSource: protocolcore.ReasoningTextContent,
		Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_2", Model: "gpt-test", Opaque: "ENC-private-response"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response.Blocks = append([]protocolcore.ContentBlock{answer}, response.Blocks...)
	return request, response
}

func TestNeutralBlocksProjectOntoStoredEvidence(t *testing.T) {
	t.Parallel()
	request, response := neutralFixture(t)
	record, err := NewRecord("exchange-neutral", frozenFixture(), environment.DefaultContentRecordingPolicy(),
		time.Date(2026, 11, 22, 1, 2, 3, 0, time.UTC), request, &response)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := CanonicalJSON(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-image", "private-pdf", "ENC-private-history", "ENC-private-response"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("stored evidence kept %s: %s", private, encoded)
		}
	}
	for _, readable := range []string{"history-reasoning-summary", "response-raw-reasoning", "PDF text"} {
		if !bytes.Contains(encoded, []byte(readable)) {
			t.Fatalf("stored evidence lost %s", readable)
		}
	}
	messages := record.Request.Messages
	image := messages[len(messages)-3].Blocks[1]
	if image.Kind != "provider_extension" || image.Availability != AvailabilityOmitted ||
		image.ProviderSource != neutralProviderSource || image.ProviderKind != "image" ||
		image.Fingerprint != fingerprintOf("iVBORw0KGgo-private-image") || image.OriginalSize != len("iVBORw0KGgo-private-image") {
		t.Fatalf("image projection = %+v", image)
	}
	history := messages[len(messages)-2].Blocks
	if len(history) != 3 ||
		!reflect.DeepEqual(history[0], Block{Kind: BlockKindReasoning, Availability: AvailabilityRecorded, Text: "history-reasoning-summary", OriginalSize: len("history-reasoning-summary"), ProviderSource: "openai-responses", ProviderKind: "reasoning_summary"}) ||
		!reflect.DeepEqual(history[1], Block{Kind: "provider_extension", Availability: AvailabilityOmitted, OriginalSize: len("ENC-private-history"), ProviderSource: "openai-responses", ProviderKind: "reasoning_encrypted_content", Fingerprint: fingerprintOf("ENC-private-history")}) ||
		history[2].Kind != "tool_call" {
		t.Fatalf("history reasoning projection = %+v", history)
	}
	result := messages[len(messages)-1].Blocks
	if len(result) != 2 || result[0].Kind != "tool_result" || result[0].Text != "PDF text" || result[0].CallID != "toolu_pdf" ||
		!reflect.DeepEqual(result[1], Block{Kind: "provider_extension", Availability: AvailabilityOmitted, OriginalSize: len("JVBERi0-private-pdf"), ProviderSource: neutralProviderSource, ProviderKind: "document", Fingerprint: fingerprintOf("JVBERi0-private-pdf")}) {
		t.Fatalf("tool-result projection = %+v", result)
	}
	answer := record.Response.Blocks
	if answer[0].Kind != BlockKindReasoning || answer[0].ProviderKind != "reasoning_content" || answer[0].Text != "response-raw-reasoning" ||
		answer[1].Kind != "provider_extension" || answer[1].Fingerprint != fingerprintOf("ENC-private-response") {
		t.Fatalf("response reasoning projection = %+v", answer[:2])
	}
	assertStoredKinds(t, record)
	decoded, err := DecodeCanonicalJSON(encoded)
	if err != nil {
		t.Fatalf("stored neutral evidence does not decode: %v", err)
	}
	if !reflect.DeepEqual(decoded.Request.Messages, record.Request.Messages) {
		t.Fatal("canonical round trip changed the projection")
	}
}

func TestNeutralBlocksInMetadataOnlyModeKeepOnlyFingerprints(t *testing.T) {
	t.Parallel()
	request, response := neutralFixture(t)
	policy := environment.DefaultContentRecordingPolicy()
	policy.Mode = environment.ContentRecordingMetadataOnly
	record, err := NewRecord("exchange-neutral-metadata", frozenFixture(), policy,
		time.Date(2026, 11, 22, 1, 2, 3, 0, time.UTC), request, &response)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := CanonicalJSON(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"history-reasoning-summary", "response-raw-reasoning", "private-pdf", "ENC-private"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("metadata-only evidence kept %s", private)
		}
	}
	history := record.Request.Messages[len(record.Request.Messages)-2].Blocks
	if len(history) != 2 || history[0].Kind != "provider_extension" || history[0].ProviderKind != "reasoning_encrypted_content" {
		t.Fatalf("metadata-only reasoning projection = %+v", history)
	}
	assertStoredKinds(t, record)
}

// Reasoning without a seal payload still leaves evidence that it existed,
// and a seal of another dialect keeps that dialect's names.
func TestReasoningWithoutPayloadAndAnthropicSeals(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		reasoning protocolcore.Reasoning
		full      bool
		want      []Block
	}{
		"no seal, full": {
			reasoning: protocolcore.Reasoning{Text: "visible"}, full: true,
			want: []Block{{Kind: BlockKindReasoning, Availability: AvailabilityRecorded, Text: "visible", OriginalSize: 7, ProviderSource: neutralProviderSource, ProviderKind: "reasoning_summary"}},
		},
		"no seal, metadata": {
			reasoning: protocolcore.Reasoning{Text: "visible"},
			want:      []Block{{Kind: "provider_extension", Availability: AvailabilityOmitted, OriginalSize: 7, ProviderSource: neutralProviderSource, ProviderKind: "reasoning_summary", Fingerprint: fingerprintOf("visible")}},
		},
		"omitted display, empty seal payload": {
			reasoning: protocolcore.Reasoning{Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealResponsesEncrypted, ItemID: "rs"}}, full: true,
			want: []Block{{Kind: "provider_extension", Availability: AvailabilityOmitted, ProviderSource: "openai-responses", ProviderKind: "reasoning_summary", Fingerprint: fingerprintOf("")}},
		},
		"anthropic signature": {
			reasoning: protocolcore.Reasoning{Text: "t", TextSource: protocolcore.ReasoningTextContent, Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicSignature, Opaque: "sig"}}, full: true,
			want: []Block{
				{Kind: BlockKindReasoning, Availability: AvailabilityRecorded, Text: "t", OriginalSize: 1, ProviderSource: "anthropic-messages", ProviderKind: "thinking"},
				{Kind: "provider_extension", Availability: AvailabilityOmitted, OriginalSize: 3, ProviderSource: "anthropic-messages", ProviderKind: "thinking", Fingerprint: fingerprintOf("sig")},
			},
		},
		"anthropic redacted": {
			reasoning: protocolcore.Reasoning{Redacted: true, Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicRedacted, Opaque: "data"}}, full: true,
			want: []Block{{Kind: "provider_extension", Availability: AvailabilityOmitted, OriginalSize: 4, ProviderSource: "anthropic-messages", ProviderKind: "redacted_thinking", Fingerprint: fingerprintOf("data")}},
		},
	} {
		got := reasoningViews(test.reasoning, test.full, nil)
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%s: views = %+v, want %+v", name, got, test.want)
		}
		mode := environment.ContentRecordingMetadataOnly
		if test.full {
			mode = environment.ContentRecordingFull
		}
		for _, block := range got {
			if err := block.Validate(mode); err != nil {
				t.Fatalf("%s: stored block %+v is invalid: %v", name, block, err)
			}
		}
	}
	file := mediaView(protocolcore.Media{Kind: protocolcore.MediaImage, MediaType: "image/png", FileID: "file_1", FileSource: protocolspec.DialectAnthropicMessages}, nil)
	if file.ProviderSource != "anthropic-messages" || file.Fingerprint != fingerprintOf("file_1") {
		t.Fatalf("file reference projection = %+v", file)
	}
}

// The streaming Source emits exactly the blocks NewRecord stores.
func TestSourceProjectsNeutralBlocksLikeRecords(t *testing.T) {
	t.Parallel()
	request, response := neutralFixture(t)
	for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingFull, environment.ContentRecordingMetadataOnly} {
		policy := environment.DefaultContentRecordingPolicy()
		policy.Mode = mode
		at := time.Date(2026, 11, 22, 1, 2, 3, 0, time.UTC)
		record, err := NewRecord("exchange-neutral-source", frozenFixture(), policy, at, request, &response)
		if err != nil {
			t.Fatal(err)
		}
		source, err := NewSource("exchange-neutral-source", frozenFixture(), policy, at, request, &response)
		if err != nil {
			t.Fatal(err)
		}
		var streamed [][]Block
		err = source.Walk(context.Background(), func(part Part, _ int, message MessageSource) error {
			var blocks []Block
			if err := message.WalkBlocks(context.Background(), func(block Block) error {
				blocks = append(blocks, block)
				return nil
			}); err != nil {
				return err
			}
			if part == RequestPart {
				streamed = append(streamed, blocks)
			}
			if part == ResponsePart && !reflect.DeepEqual(blocks, record.Response.Blocks) {
				return fmt.Errorf("streamed response blocks differ: %+v", blocks)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		for index, message := range record.Request.Messages {
			if !reflect.DeepEqual(streamed[index], message.Blocks) {
				t.Fatalf("%s: message %d streams %+v, stores %+v", mode, index, streamed[index], message.Blocks)
			}
		}
		if _, err := source.Measure(context.Background()); err != nil {
			t.Fatalf("%s: Measure() error = %v", mode, err)
		}
	}
}

func assertStoredKinds(t *testing.T, record Record) {
	t.Helper()
	check := func(blocks []Block) {
		for _, block := range blocks {
			if !storedKinds[block.Kind] {
				t.Fatalf("stored kind %q is outside the app's closed set", block.Kind)
			}
			if strings.Contains(block.Kind, "media") {
				t.Fatalf("neutral kind leaked into storage: %+v", block)
			}
		}
	}
	check(record.Request.System)
	for _, message := range record.Request.Messages {
		check(message.Blocks)
	}
	if record.Response != nil {
		check(record.Response.Blocks)
	}
}
