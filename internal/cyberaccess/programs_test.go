package cyberaccess

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestApplyPreferencePreservesOtherFields(t *testing.T) {
	catalog, err := ParseCatalog([]byte(`{"models":[{"slug":"entitled","available_access_programs":{"cyber":["standard","daybreak_blue"]}},{"slug":"ordinary","available_access_programs":{"cyber":["standard"]}},{"slug":"unknown"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, model, access string
		changed             bool
	}{
		{"standard", "entitled", `,"access_programs":{"cyber":"standard","other":{"keep":true}}`, true},
		{"missing", "entitled", ``, true},
		{"empty", "entitled", `,"access_programs":{}`, true},
		{"already blue", "entitled", `,"access_programs":{"cyber":"daybreak_blue"}`, false},
		{"explicit red", "entitled", `,"access_programs":{"cyber":"daybreak_red"}`, true},
		{"unsupported model", "ordinary", `,"access_programs":{"cyber":"standard"}`, false},
		{"unknown model", "unknown", `,"access_programs":{"cyber":"standard"}`, false},
		{"unlisted alias", "entitled-latest", ``, false},
		{"client null overridden", "entitled", `,"access_programs":{"cyber":null}`, true},
		{"invalid object preserved", "entitled", `,"access_programs":null`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"model":"` + test.model + `","input":[{"role":"user","content":"hello"}],"opaque_counter":9007199254740993` + test.access + `}`)
			got, changed, err := ApplyPreference(body, catalog, Blue)
			if test.model != "entitled" || test.name == "invalid object preserved" {
				if err == nil {
					t.Fatal("unsupported mode or invalid object must fail")
				}
				return
			}
			if err != nil || changed != test.changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			if !changed {
				if !bytes.Equal(got, body) {
					t.Fatal("unchanged request was rewritten")
				}
				return
			}
			var before, after map[string]json.RawMessage
			_ = json.Unmarshal(body, &before)
			_ = json.Unmarshal(got, &after)
			for _, name := range []string{"model", "input", "opaque_counter"} {
				if !bytes.Equal(before[name], after[name]) {
					t.Fatalf("lost %s", name)
				}
			}
			var programs map[string]json.RawMessage
			_ = json.Unmarshal(after["access_programs"], &programs)
			if string(programs["cyber"]) != `"daybreak_blue"` {
				t.Fatal("blue program was not selected")
			}
			if test.name == "standard" && string(programs["other"]) != `{"keep":true}` {
				t.Fatal("another access program changed")
			}
		})
	}
}

func TestParseProgramsKeepsUnknownSeparateFromEmpty(t *testing.T) {
	for _, test := range []struct {
		raw     string
		want    []string
		invalid bool
	}{
		{``, nil, false}, {`null`, nil, false}, {`{}`, nil, false},
		{`{"cyber":[]}`, []string{}, false},
		{`{"cyber":["standard","daybreak_blue"]}`, []string{"standard", "daybreak_blue"}, false},
		{`{"cyber":["future_program"]}`, []string{"future_program"}, false},
		{`{"cyber":"daybreak_blue"}`, nil, true},
		{`{"cyber":[null]}`, nil, true}, {`{"cyber":["daybreak_blue","daybreak_blue"]}`, nil, true},
	} {
		got, err := ParsePrograms(json.RawMessage(test.raw))
		if (err != nil) != test.invalid || (!test.invalid && !reflect.DeepEqual(got, test.want)) {
			t.Fatalf("%s -> %#v, %v", test.raw, got, err)
		}
	}
}

func TestParseCatalogRejectsAmbiguousModelIdentity(t *testing.T) {
	for _, body := range []string{
		`{"models":[{"slug":"m"},{"slug":"m","available_access_programs":{"cyber":["daybreak_blue"]}}]}`,
		`{"models":[{"slug":""}]}`, `{"models":[{"slug":42}]}`, `{"models":null}`,
	} {
		if _, err := ParseCatalog([]byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestParseCatalogUsesTheSameWireIDAsModelDiscovery(t *testing.T) {
	catalog, err := ParseCatalog([]byte(`{"models":[{"id":"wire-model","slug":"display-alias","available_access_programs":{"cyber":["daybreak_blue"]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog["wire-model"]) != 1 || catalog["display-alias"] != nil {
		t.Fatalf("inconsistent model identity: %v", catalog)
	}
}
