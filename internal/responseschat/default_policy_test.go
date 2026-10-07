package responseschat

import (
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestDefaultPolicyNestedPrecedenceAndFreshCopies(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"role":"user","content":"question"}]}`)
	response := []byte(`{"id":"response","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	semantic := protocolcore.DefaultResourceLimits()
	tight := semantic
	tight.Request = protocolcore.ResourceCost{PayloadBytes: 1, StructureBytes: 1}
	tight.Response = tight.Request
	nested := DefaultOptions()
	nested.Responses.Resources = &tight
	nested.Chat.Resources = &tight
	path, err := NewProtocolPath(nested)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := path.Client().DecodeRequest(body); err == nil {
		t.Fatal("top-level default overwrote explicit nested policy")
	}
	nested.Resources = &semantic
	path, err = NewProtocolPath(nested)
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := path.Client().DecodeRequest(body)
	if err != nil {
		t.Fatal("top-level override did not win: ", err)
	}
	if _, _, err := path.Backend().DecodeResponse(request, response); err != nil {
		t.Fatal("top-level override did not reach Chat: ", err)
	}
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			options := DefaultOptions()
			owned, err := NewProtocolPath(options)
			if err != nil {
				t.Error(err)
				return
			}
			options.Responses.Resources.Request = tight.Request
			options.Chat.Resources.Response = tight.Response
			decoded, _, err := owned.Client().DecodeRequest(body)
			if err != nil {
				t.Errorf("copied default client mutated: %v", err)
				return
			}
			if _, _, err := owned.Backend().DecodeResponse(decoded, response); err != nil {
				t.Errorf("copied default backend mutated: %v", err)
			}
			fresh := DefaultOptions()
			if fresh.Responses.Resources.Request != semantic.Request || fresh.Chat.Resources.Response != semantic.Response {
				t.Error("default options share mutable policy")
			}
			fresh.Responses.Resources = nil
			fresh.Chat.Resources = nil
			normalized, err := NewProtocolPath(fresh)
			if err != nil {
				t.Error(err)
				return
			}
			if _, _, err := normalized.Client().DecodeRequest(body); err != nil {
				t.Errorf("nil nested normalization: %v", err)
			}
		})
	}
	workers.Wait()
}
