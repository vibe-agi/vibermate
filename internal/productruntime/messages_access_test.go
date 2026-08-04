package productruntime

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
)

func TestProductionCompilerAcceptsAnthropicCompatibleAccess(t *testing.T) {
	t.Parallel()

	accessID, err := access.NewAccessID("anthropic-compatible-access")
	if err != nil {
		t.Fatal(err)
	}
	aggregate := runtimeAccessAggregate(t, accessID, 1, "Claude work account")
	origin, err := access.NewProviderOrigin("https://relay.example/anthropic")
	if err != nil {
		t.Fatal(err)
	}
	model, err := access.NewModelName("claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	aggregate.Profiles[0].Name = "Work relay"
	aggregate.Profiles[0].BackendDialect = access.DialectAnthropicMessages
	aggregate.Profiles[0].DefaultModelPolicy.FixedModel = model
	aggregate.ProviderTargets[0].Origin = origin
	aggregate.ProviderTargets[0].Protocol = access.DialectAnthropicMessages
	aggregate.AccountBindings[0].AuthDriverRef = access.AnthropicAPIKeyAuthDriverRef()

	compiler, err := productionAccessPlanCompiler()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	codec := plan.CodecPlan()
	if codec.ID().String() != anthropicchat.MessagesCodecPairID ||
		codec.ClientDialect() != access.DialectAnthropicMessages ||
		codec.ProviderDialect() != access.DialectAnthropicMessages {
		t.Fatalf("compiled codec = %+v", codec)
	}
}
