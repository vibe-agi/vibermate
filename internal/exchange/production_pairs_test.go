package exchange

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/anthropicresponses"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/responseschat"
)

// productionPairs are the pairs of productruntime's production table. That
// table cannot be read from here, because productruntime imports this
// package, so both sides are pinned to the same package constants:
// productruntime's TestProductionProtocolPathTableIsTheExistingFourAndTheTranslatedPair
// pins the table to exactly this list, in this order.
var productionPairs = []struct {
	id       string
	revision protocolspec.Revision
	client   protocolspec.Dialect
	provider protocolspec.Dialect
}{
	{anthropicchat.CodecPairID, anthropicchat.CodecRevision, protocolspec.DialectAnthropicMessages, protocolspec.DialectOpenAIChat},
	{anthropicchat.MessagesCodecPairID, anthropicchat.MessagesCodecRevision, protocolspec.DialectAnthropicMessages, protocolspec.DialectAnthropicMessages},
	{responseschat.CodecPairID, responseschat.CodecRevision, protocolspec.DialectOpenAIResponses, protocolspec.DialectOpenAIChat},
	{responseschat.ResponsesPassthroughCodecPairID, responseschat.ResponsesPassthroughCodecRevision, protocolspec.DialectOpenAIResponses, protocolspec.DialectOpenAIResponses},
	{anthropicresponses.CodecPairID, anthropicresponses.CodecRevision, protocolspec.DialectAnthropicMessages, protocolspec.DialectOpenAIResponses},
}

// The test catalog and the test selector serve exactly the production pairs:
// every dialect pair the catalog resolves is one of them, and the selector
// serves every operation of each.
func TestTestCatalogAndSelectorHoldTheProductionPairs(t *testing.T) {
	t.Parallel()
	catalog := testProtocolCatalog(t)
	selector := mustProtocolPathSelector(t)
	dialects := []protocolspec.Dialect{
		protocolspec.DialectAnthropicMessages,
		protocolspec.DialectOpenAIChat,
		protocolspec.DialectOpenAIResponses,
	}
	resolved := 0
	for _, client := range dialects {
		for _, provider := range dialects {
			plan, err := catalog.Resolve(client, provider)
			var want *struct {
				id       string
				revision protocolspec.Revision
				client   protocolspec.Dialect
				provider protocolspec.Dialect
			}
			for index := range productionPairs {
				if productionPairs[index].client == client && productionPairs[index].provider == provider {
					want = &productionPairs[index]
				}
			}
			if want == nil {
				if err == nil {
					t.Fatalf("%s -> %s: the test catalog resolves %s, which production lacks", client, provider, plan.ID().String())
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s -> %s: the test catalog lacks %s: %v", client, provider, want.id, err)
			}
			if plan.ID().String() != want.id || plan.Revision() != want.revision {
				t.Fatalf("%s -> %s: the test catalog resolves %s revision %d, production %s revision %d",
					client, provider, plan.ID().String(), plan.Revision(), want.id, want.revision)
			}
			for _, operation := range plan.ClientOperations() {
				path, err := selector.Select(plan, operation.ID())
				if err != nil {
					t.Fatalf("%s: the test selector refused %s: %v", want.id, operation.ID().String(), err)
				}
				if err := path.ValidatePlan(plan); err != nil {
					t.Fatalf("%s: the test selector chose a path for another plan: %v", want.id, err)
				}
			}
			resolved++
		}
	}
	if resolved != len(productionPairs) {
		t.Fatalf("the test catalog resolves %d pairs, production has %d", resolved, len(productionPairs))
	}
}
