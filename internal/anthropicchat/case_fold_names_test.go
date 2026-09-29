package anthropicchat

import "testing"

// The neutral projection must describe the members the upstream receives.
// Names that encoding/json would merge are rejected in every decode mode.
func TestClientRequestRejectsCaseFoldedMemberNames(t *testing.T) {
	codec := newTestCodec(t)
	for _, body := range []string{
		`{"model":"claude-test","max_tokens":8,"messages":[{"role":"user","content":"real"}],"MESSAGES":[{"role":"user","content":"decoy"}]}`,
		`{"model":"claude-test","MODEL":"other","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"claude-test","max_tokens":8,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"x","TYPE":"image"}]}]}`,
	} {
		if _, _, err := codec.DecodeCompatibleClientRequest([]byte(body)); err == nil {
			t.Errorf("same-dialect decode accepted %s", body)
		}
		if _, _, err := codec.DecodeClientRequest([]byte(body)); err == nil {
			t.Errorf("strict decode accepted %s", body)
		}
	}
}
