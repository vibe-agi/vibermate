package productruntime

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

func TestUnavailableWireVariantStillProducesPresentationOnlyActivityEvidence(
	t *testing.T,
) {
	t.Parallel()

	compiler, err := productionAccessPlanCompiler()
	if err != nil {
		t.Fatal(err)
	}
	accessID, err := access.NewAccessID("access-presentation-evidence")
	if err != nil {
		t.Fatal(err)
	}
	aggregate := runtimeAccessAggregate(t, accessID, 1, "Presentation evidence")
	aggregate.Profiles[0].UpstreamWireProfileRef =
		access.ClaudeCodeUpstreamWireProfileRef()
	plan, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	presentation := providertransport.NewWirePresentationEvidence(
		plan.UpstreamWireProfile(),
		access.ApplicationProtocolHTTP2,
	)
	evidence := activityTransportEvidence(
		presentation,
		transportprofile.Evidence{},
	)
	if evidence == nil || evidence.Presentation == nil {
		t.Fatal("missing presentation-only Activity evidence")
	}
	if evidence.Presentation.RequestedRef !=
		access.ClaudeCodeUpstreamWireProfileRef().String() ||
		evidence.Presentation.EffectiveRef != "" ||
		evidence.Presentation.ClientProtocol != "h2" ||
		evidence.Presentation.UpstreamProtocol != "" ||
		evidence.Requested != nil {
		t.Fatalf("presentation-only Activity evidence = %+v", evidence)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("presentation-only Activity evidence is invalid: %v", err)
	}
}
