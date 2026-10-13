package openairesponses

import "github.com/vibe-agi/vibermate/internal/ssewire"

// EncodeKeepalive encodes transport liveness, not response content, usage or a
// tool decision. The fixed frame carries no execution or correlation identity.
func EncodeKeepalive() ([]byte, error) {
	return ssewire.Encode(ssewire.Event{Name: "keepalive", Data: []byte(`{"type":"keepalive"}`)})
}
