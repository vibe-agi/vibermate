package runlauncher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestCodexProxyGuardOnlyUsesKnownFeaturesAndIsolatedHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX capability fixture")
	}
	for _, test := range []struct {
		name, output string
		want         []string
	}{
		{"supported", "respect_system_proxy under development true\nsystem_proxy_fallback stable true\n", []string{"exec", "prompt", "--disable", "respect_system_proxy", "--disable", "system_proxy_fallback"}},
		{"older", "unrelated stable true\n", []string{"exec", "prompt"}},
		{"unknown", "respect_system_proxy_suffix stable true\nsystem_proxy_fallback\n", []string{"exec", "prompt"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "codex")
			// This executable fails if capability discovery inherits secrets or
			// the original native home, rather than merely checking an argv string.
			script := "#!/bin/sh\ntest \"$1\" = features && test \"$2\" = list || exit 1\ntest -z \"$OPENAI_API_KEY$VIBERMATE_CODEX_PROVIDER_TOKEN\" || exit 1\ntest ! -e \"$CODEX_HOME/auth.json\" || exit 1\ntest \"$HOME\" = \"$CODEX_HOME\" || exit 1\ncat <<'FEATURES'\n" + test.output + "FEATURES\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("synthetic native secret"), 0600); err != nil {
				t.Fatal(err)
			}
			original := []string{"exec", "prompt"}
			got, err := prepareCodexProxyArguments(context.Background(), binary, original, []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "CODEX_HOME=" + dir, "OPENAI_API_KEY=synthetic", "VIBERMATE_CODEX_PROVIDER_TOKEN=synthetic"})
			if err != nil || !slices.Equal(got, test.want) || !slices.Equal(original, []string{"exec", "prompt"}) {
				t.Fatalf("guard=%v err=%v", got, err)
			}
		})
	}
}

func TestCodexFeatureProbeCancellationAndFailureAreBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX capability fixture")
	}
	for _, test := range []struct {
		name, body string
		reason     string
	}{
		{"nonzero", "exit 1\n", "exit_failed"},
		{"overflow", "yes feature\n", "output_limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "codex")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+test.body), 0700); err != nil {
				t.Fatal(err)
			}
			_, err := prepareCodexProxyArguments(context.Background(), binary, []string{"exec"}, []string{"PATH=/usr/bin:/bin"})
			var failure *CodexProbeFailure
			if !errors.As(err, &failure) || failure.Reason != test.reason {
				t.Fatalf("probe error=%v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if _, err := prepareCodexProxyArguments(ctx, "/not-executed", []string{"exec"}, nil); !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("canceled probe=%v", err)
	}
}
