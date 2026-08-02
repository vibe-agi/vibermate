package operationcatalog_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
)

// M1.0-C0a step 3 freezes a state, and a state that only prose describes is
// one a later entry can leave without anyone noticing. These are the freeze,
// written where a new catalogue entry has to pass them.
//
// Design 12 §3.2: basic Preview's `count_tokens` may be `local | estimated |
// unsupported`; it implements no `profile_endpoint` and carries no Language
// Bridge.

// The P0 cut rests on one bit meaning two things at once: a class that may
// reach the inbound origin is exactly a class that carries no client content.
// Asserting no catalogue entry is both would restate that, because the two
// predicates are written as complements and no value can satisfy both. What is
// worth holding is the complement itself, since a class added to one switch
// and forgotten in the other would split the bit and let a body through.
func TestOriginalOriginAdmissionIsExactlyTheAbsenceOfClientPayload(t *testing.T) {
	t.Parallel()

	classes := []access.OperationPayloadClass{
		access.OperationPayloadNone,
		access.OperationPayloadControl,
		access.OperationPayloadClientData,
		access.OperationPayloadClientSemantic,
		access.OperationPayloadUnknown,
	}
	for _, class := range classes {
		if class.AllowsOriginalOrigin() == class.CarriesClientPayload() {
			t.Errorf(
				"payload class %q admits the original origin and carries "+
					"client payload inconsistently: allows=%v carries=%v",
				class,
				class.AllowsOriginalOrigin(),
				class.CarriesClientPayload(),
			)
		}
	}
}

// A body-bearing method on a catalogued operation must have a payload class
// that says so. An entry that declares `none` or `control` while accepting a
// POST would take the original-origin path with a body.
func TestCataloguedBodyBearingOperationsDeclareAPayloadClass(t *testing.T) {
	t.Parallel()

	catalog, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	bodyBearing := []string{
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
	}
	for _, definition := range catalog.Definitions() {
		if definition.BodyKind() == access.ClientOperationBodyNone {
			continue
		}
		carries := false
		for _, method := range definition.Methods() {
			if slices.Contains(bodyBearing, method) {
				carries = true
			}
		}
		if !carries {
			continue
		}
		if definition.PayloadClass().AllowsOriginalOrigin() {
			t.Errorf(
				"%q accepts a body on %v but declares payload class %q, "+
					"which admits the original origin",
				definition.ID(),
				definition.Methods(),
				definition.PayloadClass(),
			)
		}
	}
}

// The named counterexample the design puts first. It is auxiliary by kind and
// client_semantic by payload class, and both forms the client actually sends
// resolve to the same entry.
func TestCountTokensStaysTheFrozenCounterexample(t *testing.T) {
	t.Parallel()

	catalog, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	var found *access.ClientOperationDefinition
	for _, definition := range catalog.Definitions() {
		if definition.ID().String() ==
			operationcatalog.AnthropicMessagesCountTokensID {
			copied := definition
			found = &copied
		}
	}
	if found == nil {
		t.Fatal("count_tokens is not catalogued")
	}
	if found.Kind() != access.ClientOperationAuxiliary {
		t.Errorf("kind is %q, want auxiliary", found.Kind())
	}
	if found.PayloadClass() != access.OperationPayloadClientSemantic {
		t.Errorf(
			"payload class is %q, want client_semantic",
			found.PayloadClass(),
		)
	}
	// The client sends the beta form, so an entry that stopped matching it
	// would silently move that body onto the uncatalogued path.
	if !slices.Contains(found.AllowedQueries(), "beta=true") {
		t.Errorf(
			"beta=true is not an allowed query: %v",
			found.AllowedQueries(),
		)
	}
}
