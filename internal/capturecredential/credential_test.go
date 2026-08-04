package capturecredential

import (
	"fmt"
	"strings"
	"testing"
)

func TestCredentialKindsAreDisjointAndRoundTrip(t *testing.T) {
	t.Parallel()
	entropy := make([]byte, EntropyBytes)
	for index := range entropy {
		entropy[index] = byte(index + 1)
	}
	managed, err := New(KindManagedRun, entropy)
	if err != nil {
		t.Fatal(err)
	}
	manual, err := New(KindManualCapture, entropy)
	if err != nil {
		t.Fatal(err)
	}
	if managed.Value() == manual.Value() {
		t.Fatal("credential kind did not change the wire namespace")
	}
	for _, credential := range []Credential{managed, manual} {
		parsed, err := Parse(credential.Value())
		if err != nil || !parsed.Valid() || parsed.Kind() != credential.Kind() ||
			parsed.Value() != credential.Value() {
			t.Fatalf("parsed credential=%+v err=%v", parsed, err)
		}
		if strings.Contains(fmt.Sprint(credential), credential.Value()) ||
			strings.Contains(fmt.Sprintf("%#v", credential), credential.Value()) {
			t.Fatal("credential formatting exposed its wire value")
		}
	}
}

func TestCredentialRejectsUnknownOrNonCanonicalShapes(t *testing.T) {
	t.Parallel()
	valid, err := New(KindManagedRun, make([]byte, EntropyBytes))
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.TrimPrefix(valid.Value(), "run_")
	for _, value := range []string{
		"",
		"unknown_" + suffix,
		"run_" + suffix + "=",
		"run_" + suffix[:len(suffix)-1],
		"manual_" + suffix + "extra",
	} {
		if _, err := Parse(value); err == nil {
			t.Fatalf("accepted invalid credential %q", value)
		}
	}
}
