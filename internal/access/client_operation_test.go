package access_test

import (
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
)

func TestClientOperationConstructorOwnsAndCanonicalizesCollections(
	t *testing.T,
) {
	t.Parallel()

	identifier, err := access.NewClientOperationID("responses-create")
	if err != nil {
		t.Fatal(err)
	}
	methods := []string{http.MethodPost}
	queries := []string{"mode=fast", "include=usage"}
	definition, err := access.NewClientOperationDefinition(
		access.ClientOperationOptions{
			ID:             identifier,
			Revision:       3,
			ClientDialect:  access.DialectOpenAIResponses,
			Methods:        methods,
			PathPattern:    "/v1/responses",
			PathMatch:      access.ClientOperationPathExact,
			Kind:           access.ClientOperationSemantic,
			Transport:      access.ClientOperationTransportHTTP,
			BodyKind:       access.ClientOperationBodyJSON,
			ReplayClass:    access.ClientReplayGenerationCostOnly,
			CodecFeature:   "responses",
			MaxBodyBytes:   16 << 20,
			AllowedQueries: queries,
			EgressBearing:  true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	methods[0] = http.MethodDelete
	queries[0] = "mutated=true"
	returnedMethods := definition.Methods()
	returnedQueries := definition.AllowedQueries()
	returnedMethods[0] = http.MethodGet
	returnedQueries[0] = "mutated=again"
	if definition.Methods()[0] != http.MethodPost {
		t.Fatal("client operation retained a method alias")
	}
	if definition.Transport() != access.ClientOperationTransportHTTP {
		t.Fatalf("client operation transport = %q", definition.Transport())
	}
	if got := definition.AllowedQueries(); len(got) != 2 ||
		got[0] != "include=usage" ||
		got[1] != "mode=fast" {
		t.Fatalf("canonical allowed queries = %v", got)
	}
}

func TestClientOperationConstructorRejectsUnsafeCapabilities(t *testing.T) {
	t.Parallel()

	identifier, err := access.NewClientOperationID("invalid-operation")
	if err != nil {
		t.Fatal(err)
	}
	base := access.ClientOperationOptions{
		ID:            identifier,
		Revision:      1,
		ClientDialect: access.DialectOpenAIResponses,
		Methods:       []string{http.MethodPost},
		PathPattern:   "/v1/responses",
		PathMatch:     access.ClientOperationPathExact,
		Kind:          access.ClientOperationSemantic,
		Transport:     access.ClientOperationTransportHTTP,
		BodyKind:      access.ClientOperationBodyJSON,
		ReplayClass:   access.ClientReplayGenerationCostOnly,
		CodecFeature:  "responses",
		MaxBodyBytes:  16 << 20,
		EgressBearing: true,
	}
	tests := []struct {
		name   string
		mutate func(*access.ClientOperationOptions)
	}{
		{
			name: "unknown transport",
			mutate: func(options *access.ClientOperationOptions) {
				options.Transport = "unknown"
			},
		},
		{
			name: "lowercase method",
			mutate: func(options *access.ClientOperationOptions) {
				options.Methods = []string{"post"}
			},
		},
		{
			name: "encoded path",
			mutate: func(options *access.ClientOperationOptions) {
				options.PathPattern = "/v1/%72esponses"
			},
		},
		{
			name: "semantic prefix",
			mutate: func(options *access.ClientOperationOptions) {
				options.PathMatch = access.ClientOperationPathPrefix
			},
		},
		{
			name: "duplicate method",
			mutate: func(options *access.ClientOperationOptions) {
				options.Methods = []string{http.MethodPost, http.MethodPost}
			},
		},
		{
			name: "semantic without egress",
			mutate: func(options *access.ClientOperationOptions) {
				options.EgressBearing = false
			},
		},
		{
			name: "unsupported with codec",
			mutate: func(options *access.ClientOperationOptions) {
				options.Kind = access.ClientOperationUnsupported
				options.CodecFeature = "responses"
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := base
			test.mutate(&options)
			if _, err := access.NewClientOperationDefinition(options); err == nil {
				t.Fatal("unsafe client operation was accepted")
			}
		})
	}
}
