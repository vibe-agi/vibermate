package protocolcore

import (
	"strings"
	"testing"
)

func TestResourceJSONPreflightRejectsDenseNamesWithoutMaps(t *testing.T) {
	document := []byte(`{"items":[` + strings.Repeat(`{"a":""},`, 100000) + `{"tail":true}]}`)
	limit := ResourceCost{64 << 20, 1024}
	allocations := testing.AllocsPerRun(3, func() {
		if err := ValidateJSONWithin(document, limit); err == nil {
			t.Fatal("dense JSON accepted")
		}
	})
	if allocations > 16 {
		t.Fatalf("dense names allocated before resource refusal: %f allocations", allocations)
	}
}

func TestResourceJSONGrammarAndNames(t *testing.T) {
	limit := ResourceCost{64 << 20, 64 << 20}
	for _, document := range []string{`{"a":"\\\"","b":"\uD834\uDD1E","c":"世界"}`, strings.Repeat("[", 9999) + "0" + strings.Repeat("]", 9999)} {
		if err := ValidateJSONWithin([]byte(document), limit); err != nil {
			t.Fatalf("valid JSON refused: %v", err)
		}
	}
	for _, document := range []string{`{"a":1,"A":2}`, `{"ſ":1,"S":2}`, `{"a":1,"\u0061":2}`, `{"ok":1} false`, `[1,]`, `{"a":"unterminated}`, `[true fals]`} {
		if err := ValidateJSONWithin([]byte(document), limit); err == nil {
			t.Fatalf("invalid JSON accepted: %s", document)
		}
	}
}
