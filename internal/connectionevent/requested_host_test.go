package connectionevent

import (
	"testing"
	"time"
)

// A record carries the host and the port as separate facts. A field named for
// the host that holds an authority says the port twice, and any reader that
// joins the two — a window, a report, a rule — renders it twice.
func TestARequestedHostIsAHostAndNotAnAuthority(t *testing.T) {
	t.Parallel()

	started := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	event := Event{
		ConnectionID:     "connection-authority",
		SourceConfidence: SourceConfidenceUnknown,
		RequestedHost:    "api.example.com:443",
		Port:             443,
		Decryption:       DecryptionNone,
		Phase:            PhaseAttempted,
		StartedAt:        started,
	}
	if err := event.Validate(); err == nil {
		t.Fatal("a requested host carrying a port was accepted")
	}
	event.RequestedHost = "api.example.com"
	if err := event.Validate(); err != nil {
		t.Fatalf("a plain host was rejected: %v", err)
	}
}
