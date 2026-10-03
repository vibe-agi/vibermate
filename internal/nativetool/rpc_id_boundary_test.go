package nativetool

import (
	"strings"
	"testing"
)

func TestNativeBindingLogicalRPCIDExactBoundary(t *testing.T) {
	for _, encoded := range []string{`"` + strings.Repeat("a", 512) + `"`, `"` + strings.Repeat(`\u0061`, 512) + `"`} {
		input := bindingFixture(t)
		input.Kind = RequestPing
		input.Intents = nil
		input.ObservedCallID = nil
		input.CatalogGeneration = 0
		input.CatalogDigest = nil
		input.ExactRPC = []byte(`{"jsonrpc":"2.0","id":` + encoded + `,"method":"ping"}`)
		if _, err := NewBinding(input); err != nil {
			t.Fatalf("logical512 rejected: %v", err)
		}
	}
}

func TestNativeRPCIDMalformedAndTypedCanonicalBoundary(t *testing.T) {
	for _, raw := range []string{`"` + strings.Repeat("a", 513) + `"`, `"` + strings.Repeat(`\u0061`, 513) + `"`, `1 2`, `1.0`, `1e0`, `9223372036854775808`, `null`, `true`, `{}`, `[]`, `""`} {
		if validRPCID([]byte(raw)) {
			t.Errorf("invalid ID %s", raw)
		}
	}
	for _, pair := range [][2]string{{`"a"`, `"\u0061"`}, {"\"\u754c\"", `"\u754c"`}} {
		if !validRPCID([]byte(pair[0])) || !validRPCID([]byte(pair[1])) || rpcIdentityDigest([]byte(pair[0])) != rpcIdentityDigest([]byte(pair[1])) {
			t.Fatal("equivalent escaped typed ID diverged")
		}
	}
	if rpcIdentityDigest([]byte(`1`)) == rpcIdentityDigest([]byte(`"1"`)) {
		t.Fatal("numeric/string identity collapsed")
	}
}
