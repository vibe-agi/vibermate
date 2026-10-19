package cyberaccess

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestPreferenceRoundTripsCanonicalWireModes(t *testing.T) {
	for _, wire := range []string{"standard", "daybreak_blue", "daybreak_red"} {
		value, err := ParsePreference(wire)
		if err != nil || !value.Valid() || value.WireValue() != wire {
			t.Fatalf("%s -> %q, %v", wire, value, err)
		}
	}
	if Standard != Preference("") || Standard.WireValue() != "standard" {
		t.Fatal("zero preference must select Standard")
	}
	for _, wire := range []string{"", "follow_client", "true", "false", "null", "auto", "DAYBREAK_BLUE", "future_program"} {
		if _, err := ParsePreference(wire); err == nil {
			t.Fatalf("accepted invalid external mode %q", wire)
		}
	}
}

func TestApplyPreferenceSelectsOneSupportedMode(t *testing.T) {
	catalog := Catalog{"general": {"standard", "daybreak_blue"}, "cyber": {"daybreak_red"}}
	for _, test := range []struct {
		name, model, current string
		mode                 Preference
		want                 string
		changed              bool
	}{

		{"standard replaces Blue", "general", "daybreak_blue", Standard, "standard", true},
		{"Blue replaces standard", "general", "standard", Blue, "daybreak_blue", true},
		{"Red replaces standard", "cyber", "standard", Red, "daybreak_red", true},
		{"Red replaces Blue", "cyber", "daybreak_blue", Red, "daybreak_red", true},
		{"same mode is untouched", "cyber", "daybreak_red", Red, "daybreak_red", false},
		{"Red falls back to advertised Standard rather than Blue", "general", "standard", Red, "standard", false},
		{"standard overrides client", "cyber", "daybreak_red", Standard, "standard", true},
		{"unknown model", "absent", "standard", Blue, "", false},
		{"client value is overridden", "general", "future_program", Blue, "daybreak_blue", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"model":"` + test.model + `","input":"hi","access_programs":{"cyber":"` + test.current + `"}}`)
			got, changed, err := ApplyPreference(body, catalog, test.mode)
			if test.want == "" {
				if err != ErrProgramUnavailable {
					t.Fatalf("want unavailable mode error, got %v", err)
				}
				return
			}
			if err != nil || changed != test.changed {
				t.Fatalf("changed=%v error=%v", changed, err)
			}
			var decoded struct {
				Access struct {
					Cyber string `json:"cyber"`
				} `json:"access_programs"`
			}
			if json.Unmarshal(got, &decoded) != nil || decoded.Access.Cyber != test.want {
				t.Fatalf("body=%s", got)
			}
			if !changed && !bytes.Equal(body, got) {
				t.Fatal("unchanged body was rewritten")
			}
		})
	}
}
