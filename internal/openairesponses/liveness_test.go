package openairesponses

import (
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/ssewire"
	"testing"
)

func TestKeepaliveHasNoModelOrExecutableContent(t *testing.T) {
	wire, err := EncodeKeepalive()
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	events, err := decoder.Feed(wire)
	if err != nil || len(events) != 1 {
		t.Fatalf("invalid liveness frame: %v", err)
	}
	event := events[0]
	var payload map[string]any
	if json.Unmarshal(event.Data, &payload) != nil || len(payload) != 1 || payload["type"] != "keepalive" || event.Name != "keepalive" || event.ID != "" || event.Retry != nil {
		t.Fatal("liveness acquired content or correlation")
	}
	wire[0] = 'x'
	next, err := EncodeKeepalive()
	if err != nil || string(next) != "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n" {
		t.Fatal("returned buffer contaminated another request")
	}
}
