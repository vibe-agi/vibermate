package openairesponses_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// deltaPayloads is one public-API turn with the given number of
// output_text.delta events. No recording of that length exists in the dumps,
// so it is synthesized from the public API's event shapes.
func deltaPayloads(deltas int) []string {
	text := func(sequence int, kind, members string) string {
		return `{"type":"response.` + kind + `","sequence_number":` + strconv.Itoa(sequence) +
			`,"item_id":"msg_1","output_index":0,"content_index":0,` + members + `}`
	}
	const word = "token "
	final := ""
	payloads := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1700000000,"status":"in_progress","model":"` + mappedModel + `","output":[],"usage":null}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","content":[],"role":"assistant"}}`,
		text(2, "content_part.added", `"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}`),
	}
	for index := 0; index < deltas; index++ {
		payloads = append(payloads, text(3+index, "output_text.delta", `"delta":"`+word+`","logprobs":[],"obfuscation":"a1b2c3"`))
		final += word
	}
	item := `{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"` + final + `"}],"role":"assistant"}`
	sequence := 3 + deltas
	payloads = append(payloads,
		text(sequence, "output_text.done", `"text":"`+final+`","logprobs":[]`),
		text(sequence+1, "content_part.done", `"part":{"type":"output_text","annotations":[],"logprobs":[],"text":"`+final+`"}`),
		`{"type":"response.output_item.done","sequence_number":`+strconv.Itoa(sequence+2)+`,"output_index":0,"item":`+item+`}`,
		`{"type":"response.completed","sequence_number":`+strconv.Itoa(sequence+3)+`,"response":{"id":"resp_1","object":"response","created_at":1700000000,"status":"completed","model":"`+mappedModel+`","output":[`+item+`],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":0},"output_tokens":`+strconv.Itoa(deltas)+`,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":`+strconv.Itoa(deltas+10)+`}}}`,
	)
	return payloads
}

// deltaRecording frames deltaPayloads one fragment per event, as the
// network delivers them.
func deltaRecording(t testing.TB, deltas int) [][]byte {
	t.Helper()
	payloads := deltaPayloads(deltas)
	fragments := make([][]byte, len(payloads))
	for index, payload := range payloads {
		fragments[index] = sseWire(t, payload)
	}
	return fragments
}

func feedRecording(b *testing.B, stream protocolpath.Stream, fragments [][]byte) {
	for _, fragment := range fragments {
		if _, err := stream.Feed(context.Background(), fragment); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := stream.FinishDecoded(context.Background()); err != nil {
		b.Fatal(err)
	}
}

// BenchmarkTranslatedStreamDeltas measures the per-event cost of the
// translating driver with the Anthropic client encoder (translated) and of
// the native relay R->R uses (native) on the same 10,000-delta recording
// (design 14.2). The gate is translated at most 1.25x native per event.
func BenchmarkTranslatedStreamDeltas(b *testing.B) {
	fragments := deltaRecording(b, 10_000)
	events := float64(len(fragments))

	b.Run("native", func(b *testing.B) {
		codec, err := openairesponses.New(openairesponses.DefaultOptions())
		if err != nil {
			b.Fatal(err)
		}
		request, _, err := codec.DecodeCompatibleClientRequest([]byte(`{"model":"` + mappedModel + `","input":[{"role":"user","content":"hi"}],"stream":true,"store":false}`))
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			stream, err := codec.NewProviderStream(request)
			if err != nil {
				b.Fatal(err)
			}
			feedRecording(b, stream, fragments)
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/events, "ns/event")
	})
	b.Run("translated", func(b *testing.B) {
		side := newProviderSide(b, nil)
		request := streamRequest(b, "summarized", true)
		b.ReportAllocs()
		for b.Loop() {
			stream, err := side.NewProviderStream(streamContext, request, clientEncoder(b, request))
			if err != nil {
				b.Fatal(err)
			}
			feedRecording(b, stream, fragments)
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/events, "ns/event")
	})
}

// TestDeltaRecordingIsAValidStream keeps the benchmark's input honest:
// openai-go parses its events and Claude Code would read the whole text.
func TestDeltaRecordingIsAValidStream(t *testing.T) {
	t.Parallel()

	checkOpenAIEvents(t, deltaPayloads(50)...)
	fragments := deltaRecording(t, 50)
	side, _ := newStreamSide(t)
	request := streamRequest(t, "summarized", true)
	result := mustFeed(t, side, request, fragments...)
	message := accumulate(t, approved(t, result))
	if len(message.Content) != 1 || len(message.Content[0].Text) != 50*len("token ") {
		t.Fatalf("message = %+v", message.Content)
	}
}
