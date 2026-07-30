package secretstore

import (
	"bytes"
	"errors"
	"testing"
)

func TestReferenceRoundTripAndInvalidForms(t *testing.T) {
	t.Parallel()

	reference, err := ParseReference("secret://provider/account/main")
	if err != nil {
		t.Fatalf("ParseReference() error = %v", err)
	}
	if reference.Namespace() != "provider" ||
		reference.ID() != "account/main" ||
		reference.String() != "secret://provider/account/main" {
		t.Fatalf("reference = %#v", reference)
	}
	for _, value := range []string{
		"",
		"provider/account",
		"secret://",
		"secret://provider",
		"secret://provider/../account",
		"secret://provider/account?value=x",
		"secret://provider/account value",
	} {
		if _, err := ParseReference(value); !errors.Is(err, ErrInvalidReference) {
			t.Fatalf("ParseReference(%q) error = %v, want ErrInvalidReference", value, err)
		}
	}
}

func TestValueOwnsBytesAndDestroyIsIdempotent(t *testing.T) {
	t.Parallel()

	input := []byte("secret-value")
	value, err := NewValue(input)
	if err != nil {
		t.Fatalf("NewValue() error = %v", err)
	}
	input[0] = 'X'
	first, err := value.CopyBytes()
	if err != nil {
		t.Fatalf("CopyBytes() error = %v", err)
	}
	if !bytes.Equal(first, []byte("secret-value")) {
		t.Fatalf("owned bytes = %q", first)
	}
	first[0] = 'Y'
	second, err := value.CopyBytes()
	if err != nil {
		t.Fatalf("second CopyBytes() error = %v", err)
	}
	if !bytes.Equal(second, []byte("secret-value")) {
		t.Fatalf("getter exposed internal storage: %q", second)
	}
	value.Destroy()
	value.Destroy()
	if _, err := value.CopyBytes(); !errors.Is(err, ErrDestroyed) {
		t.Fatalf("CopyBytes() after Destroy error = %v", err)
	}
}

func TestValueRejectsHeaderControlBytes(t *testing.T) {
	t.Parallel()

	for _, input := range [][]byte{
		{},
		[]byte("line\nbreak"),
		[]byte("line\rbreak"),
		{'a', 0, 'b'},
	} {
		if _, err := NewValue(input); err == nil {
			t.Fatalf("NewValue(%q) succeeded", input)
		}
	}
}

func TestMetadataAllowsOnlyCanonicalStateRevisionPairs(t *testing.T) {
	t.Parallel()

	valid := []Metadata{
		{State: StateConfigured, Revision: 1},
		{State: StateConfigured, Revision: MaxRevision},
		{State: StateMissing},
		{State: StateUnavailable},
		{State: StateUnavailable, Revision: 1},
		{State: StateUnavailable, Revision: MaxRevision},
	}
	for _, metadata := range valid {
		if err := metadata.Validate(); err != nil {
			t.Fatalf("Metadata.Validate(%+v) error = %v", metadata, err)
		}
	}
	invalid := []Metadata{
		{},
		{State: StateConfigured},
		{State: StateConfigured, Revision: MaxRevision + 1},
		{State: StateMissing, Revision: 1},
		{State: StateUnavailable, Revision: MaxRevision + 1},
	}
	for _, metadata := range invalid {
		if err := metadata.Validate(); err == nil {
			t.Fatalf("Metadata.Validate(%+v) succeeded", metadata)
		}
	}
}
