package egressprofile

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Catches changing legacy authored bytes or allowing malformed inheritance.
func TestChoicePreservesExplicitAndRejectsAlternateShapes(t *testing.T) {
	profile := Direct()
	choice, err := Explicit(profile)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(choice)
	if err != nil || string(got) != string(want) {
		t.Fatalf("legacy bytes %s err=%v", got, err)
	}
	var round Choice
	if err := json.Unmarshal(got, &round); err != nil || round != choice {
		t.Fatalf("roundtrip %+v %v", round, err)
	}
	got, err = json.Marshal(Inherit())
	if err != nil || string(got) != `{"mode":"inherit"}` {
		t.Fatalf("inherit %s %v", got, err)
	}
	for _, bad := range []string{`null`, `{}`, `{"Mode":"inherit"}`, `{"mode":"inherit","mode":"inherit"}`, `{"mode":"inherit","Mode":"inherit"}`, `{"mode":"inherit","profile":null}`, `{"mode":"direct"}`, `{"mode":null}`, `{"mode":"inherit"} {}`, `{"mode":"inherit","mo\u0064e":"inherit"}`} {
		if err := json.Unmarshal([]byte(bad), &round); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, err := json.Marshal(Choice{}); err == nil {
		t.Fatal("zero choice became inherit")
	}
	nullEndpoint := bytes.Replace(want, []byte(`"kind":"direct"`), []byte(`"kind":"direct","endpoint":null`), 1)
	if err := json.Unmarshal(nullEndpoint, &round); err == nil {
		t.Fatal("null nested profile field accepted")
	}
	profile.DisplayName = "Forged direct"
	if _, err := Explicit(profile); err == nil {
		t.Fatal("forged builtin accepted")
	}
}

func TestResolvedRejectsSourceRevisionAndBuiltinMismatch(t *testing.T) {
	for _, test := range []struct {
		source   SelectionSource
		revision uint64
		good     bool
	}{{SelectionAccount, 0, true}, {SelectionFlow, 0, true}, {SelectionGlobal, 1, true}, {SelectionBuiltin, 1, true}, {SelectionAccount, 1, false}, {SelectionGlobal, 0, false}, {"unknown", 1, false}, {SelectionBuiltin, 1 << 63, false}} {
		r, err := RestoreResolved(Direct(), test.source, test.revision)
		if (err == nil) != test.good {
			t.Errorf("source %s rev%d err=%v", test.source, test.revision, err)
		}
		if test.good && (r.Validate() != nil || r.Source() != test.source || r.DefaultRevision() != test.revision || !r.Profile().Equal(Direct())) {
			t.Fatal("resolved fact changed")
		}
	}
}
