package anthropicchat

import (
	"context"
	"testing"
)

// During long extended thinking the provider may send only pings for minutes.
// A ping is the provider saying the message is still being produced, so it
// renews the idle budget; only silence means the upstream stalled.
func TestPingRenewsStreamLiveness(t *testing.T) {
	t.Parallel()
	path, err := NewMessagesProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := path.Client().DecodeRequest([]byte(`{"model":"claude-test","max_tokens":32,"messages":[{"role":"user","content":"think"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := path.Streaming().NewStream(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Feed(context.Background(), []byte(actionMessageStart)); err != nil {
		t.Fatal(err)
	}
	before := stream.SemanticProgress()
	if _, err := stream.Feed(context.Background(), []byte(sse("ping", `{"type":"ping"}`))); err != nil {
		t.Fatal(err)
	}
	if stream.SemanticProgress() <= before {
		t.Fatal("a provider ping did not renew stream liveness")
	}
}
