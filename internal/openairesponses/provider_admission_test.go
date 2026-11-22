package openairesponses

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestAdmitRequestRefusesProviderFileReferences(t *testing.T) {
	t.Parallel()
	side := newPlanSide(t, nil)
	file := func(kind protocolcore.MediaKind) *protocolcore.Media {
		return &protocolcore.Media{Kind: kind, FileID: "file_1", FileSource: protocolspec.DialectAnthropicMessages}
	}
	inline := &protocolcore.Media{Kind: protocolcore.MediaImage, MediaType: "image/png", Data: "iVBORw0KGgo="}
	media := func(value *protocolcore.Media) protocolcore.Message {
		block, err := protocolcore.NewMediaBlock(*value)
		return protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{mustBlock(t, block, err)}}
	}
	result := func(value *protocolcore.Media) protocolcore.Message {
		block, err := protocolcore.NewToolResultBlock(protocolcore.ToolResult{
			Key:   callKey(t, "toolu_1"),
			Parts: &protocolcore.ToolResultParts{Items: []protocolcore.ToolResultPart{{Kind: protocolcore.ToolResultPartMedia, Media: value}}},
		})
		return protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{mustBlock(t, block, err)}}
	}
	for name, test := range map[string]struct {
		message protocolcore.Message
		want    protocolcore.ProviderErrorClass
	}{
		"an inline image":                      {message: media(inline)},
		"a Files API image":                    {message: media(file(protocolcore.MediaImage)), want: protocolcore.ProviderErrorImage},
		"a Files API document":                 {message: media(file(protocolcore.MediaDocument)), want: protocolcore.ProviderErrorDocument},
		"a Files API image a tool returned":    {message: result(file(protocolcore.MediaImage)), want: protocolcore.ProviderErrorImage},
		"a Files API document a tool returned": {message: result(file(protocolcore.MediaDocument)), want: protocolcore.ProviderErrorDocument},
		"an inline image a tool returned":      {message: result(inline)},
	} {
		request := planRequestFor(t, userText(t, "look"), test.message)
		refused := side.AdmitRequest(request)
		switch {
		case test.want == "" && refused != nil:
			t.Errorf("%s: refused as %+v", name, refused)
		case test.want != "" && (refused == nil || refused.Class != test.want || refused.Validate() != nil):
			t.Errorf("%s: admission = %+v, want class %s", name, refused, test.want)
		}
	}
}

// TestReadmissionUsesTheProviderLimit admits a post-script body above the
// client limit: a 16 MiB client body grows by item wrappers and data URL
// prefixes, and refusing it at re-admission would blame a script.
func TestReadmissionUsesTheProviderLimit(t *testing.T) {
	t.Parallel()
	large := func(total int) []byte {
		// Each text stays below the neutral model's per-block limit.
		third := `{"type":"input_text","text":"` + strings.Repeat("a", total/3) + `"}`
		return []byte(`{"model":"m","store":false,"input":[{"role":"user","content":[` +
			third + `,` + third + `,` + third + `]}]}`)
	}
	side := newPlanSide(t, nil)
	if err := side.ValidateTransformedRequest(large(17 << 20)); err != nil {
		t.Fatalf("a 17 MiB body was refused: %v", err)
	}
	if _, _, err := newTestCodec(t).DecodeCompatibleClientRequest(large(17 << 20)); err == nil {
		t.Fatal("the client codec admitted a body above its own limit")
	}
	if err := side.ValidateTransformedRequest(large(21 << 20)); err == nil {
		t.Fatal("a body above the provider limit was admitted")
	}
	if err := side.ValidateTransformedRequest([]byte(`{"model":"m","store":true,"input":"hi"}`)); err == nil {
		t.Fatal("stored state was admitted")
	}
}
