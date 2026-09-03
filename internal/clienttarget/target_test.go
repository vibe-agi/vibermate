package clienttarget

import "testing"

func TestClaudeProfileBindsExplicitPrivateHTTPToCanonicalMessagesFlow(t *testing.T) {
	t.Parallel()
	profile, err := NewProfile(claudeClientID, EnvironmentFacts{
		AnthropicBaseURL: "http://127.0.0.1:23333",
	})
	if err != nil {
		t.Fatal(err)
	}
	target, available, err := profile.Resolve(nil, nil)
	if err != nil || !available {
		t.Fatalf("Resolve() target=%+v available=%t error=%v", target, available, err)
	}
	if target.ActualOrigin().String() != "http://127.0.0.1:23333" ||
		target.CanonicalOrigin().String() != defaultAnthropicOrigin ||
		!target.ContainsPath("/v1/messages") {
		t.Fatalf("resolved target = %+v", target)
	}
}

func TestEnvironmentOverlaySelectsTheTargetTheChildActuallyReceives(t *testing.T) {
	t.Parallel()
	profile, err := NewProfile(claudeClientID, EnvironmentFacts{
		AnthropicBaseURL: "https://ambient.example/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	target, available, err := profile.Resolve(
		map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:23333/api"},
		[]string{"ANTHROPIC_BASE_URL"},
	)
	if err != nil || !available {
		t.Fatalf("Resolve() available=%t error=%v", available, err)
	}
	if target.ActualOrigin().String() != "http://127.0.0.1:23333/api" ||
		!target.ContainsPath("/api/v1/messages") ||
		target.ContainsPath("/v1/messages") {
		t.Fatalf("overlay target = %+v", target)
	}
}

func TestTargetFactsNeverCarryCredentialValues(t *testing.T) {
	t.Parallel()
	const secret = "must-not-cross-control-seam"
	facts := FromEnvironment([]string{
		"ANTHROPIC_BASE_URL=http://127.0.0.1:23333",
		"ANTHROPIC_AUTH_TOKEN=" + secret,
		"OPENAI_API_KEY=" + secret,
		"UNRELATED_SECRET=" + secret,
	})
	if facts.AnthropicBaseURL != "http://127.0.0.1:23333" ||
		!facts.OpenAIAPIKeyPresent || facts.CodexAPIKeyPresent {
		t.Fatalf("target facts = %+v", facts)
	}
	for _, value := range []string{
		facts.AnthropicBaseURL,
		facts.CodexBaseURL,
		facts.OpenAIBaseURL,
	} {
		if value == secret {
			t.Fatal("a credential value crossed the client-target seam")
		}
	}
}

func TestUnknownClientCannotClaimAnExplicitSemanticTarget(t *testing.T) {
	t.Parallel()
	profile, err := NewProfile("unknown-agent", EnvironmentFacts{
		AnthropicBaseURL: "http://127.0.0.1:23333",
	})
	if err != nil {
		t.Fatal(err)
	}
	if target, available, err := profile.Resolve(nil, nil); err != nil || available || target.Available() {
		t.Fatalf("unknown client resolved target=%+v available=%t error=%v", target, available, err)
	}
}
