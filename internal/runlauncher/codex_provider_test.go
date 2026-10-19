package runlauncher

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

func providerPolicyGrant() capturecontrol.LaunchGrant {
	return capturecontrol.LaunchGrant{LaunchRecipe: clientadapter.LaunchCodexResponsesHTTP, ProtectedAuthorities: []string{"chatgpt.com:443", "api.openai.com:443"}, ManagedCredentialAuthorities: []string{"chatgpt.com:443"}}
}

func TestCodexProviderInvocationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		reject bool
	}{
		{"separate config", []string{"--config", `model_provider="other"`}, true},
		{"long attached config", []string{`--config=model_provider="other"`}, true},
		{"compact short config", []string{`-cmodel_provider="other"`}, true},
		{"equals compact config", []string{`-c=model_provider="other"`}, true},
		{"short cluster", []string{`-hcmodel_provider="other"`}, true},
		{"reserved table", []string{"-c", `model_providers.vibermate={name="other"}`}, true},
		{"whole provider table", []string{"-c", `model_providers={vibermate={name="other"}}`}, true},
		{"quoted reserved key", []string{"-c", `model_providers."vibermate".env_key="OTHER"`}, true},
		{"command bearer", []string{"-c", `model_providers.vibermate.experimental_bearer_token_command=["echo","secret"]`}, true},
		{"base config", []string{"-c", `openai_base_url="https://other.invalid"`}, true},
		{"oss", []string{"--oss"}, true},
		{"local provider", []string{"--local-provider=ollama"}, true},
		{"unknown option", []string{"--unknown-flag"}, true},
		{"unknown short", []string{"-zmfixture"}, true},
		{"model consumes value", []string{"--model", `model_provider="other"`}, false},
		{"compact model consumes value", []string{"-m--oss"}, false},
		{"profile consumes value", []string{"-pmodel_provider=other"}, false},
		{"image consumes flags as opaque value", []string{"--image=--oss"}, false},
		{"variadic image", []string{"--image", "one.png", `model_provider=other`}, false},
		{"prompt contents", []string{"prompt --oss -c model_provider=other"}, false},
		{"delimiter", []string{"--", "--oss", `-cmodel_provider=other`}, false},
		{"unrelated settings", []string{"-c", `model_reasoning_effort="high"`, "-c", `model_providers.other.env_key="OTHER"`, "--profile", "work", "--model", "fixture"}, false},
		{"matching base", []string{"-c", `openai_base_url="https://chatgpt.com/backend-api/codex/"`}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := append([]string{"exec"}, tc.args...)
			before := slices.Clone(original)
			env := []string{"CODEX_HOME=/synthetic/home", "OPENAI_API_KEY=synthetic-native", codexProviderTokenEnvironment + "=stale"}
			envBefore := slices.Clone(env)
			args, child, err := prepareCodexProvider(providerPolicyGrant(), "work", original, env, env)
			if tc.reject {
				if err == nil {
					t.Fatalf("conflicting invocation accepted: %v", original)
				}
				var typed *ManagedAuthError
				if !errors.As(err, &typed) && !errors.Is(err, ErrCodexIsolationUnsupported) {
					t.Fatalf("unexpected policy error: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(args, `model_provider="vibermate"`) {
					t.Fatal("provider selector missing")
				}
				if environmentValue(child, codexProviderTokenEnvironment) != clientCredentialPlaceholder {
					t.Fatal("placeholder missing")
				}
			}
			if !reflect.DeepEqual(original, before) || !reflect.DeepEqual(env, envBefore) {
				t.Fatal("input mutated")
			}
		})
	}
}

func TestCodexProviderManagedAuthorityIntersection(t *testing.T) {
	for _, tc := range []struct {
		name               string
		protected, managed []string
		want               string
	}{
		{"protected only", []string{"chatgpt.com:443"}, nil, ""},
		{"managed only", nil, []string{"chatgpt.com:443"}, ""},
		{"different sets", []string{"chatgpt.com:443"}, []string{"api.openai.com:443"}, ""},
		{"noncanonical", []string{"chatgpt.com:8443"}, []string{"chatgpt.com:8443"}, ""},
		{"unrelated managed", []string{"fixture.invalid:443"}, []string{"fixture.invalid:443"}, ""},
		{"api fallback", []string{"api.openai.com:443", "chatgpt.com:443"}, []string{"api.openai.com:443"}, "https://api.openai.com/v1"},
		{"chatgpt preference", []string{"api.openai.com:443", "chatgpt.com:443"}, []string{"api.openai.com:443", "chatgpt.com:443"}, "https://chatgpt.com/backend-api/codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := providerPolicyGrant()
			grant.ProtectedAuthorities = tc.protected
			grant.ManagedCredentialAuthorities = tc.managed
			args, _, err := prepareCodexProvider(grant, "work", []string{"exec"}, nil, nil)
			if tc.want == "" {
				if !errors.Is(err, ErrManagedAuthDestinationRequired) {
					t.Fatalf("authority accepted: %v", err)
				}
				return
			}
			if err != nil || !strings.Contains(strings.Join(args, "\n"), tc.want) {
				t.Fatalf("selection: %v %v", args, err)
			}
		})
	}
}

func TestCodexProviderBaseInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    []string
		reject bool
	}{
		{"matching base", []string{"OPENAI_BASE_URL=https://chatgpt.com/backend-api/codex/"}, false},
		{"conflicting openai", []string{"OPENAI_BASE_URL=https://other.invalid"}, true},
		{"conflicting codex", []string{"CODEX_BASE_URL=https://api.openai.com/v1"}, true},
		{"lower priority conflict", []string{"CODEX_BASE_URL=https://chatgpt.com/backend-api/codex", "OPENAI_BASE_URL=https://other.invalid"}, true},
		{"duplicate conflict", []string{"CODEX_BASE_URL=https://chatgpt.com/backend-api/codex", "CODEX_BASE_URL=https://other.invalid"}, true},
		{"invalid base", []string{"CODEX_BASE_URL=https://name:synthetic@chatgpt.com/backend-api/codex"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := prepareCodexProvider(providerPolicyGrant(), "work", []string{"exec"}, tc.env, tc.env)
			if tc.reject != (err != nil) {
				t.Fatalf("base policy: %v", err)
			}
			if err != nil && !errors.Is(err, ErrManagedAuthConflict) {
				t.Fatalf("unlocalized error: %v", err)
			}
		})
	}
}
