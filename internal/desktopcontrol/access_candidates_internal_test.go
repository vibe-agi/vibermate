package desktopcontrol

import (
	pathpkg "path"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accessapply"
)

func TestBuildAddedCandidateOwnsProviderAndAuthDefaults(t *testing.T) {
	aggregate := candidateTestAggregate(t)
	tests := []struct {
		name        string
		input       AddAccessCandidateInput
		wantOrigin  string
		wantAuth    access.AuthDriverRef
		wantBackend access.Dialect
		wantWire    access.UpstreamWireProfileRef
		wantError   bool
	}{
		{
			name: "official OpenAI",
			input: AddAccessCandidateInput{
				Name:     "OpenAI official",
				Provider: AccessCandidateProviderOpenAI,
				Model:    "gpt-5.2-codex",
			},
			wantOrigin:  "https://api.openai.com/v1",
			wantAuth:    access.StaticHeaderAuthDriverRef(),
			wantBackend: access.DialectOpenAIChat,
			wantWire:    access.FollowClientUpstreamWireProfileRef(),
		},
		{
			name: "compatible OpenAI relay",
			input: AddAccessCandidateInput{
				Name:     "OpenAI relay",
				Provider: AccessCandidateProviderOpenAICompatible,
				BaseURL:  "https://relay.example.test/v1",
				Model:    "gpt-5.2-codex",
			},
			wantOrigin:  "https://relay.example.test/v1",
			wantAuth:    access.StaticHeaderAuthDriverRef(),
			wantBackend: access.DialectOpenAIChat,
			wantWire:    access.FollowClientUpstreamWireProfileRef(),
		},
		{
			name: "official Anthropic",
			input: AddAccessCandidateInput{
				Name:     "Claude official",
				Provider: AccessCandidateProviderAnthropic,
				Model:    "claude-sonnet-4-5",
			},
			wantOrigin:  "https://api.anthropic.com",
			wantAuth:    access.AnthropicAPIKeyAuthDriverRef(),
			wantBackend: access.DialectAnthropicMessages,
			wantWire:    access.FollowClientUpstreamWireProfileRef(),
		},
		{
			name: "compatible bearer relay with explicit Claude presentation",
			input: AddAccessCandidateInput{
				Name:                 "Claude relay",
				Provider:             AccessCandidateProviderAnthropicCompatible,
				BaseURL:              "https://relay.example.test/anthropic",
				Model:                "claude-sonnet-4-5",
				AuthDriverRef:        access.AuthDriverStaticHeaderValue,
				UpstreamPresentation: access.UpstreamWireProfileClaudeCodeValue,
			},
			wantOrigin:  "https://relay.example.test/anthropic",
			wantAuth:    access.StaticHeaderAuthDriverRef(),
			wantBackend: access.DialectAnthropicMessages,
			wantWire:    access.ClaudeCodeUpstreamWireProfileRef(),
		},
		{
			name: "official origin cannot be overridden",
			input: AddAccessCandidateInput{
				Name:     "Misleading official",
				Provider: AccessCandidateProviderAnthropic,
				BaseURL:  "https://relay.example.test",
				Model:    "claude-sonnet-4-5",
			},
			wantError: true,
		},
		{
			name: "compatible origin is required",
			input: AddAccessCandidateInput{
				Name:     "Missing relay",
				Provider: AccessCandidateProviderAnthropicCompatible,
				Model:    "claude-sonnet-4-5",
			},
			wantError: true,
		},
		{
			name: "unknown upstream presentation is rejected",
			input: AddAccessCandidateInput{
				Name:                 "Unknown presentation",
				Provider:             AccessCandidateProviderOpenAI,
				Model:                "gpt-5.2-codex",
				UpstreamPresentation: "unknown-product",
			},
			wantError: true,
		},
		{
			name: "official OpenAI origin cannot be overridden",
			input: AddAccessCandidateInput{
				Name:     "Misleading OpenAI official",
				Provider: AccessCandidateProviderOpenAI,
				BaseURL:  "https://relay.example.test/v1",
				Model:    "gpt-5.2-codex",
			},
			wantError: true,
		},
		{
			name: "compatible OpenAI origin is required",
			input: AddAccessCandidateInput{
				Name:     "Missing OpenAI relay",
				Provider: AccessCandidateProviderOpenAICompatible,
				Model:    "gpt-5.2-codex",
			},
			wantError: true,
		},
		{
			name: "auth driver is closed",
			input: AddAccessCandidateInput{
				Name:          "Unknown auth",
				Provider:      AccessCandidateProviderAnthropicCompatible,
				BaseURL:       "https://relay.example.test",
				Model:         "claude-sonnet-4-5",
				AuthDriverRef: "browser_supplied_headers",
			},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate, err := buildAddedCandidate(aggregate, test.input)
			if test.wantError {
				if err == nil {
					t.Fatalf("buildAddedCandidate() = %+v, want error", candidate)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if candidate.origin.String() != test.wantOrigin ||
				candidate.authDriver != test.wantAuth ||
				candidate.backend != test.wantBackend ||
				candidate.wireProfile != test.wantWire {
				t.Fatalf("candidate = %+v", candidate)
			}
		})
	}
	responsesAccess := aggregate.Clone()
	responsesAccess.AgentEndpoint.ClientDialect = access.DialectOpenAIResponses
	if candidate, err := buildAddedCandidate(
		responsesAccess,
		AddAccessCandidateInput{
			Name:     "Codex official",
			Provider: AccessCandidateProviderOpenAI,
			Model:    "gpt-5.2-codex",
		},
	); err != nil || candidate.backend != access.DialectOpenAIChat {
		t.Fatalf("Responses Access rejected OpenAI candidate: candidate=%+v err=%v", candidate, err)
	}
	if candidate, err := buildAddedCandidate(
		responsesAccess,
		AddAccessCandidateInput{
			Name:     "Codex relay account",
			Provider: AccessCandidateProviderOpenAICompatible,
			BaseURL:  "https://codex-relay.example.test/v1/chat/completions",
			Model:    "gpt-5.2-codex",
		},
	); err != nil || candidate.origin.String() != "https://codex-relay.example.test/v1" {
		t.Fatalf("Responses Access rejected OpenAI relay: candidate=%+v err=%v", candidate, err)
	}
	if candidate, err := buildAddedCandidate(
		responsesAccess,
		AddAccessCandidateInput{
			Name:     "Wrong client dialect",
			Provider: AccessCandidateProviderAnthropic,
			Model:    "claude-sonnet-4-5",
		},
	); err == nil {
		t.Fatalf("Responses Access accepted Anthropic candidate: %+v", candidate)
	}
}

func TestBuildAddedCandidateNormalizesCopiedEndpointURLs(t *testing.T) {
	aggregate := candidateTestAggregate(t)
	tests := []struct {
		name       string
		provider   AccessCandidateProvider
		baseURL    string
		wantOrigin string
		relative   string
		wantPath   string
	}{
		{
			name:       "Anthropic version base",
			provider:   AccessCandidateProviderAnthropicCompatible,
			baseURL:    "https://relay.example.test/team/v1",
			wantOrigin: "https://relay.example.test/team",
			relative:   "v1/messages",
			wantPath:   "/team/v1/messages",
		},
		{
			name:       "Anthropic complete endpoint",
			provider:   AccessCandidateProviderAnthropicCompatible,
			baseURL:    "https://relay.example.test/team/v1/messages",
			wantOrigin: "https://relay.example.test/team",
			relative:   "v1/messages",
			wantPath:   "/team/v1/messages",
		},
		{
			name:       "OpenAI version base",
			provider:   AccessCandidateProviderOpenAICompatible,
			baseURL:    "https://relay.example.test/team/v1",
			wantOrigin: "https://relay.example.test/team/v1",
			relative:   "chat/completions",
			wantPath:   "/team/v1/chat/completions",
		},
		{
			name:       "OpenAI complete endpoint",
			provider:   AccessCandidateProviderOpenAICompatible,
			baseURL:    "https://relay.example.test/team/v1/chat/completions",
			wantOrigin: "https://relay.example.test/team/v1",
			relative:   "chat/completions",
			wantPath:   "/team/v1/chat/completions",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate, err := buildAddedCandidate(
				aggregate,
				AddAccessCandidateInput{
					Name:     test.name,
					Provider: test.provider,
					BaseURL:  test.baseURL,
					Model:    "test-model",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			path := pathpkg.Join(candidate.origin.BasePath(), test.relative)
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			if candidate.origin.String() != test.wantOrigin || path != test.wantPath {
				t.Fatalf("origin=%q finalPath=%q", candidate.origin.String(), path)
			}
		})
	}
}

func candidateTestAggregate(t *testing.T) access.Aggregate {
	t.Helper()
	input := validAccessApplyInput()
	command, err := accessapply.BuildCommand(input.Access.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	return command.Aggregate
}
