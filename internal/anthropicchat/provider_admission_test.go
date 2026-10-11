package anthropicchat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestTransformedChatAdmissionValidatesCompleteTailAndUnknownCost(t *testing.T) {
	options := DefaultOptions()
	options.Resources = &protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 8 << 20, StructureBytes: 32 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 8 << 20, StructureBytes: 32 << 20}}
	codec, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	prefix := `{"model":"m","messages":[` + strings.Repeat(`{"role":"user","content":"history"},`, 4110)
	for _, tail := range []struct {
		raw   string
		valid bool
	}{
		{`{"role":"user","content":"final sentinel"}`, true},
		{`{"role":"unknown","content":"final sentinel"}`, false},
		{`{"role":"user","content":123}`, false},
		{`{"role":"assistant","tool_calls":[{"type":"function","id":"c","function":{"name":"f","arguments":"invalid"}}]}`, false},
		{`{"role":"user","Role":"assistant","content":"last"}`, false},
	} {
		body := prefix + tail.raw + `],"native_extension":{"preserved":true}}`
		err := codec.ValidateTransformedProviderRequest([]byte(body))
		if (err == nil) != tail.valid {
			t.Fatalf("tail=%s valid=%v err=%v", tail.raw, tail.valid, err)
		}
	}
	large := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"small"}],"inactive_unknown":%q}`, strings.Repeat("x", 2<<20))
	if err := codec.ValidateTransformedProviderRequest([]byte(large)); err == nil {
		t.Fatal("unknown allocation escaped finite lexical admission")
	}
}
