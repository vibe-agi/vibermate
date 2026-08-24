package runlauncher

import (
	"slices"
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"net/url"
	"strings"
)

func TestBuildChildArgumentsKeepsNonCodexInvocationsExact(t *testing.T) {
	t.Parallel()

	for _, recipe := range []clientadapter.LaunchRecipe{
		clientadapter.LaunchGeneric,
		clientadapter.LaunchNodeEnvProxy,
	} {
		command := []string{"agent", "first", "two words"}
		arguments, err := buildChildArguments(
			command,
			nil,
			recipe,
		)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(arguments, command[1:]) {
			t.Fatalf("%s arguments = %v", recipe, arguments)
		}
		command[1] = "mutated"
		if arguments[0] != "first" {
			t.Fatal("child arguments retained caller-owned command storage")
		}
	}
}

func TestBuildChildArgumentsPinsCodexToResponsesHTTP(t *testing.T) {
	t.Parallel()

	arguments, err := buildChildArguments(
		[]string{"codex", "exec", "--json", "probe"},
		nil,
		clientadapter.LaunchCodexResponsesHTTP,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := []string{
		"--config", `model_provider="openai"`,
		"--config", `openai_base_url="https://chatgpt.com/backend-api/codex"`,
		"--config", `features.responses_websockets=false`,
		"--config", `request_max_retries=0`,
		"--config", `stream_max_retries=0`,
		"--disable", "enable_request_compression",
	}
	if len(arguments) != len(wantPrefix)+3 ||
		!slices.Equal(arguments[:len(wantPrefix)], wantPrefix) ||
		!slices.Equal(arguments[len(wantPrefix):], []string{"exec", "--json", "probe"}) {
		t.Fatalf("Codex arguments = %v", arguments)
	}
}

// A Codex rollout persists its provider ID in session_meta. New rollouts must
// therefore persist Codex's built-in provider so a later plain `codex resume`
// can load them without ViberMate's launch-time configuration.
func TestBuildChildArgumentsMakesNewCodexSessionsPortable(t *testing.T) {
	t.Parallel()

	arguments, err := buildChildArguments(
		[]string{"codex", "resume", "old-session-id"},
		nil,
		clientadapter.LaunchCodexResponsesHTTP,
	)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"--config", `model_provider="openai"`,
		"--config", `openai_base_url="https://chatgpt.com/backend-api/codex"`,
		"--config", `features.responses_websockets=false`,
		"--config", `request_max_retries=0`,
		"--config", `stream_max_retries=0`,
		"--disable", "enable_request_compression",
		"resume", "old-session-id",
	}
	if !slices.Equal(arguments, want) {
		t.Fatalf("Codex resume arguments = %v", arguments)
	}
}

func TestCodexResponsesBaseURLFollowsExplicitOriginThenAuthMode(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		environment []string
		want        string
	}{
		{
			name: "ChatGPT login by default",
			want: codexChatGPTBaseURL,
		},
		{
			name:        "API key",
			environment: []string{"OPENAI_API_KEY=secret"},
			want:        codexAPIBaseURL,
		},
		{
			name: "explicit Codex origin wins",
			environment: []string{
				"OPENAI_BASE_URL=https://ignored.example/v1",
				"CODEX_BASE_URL=https://relay.example/codex/",
			},
			want: "https://relay.example/codex",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := codexOrigin(test.environment)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("base URL = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCodexResponsesBaseURLRejectsCredentialBearingOrigin(t *testing.T) {
	t.Parallel()

	if _, err := codexOrigin([]string{
		"CODEX_BASE_URL=https://name:secret@relay.example/codex",
	}); err == nil {
		t.Fatal("credential-bearing Codex base URL was accepted")
	}
}

// The launch recipe and the managed-credential decision must agree about where
// a Codex child sends traffic. They derived it twice from the same environment
// with different fallbacks, so in the ChatGPT-login case — no base URL, no API
// key, the common one — the child was pointed at chatgpt.com while the
// credential decision was made for api.openai.com.
func TestCodexOriginIsOneAnswerForRecipeAndAuthority(t *testing.T) {
	t.Parallel()

	for name, environment := range map[string][]string{
		"chatgpt login": {"HOME=/Users/mira"},
		"api key":       {"OPENAI_API_KEY=sk-test"},
		"explicit base": {"CODEX_BASE_URL=https://relay.example.com/v1"},
	} {
		origin, err := codexOrigin(environment)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		authority, ok := clientTargetAuthority(environment, "codex-cli")
		if !ok {
			t.Fatalf("%s: authority was not resolved", name)
		}
		parsed, err := url.Parse(origin)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wantHost := strings.ToLower(parsed.Hostname())
		if !strings.HasPrefix(authority, wantHost+":") {
			t.Fatalf(
				"%s: recipe points at %q but the credential decision is made for %q",
				name, origin, authority,
			)
		}
	}
}

// POSIX environment variables are case-sensitive and Codex reads OPENAI_BASE_URL
// exactly. Accepting a lower-case spelling let a variable the client ignores
// decide the base_url ViberMate writes into the client's own configuration.
func TestCodexOriginIgnoresEnvironmentNamesCodexItselfIgnores(t *testing.T) {
	t.Parallel()

	origin, err := codexOrigin([]string{
		"openai_base_url=https://not-read-by-codex.example.com",
		"codex_api_key=sk-not-read-by-codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	if origin != codexChatGPTBaseURL {
		t.Fatalf(
			"origin = %q; a name Codex does not read decided the launch recipe",
			origin,
		)
	}
	authority, ok := clientTargetAuthority([]string{
		"openai_base_url=https://not-read-by-codex.example.com",
	}, "codex-cli")
	if !ok || strings.HasPrefix(authority, "not-read-by-codex") {
		t.Fatalf("authority = %q, resolved = %v", authority, ok)
	}
}
