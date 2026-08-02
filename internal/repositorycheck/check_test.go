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

func TestDataPlaneAccessBoundaryUsesPublicCheckWithGoodAndBadFixtures(
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
	if !strings.Contains(err.Error(), "data-plane-access-boundary") {
		t.Fatalf("public Check did not report data-plane-access-boundary: %v", err)
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
		"desktop-host-boundary",
		"desktop-capability-storage",
		"frontend-i18n",
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
		"ui/desktop/src-tauri/src/trust.rs",
		"ui/desktop/src/trust.ts",
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
// Each of the six rules is broken in the bad repository, and each is reported
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
		"unreviewed-runtime-composition",
	} {
		if !strings.Contains(err.Error(), rule) {
			t.Fatalf("public Check did not report %s: %v", rule, err)
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
