package responseschat

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestCandidateChatScratchWithRetainingResponsesEncoder(t *testing.T) {
	options := DefaultOptions()
	options.Resources = &protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 1 << 20, StructureBytes: 1 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 64 << 10, StructureBytes: 64 << 10}}
	path, err := NewProtocolPath(options)
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := path.Client().DecodeRequest([]byte(`{"model":"m","input":[{"role":"user","content":"q"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := path.Streaming().NewStream(request)
	if err != nil {
		t.Fatal(err)
	}
	const usage = `data: {"id":"r","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}` + "\n\n"
	if _, err = stream.Feed(context.Background(), []byte(strings.Repeat(usage, 256))); err != nil {
		t.Fatal(err)
	}
	wire := []byte(`data: {"id":"r","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"owned response tail"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n")
	output, err := stream.Feed(context.Background(), wire)
	if err != nil {
		t.Fatal(err)
	}
	clear(wire)
	clear(output)
	terminal, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	copy := terminal.DecodedResponse()
	copy.Blocks[0].Text = "caller mutation"
	if terminal.DecodedResponse().Blocks[0].Text != "owned response tail" {
		t.Fatal("terminal borrowed a caller mutation")
	}
	approved, err := terminal.Approve()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(approved, []byte("response.completed")) || !bytes.Contains(approved, []byte("owned response tail")) {
		t.Fatal("retaining encoder lost complete terminal")
	}
}
