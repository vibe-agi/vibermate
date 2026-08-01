package anthropicchat_test

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// A client adds fields faster than any translator learns them. Claude Code
// sends `defer_loading`; refusing the whole request over one field it does not
// model made the client unusable through vibermate.
//
// Design 07 §2.3 requires the wire layer to keep unknown fields rather than
// let a typed unmarshal make them disappear, and §3.2 allows lossy translation
// only when the loss is declared. So an unmodelled field is reported, not
// fatal and not silent.
func TestAnUnknownRequestFieldIsReportedRatherThanRefused(t *testing.T) {
	t.Parallel()

	codec, err := anthropicchat.New(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":64,
		"defer_loading":true,
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]
	}`)
	decoded, report, err := codec.DecodeClientRequest(body)
	if err != nil {
		t.Fatalf("an unmodelled field refused the request: %v", err)
	}
	if len(decoded.Messages) != 1 {
		t.Fatalf("messages = %+v", decoded.Messages)
	}
	found := false
	for _, notice := range report.Notices() {
		if notice.Code == protocolcore.NoticeUnknownRequestFieldNotForwarded &&
			notice.Path == "$.defer_loading" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the dropped field was not declared: %+v", report.Notices())
	}
}

// Silence is the failure mode this guards against. A field that vanishes
// without a notice is a translation nobody can audit.
func TestAModelledRequestReportsNoUnknownField(t *testing.T) {
	t.Parallel()

	codec, err := anthropicchat.New(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":64,
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]
	}`)
	_, report, err := codec.DecodeClientRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, notice := range report.Notices() {
		if notice.Code == protocolcore.NoticeUnknownRequestFieldNotForwarded {
			t.Fatalf("a modelled request declared a dropped field: %+v", notice)
		}
	}
}

// A duplicate name is still a malformed request, not an unknown field.
func TestADuplicateFieldIsStillRefused(t *testing.T) {
	t.Parallel()

	codec, err := anthropicchat.New(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
		"model":"a","model":"b",
		"max_tokens":64,
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]
	}`)
	if _, _, err := codec.DecodeClientRequest(body); err == nil {
		t.Fatal("a duplicate field was accepted")
	}
}

// A refusal inside a modelled field names that field. "Somewhere in the
// request" is what the person is already looking at.
func TestARefusalInsideAFieldNamesTheField(t *testing.T) {
	t.Parallel()

	codec, err := anthropicchat.New(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	// A content block type this dialect cannot express is a refusal, not a
	// declared loss: the block is what the message says.
	body := []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":64,
		"messages":[{
			"role":"user",
			"content":[{"type":"text","text":"hello","unknown_option":true}]
		}]
	}`)
	_, _, err = codec.DecodeClientRequest(body)
	if err == nil {
		t.Fatal("an unknown field inside a content block was accepted")
	}
	if !strings.Contains(err.Error(), "$.messages") {
		t.Fatalf("the refusal does not name the field: %v", err)
	}
}
