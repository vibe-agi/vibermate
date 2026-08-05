package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
)

func TestAppBundlePathRequiresPackagedMembers(t *testing.T) {
	t.Parallel()

	bundle := filepath.Join(t.TempDir(), "VibeMate.app")
	executableDirectory := filepath.Join(bundle, "Contents", "MacOS")
	if err := os.MkdirAll(executableDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(bundle, "Contents", "Info.plist"),
		[]byte("plist"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"vibermate-desktop",
		"vibermated",
		"vibermate",
	} {
		if err := os.WriteFile(
			filepath.Join(executableDirectory, name),
			[]byte("binary-"+name),
			0o700,
		); err != nil {
			t.Fatal(err)
		}
	}
	canonical, err := appBundlePath(bundle)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := filepath.EvalSymlinks(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if canonical != expected {
		t.Fatalf("canonical bundle = %q", canonical)
	}
	if err := os.Chmod(
		filepath.Join(executableDirectory, "vibermated"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := appBundlePath(bundle); err == nil {
		t.Fatal("App bundle without an executable was accepted")
	}
}

func TestPackagedExecutableInputCannotSelectAnotherBinary(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	packaged := filepath.Join(directory, "VibeMate.app", "Contents", "MacOS", "vibermated")
	external := filepath.Join(directory, "external-vibermated")
	for _, path := range []string{packaged, external} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	canonicalPackaged, err := executablePath(packaged)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePackagedExecutableInput(
		"daemon",
		"",
		canonicalPackaged,
	); err != nil {
		t.Fatalf("omitted compatibility input was rejected: %v", err)
	}
	if err := validatePackagedExecutableInput(
		"daemon",
		packaged,
		canonicalPackaged,
	); err != nil {
		t.Fatalf("exact packaged member was rejected: %v", err)
	}
	if err := validatePackagedExecutableInput(
		"daemon",
		external,
		canonicalPackaged,
	); err == nil {
		t.Fatal("external daemon was accepted for the selected App")
	}
}

func TestClientInvocationPathPreservesVerifiedWrapperLabel(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	native := filepath.Join(directory, "lib", "codex.js")
	if err := os.MkdirAll(filepath.Dir(native), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("fixed-wrapper"), 0o700); err != nil {
		t.Fatal(err)
	}
	invocation := filepath.Join(directory, "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(invocation), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(native, invocation); err != nil {
		t.Fatal(err)
	}
	selected, err := clientInvocationPath(invocation, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if selected != invocation || filepath.Base(selected) != "codex" {
		t.Fatalf("client invocation path = %q", selected)
	}
	canonical, err := executablePath(selected)
	if err != nil {
		t.Fatal(err)
	}
	canonicalNative, err := filepath.EvalSymlinks(native)
	if err != nil {
		t.Fatal(err)
	}
	if canonical != canonicalNative {
		t.Fatalf("canonical client target = %q", canonical)
	}
	if _, err := clientInvocationPath(native, "codex"); err == nil {
		t.Fatal("resolved target name was accepted as an invocation label")
	}
}

func TestAcceptanceRouteRequiresExplicitProviderIdentity(t *testing.T) {
	t.Parallel()

	defaults := defaultConfig()
	if defaults.providerOrigin != "" || defaults.providerModel != "" {
		t.Fatalf(
			"implicit route origin=%q model=%q",
			defaults.providerOrigin,
			defaults.providerModel,
		)
	}
	explicit := defaults
	explicit.providerOrigin = "https://gateway.example/v1"
	explicit.providerModel = "example-model"
	aggregate, err := assemblyAccess(explicit, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.ProviderTargets) != 1 ||
		aggregate.ProviderTargets[0].Origin != explicit.providerOrigin ||
		len(aggregate.Profiles) != 1 ||
		aggregate.Profiles[0].DefaultModelPolicy.FixedModel !=
			explicit.providerModel {
		t.Fatalf("explicit executable route = %+v", aggregate)
	}
}

func TestAcceptanceClientSelectionKeepsUnknownUserVersionsOutOfFixedEvidence(
	t *testing.T,
) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		config      config
		id          acceptanceClientID
		version     string
		origin      string
		dialect     access.Dialect
		executable  string
		adapterID   string
		reportLabel string
	}{
		{
			name: "Claude",
			config: config{
				clientID:   acceptanceClientClaudeCode,
				claudePath: "/fixed/claude",
			},
			id:          acceptanceClientClaudeCode,
			version:     "2.1.220",
			origin:      "https://api.anthropic.com",
			dialect:     access.DialectAnthropicMessages,
			executable:  "/fixed/claude",
			adapterID:   "claude-code",
			reportLabel: "Claude Code",
		},
		{
			name: "Codex",
			config: config{
				clientID:  acceptanceClientCodexCLI,
				codexPath: "/fixed/codex",
			},
			id:          acceptanceClientCodexCLI,
			version:     "0.145.0",
			origin:      "https://api.openai.com",
			dialect:     access.DialectOpenAIResponses,
			executable:  "/fixed/codex",
			adapterID:   "codex-cli",
			reportLabel: "Codex CLI",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			client, err := selectedAcceptanceClient(test.config)
			if err != nil {
				t.Fatal(err)
			}
			if client.ID != test.id ||
				client.Version != test.version ||
				client.ClientOrigin != test.origin ||
				client.ClientDialect != test.dialect ||
				client.ExecutablePath != test.executable ||
				client.Release.ID != test.adapterID ||
				client.ReportLabel != test.reportLabel {
				t.Fatalf("selected client = %+v", client)
			}
		})
	}

	if _, err := selectedAcceptanceClient(config{
		clientID: acceptanceClientID("codex-latest"),
	}); err == nil {
		t.Fatal("unversioned acceptance client identity was accepted")
	}
}
