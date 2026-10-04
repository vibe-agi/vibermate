package productruntime

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"testing"
)

type clientToolPreparation interface {
	PrepareClientToolAccess(context.Context, environment.Environment, *environment.ClientToolAccessPolicy) (environment.ClientToolAccessPolicy, error)
}

func TestClientToolAccessUsesOriginalClientRegistration(t *testing.T) {
	ctx := context.Background()
	runtime, err := Start(ctx, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntime(t, runtime)
	builder, ok := any(runtime).(clientToolPreparation)
	if !ok {
		t.Fatal("runtime does not prepare original-client tool permission")
	}
	base, err := runtime.Environments().Get(ctx, environment.SystemTransparentID)
	if err != nil {
		t.Fatal(err)
	}
	candidate := base.Aggregate()
	candidate.ID = "work.tools"
	candidate.Name = "Work tools"
	candidate.Revision = 1
	policy, err := builder.PrepareClientToolAccess(ctx, candidate, &environment.ClientToolAccessPolicy{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Validate() != nil || policy.Route.TargetOrigin.String() != "https://chatgpt.com" || policy.Route.Path != "/backend-api/ps/mcp" || policy.Route.CredentialOwner != "original_client" || policy.Route.Contract != "codex_legacy_2025_06_18" {
		t.Fatalf("wrong original registration: %+v", policy)
	}
	matching := -1
	for index := range candidate.ClientEndpoints {
		if candidate.ClientEndpoints[index].ClientOrigin.String() == "https://chatgpt.com" {
			matching = index
		}
	}
	if matching < 0 {
		t.Fatal("fixture lacks ChatGPT client origin")
	}
	// Model destinations/accounts provide no authority for native login.
	model := candidate.Clone()
	for pi := range model.ClientEndpoints[matching].ProtocolPlans {
		plan := &model.ClientEndpoints[matching].ProtocolPlans[pi]
		plan.Destination = environment.DestinationPlan{Kind: environment.DestinationKindOriginal}
	}
	independent, err := builder.PrepareClientToolAccess(ctx, model, &environment.ClientToolAccessPolicy{Enabled: true})
	if err != nil || independent.Route.CredentialOwner != "original_client" || independent.Route.TargetOrigin != policy.Route.TargetOrigin {
		t.Fatalf("model destination changed tool login: %+v %v", independent, err)
	}
	proxy := egressnetwork.DefaultPolicy()
	proxy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "tool-egress.synthetic:1080"}
	profile, err := runtime.EgressProfiles().Publish(ctx, egressprofile.PublishCommand{ID: "profile.native", DisplayName: "Native network", Policy: proxy})
	if err != nil {
		t.Fatal(err)
	}
	unsaved := candidate.Clone()
	for pi := range unsaved.ClientEndpoints[matching].ProtocolPlans {
		unsaved.ClientEndpoints[matching].ProtocolPlans[pi].EgressProfile = profile
	}
	prepared, err := builder.PrepareClientToolAccess(ctx, unsaved, &environment.ClientToolAccessPolicy{Enabled: true})
	if err != nil || !prepared.Route.EgressProfile.Equal(profile) {
		t.Fatalf("ignored unsaved original client egress: %v", err)
	}
	ambiguous := unsaved.Clone()
	second := ambiguous.ClientEndpoints[matching].ProtocolPlans[0]
	second.EgressProfile = egressprofile.Direct()
	ambiguous.ClientEndpoints[matching].ProtocolPlans = append(ambiguous.ClientEndpoints[matching].ProtocolPlans, second)
	if _, err = builder.PrepareClientToolAccess(ctx, ambiguous, &environment.ClientToolAccessPolicy{Enabled: true}); err == nil {
		t.Fatal("ambiguous original-client egress silently selected first")
	}
	for _, origin := range []string{"https://chatgpt.com.evil.example", "https://api.openai.com", "https://staging.chatgpt.com"} {
		unproven := candidate.Clone()
		unproven.ClientEndpoints = unproven.ClientEndpoints[matching : matching+1]
		unproven.ClientEndpoints[0].ClientOrigin, _ = originidentity.ParseClientOrigin(origin)
		if _, err = builder.PrepareClientToolAccess(ctx, unproven, &environment.ClientToolAccessPolicy{Enabled: true}); err == nil {
			t.Fatalf("unregistered origin inferred: %s", origin)
		}
	}
	substituted := *policy.Route
	substituted.ID = "caller.route"
	if _, err = builder.PrepareClientToolAccess(ctx, candidate, &environment.ClientToolAccessPolicy{Enabled: true, Route: &substituted}); err == nil {
		t.Fatal("caller provided new authority")
	}
	candidate.ClientEndpoints = nil
	if _, err := builder.PrepareClientToolAccess(ctx, candidate, &environment.ClientToolAccessPolicy{Enabled: true}); err == nil {
		t.Fatal("missing original-client egress was guessed")
	}
}
