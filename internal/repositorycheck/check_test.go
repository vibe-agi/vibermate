package repositorycheck

import (
	"errors"
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
