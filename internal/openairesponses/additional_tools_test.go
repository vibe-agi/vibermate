package openairesponses_test

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/responseschat"
)

// These same native declarations already pass at the top level. Moving them
// into Codex's additional_tools item must not prevent same-dialect forwarding
// or invent a modeled tool that could acquire execution approval.
func TestCompatibleAdditionalToolsAcceptsNativeDeclarations(t *testing.T) {
	for name, declaration := range map[string]string{
		"tool_search":      `{"type":"tool_search","execution":"client","description":"Find available tools"}`,
		"web_search":       `{"type":"web_search","filters":{"allowed_domains":["example.invalid"]},"search_context_size":"low"}`,
		"mcp":              `{"type":"mcp","server_label":"fixture","server_url":"https://mcp.example.invalid","require_approval":"never"}`,
		"local_shell":      `{"type":"local_shell"}`,
		"image_generation": `{"type":"image_generation"}`,
	} {
		t.Run(name, func(t *testing.T) {
			codec, err := openairesponses.New(openairesponses.DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			for _, embedded := range []bool{false, true} {
				source := additionalToolRequest(declaration, embedded)
				request, _, err := codec.DecodeCompatibleClientRequest(source)
				if err != nil {
					t.Fatalf("compatible embedded=%t declaration rejected: %v", embedded, err)
				}
				if len(request.Messages) != 1 || len(request.Tools) != 1 || request.Tools[0].Name != "inspect" || len(request.ToolNamespaces) != 0 {
					t.Fatalf("native declaration changed modeled message/tool authority: messages=%d tools=%d namespaces=%d", len(request.Messages), len(request.Tools), len(request.ToolNamespaces))
				}
			}
			if _, _, err := codec.DecodeClientRequest(additionalToolRequest(declaration, true)); err == nil {
				t.Fatal("native additional declaration crossed the strict dialect boundary")
			}
		})
	}
}

// The compatible inspection view must not delete a declaration from the
// provider's authoritative source request when it cannot model that tool.
func TestCompatibleAdditionalToolsPreservesAuthoritativeProviderBytes(t *testing.T) {
	source := []byte(`{"input":[{"type":"additional_tools","role":"developer","tools":[{"type":"tool_search","execution":"client","description":"Find tools"},{"type":"web_search","filters":{"allowed_domains":["example.invalid"]}},{"type":"function","name":"inspect","parameters":{"type":"object","properties":{}}}]},{"role":"user","content":"hello"}],"model":"fixture-model","store":false,"stream":true}`)
	path, err := responseschat.NewResponsesPassthroughProtocolPath(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := path.Client().DecodeRequest(source)
	if err != nil {
		t.Fatal(err)
	}
	encoded, report, err := path.EncodeProviderRequest(request, source, make(http.Header))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source, encoded.Body()) || len(report.Notices()) != 0 {
		t.Fatal("same-dialect provider request changed native declaration bytes")
	}
}

func TestCompatibleAdditionalToolsKeepsModeledAndStructuralValidation(t *testing.T) {
	for name, declaration := range map[string]string{
		"function schema is not an object": `{"type":"function","name":"invalid","parameters":[]}`,
		"custom format is malformed":       `{"type":"custom","name":"invalid","format":{"type":"grammar","syntax":"unknown","definition":"fixture"}}`,
		"namespace member is malformed":    `{"type":"namespace","name":"invalid","description":"Fixture","tools":[{"type":"function","name":"member","parameters":[]}]}`,
		"native type is missing":           `{"execution":"client"}`,
		"native type is not a string":      `{"type":42}`,
		"native type is duplicated":        `{"type":"tool_search","type":"mcp"}`,
		"native type case is ambiguous":    `{"type":"tool_search","Type":"mcp"}`,
		"web search known field malformed": `{"type":"web_search","external_web_access":"yes"}`,
	} {
		t.Run(name, func(t *testing.T) {
			codec, err := openairesponses.New(openairesponses.DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := codec.DecodeCompatibleClientRequest(additionalToolRequest(declaration, true)); err == nil || protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidClientRequest {
				t.Fatalf("invalid additional declaration gained compatibility: %v", err)
			}
		})
	}
	codec, err := openairesponses.New(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{
		`{"model":"fixture-model","input":[{"type":"additional_tools","role":"user","tools":[{"type":"tool_search"}]},{"role":"user","content":"hello"}]}`,
		`{"model":"fixture-model","input":[{"type":"additional_tools","role":"developer","tools":[]},{"role":"user","content":"hello"}]}`,
	} {
		if _, _, err := codec.DecodeCompatibleClientRequest([]byte(source)); err == nil {
			t.Fatal("malformed additional_tools item was accepted")
		}
	}
}

// JSON preflight admits this source, but only a small amount of notice storage
// remains. The decoder must refuse before growing through the native prefix
// and parsing a later malformed modeled tool.
func TestCompatibleAdditionalToolsChargesNoticesBeforeParsingTheTail(t *testing.T) {
	declarations := make([]string, 64)
	for index := range declarations {
		declarations[index] = `{"type":"tool_search","execution":"client"}`
	}
	declarations = append(declarations, `{"type":"function","name":"invalid-tail","parameters":[]}`)
	source := []byte(`{"model":"fixture-model","input":[{"type":"additional_tools","role":"developer","tools":[` + strings.Join(declarations, ",") + `]},{"role":"user","content":"hello"}]}`)
	jsonCost, err := protocolcore.MeasureJSON(source)
	if err != nil {
		t.Fatal(err)
	}
	limits := protocolcore.DefaultResourceLimits()
	limits.Request = protocolcore.ResourceCost{PayloadBytes: jsonCost.PayloadBytes + (1 << 20), StructureBytes: jsonCost.StructureBytes + 128}
	options := openairesponses.DefaultOptions()
	options.Resources = &limits
	codec, err := openairesponses.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := codec.ValidateRequestJSON(source); err != nil {
		t.Fatalf("fixture did not leave notice budget after JSON preflight: %v", err)
	}
	_, report, err := codec.DecodeCompatibleClientRequest(source)
	if protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidClientRequest || !strings.Contains(err.Error(), "resource budget exceeded") {
		t.Fatalf("notice growth reached the later malformed tool before refusing: %v", err)
	}
	if !report.Empty() {
		t.Fatal("a failed additional_tools item published partial notices")
	}
}

func additionalToolRequest(declaration string, embedded bool) []byte {
	const function = `{"type":"function","name":"inspect","parameters":{"type":"object","properties":{}}}`
	if embedded {
		return []byte(fmt.Sprintf(`{"model":"fixture-model","input":[{"type":"additional_tools","role":"developer","tools":[%s,%s]},{"role":"user","content":"hello"}]}`, function, declaration))
	}
	return []byte(fmt.Sprintf(`{"model":"fixture-model","input":[{"role":"user","content":"hello"}],"tools":[%s,%s]}`, function, declaration))
}
