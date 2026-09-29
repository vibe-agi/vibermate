package protocolcore

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateJSONNamesRejectsNamesEncodingJSONWouldMerge(t *testing.T) {
	for _, document := range []string{
		`{"type":"a","type":"b"}`,
		`{"type":"tool_use","TYPE":"server_tool_use"}`,
		`{"a":{"stop_reason":"x","Stop_Reason":"y"}}`,
		`[{"ok":1},{"model":"a","MODEL":"b"}]`,
		// U+212A KELVIN SIGN and U+017F LATIN SMALL LETTER LONG S fold to k and s.
		"{\"kind\":1,\"Kind\":2}",
		"{\"stop\":1,\"ſtop\":2}",
	} {
		if err := ValidateJSONNames([]byte(document)); err == nil {
			t.Errorf("accepted ambiguous document %s", document)
		}
	}
}

func TestValidateJSONNamesAgreesWithEncodingJSONBinding(t *testing.T) {
	// Every pair accepted as distinct must also be bound separately by
	// encoding/json; otherwise the check would not close the ambiguity.
	type probe struct {
		Kind string `json:"kind"`
	}
	for _, name := range []string{"kind", "KIND", "Kind", "Kind", "kınd", "kind\u0000"} {
		var value probe
		_ = json.Unmarshal([]byte(`{"`+name+`":"x"}`), &value)
		binds := value.Kind == "x"
		folds := foldJSONName(name) == foldJSONName("kind")
		if binds != folds {
			t.Errorf("name %q: encoding/json binds=%t, fold equal=%t", name, binds, folds)
		}
	}
}

func TestValidateJSONNamesAcceptsDistinctNamesAndRejectsTrailingData(t *testing.T) {
	if err := ValidateJSONNames([]byte(`{"type":"a","types":[{"type":1},{"type":2}],"n":null}`)); err != nil {
		t.Fatal(err)
	}
	for _, document := range []string{`{"a":1} {"b":2}`, `{"a":1`, `[1,2`, ``} {
		if err := ValidateJSONNames([]byte(document)); err == nil {
			t.Errorf("accepted malformed document %q", document)
		}
	}
	if err := ValidateJSONNames([]byte(`{"TYPE":"x","type":"y"}`)); err == nil ||
		!strings.Contains(err.Error(), "differ only by case") {
		t.Fatalf("case-folded names error = %v", err)
	}
}
