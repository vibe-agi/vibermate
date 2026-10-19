package cyberaccess

import (
	"bytes"
	"encoding/json"
	"errors"
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
		{"Standard-only model already Standard", "ordinary", `,"access_programs":{"cyber":"standard"}`, false},
		{"unknown model", "unknown", `,"access_programs":{"cyber":"standard"}`, false},
		{"unlisted alias", "entitled-latest", ``, false},
		{"client null overridden", "entitled", `,"access_programs":{"cyber":null}`, true},
		{"invalid object preserved", "entitled", `,"access_programs":null`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"model":"` + test.model + `","input":[{"role":"user","content":"hello"}],"opaque_counter":9007199254740993` + test.access + `}`)
			got, changed, err := ApplyPreference(body, catalog, Blue)
			if (test.model != "entitled" && test.model != "ordinary") || test.name == "invalid object preserved" {
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

// These literal fixtures catch rejecting verified Standard fallback, choosing the
// other Daybreak mode, trusting a different model, or losing raw request fields.
func TestApplyPreferenceResolvesExactModelProgram(t *testing.T) {
	for _, test := range []struct {
		name                 string
		mode                 Preference
		catalog              Catalog
		current, want        string
		changed, unavailable bool
	}{
		{name: "Blue Standard-only missing client mode", mode: Blue, catalog: Catalog{"ordinary": {"standard"}}, want: "standard", changed: true},
		{name: "Blue Standard-only client Standard unchanged", mode: Blue, catalog: Catalog{"ordinary": {"standard"}}, current: "standard", want: "standard"},
		{name: "Blue Standard-only client Blue", mode: Blue, catalog: Catalog{"ordinary": {"standard"}}, current: "daybreak_blue", want: "standard", changed: true},
		{name: "Blue Standard-only client Red", mode: Blue, catalog: Catalog{"ordinary": {"standard"}}, current: "daybreak_red", want: "standard", changed: true},
		{name: "Red Standard-only missing client mode", mode: Red, catalog: Catalog{"ordinary": {"standard"}}, want: "standard", changed: true},
		{name: "Red Standard-only client Standard unchanged", mode: Red, catalog: Catalog{"ordinary": {"standard"}}, current: "standard", want: "standard"},
		{name: "Red Standard-only client Blue", mode: Red, catalog: Catalog{"ordinary": {"standard"}}, current: "daybreak_blue", want: "standard", changed: true},
		{name: "Red Standard-only client Red", mode: Red, catalog: Catalog{"ordinary": {"standard"}}, current: "daybreak_red", want: "standard", changed: true},
		{name: "Blue preferred wins over Standard", mode: Blue, catalog: Catalog{"ordinary": {"standard", "daybreak_blue", "daybreak_red"}}, current: "daybreak_red", want: "daybreak_blue", changed: true},
		{name: "Red preferred wins over Standard", mode: Red, catalog: Catalog{"ordinary": {"standard", "daybreak_blue", "daybreak_red"}}, current: "daybreak_blue", want: "daybreak_red", changed: true},
		{name: "Blue other Daybreak with Standard", mode: Blue, catalog: Catalog{"ordinary": {"daybreak_red", "standard"}}, current: "daybreak_red", want: "standard", changed: true},
		{name: "Red other Daybreak with Standard", mode: Red, catalog: Catalog{"ordinary": {"daybreak_blue", "standard"}}, current: "daybreak_blue", want: "standard", changed: true},
		{name: "Blue other Daybreak alone unavailable", mode: Blue, catalog: Catalog{"ordinary": {"daybreak_red"}}, current: "daybreak_red", unavailable: true},
		{name: "Red other Daybreak alone unavailable", mode: Red, catalog: Catalog{"ordinary": {"daybreak_blue"}}, current: "daybreak_blue", unavailable: true},
		{name: "Blue nil catalog unavailable", mode: Blue, unavailable: true},
		{name: "Red nil catalog unavailable", mode: Red, unavailable: true},
		{name: "Blue missing model unavailable", mode: Blue, catalog: Catalog{}, unavailable: true},
		{name: "Red missing model unavailable", mode: Red, catalog: Catalog{}, unavailable: true},
		{name: "Blue nil programs unavailable", mode: Blue, catalog: Catalog{"ordinary": nil}, unavailable: true},
		{name: "Red nil programs unavailable", mode: Red, catalog: Catalog{"ordinary": nil}, unavailable: true},
		{name: "Blue empty programs unavailable", mode: Blue, catalog: Catalog{"ordinary": {}}, unavailable: true},
		{name: "Red empty programs unavailable", mode: Red, catalog: Catalog{"ordinary": {}}, unavailable: true},
		{name: "Blue different model cannot authorize Standard", mode: Blue, catalog: Catalog{"different": {"standard", "daybreak_blue"}}, unavailable: true},
		{name: "Red different model cannot authorize Standard", mode: Red, catalog: Catalog{"different": {"standard", "daybreak_red"}}, unavailable: true},
		{name: "Blue unknown program unavailable", mode: Blue, catalog: Catalog{"ordinary": {"future_program"}}, unavailable: true},
		{name: "Red unknown program unavailable", mode: Red, catalog: Catalog{"ordinary": {"future_program"}}, unavailable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := ""
			if test.current != "" {
				client = `"cyber":"` + test.current + `",`
			}
			body := []byte(`{ "model":"ordinary", "input":[{"role":"user","content":"hello"}], "opaque_counter":9007199254740993, "access_programs":{` + client + `"other":{"keep":true}} }`)
			got, changed, err := ApplyPreference(body, test.catalog, test.mode)
			if test.unavailable {
				if !errors.Is(err, ErrProgramUnavailable) || changed {
					t.Fatalf("want unavailable and unchanged, changed=%v err=%v", changed, err)
				}
				return
			}
			if err != nil || changed != test.changed {
				t.Fatalf("changed=%v want %v err=%v", changed, test.changed, err)
			}
			if !changed && !bytes.Equal(got, body) {
				t.Fatal("unchanged request bytes were rewritten")
			}
			var wire struct {
				Model   string          `json:"model"`
				Input   json.RawMessage `json:"input"`
				Counter json.RawMessage `json:"opaque_counter"`
				Access  struct {
					Cyber string          `json:"cyber"`
					Other json.RawMessage `json:"other"`
				} `json:"access_programs"`
			}
			if err := json.Unmarshal(got, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.Model != "ordinary" || string(wire.Input) != `[{"role":"user","content":"hello"}]` || string(wire.Counter) != "9007199254740993" || string(wire.Access.Other) != `{"keep":true}` || wire.Access.Cyber != test.want {
				t.Fatalf("unexpected outgoing request: %s", got)
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
