package clientadapter_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

func TestOfficialInstalledClientPackagesMatchTheBuiltInCatalog(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name        string
		environment string
		label       string
		id          string
		recipe      clientadapter.LaunchRecipe
	}{
		{
			name:        "Claude Code",
			environment: "VIBERMATE_OFFICIAL_CLAUDE",
			label:       "claude",
			id:          "claude-code",
			recipe:      clientadapter.LaunchNodeEnvProxy,
		},
		{
			name:        "Codex CLI",
			environment: "VIBERMATE_OFFICIAL_CODEX",
			label:       "codex",
			id:          "codex-cli",
			recipe:      clientadapter.LaunchCodexResponsesHTTP,
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			executable := os.Getenv(testCase.environment)
			if executable == "" {
				t.Skip(testCase.environment + " is not configured")
			}
			if !filepath.IsAbs(executable) {
				t.Fatal("official client path must be absolute")
			}
			verifier, err := clientadapter.NewReleaseVerifier(
				clientadapter.BuiltInCatalog(),
			)
			if err != nil {
				t.Fatal(err)
			}
			detection, err := verifier.Verify(
				context.Background(),
				clientadapter.Request{
					Command:        []string{testCase.label},
					CWD:            filepath.Dir(executable),
					ExecutablePath: executable,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if detection.Status != clientadapter.StatusVerified ||
				detection.Recognition != clientadapter.RecognitionVerified ||
				detection.Evidence == nil ||
				detection.Evidence.ID != testCase.id ||
				detection.Evidence.LaunchRecipe != testCase.recipe ||
				!detection.Evidence.LaunchRecipe.RequiresRoot() {
				t.Fatalf("official package detection = %+v", detection)
			}
		})
	}
}
