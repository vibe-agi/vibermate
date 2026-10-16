package runlauncher

import (
	"context"
	"slices"
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"net/url"
	"strings"
)

// Losing any mandatory recipe override must prevent the legacy interactive
// fallback at the actual recipe consumer, not merely in a literal fixture.
func TestCodexCaptureIsolationConsumesLegacyLaunchRecipe(t *testing.T) {
	for _, original := range [][]string{nil, {"resume", "thread"}, {"fork", "--last"}} {
		prepared, err := buildChildArguments(append([]string{"codex"}, original...), nil, clientadapter.LaunchCodexResponsesHTTP)
		if err != nil {
			t.Fatal(err)
		}
		got, err := prepareCodexCaptureArguments(context.Background(), "/not/executed", original, prepared, nil, true)
		if err != nil || !slices.Equal(got, prepared) {
			t.Fatalf("recipe consumer lost legacy isolation: %v %v", got, err)
		}
	}
}

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

func TestCodexSessionEntrypointsStayNative(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][]string{
		nil, {"resume"}, {"resume", "--last"}, {"resume", "--all"},
		{"resume", "12345678-1234-4234-9234-123456789abc"}, {"resume", "named-session"},
		{"fork"}, {"fork", "--last"}, {"exec", "resume", "--last", "synthetic prompt"},
		{"--model", "fixture", "resume", "named-session", "continue without changing permissions"},
		{"explain resume and fork"},
	} {
		invocation := append([]string{"codex"}, arguments...)
		got, err := buildChildArguments(invocation, nil, clientadapter.LaunchCodexResponsesHTTP)
		if err != nil || len(got) < len(arguments) || !slices.Equal(got[len(got)-len(arguments):], arguments) {
			t.Fatalf("native session arguments were rewritten: %v => %v (%v)", arguments, got, err)
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
		"--config", `features.responses_websockets=false`,
		"--disable", "enable_request_compression",
	}
	if len(arguments) != len(wantPrefix)+3 ||
		!slices.Equal(arguments[:len(wantPrefix)], wantPrefix) ||
		!slices.Equal(arguments[len(wantPrefix):], []string{"exec", "--json", "probe"}) {
		t.Fatalf("Codex arguments = %v", arguments)
	}
}

// A Codex rollout must retain its native provider so plain resume can load it.
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
		"--config", `features.responses_websockets=false`,
		"--disable", "enable_request_compression",
		"resume", "old-session-id",
	}
	if !slices.Equal(arguments, want) {
		t.Fatalf("Codex resume arguments = %v", arguments)
	}
}

func TestCodexResponsesBaseURLFollowsOnlyExplicitOrigin(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		environment []string
		want        string
	}{
		{
			name: "native login is unknown",
			want: "",
		},
		{
			name:        "API key",
			environment: []string{"OPENAI_API_KEY=secret"},
			want:        "",
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

// Only an explicit base URL establishes both recipe and credential authority.
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
		if origin == "" {
			if ok || authority != "" {
				t.Fatalf("%s: native authority was guessed: %q", name, authority)
			}
			continue
		}
		if !ok {
			t.Fatalf("%s: explicit authority was not resolved", name)
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
	if origin != "" {
		t.Fatalf(
			"origin = %q; a name Codex does not read decided the launch recipe",
			origin,
		)
	}
	authority, ok := clientTargetAuthority([]string{
		"openai_base_url=https://not-read-by-codex.example.com",
	}, "codex-cli")
	if ok || authority != "" {
		t.Fatalf("authority = %q, resolved = %v", authority, ok)
	}
}
