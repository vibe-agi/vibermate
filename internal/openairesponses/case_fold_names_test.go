package openairesponses

import "testing"

func TestClientRequestRejectsCaseFoldedMemberNames(t *testing.T) {
	codec := newTestCodec(t)
	for _, body := range []string{
		`{"model":"gpt-test","input":[{"type":"message","role":"user","content":"real"}],"INPUT":[{"type":"message","role":"user","content":"decoy"}]}`,
		`{"model":"gpt-test","input":[{"type":"function_call_output","call_id":"c","output":"x","Type":"message"}]}`,
	} {
		if _, _, err := codec.DecodeCompatibleClientRequest([]byte(body)); err == nil {
			t.Errorf("same-dialect decode accepted %s", body)
		}
	}
}
