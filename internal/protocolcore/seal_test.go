package protocolcore

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestReasoningSealRoundTrip(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		seal   ReasoningSeal
		source ReasoningTextSource
		prefix string
	}{
		"responses summary": {testSeal(), ReasoningTextSummary, "vmr1."},
		"responses default": {testSeal(), "", "vmr1."},
		"responses content": {testSeal(), ReasoningTextContent, "vmr1."},
		"empty payload":     {ReasoningSeal{Format: SealResponsesEncrypted, ItemID: "rs_empty", Model: "gpt-test"}, "", "vmr1."},
		"dotted payload":    {ReasoningSeal{Format: SealResponsesEncrypted, ItemID: "rs_dots", Opaque: "a.b..c."}, "", "vmr1."},
		"no model, binding": {ReasoningSeal{Format: SealResponsesEncrypted, ItemID: "rs_bare", Opaque: "x"}, "", "vmr1."},
		"anthropic":         {ReasoningSeal{Format: SealAnthropicSignature, Model: "claude-test", Binding: "b", Opaque: "EqQBCkgIBhABGAIiQL"}, "", "vma1."},
		"anthropic content": {ReasoningSeal{Format: SealAnthropicSignature, Opaque: "sig"}, ReasoningTextContent, "vma1."},
		"redacted":          {ReasoningSeal{Format: SealAnthropicRedacted, Model: "claude-test", Opaque: "data"}, "", "vma1."},
	} {
		formatted, err := FormatReasoningSeal(test.seal, test.source)
		if err != nil {
			t.Fatalf("%s: FormatReasoningSeal() error = %v", name, err)
		}
		if !strings.HasPrefix(formatted, test.prefix) || !strings.HasSuffix(formatted, "."+test.seal.Opaque) {
			t.Fatalf("%s: formatted seal %q does not end in the verbatim payload", name, formatted)
		}
		parsed, source, ok := ParseReasoningSeal(formatted)
		if !ok || parsed != test.seal {
			t.Fatalf("%s: ParseReasoningSeal() = %+v, %v", name, parsed, ok)
		}
		wantSource := test.source
		if wantSource == ReasoningTextSummary || test.prefix == "vma1." {
			wantSource = ""
		}
		if source != wantSource {
			t.Fatalf("%s: text source = %q, want %q", name, source, wantSource)
		}
		again, err := FormatReasoningSeal(parsed, source)
		if err != nil || again != formatted {
			t.Fatalf("%s: format is not stable: %q vs %q (%v)", name, again, formatted, err)
		}
	}
	// The header is the documented JSON, in its documented member order.
	formatted, err := FormatReasoningSeal(testSeal(), ReasoningTextContent)
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.SplitN(formatted, ".", 3)[1]
	header, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || string(header) != `{"i":"rs_1","m":"gpt-test","b":"0123456789abcdef0123456789abcdef","t":"content"}` {
		t.Fatalf("header = %s (%v)", header, err)
	}
	// About 60 to 100 bytes of header around the verbatim payload.
	if overhead := len(formatted) - len(testSeal().Opaque); overhead < 40 || overhead > 140 {
		t.Fatalf("seal overhead = %d bytes", overhead)
	}
}

func TestFormatReasoningSealRefusesInvalidSeals(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		seal   ReasoningSeal
		source ReasoningTextSource
	}{
		"zero":                {ReasoningSeal{}, ""},
		"responses no item":   {ReasoningSeal{Format: SealResponsesEncrypted, Opaque: "x"}, ""},
		"unknown format":      {ReasoningSeal{Format: "other", Opaque: "x"}, ""},
		"unknown text source": {testSeal(), "other"},
		"anthropic summary":   {ReasoningSeal{Format: SealAnthropicSignature, Opaque: "x"}, ReasoningTextSummary},
		"oversized header":    {ReasoningSeal{Format: SealResponsesEncrypted, ItemID: strings.Repeat("r", 500), Model: strings.Repeat("m", 500), Binding: strings.Repeat("b", 100)}, ""},
	} {
		if _, err := FormatReasoningSeal(test.seal, test.source); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestParseReasoningSealTreatsEverythingElseAsForeign(t *testing.T) {
	t.Parallel()
	header := func(json string) string { return base64.RawURLEncoding.EncodeToString([]byte(json)) }
	for name, value := range map[string]string{
		"empty":                      "",
		"genuine signature":          "EqQBCkgIBhABGAIiQLm9wWm3aT8Kc3p6Zm9vYmFyYmF6",
		"plain base64 of a header":   header(`{"i":"rs_1","m":"","b":""}`),
		"prefix only":                "vmr1.",
		"no payload separator":       "vmr1." + header(`{"i":"rs_1","m":"","b":""}`),
		"empty header":               "vmr1..payload",
		"padded header":              "vmr1." + base64.URLEncoding.EncodeToString([]byte(`{"i":"rs_1","m":"","b":""}`)) + ".x",
		"invalid base64 character":   "vmr1.e30*.x",
		"line break in header":       "vmr1." + header(`{"i":"rs_1","m":"","b":""}`)[:8] + "\n" + header(`{"i":"rs_1","m":"","b":""}`)[8:] + ".x",
		"not JSON":                   "vmr1." + header(`not json`) + ".x",
		"unknown member":             "vmr1." + header(`{"i":"rs_1","m":"","b":"","x":1}`) + ".x",
		"missing item":               "vmr1." + header(`{"m":"","b":""}`) + ".x",
		"empty item":                 "vmr1." + header(`{"i":"","m":"","b":""}`) + ".x",
		"reordered members":          "vmr1." + header(`{"m":"","i":"rs_1","b":""}`) + ".x",
		"whitespace":                 "vmr1." + header(`{"i":"rs_1", "m":"","b":""}`) + ".x",
		"duplicate member":           "vmr1." + header(`{"i":"rs_1","i":"rs_2","m":"","b":""}`) + ".x",
		"summary marker":             "vmr1." + header(`{"i":"rs_1","m":"","b":"","t":"summary"}`) + ".x",
		"unknown text source":        "vmr1." + header(`{"i":"rs_1","m":"","b":"","t":"raw"}`) + ".x",
		"trailing value":             "vmr1." + header(`{"i":"rs_1","m":"","b":""}{}`) + ".x",
		"escaped member":             "vmr1." + header(`{"\`+`u0069":"rs_1","m":"","b":""}`) + ".x",
		"anthropic unknown kind":     "vma1." + header(`{"m":"","b":"","k":"other"}`) + ".x",
		"anthropic with item":        "vma1." + header(`{"i":"rs","m":"","b":"","k":"thinking"}`) + ".x",
		"wrong version":              "vmr2." + header(`{"i":"rs_1","m":"","b":""}`) + ".x",
		"header over limit":          "vmr1." + header(`{"i":"`+strings.Repeat("r", 1100)+`","m":"","b":""}`) + ".x",
		"model with surrounding spc": "vmr1." + header(`{"i":"rs_1","m":" gpt","b":""}`) + ".x",
	} {
		if seal, _, ok := ParseReasoningSeal(value); ok {
			t.Fatalf("%s parsed as %+v", name, seal)
		}
	}
}

func TestSealDigestIsStableAndCoversEveryField(t *testing.T) {
	t.Parallel()
	base := testSeal()
	digest := SealDigest(base)
	if digest != SealDigest(base) {
		t.Fatal("SealDigest is not deterministic")
	}
	// A fixed value guards against an accidental change of the digest input,
	// which would make a restarted memory miss nothing but a future one diverge.
	if got := base64.StdEncoding.EncodeToString(digest[:]); got != sealDigestGolden {
		t.Fatalf("SealDigest(testSeal()) = %s", got)
	}
	variants := []ReasoningSeal{
		{Format: SealAnthropicSignature, ItemID: base.ItemID, Model: base.Model, Binding: base.Binding, Opaque: base.Opaque},
		{Format: base.Format, ItemID: "rs_2", Model: base.Model, Binding: base.Binding, Opaque: base.Opaque},
		{Format: base.Format, ItemID: base.ItemID, Model: "gpt-other", Binding: base.Binding, Opaque: base.Opaque},
		{Format: base.Format, ItemID: base.ItemID, Model: base.Model, Binding: "other", Opaque: base.Opaque},
		{Format: base.Format, ItemID: base.ItemID, Model: base.Model, Binding: base.Binding, Opaque: "ENC-2"},
		// Moving bytes between fields must change the digest.
		{Format: base.Format, ItemID: base.ItemID + "g", Model: "pt-test", Binding: base.Binding, Opaque: base.Opaque},
	}
	for index, variant := range variants {
		if SealDigest(variant) == digest {
			t.Fatalf("variant %d shares the digest", index)
		}
	}
}

const sealDigestGolden = "WrUKZKymOFTx4dnoEn2Zp+DyE+SkhcU4yRGoX6Z4g/g="

func FuzzParseReasoningSeal(f *testing.F) {
	for _, seal := range []ReasoningSeal{testSeal(), {Format: SealAnthropicRedacted, Opaque: "d.a.t.a"}, {Format: SealResponsesEncrypted, ItemID: "rs"}} {
		formatted, err := FormatReasoningSeal(seal, "")
		if err != nil {
			f.Fatal(err)
		}
		f.Add(formatted)
	}
	f.Add("EqQBCkgIBhABGAIiQLm9wWm3aT8K")
	f.Add("vmr1.e30.x")
	f.Add("vma1..")
	f.Fuzz(func(t *testing.T, value string) {
		seal, source, ok := ParseReasoningSeal(value)
		if !ok {
			if !seal.IsZero() || source != "" {
				t.Fatalf("a foreign value returned state: %+v %q", seal, source)
			}
			return
		}
		if !strings.HasPrefix(value, "vmr1.") && !strings.HasPrefix(value, "vma1.") {
			t.Fatalf("accepted a value without a seal prefix: %q", value)
		}
		if err := seal.Validate(); err != nil {
			t.Fatalf("accepted an invalid seal: %v", err)
		}
		// Only the first two dots delimit: the payload is the rest, verbatim.
		parts := strings.SplitN(value, ".", 3)
		if len(parts) != 3 || parts[2] != seal.Opaque {
			t.Fatalf("payload %q is not the text after the second dot of %q", seal.Opaque, value)
		}
		formatted, err := FormatReasoningSeal(seal, source)
		if err != nil || formatted != value {
			t.Fatalf("accepted a non-canonical seal %q (re-formats to %q, %v)", value, formatted, err)
		}
	})
}
