package loopbackproxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// The installed-Claude fallback proof (runlauncher, ADR 0026) ends a stream
// with exactly the bytes the proxy writes when an Upstream stream fails after
// ViberMate has answered. The sample is generated, not maintained:
// VIBERMATE_UPDATE=1 rewrites it from the production encoder.
func TestAnthropicStreamFailureSampleIsCurrent(t *testing.T) {
	encoded, err := encodeExchangeStreamFailure(
		protocolspec.DialectAnthropicMessages,
		exchange.FailureNotice{ReasonCode: exchange.ReasonProviderTransportFailed},
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "api", "samples", "anthropic-stream-failure.sse")
	if os.Getenv("VIBERMATE_UPDATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the Anthropic stream failure sample: %v", err)
	}
	if string(current) != string(encoded) {
		t.Fatalf("the Anthropic stream failure sample is stale; rerun with VIBERMATE_UPDATE=1\n--- stored\n%q\n--- current\n%q",
			current, encoded)
	}
}
