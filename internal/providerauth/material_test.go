package providerauth

import (
	"errors"
	"testing"
)

func TestParseMaterialRejectsEveryNonWhitespaceTrailingValue(t *testing.T) {
	t.Parallel()

	material, err := NewMaterial(
		"provider-secret",
		map[string]string{"X-Relay-Tenant": "team-a"},
		nil,
	)
	if err != nil {
		t.Fatalf("NewMaterial() error = %v", err)
	}
	encoded, err := material.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary() error = %v", err)
	}
	for _, suffix := range []string{"true", "0", `[]`, `{}`, "broken"} {
		t.Run(suffix, func(t *testing.T) {
			input := append(append([]byte(nil), encoded...), suffix...)
			parsed, parseErr := ParseMaterial(input)
			parsed.Destroy()
			if !errors.Is(parseErr, ErrInvalidAuthentication) {
				t.Fatalf(
					"ParseMaterial(trailing %q) error = %v, want ErrInvalidAuthentication",
					suffix,
					parseErr,
				)
			}
		})
	}
}
