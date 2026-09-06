package openairesponses

import "testing"

func TestCompatibleTextFormatsRemainStructurallyValidated(t *testing.T) {
	codec := newTestCodec(t)
	for _, test := range []struct {
		format string
		valid  bool
	}{
		{`{"type":"text"}`, true},
		{`{"type":"json_object"}`, true},
		{`{"type":"json_schema","name":"result","schema":{},"strict":false}`, true},
		{`{"type":"json_schema","schema":{}}`, false},
		{`{"type":"json_schema","name":"result","schema":null}`, false},
		{`{"type":"json_schema","name":"result","schema":[]}`, false},
		{`{"type":"json_schema","name":"result","schema":{},"strict":"true"}`, false},
		{`{"type":"text","schema":{}}`, false},
		{`{"type":"unsupported"}`, false},
		{`[]`, false},
	} {
		t.Run(test.format, func(t *testing.T) {
			body := []byte(`{"model":"model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"text":{"format":` + test.format + `}}`)
			_, _, err := codec.DecodeCompatibleClientRequest(body)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v, error=%v", test.valid, err)
			}
		})
	}
}
