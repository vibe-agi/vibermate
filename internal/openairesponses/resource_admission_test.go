package openairesponses

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

func resourceHistory(count, textBytes int) []byte {
	var body strings.Builder
	body.WriteString(`{"model":"m","input":[`)
	for i := 0; i < count; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		text := strings.Repeat("x", textBytes)
		if i == count-1 {
			text = "distinct tail"
		}
		fmt.Fprintf(&body, `{"role":"user","content":%q}`, text)
	}
	body.WriteString(`]}`)
	return []byte(body.String())
}

func TestResponsesResourceAdmissionStreamAggregate(t *testing.T) {
	policy := resourcePolicy()
	policy.Response.StructureBytes = 20000
	options := DefaultOptions()
	options.Resources = &policy
	codec, _ := New(options)
	request, _, err := codec.DecodeClientRequest(resourceHistory(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	request.Stream = true
	stream, err := codec.NewProviderStream(request)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		fragment := appendResponseEvent(t, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": i, "item": map[string]any{"type": "message", "id": fmt.Sprint(i), "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": ""}}}})
		_, err := stream.Feed(context.Background(), fragment)
		if i == 0 && err != nil {
			t.Fatal(err)
		}
		if err != nil {
			if !strings.Contains(err.Error(), "resource budget") {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("stream accumulated materialized cells beyond finite response policy")
}

func TestResponsesResourceAdmissionEncoderAggregate(t *testing.T) {
	policy := resourcePolicy()
	policy.Response.StructureBytes = 20000
	options := DefaultOptions()
	options.Resources = &policy
	codec, _ := New(options)
	request, _, err := codec.DecodeClientRequest(resourceHistory(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	request.Stream = true
	encoder, err := codec.NewStreamEncoder(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encoder.Start(protocolpath.StreamStart{ResponseID: "r", ReportedModel: "m", CreatedAtUnix: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := encoder.StartText(i); err != nil {
			if !strings.Contains(err.Error(), "resource budget") {
				t.Fatal(err)
			}
			return
		}
		if _, err := encoder.StopText(i); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("encoder accumulated empty cells beyond finite response policy")
}

func resourcePolicy() protocolcore.ResourceLimits {
	return protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{128 << 20, 128 << 20}, Response: protocolcore.ResourceCost{128 << 20, 128 << 20}}
}

func TestResponsesResourceAdmissionCompleteHistories(t *testing.T) {
	policy := resourcePolicy()
	options := DefaultOptions()
	options.Resources = &policy
	codec, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := New(DefaultOptions())
	// Mutating caller options must not mutate the live codec's finite policy.
	policy.Request = protocolcore.ResourceCost{1, 1}
	for _, count := range []int{4095, 4096, 4097, 4102, 4111, 16384} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			textBytes := 0
			if count == 4111 {
				textBytes = 1000
			}
			body := resourceHistory(count, textBytes)
			request, _, err := codec.DecodeClientRequest(body)
			if err != nil {
				t.Fatal(err)
			}
			if len(request.Messages) != count || request.Messages[count-1].Blocks[0].Text != "distinct tail" {
				t.Fatal("complete history lost")
			}
			if count > 4096 {
				if _, _, err := legacy.DecodeClientRequest(body); err == nil || !strings.Contains(err.Error(), "message count") {
					t.Fatalf("legacy count semantics changed: %v", err)
				}
			}
			request.Stream = true
			if _, err := codec.NewProviderStream(request); err != nil {
				t.Fatal(err)
			}
			if _, err := codec.NewStreamEncoder(request); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResponsesResourceAdmissionEarlyJSONAndTail(t *testing.T) {
	policy := resourcePolicy()
	policy.Request.StructureBytes = 1024
	options := DefaultOptions()
	options.Resources = &policy
	codec, _ := New(options)
	body := resourceHistory(16384, 0)
	allocs := testing.AllocsPerRun(3, func() {
		if _, _, err := codec.DecodeClientRequest(body); err == nil {
			t.Fatal("dense JSON accepted")
		}
	})
	if allocs > 32 {
		t.Fatalf("root decoding allocated before refusal: %f", allocs)
	}
	policy = resourcePolicy()
	options.Resources = &policy
	codec, _ = New(options)
	for _, bad := range []string{`{"role":"invalid","content":"tail"}`, `{"type":"function_call","call_id":"last","name":"f","arguments":"{bad}"}`, `{"role":"user","ROLE":"assistant","content":"tail"}`} {
		body = resourceHistory(4111, 0)
		last := strings.LastIndex(string(body), `{"role"`)
		body = append(body[:last], []byte(bad+`]}`)...)
		if _, _, err := codec.DecodeClientRequest(body); err == nil {
			t.Fatalf("invalid tail accepted: %s", bad)
		}
	}
}

func TestResponsesResourceAdmissionResponseAllowanceIsIndependent(t *testing.T) {
	policy := resourcePolicy()
	options := DefaultOptions()
	options.Resources = &policy
	codec, _ := New(options)
	text := strings.Repeat("&", 6<<20)
	body := []byte(`{"id":"r","object":"response","status":"completed","created_at":1,"model":"m","output":[{"type":"message","id":"out","role":"assistant","content":[{"type":"output_text","text":"` + text + `"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}`)
	for _, count := range []int{1, 4111} {
		request, _, err := codec.DecodeClientRequest(resourceHistory(count, 0))
		if err != nil {
			t.Fatal(err)
		}
		response, _, err := codec.DecodeProviderResponse(request, body)
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Blocks) != 1 || response.Blocks[0].Text != text {
			t.Fatal("nonstreaming response changed")
		}
		request.Stream = true
		stream, err := codec.NewProviderStream(request)
		if err != nil {
			t.Fatal(err)
		}
		event := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + string(body) + "}\n\n")
		if _, err := stream.Feed(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		terminal, err := stream.FinishDecoded(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if terminal.DecodedResponse().Blocks[0].Text != text {
			t.Fatal("streaming response changed")
		}
	}
}

func TestResponsesResourceAdmissionConstructorRejectsZeroPolicy(t *testing.T) {
	for _, policy := range []protocolcore.ResourceLimits{{}, {Request: protocolcore.ResourceCost{1, 1}, Response: protocolcore.ResourceCost{1, 0}}} {
		options := DefaultOptions()
		options.Resources = &policy
		if _, err := New(options); err == nil {
			t.Fatal("zero finite policy accepted")
		}
	}
}

func TestResponsesResourceAdmissionStrictCompatiblePhaseOrder(t *testing.T) {
	policy := resourcePolicy()
	options := DefaultOptions()
	options.Resources = &policy
	codec, _ := New(options)
	body := resourceHistory(4111, 0)
	body = append(body[:len(body)-2], []byte(`,{"type":"message","role":"assistant","phase":"commentary","id":"phase-tail","content":[{"type":"output_text","text":"assistant tail"}]}]}`)...)
	if _, _, err := codec.DecodeClientRequest(body); err == nil || !strings.Contains(err.Error(), "$.input[4111].phase") {
		t.Fatalf("strict phase distinction lost: %v", err)
	}
	request, report, err := codec.DecodeCompatibleClientRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 4112 || request.Messages[4110].Blocks[0].Text != "distinct tail" || request.Messages[4111].Blocks[0].Text != "assistant tail" {
		t.Fatal("compatible history order lost")
	}
	notices := report.Notices()
	if len(notices) != 2 || notices[0].Code != protocolcore.NoticeMessagePhaseNotProjected || notices[1].Code != protocolcore.NoticeMessageItemIdentityNotForwarded {
		t.Fatalf("phase/item report order changed: %+v", notices)
	}
}
