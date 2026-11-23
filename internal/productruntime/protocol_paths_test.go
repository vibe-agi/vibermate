package productruntime

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/anthropicresponses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/responseschat"
)

// TestProductionCatalogAndSelectorAgree checks that the Environment
// compiler's catalog and the Exchange's selector hold the same protocol
// paths, and that every constructed path matches its catalog definition.
func TestProductionCatalogAndSelectorAgree(t *testing.T) {
	t.Parallel()
	specs, err := productionProtocolPaths()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := productionProtocolCatalog()
	if err != nil {
		t.Fatal(err)
	}
	limits := protocolcore.DefaultResourceLimits()
	for _, resources := range []*protocolcore.ResourceLimits{nil, &limits} {
		selector, err := productionProtocolSelector(resources)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range specs {
			definition := spec.Definition
			plan, err := catalog.Resolve(definition.ClientDialect, definition.ProviderDialect)
			if err != nil {
				t.Fatalf("%s: catalog has no entry: %v", definition.ID.String(), err)
			}
			if plan.ID() != definition.ID || plan.Revision() != definition.Revision {
				t.Fatalf("%s: catalog resolves %s revision %d", definition.ID.String(), plan.ID().String(), plan.Revision())
			}
			path, err := spec.Build(resources)
			if err != nil {
				t.Fatal(err)
			}
			if path.Client().Dialect() != definition.ClientDialect || path.Backend().Dialect() != definition.ProviderDialect {
				t.Fatalf("%s: path dialects %s -> %s", definition.ID.String(), path.Client().Dialect(), path.Backend().Dialect())
			}
			if err := path.ValidatePlan(plan); err != nil {
				t.Fatalf("%s: constructed path does not serve its catalog plan: %v", definition.ID.String(), err)
			}
			operations := path.ClientOperationIDs()
			wanted := slices.Clone(definition.ClientOperationIDs)
			slices.SortFunc(wanted, func(left, right protocolspec.ClientOperationID) int {
				return strings.Compare(left.String(), right.String())
			})
			if !slices.Equal(operations, wanted) {
				t.Fatalf("%s: path operations %v, definition %v", definition.ID.String(), operations, wanted)
			}
			for _, operation := range definition.ClientOperationIDs {
				selected, err := selector.Select(plan, operation)
				if err != nil {
					t.Fatalf("%s: selector refused %s: %v", definition.ID.String(), operation.String(), err)
				}
				if selected.ValidatePlan(plan) != nil {
					t.Fatalf("%s: selector chose a path for another plan", definition.ID.String())
				}
			}
		}
	}
	// Every dialect pair the catalog resolves comes from the table, so the
	// selector cannot lack a path the compiler accepts.
	dialects := []protocolspec.Dialect{
		protocolspec.DialectAnthropicMessages,
		protocolspec.DialectOpenAIChat,
		protocolspec.DialectOpenAIResponses,
	}
	for _, client := range dialects {
		for _, provider := range dialects {
			_, err := catalog.Resolve(client, provider)
			inTable := slices.ContainsFunc(specs, func(spec protocolPathSpec) bool {
				return spec.Definition.ClientDialect == client && spec.Definition.ProviderDialect == provider
			})
			if inTable != (err == nil) {
				t.Fatalf("%s -> %s: in table=%v, catalog error=%v", client, provider, inTable, err)
			}
			if err != nil && !errors.Is(err, protocolspec.ErrUnsupportedCodecPair) {
				t.Fatalf("%s -> %s: %v", client, provider, err)
			}
		}
	}
}

// The table is the four paths that existed before it, in the catalog order
// the compiler used, followed by the translated Anthropic Messages to OpenAI
// Responses path. Only that path refuses a ChatGPT-plan target.
func TestProductionProtocolPathTableIsTheExistingFourAndTheTranslatedPair(t *testing.T) {
	t.Parallel()
	specs, err := productionProtocolPaths()
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		id       string
		revision protocolspec.Revision
		chatGPT  chatGPTTargetRule
	}{
		{anthropicchat.CodecPairID, anthropicchat.CodecRevision, chatGPTTargetNative},
		{anthropicchat.MessagesCodecPairID, anthropicchat.MessagesCodecRevision, chatGPTTargetNative},
		{responseschat.CodecPairID, responseschat.CodecRevision, chatGPTTargetNative},
		{responseschat.ResponsesPassthroughCodecPairID, responseschat.ResponsesPassthroughCodecRevision, chatGPTTargetNative},
		{anthropicresponses.CodecPairID, anthropicresponses.CodecRevision, chatGPTTargetRefused},
	}
	if len(specs) != len(want) {
		t.Fatalf("table has %d paths, want %d", len(specs), len(want))
	}
	for index, spec := range specs {
		if spec.Definition.ID.String() != want[index].id || spec.Definition.Revision != want[index].revision {
			t.Fatalf("table[%d] = %s revision %d", index, spec.Definition.ID.String(), spec.Definition.Revision)
		}
		if spec.ChatGPTTarget != want[index].chatGPT {
			t.Fatalf("%s ChatGPT target rule = %q, want %q", spec.Definition.ID.String(), spec.ChatGPTTarget, want[index].chatGPT)
		}
		if len(spec.Definition.RequiredCapabilities) != 3 {
			t.Fatalf("%s required capabilities = %v", spec.Definition.ID.String(), spec.Definition.RequiredCapabilities)
		}
		path, err := spec.Build(nil)
		if err != nil {
			t.Fatal(err)
		}
		// Only the composed path carries the refused rule.
		if path.IsComposed() != (spec.ChatGPTTarget == chatGPTTargetRefused) {
			t.Fatalf("%s composed=%v but ChatGPT rule %q", spec.Definition.ID.String(), path.IsComposed(), spec.ChatGPTTarget)
		}
	}
}
