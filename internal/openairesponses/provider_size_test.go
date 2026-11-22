package openairesponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// encodingJSONString is the reference: encoding/json with HTML escaping off.
func encodingJSONString(t testing.TB, value string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
}

var escapingSamples = []string{
	"",
	"plain ascii",
	"<html> & \"quotes\" \\ back/slash",
	"\x00\x01\x07\b\t\n\v\f\r\x1b\x1f\x7f",
	"line\u2028separator\u2029paragraph",
	"\u4f60\u597d\uff0c\u4e16\u754c",
	"emoji \U0001f600 and accents \u00e9\u00e8",
	"invalid \xff\xfe utf-8 \xc3",
	"truncated \xe2\x80",
	"\ufeffbyte order mark",
}

// TestJSONStringsMatchEncodingJSON keeps the encoder's one escaper exactly
// equal to encoding/json with HTML escaping off, for strings and for the
// bytes of tool arguments.
func TestJSONStringsMatchEncodingJSON(t *testing.T) {
	t.Parallel()
	for _, sample := range escapingSamples {
		want := encodingJSONString(t, sample)
		if got := appendJSONString(nil, sample); !bytes.Equal(got, want) {
			t.Errorf("appendJSONString(%q) = %s, want %s", sample, got, want)
		}
		if got := appendJSONBytesString([]byte("x"), []byte(sample)); !bytes.Equal(got, append([]byte("x"), want...)) {
			t.Errorf("appendJSONBytesString(%q) = %s, want %s", sample, got, want)
		}
	}
}

func FuzzAppendJSONString(f *testing.F) {
	for _, sample := range escapingSamples {
		f.Add(sample)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if got, want := appendJSONString(nil, value), encodingJSONString(t, value); !bytes.Equal(got, want) {
			t.Fatalf("appendJSONString(%q) = %s, want %s", value, got, want)
		}
	})
}

func mediaMessage(t *testing.T, media ...protocolcore.Media) protocolcore.Message {
	t.Helper()
	message := protocolcore.Message{Role: protocolcore.RoleUser}
	for _, value := range media {
		block, err := protocolcore.NewMediaBlock(value)
		message.Blocks = append(message.Blocks, mustBlock(t, block, err))
	}
	return message
}

func image(data string) protocolcore.Media {
	return protocolcore.Media{Kind: protocolcore.MediaImage, MediaType: "image/png", Data: data}
}

// TestEncoderEscapesEveryCopiedValue: only JSON documents (schemas) are
// spliced as bytes; media data, seal payloads, call ids and arguments are
// merely valid UTF-8 and must go through the escaper.
func TestEncoderEscapesEveryCopiedValue(t *testing.T) {
	t.Parallel()
	hostile := "a\"b\\c</d>\u2028\x01"
	seal := validSeal()
	seal.Opaque = hostile
	arguments, err := protocolcore.NewJSONObject([]byte(`{"html":"<b>&amp;</b>","quote":"\"","line":"\u2028"}`), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	call, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{Key: callKey(t, "toolu_1"), Name: "Read", Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	assistant := protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{
		reasoningBlock(t, protocolcore.Reasoning{Text: hostile, Seal: seal}),
		textBlock(t, "<tool> & more"),
		call,
	}}
	request := planRequestFor(t,
		mediaMessage(t, image(hostile)),
		assistant,
		protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{readResultBlock(t, "toolu_1")}},
	)
	side := newPlanSide(t, nil)
	encoded, _, err := side.EncodeRequestFor(protocolpath.ProviderContext{ReasoningBinding: planBinding}, request)
	if err != nil {
		t.Fatal(err)
	}
	body := encoded.Body()
	if !json.Valid(body) {
		t.Fatalf("body is not JSON: %s", body)
	}
	if !bytes.Contains(body, []byte(`"<tool> & more"`)) || bytes.Contains(body, []byte(`\u003c`)) {
		t.Fatalf("HTML characters were escaped: %s", body)
	}
	var wire struct {
		Input []struct {
			Type             string `json:"type"`
			EncryptedContent string `json:"encrypted_content"`
			Arguments        string `json:"arguments"`
			Content          []struct {
				ImageURL string `json:"image_url"`
			} `json:"content"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if got := wire.Input[0].Content[0].ImageURL; got != "data:image/png;base64,"+hostile {
		t.Errorf("image URL = %q", got)
	}
	if reasoning := wire.Input[1]; reasoning.EncryptedContent != hostile || reasoning.Summary[0].Text != hostile {
		t.Errorf("reasoning = %+v", reasoning)
	}
	if got := wire.Input[3].Arguments; got != `{"html":"<b>&amp;</b>","quote":"\"","line":"\u2028"}` {
		t.Errorf("arguments = %q", got)
	}
}

func failureAt(t *testing.T, err error, path string) {
	t.Helper()
	if protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidClientRequest || failurePath(err) != path {
		t.Fatalf("error = %v, want an invalid request at %s", err, path)
	}
}

// TestEncoderFailsBeforeReadmission: a value that fits the client's limits
// can grow in translation past what re-admission accepts. The encoder fails
// first, at the value's own path, so the request is not blamed on a script.
func TestEncoderFailsBeforeReadmission(t *testing.T) {
	t.Parallel()
	t.Run("the body limit", func(t *testing.T) {
		t.Parallel()
		options := DefaultProviderSideOptions()
		options.MaxRequestBytes = 4096
		side, err := NewProviderSide(options)
		if err != nil {
			t.Fatal(err)
		}
		request := planRequestFor(t, userText(t, "short"), userText(t, strings.Repeat("\x01", 1024)))
		_, _, err = side.EncodeRequestFor(protocolpath.ProviderContext{}, request)
		failureAt(t, err, "$.messages[1]")
	})
	t.Run("attachments per message", func(t *testing.T) {
		t.Parallel()
		images := make([]protocolcore.Media, protocolcore.MaxProviderExtensions+1)
		for index := range images {
			images[index] = image("iVBORw0KGgo=")
		}
		_, err := newPlanSide(t, nil).planRequest(protocolpath.ProviderContext{}, planRequestFor(t, mediaMessage(t, images...)))
		failureAt(t, err, fmt.Sprintf("$.messages[0].blocks[%d]", protocolcore.MaxProviderExtensions))
	})
	t.Run("attachment bytes", func(t *testing.T) {
		t.Parallel()
		side := newPlanSide(t, nil)
		// Two images whose base64 fits the request's media budget exactly,
		// until their part wrappers are counted.
		half := protocolcore.MaxMediaBytes/2 - 20
		request := planRequestFor(t, mediaMessage(t, image(strings.Repeat("A", half)), image(strings.Repeat("A", half))))
		_, _, err := side.EncodeRequestFor(protocolpath.ProviderContext{}, request)
		failureAt(t, err, "$.messages[0]")

		fitting := protocolcore.MaxMediaBytes/2 - 200
		request = planRequestFor(t, mediaMessage(t, image(strings.Repeat("A", fitting)), image(strings.Repeat("A", fitting))))
		encoded, _, err := side.EncodeRequestFor(protocolpath.ProviderContext{}, request)
		if err != nil {
			t.Fatal(err)
		}
		if err := side.ValidateTransformedRequest(encoded.Body()); err != nil {
			t.Fatalf("re-admission refused a body the encoder admitted: %v", err)
		}
	})
	t.Run("a converted document", func(t *testing.T) {
		t.Parallel()
		document := protocolcore.Media{Kind: protocolcore.MediaDocument, MediaType: "text/plain", Text: strings.Repeat("d", protocolcore.MaxTextBytes-5)}
		_, err := newPlanSide(t, nil).planRequest(protocolpath.ProviderContext{}, planRequestFor(t, mediaMessage(t, document)))
		failureAt(t, err, "$.messages[0].blocks[0]")
	})
	t.Run("a marked error output", func(t *testing.T) {
		t.Parallel()
		result, err := protocolcore.NewToolResultBlock(protocolcore.ToolResult{
			Key: callKey(t, "toolu_1"), Content: strings.Repeat("e", protocolcore.MaxTextBytes-5), IsError: true,
		})
		request := planRequestFor(t, userText(t, "go"),
			protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{readCallBlock(t, "toolu_1")}},
			protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{mustBlock(t, result, err)}})
		_, err = newPlanSide(t, nil).planRequest(protocolpath.ProviderContext{}, request)
		failureAt(t, err, "$.messages[2].blocks[0]")
	})
	t.Run("joined instructions", func(t *testing.T) {
		t.Parallel()
		side := newPlanSide(t, func(profile *ProviderRequestProfile) {
			profile.InstructionPlacement = InstructionPlacementInstructions
		})
		request := planRequestFor(t, userText(t, "go"))
		request.System = []protocolcore.ContentBlock{
			textBlock(t, strings.Repeat("s", protocolcore.MaxTextBytes/2+1)),
			textBlock(t, strings.Repeat("s", protocolcore.MaxTextBytes/2)),
		}
		_, err := side.planRequest(protocolpath.ProviderContext{}, request)
		failureAt(t, err, "$.system")
		// One developer message holds them as separate parts.
		if _, err := newPlanSide(t, nil).planRequest(protocolpath.ProviderContext{}, request); err != nil {
			t.Fatalf("developer-message placement refused: %v", err)
		}
	})
	t.Run("a list output's joined text", func(t *testing.T) {
		t.Parallel()
		half := strings.Repeat("t", protocolcore.MaxTextBytes/2)
		result, err := protocolcore.NewToolResultBlock(protocolcore.ToolResult{
			Key:     callKey(t, "toolu_1"),
			Content: half + half,
			Parts: &protocolcore.ToolResultParts{Items: []protocolcore.ToolResultPart{
				{Kind: protocolcore.ToolResultPartText, Text: half},
				{Kind: protocolcore.ToolResultPartText, Text: half},
			}},
		})
		request := planRequestFor(t, userText(t, "go"),
			protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{readCallBlock(t, "toolu_1")}},
			protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{mustBlock(t, result, err)}})
		_, err = newPlanSide(t, nil).planRequest(protocolpath.ProviderContext{}, request)
		failureAt(t, err, "$.messages[2].blocks[0]")
	})
	t.Run("a tool schema", func(t *testing.T) {
		t.Parallel()
		schema, err := protocolcore.NewJSONObject([]byte(`{"type":"object","description":"`+strings.Repeat("d", protocolcore.MaxToolJSONBytes)+`"}`), 16<<20)
		if err != nil {
			t.Fatal(err)
		}
		request := planRequestFor(t, userText(t, "go"))
		request.Tools[0].InputSchema = schema
		_, err = newPlanSide(t, nil).planRequest(protocolpath.ProviderContext{}, request)
		failureAt(t, err, "$.tools[0].input_schema")
	})
	t.Run("tool call arguments", func(t *testing.T) {
		t.Parallel()
		arguments, err := protocolcore.NewJSONObject([]byte(`{"path":"`+strings.Repeat("p", protocolcore.MaxToolJSONBytes)+`"}`), 16<<20)
		if err != nil {
			t.Fatal(err)
		}
		call, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{Key: callKey(t, "toolu_1"), Name: "Read", Arguments: arguments})
		request := planRequestFor(t, userText(t, "go"),
			protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{mustBlock(t, call, err)}},
			protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{readResultBlock(t, "toolu_1")}})
		_, err = newPlanSide(t, nil).planRequest(protocolpath.ProviderContext{}, request)
		failureAt(t, err, "$.messages[1].blocks[0].arguments")
	})
	t.Run("reasoning state", func(t *testing.T) {
		t.Parallel()
		side := newPlanSide(t, nil)
		turn := func(itemID string) protocolcore.Message {
			seal := validSeal()
			seal.ItemID = itemID
			seal.Opaque = strings.Repeat("E", protocolcore.MaxProviderExtensionBytes/4)
			return protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{
				reasoningBlock(t, protocolcore.Reasoning{Text: strings.Repeat("s", protocolcore.MaxProviderExtensionBytes/4), Seal: seal}),
				textBlock(t, "answer"),
			}}
		}
		request := planRequestFor(t, userText(t, "one"), turn("rs_a"), userText(t, "two"), turn("rs_b"), userText(t, "three"))
		_, _, err := side.EncodeRequestFor(protocolpath.ProviderContext{ReasoningBinding: planBinding}, request)
		failureAt(t, err, "$.messages[3]")
	})
}
