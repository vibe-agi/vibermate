package repositorycheck

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnglishSourceRuleUsesKnownGoodAndInjectedBadFixtures(t *testing.T) {
	t.Parallel()

	goodRoot := filepath.Join("testdata", "english", "good")
	if violations := checkEnglishFile(goodRoot, filepath.Join(goodRoot, "good.go")); len(violations) != 0 {
		t.Fatalf("known-good fixture produced violations: %v", violations)
	}

	badRoot := filepath.Join("testdata", "english", "bad")
	violations := checkEnglishFile(badRoot, filepath.Join(badRoot, "bad.go"))
	if len(violations) == 0 {
		t.Fatal("injected bad fixture was not rejected")
	}
	if violations[0].Rule != "english-source" {
		t.Fatalf("unexpected rule: %q", violations[0].Rule)
	}
}

func TestLocaleRuleUsesKnownGoodAndInjectedBadFixtures(t *testing.T) {
	t.Parallel()

	fixtureRoot := filepath.Join("testdata", "catalogs")
	tests := []struct {
		name       string
		directory  string
		wantRule   string
		wantIssues bool
	}{
		{name: "known good", directory: "good"},
		{
			name:       "missing key",
			directory:  "bad-missing",
			wantRule:   "locale-parity",
			wantIssues: true,
		},
		{
			name:       "empty value",
			directory:  "bad-empty",
			wantRule:   "locale-nonempty",
			wantIssues: true,
		},
		{
			name:       "parameter mismatch",
			directory:  "bad-parameters",
			wantRule:   "locale-parameters",
			wantIssues: true,
		},
		{
			name:       "invalid catalog",
			directory:  "bad-invalid",
			wantRule:   "locale-catalog",
			wantIssues: true,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := filepath.Join(fixtureRoot, test.directory)
			violations := CheckCatalogPair(
				filepath.Join(directory, "en-US.json"),
				filepath.Join(directory, "zh-CN.json"),
			)
			if !test.wantIssues {
				if len(violations) != 0 {
					t.Fatalf("known-good fixture produced violations: %v", violations)
				}
				return
			}
			if len(violations) == 0 {
				t.Fatal("injected bad fixture was not rejected")
			}
			found := false
			for _, violation := range violations {
				if violation.Rule == test.wantRule {
					found = true
				}
			}
			if !found {
				t.Fatalf("rule %q was not reported: %v", test.wantRule, violations)
			}
		})
	}
}

func TestRepositoryCheckReportsStableSentinel(t *testing.T) {
	t.Parallel()

	err := Check(filepath.Join("testdata", "repository-bad"))
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
}

func TestRetiredProductAuthorityRuleUsesPublicCheck(t *testing.T) {
	t.Parallel()

	repositoryRoot := t.TempDir()
	for _, directory := range []string{"internal/example", "locales"} {
		if err := os.MkdirAll(filepath.Join(repositoryRoot, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, contents := range map[string]string{
		"internal/example/example.go": "package example\n\ntype EnvironmentID string\n",
		"locales/en-US.json":          "{\"fixture\":\"Fixture\"}\n",
		"locales/zh-CN.json":          "{\"fixture\":\"Localized fixture\"}\n",
	} {
		if err := os.WriteFile(
			filepath.Join(repositoryRoot, path),
			[]byte(contents),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := Check(repositoryRoot); err != nil {
		t.Fatalf("known-good repository fixture failed: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repositoryRoot, "internal/example/example.go"),
		[]byte("package example\n\ntype Config struct { AccessID string }\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	err := Check(repositoryRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "retired-product-authority") {
		t.Fatalf("public Check did not report retired authority: %v", err)
	}
}

func TestAtRestEncryptionRuleUsesPublicCheck(t *testing.T) {
	t.Parallel()

	repositoryRoot := t.TempDir()
	for _, directory := range []string{"internal/example", "locales"} {
		if err := os.MkdirAll(filepath.Join(repositoryRoot, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, contents := range map[string]string{
		"internal/example/example.go": "package example\n\ntype Envelope struct { Payload []byte }\n",
		"locales/en-US.json":          "{\"fixture\":\"Fixture\"}\n",
		"locales/zh-CN.json":          "{\"fixture\":\"Localized fixture\"}\n",
	} {
		if err := os.WriteFile(
			filepath.Join(repositoryRoot, path),
			[]byte(contents),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := Check(repositoryRoot); err != nil {
		t.Fatalf("known-good repository fixture failed: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repositoryRoot, "internal/example/example.go"),
		[]byte("package example\n\ntype Envelope struct { CipherNonce []byte }\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	err := Check(repositoryRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "at-rest-encryption") {
		t.Fatalf("public Check did not report at-rest encryption: %v", err)
	}
}

// Symbols were not the only way encryption came back. Comments claiming the
// archive is encrypted survived the removal and read as current documentation,
// so the rule has to cover prose in the packages that own stored evidence.
func TestStorageProseMayNotClaimTheArchiveIsEncrypted(t *testing.T) {
	t.Parallel()

	repositoryRoot := t.TempDir()
	for _, directory := range []string{"internal/rawevidence", "locales"} {
		if err := os.MkdirAll(filepath.Join(repositoryRoot, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, contents := range map[string]string{
		// A comment stating the absence is exactly what the product must say.
		"internal/rawevidence/types.go": "package rawevidence\n\n" +
			"// INV-STORE-DISCLOSED forbids application-layer field encryption.\n" +
			"type Envelope struct{ Payload []byte }\n",
		"locales/en-US.json": "{\"fixture\":\"Fixture\"}\n",
		"locales/zh-CN.json": "{\"fixture\":\"Localized fixture\"}\n",
	} {
		if err := os.WriteFile(
			filepath.Join(repositoryRoot, path),
			[]byte(contents),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := Check(repositoryRoot); err != nil {
		t.Fatalf("a comment stating the absence was rejected: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(repositoryRoot, "internal/rawevidence/types.go"),
		[]byte("package rawevidence\n\n"+
			"// Payload is encrypted before queue admission.\n"+
			"type Envelope struct{ Payload []byte }\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	err := Check(repositoryRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "at-rest-encryption") {
		t.Fatalf("public Check did not report the stale claim: %v", err)
	}
}

// A credential-absence claim that does not say what it covers is the failure
// this rule exists to stop. The product removes credential values it recognizes
// by header name; bodies, tool arguments and query strings are retained as the
// client sent them. A comment or a UI string that says "no credential value is
// stored" tells a user it is safe to paste a key into a prompt.
//
// Fixing the sentences once was not enough: they were found by hand, and the
// next one would be too. This is the gate that finds them.
func TestCredentialAbsenceClaimsMustNameTheirScope(t *testing.T) {
	t.Parallel()

	repositoryRoot := t.TempDir()
	for _, directory := range []string{
		"internal/rawevidence", "internal/runtimepersistence", "locales",
	} {
		if err := os.MkdirAll(filepath.Join(repositoryRoot, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, contents := range map[string]string{
		// A scoped claim is exactly what the product must say.
		"internal/rawevidence/types.go": "package rawevidence\n\n" +
			"// Credential header values are removed before the payload exists.\n" +
			"type Envelope struct{ Payload []byte }\n",
		"locales/en-US.json": "{\"fixture\":\"Fixture\"}\n",
		"locales/zh-CN.json": "{\"fixture\":\"Localized fixture\"}\n",
	} {
		if err := os.WriteFile(
			filepath.Join(repositoryRoot, path), []byte(contents), 0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := Check(repositoryRoot); err != nil {
		t.Fatalf("a scoped credential claim was rejected: %v", err)
	}

	// A control-plane claim is a different claim, and it is absolute because it
	// is true: ProviderAccount credentials belong to the host SecretStore and
	// genuinely never enter SQLite. Narrowing that sentence would make it false.
	if err := os.WriteFile(
		filepath.Join(repositoryRoot, "internal/runtimepersistence/accounts.sql"),
		[]byte("-- The credential bytes belong exclusively to the host-selected\n"+
			"-- SecretStore; secret_reference is an opaque typed locator stored\n"+
			"-- in this database, never a credential value.\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := Check(repositoryRoot); err != nil {
		t.Fatalf("a control-plane credential claim was rejected: %v", err)
	}

	// The rule judges prose, and code is not prose. A parameter named
	// `credential` and an unrelated comment elsewhere in the file must not
	// combine into a claim neither of them makes.
	if err := os.WriteFile(
		filepath.Join(repositoryRoot, "internal/rawevidence/write.go"),
		[]byte("package rawevidence\n\n"+
			"func Store(credential string) error {\n"+
			"\tif credential == \"\" {\n"+
			"\t\treturn nil\n"+
			"\t}\n"+
			"\t// The database column is never widened in place.\n"+
			"\treturn nil\n"+
			"}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := Check(repositoryRoot); err != nil {
		t.Fatalf("code was judged as a credential claim: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(repositoryRoot, "internal/rawevidence/types.go"),
		[]byte("package rawevidence\n\n"+
			"// No credential value is ever stored in any column.\n"+
			"type Envelope struct{ Payload []byte }\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	err := Check(repositoryRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "credential-claim-scope") {
		t.Fatalf("public Check did not report the unscoped claim: %v", err)
	}
}

func TestEnglishSourceRuleIsWiredThroughRepositoryCheck(t *testing.T) {
	t.Parallel()

	goodRoot := filepath.Join("testdata", "repository-english-good")
	if err := Check(goodRoot); err != nil {
		t.Fatalf("known-good repository fixture failed: %v", err)
	}

	badRoot := filepath.Join("testdata", "repository-english-bad")
	err := Check(badRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "english-source") {
		t.Fatalf("public Check did not report english-source: %v", err)
	}
}

func TestProtocolSDKHotPathRuleUsesPublicCheckWithGoodAndBadFixtures(t *testing.T) {
	t.Parallel()

	goodRoot := filepath.Join("testdata", "repository-sdk-good")
	if err := Check(goodRoot); err != nil {
		t.Fatalf("known-good repository fixture failed: %v", err)
	}

	badRoot := filepath.Join("testdata", "repository-sdk-bad")
	err := Check(badRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "protocol-sdk-hotpath") {
		t.Fatalf("public Check did not report protocol-sdk-hotpath: %v", err)
	}
}

func TestProtocolSDKHotPathProtectsTypedCompositionThroughPublicCheck(
	t *testing.T,
) {
	t.Parallel()

	goodRoot := filepath.Join(
		"testdata",
		"repository-sdk-composition-good",
	)
	if err := Check(goodRoot); err != nil {
		t.Fatalf("known-good repository fixture failed: %v", err)
	}

	badRoot := filepath.Join(
		"testdata",
		"repository-sdk-composition-bad",
	)
	err := Check(badRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "protocol-sdk-hotpath") ||
		!strings.Contains(
			err.Error(),
			filepath.Join("internal", "responseschat", "path.go"),
		) {
		t.Fatalf("public Check did not protect typed composition: %v", err)
	}
}

func TestExternalEgressGateRuleUsesPublicCheckWithGoodAndBadFixtures(t *testing.T) {
	t.Parallel()

	goodRoot := filepath.Join("testdata", "repository-egress-good")
	if err := Check(goodRoot); err != nil {
		t.Fatalf("known-good repository fixture failed: %v", err)
	}

	badRoot := filepath.Join("testdata", "repository-egress-bad")
	err := Check(badRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "external-egress-gate") {
		t.Fatalf("public Check did not report external-egress-gate: %v", err)
	}
	if !strings.Contains(
		err.Error(),
		filepath.Join("internal", "originaltransport", "client.go"),
	) {
		t.Fatalf("public Check did not reject raw dial outside the probe: %v", err)
	}
}

func TestDataPlaneEnvironmentBoundaryUsesPublicCheckWithGoodAndBadFixtures(
	t *testing.T,
) {
	t.Parallel()

	goodRoot := filepath.Join("testdata", "repository-data-plane-good")
	if err := Check(goodRoot); err != nil {
		t.Fatalf("known-good repository fixture failed: %v", err)
	}

	badRoot := filepath.Join("testdata", "repository-data-plane-bad")
	err := Check(badRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "data-plane-environment-boundary") {
		t.Fatalf("public Check did not report data-plane-environment-boundary: %v", err)
	}
}

func TestDesktopFrontendBoundaryUsesPublicCheckWithGoodAndBadFixtures(
	t *testing.T,
) {
	t.Parallel()

	goodRoot := filepath.Join("testdata", "repository-frontend-good")
	if err := Check(goodRoot); err != nil {
		t.Fatalf("known-good repository fixture failed: %v", err)
	}

	badRoot := filepath.Join("testdata", "repository-frontend-bad")
	err := Check(badRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	for _, rule := range []string{
		"desktop-io-boundary",
		"desktop-control-network-boundary",
		"desktop-process-boundary",
		"desktop-ffi-boundary",
		"desktop-authority-storage",
		"desktop-capability-storage",
	} {
		if !strings.Contains(err.Error(), rule) {
			t.Fatalf("public Check did not report %s: %v", rule, err)
		}
	}
}

func TestSystemTrustBoundaryUsesPublicCheckWithGoodAndBadFixtures(
	t *testing.T,
) {
	t.Parallel()

	goodRoot := filepath.Join("testdata", "repository-system-trust-good")
	if err := Check(goodRoot); err != nil {
		t.Fatalf("known-good repository fixture failed: %v", err)
	}

	badRoot := filepath.Join("testdata", "repository-system-trust-bad")
	err := Check(badRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	for _, rule := range []string{
		"system-trust-composition",
		"system-trust-live-executor",
		"system-trust-production-executor",
		"system-trust-command-scope",
		"system-trust-user-surface",
	} {
		if !strings.Contains(err.Error(), rule) {
			t.Fatalf("public Check did not report %s: %v", rule, err)
		}
	}
	for _, path := range []string{
		"api/openapi.yaml",
		"internal/systemtrust/runner.go",
		"ui/flutter_app/lib/core/trust.dart",
		"ui/flutter_app/lib/features/trust.dart",
	} {
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("public Check did not inspect %s: %v", path, err)
		}
	}
}

func TestPayloadDispatchBoundaryUsesPublicCheckWithGoodAndBadFixtures(
	t *testing.T,
) {
	t.Parallel()

	goodRoot := filepath.Join("testdata", "repository-payload-dispatch-good")
	if err := Check(goodRoot); err != nil {
		t.Fatalf("known-good repository fixture failed: %v", err)
	}

	badRoot := filepath.Join("testdata", "repository-payload-dispatch-bad")
	err := Check(badRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "payload-dispatch-boundary") {
		t.Fatalf("public Check did not report payload-dispatch-boundary: %v", err)
	}
	if !strings.Contains(
		err.Error(),
		filepath.Join("internal", "loopbackproxy", "handler.go"),
	) {
		t.Fatalf("public Check did not locate the merged dispatch arm: %v", err)
	}
}

func TestIdentityCompositionUsesPublicCheckWithGoodAndBadFixtures(t *testing.T) {
	t.Parallel()

	goodRoot := filepath.Join("testdata", "repository-identity-good")
	if err := Check(goodRoot); err != nil {
		t.Fatalf("known-good repository fixture failed: %v", err)
	}

	badRoot := filepath.Join("testdata", "repository-identity-bad")
	err := Check(badRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "identity-composition") {
		t.Fatalf("public Check did not report identity-composition: %v", err)
	}
}

// The signer-identity rules get the same treatment as every other boundary:
// a repository that keeps them and one that breaks them, both on disk.
//
// A mutation run by hand proved the rules discriminated on the day they were
// written. It cannot stop them being weakened later, which is what a fixture
// is for.
func TestSignerIdentityBoundaryUsesPublicCheckWithGoodAndBadFixtures(
	t *testing.T,
) {
	t.Parallel()

	goodRoot := filepath.Join("testdata", "repository-signer-identity-good")
	if err := Check(goodRoot); err != nil {
		t.Fatalf("known-good repository fixture failed: %v", err)
	}

	badRoot := filepath.Join("testdata", "repository-signer-identity-bad")
	err := Check(badRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	for _, rule := range []string{
		"signer-requirement-literal",
		"signer-verification-boundary",
	} {
		if !strings.Contains(err.Error(), rule) {
			t.Fatalf("public Check did not report %s: %v", rule, err)
		}
	}
	// The generator itself is exempt, so a failure naming it would mean the
	// exemption had been lost rather than a rule enforced.
	if strings.Contains(err.Error(), "internal/codesignature/identity.go") {
		t.Fatalf("the generator was reported as a violation: %v", err)
	}
}

// Each fragment of the requirement language has to be doing work.
//
// Asserting only that the rule fired leaves every fragment but one removable:
// the fixture's literals overlap, so one surviving fragment catches them all
// and the rule still reports. This checks the fragments one at a time, against
// a file that carries each of them alone.
func TestEveryRequirementFragmentIsLoadBearing(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		content string
	}{
		{"the Apple anchor alone", "const x = `anchor apple generic`\n"},
		{"the team field alone", "const x = `certificate 1[subject.OU]`\n"},
		{"a leaf certificate clause alone", "const x = `certificate leaf[field.9]`\n"},
		{"a Developer ID OID alone", "const x = `1.2.840.113635.100.6.2.6`\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			directory := filepath.Join(root, "internal", "clientadapter")
			if err := os.MkdirAll(directory, 0o755); err != nil {
				t.Fatal(err)
			}
			source := "package clientadapter\n\n" + testCase.content
			if err := os.WriteFile(
				filepath.Join(directory, "signer.go"), []byte(source), 0o644,
			); err != nil {
				t.Fatal(err)
			}
			violations := CheckSignerIdentityBoundary(root)
			found := false
			for _, violation := range violations {
				if violation.Rule == "signer-requirement-literal" {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s was not reported: %+v", testCase.name, violations)
			}
		})
	}
}

// The Desktop production chain, as fixtures rather than as a fact about the
// current source.
//
// Each composition rule is broken in the bad repository, and each is reported
// by name. The good one keeps the chain whole. Both go through the public
// Check, so a rule that stops being registered fails here too.
func TestProductionCompositionBoundaryUsesPublicCheckWithGoodAndBadFixtures(
	t *testing.T,
) {
	t.Parallel()

	goodRoot := filepath.Join("testdata", "repository-composition-good")
	if err := Check(goodRoot); err != nil {
		t.Fatalf("known-good repository fixture failed: %v", err)
	}

	badRoot := filepath.Join("testdata", "repository-composition-bad")
	err := Check(badRoot)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("expected ErrCheckFailed, got %v", err)
	}
	for _, rule := range []string{
		"desktop-entry-reaches-past-the-daemon",
		"desktop-entry-skips-production-options",
		"desktop-entry-does-not-run-the-daemon",
		"daemon-does-not-start-the-host",
		"host-does-not-start-the-runtime",
		"runtime-does-not-select-production-builders",
		"host-does-not-create-control-authority",
		"host-does-not-create-capture-grant-issuer",
		"host-does-not-create-manual-capture-handler",
		"host-does-not-create-capture-control-handler",
		"capture-run-create-outside-issuer",
		"manual-capture-create-outside-issuer",
		"proxy-imports-capture-run",
		"proxy-imports-manual-capture",
		"unreviewed-runtime-composition",
		"unreviewed-host-composition",
		"unreviewed-control-authority-composition",
		"unreviewed-capture-grant-composition",
		"unreviewed-manual-capture-handler-composition",
		"unreviewed-capture-control-composition",
		"composition-dot-import",
	} {
		if !strings.Contains(err.Error(), rule) {
			t.Fatalf("public Check did not report %s: %v", rule, err)
		}
	}
	// The bypasses a review found, each named by the file that carries it.
	for _, path := range []string{
		// A decoy call elsewhere in the package no longer satisfies the link.
		"internal/desktopdaemon",
		"internal/desktophost",
		"internal/productruntime",
		"internal/loopbackproxy/handler.go",
		// An alias, and taking the function as a value.
		"internal/sneakyhost/host.go",
		// A second entry point composing the product.
		"cmd/secondentry/main.go",
		// A dot import, which would erase the qualifier every check needs.
		"internal/dotimporter/dot.go",
	} {
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("public Check did not report %s: %v", path, err)
		}
	}
}

// A package that names the runtime's types is not composing one. Both of these
// exist in the real source, and a guard that flagged them would teach everyone
// to widen the allowlist — which is the opposite of what a short allowlist is
// for.
func TestNamingRuntimeTypesIsNotComposingARuntime(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	directory := filepath.Join(root, "internal", "somepackage")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	source := "package somepackage\n\n" +
		"import \"github.com/vibe-agi/vibermate/internal/productruntime\"\n\n" +
		"type Reader interface{ Status() productruntime.RuntimeStatus }\n\n" +
		"func options() productruntime.Options { return productruntime.Options{} }\n"
	if err := os.WriteFile(
		filepath.Join(directory, "reader.go"), []byte(source), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	for _, violation := range CheckProductionCompositionBoundary(root) {
		if violation.Rule == "unreviewed-runtime-composition" {
			t.Fatalf("using runtime types was reported as composing: %+v", violation)
		}
	}
}

// The Flutter copy table resolves a missing key to the key itself, so an
// untranslated string ships as `exchange.system_parameter` in the UI and no test
// notices. The JSON locales already have a parity gate; this closes the same hole
// for the Dart table.
func TestFlutterCopyPairUsesKnownGoodAndInjectedBadFixtures(t *testing.T) {
	t.Parallel()

	write := func(t *testing.T, root, body string) {
		t.Helper()
		directory := filepath.Join(root, "ui", "flutter_app", "lib", "core", "i18n")
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(directory, "app_copy.dart"), []byte(body), 0o600,
		); err != nil {
			t.Fatal(err)
		}
	}

	balanced := `final class AppCopy {
  static const _en = <String, String>{
    'a.one': 'One',
    'a.two':
        'Two wrapped '
        'across lines',
  };

  static const _zh = <String, String>{
    'a.one': 'One localized',
    'a.two': 'Two localized',
  };
}
`
	good := t.TempDir()
	write(t, good, balanced)
	if violations := CheckFlutterCopyPair(good); len(violations) != 0 {
		t.Fatalf("balanced copy table reported %v", violations)
	}

	missing := t.TempDir()
	write(t, missing, `final class AppCopy {
  static const _en = <String, String>{
    'a.one': 'One',
    'a.two': 'Two',
  };

  static const _zh = <String, String>{
    'a.one': 'One localized',
  };
}
`)
	violations := CheckFlutterCopyPair(missing)
	if len(violations) != 1 || !strings.Contains(violations[0].Message, "a.two") {
		t.Fatalf("missing translation reported %v", violations)
	}
}

// The prose rule was scoped to two Go packages, so the half of
// INV-STORE-DISCLOSED about UI strings had no guard at all — while the goal
// claims no UI surface asserts encryption or a rotatable data key.
func TestUICopyMayNotClaimStoredContentIsEncrypted(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	copyPath := filepath.Join(root, "ui", "flutter_app", "lib", "core", "i18n")
	if err := os.MkdirAll(copyPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "locales"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		"locales/en-US.json": "{\"fixture\":\"Fixture\"}\n",
		"locales/zh-CN.json": "{\"fixture\":\"Localized fixture\"}\n",
	} {
		if err := os.WriteFile(
			filepath.Join(root, path), []byte(contents), 0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	write := func(t *testing.T, body string) {
		t.Helper()
		if err := os.WriteFile(
			filepath.Join(copyPath, "app_copy.dart"), []byte(body), 0o600,
		); err != nil {
			t.Fatal(err)
		}
	}

	// Provider-encrypted reasoning and TLS tunnelling legitimately use the word.
	write(t, `final class AppCopy {
  static const _en = <String, String>{
    'exchange.content.reasoning_encrypted': 'Encrypted reasoning state',
    'network.value.decryption.blind': 'Encrypted passthrough',
  };

  static const _zh = <String, String>{
    'exchange.content.reasoning_encrypted': 'Localized reasoning',
    'network.value.decryption.blind': 'Localized passthrough',
  };
}
`)
	if err := Check(root); err != nil {
		t.Fatalf("provider and TLS wording was rejected: %v", err)
	}

	for _, claim := range []string{
		`'settings.storage.claim': 'Stored content is encrypted at rest',`,
		`'settings.storage.claim': 'Rotate the data key',`,
	} {
		write(t, `final class AppCopy {
  static const _en = <String, String>{
    `+claim+`
  };

  static const _zh = <String, String>{
    'settings.storage.claim': 'Localized claim',
  };
}
`)
		err := Check(root)
		if !errors.Is(err, ErrCheckFailed) ||
			!strings.Contains(err.Error(), "at-rest-encryption") {
			t.Fatalf("claim %q was accepted: %v", claim, err)
		}
	}
}
