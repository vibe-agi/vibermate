package modelcatalog

import (
	"strings"
	"testing"
)

func TestModelIDMatchesEnvironmentMappingLimit(t *testing.T) {
	t.Parallel()

	if !validModelID(strings.Repeat("m", 256)) {
		t.Fatal("256-byte opaque model ID should fit an Environment mapping")
	}
	if validModelID(strings.Repeat("m", 257)) {
		t.Fatal("catalog must not advertise a model ID the Environment cannot save")
	}
}

func TestModelIDPreservesPrintableEdgeWhitespace(t *testing.T) {
	t.Parallel()

	if !validModelID(" relay custom:model ") {
		t.Fatal("printable edge whitespace is part of an opaque model ID")
	}
}
