package repositorycheck

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	capabilityVersionRE = regexp.MustCompile(
		`Source package version: \*\*(\d+\.\d+\.\d+)\*\*\. Latest published release: \*\*v(\d+\.\d+\.\d+)\*\*\.`,
	)
	pubspecVersionRE = regexp.MustCompile(`(?m)^version: (\d+\.\d+\.\d+)\+\d+$`)
	dockerVersionRE  = regexp.MustCompile(`org\.opencontainers\.image\.version="(\d+\.\d+\.\d+)"`)
	capabilityRowRE  = regexp.MustCompile(
		`(?m)^\| ` + "`" + `([a-z0-9.-]+)` + "`" + ` \| (Released|Experimental|Branch-only|Unsupported) \|`,
	)
)

// CheckCapabilityDocs keeps the public status table tied to the versioned
// product surfaces. It cannot prove a capability; it prevents a release bump
// or a missing critical status row from silently leaving stale documentation.
func CheckCapabilityDocs(repositoryRoot string) []Violation {
	const rule = "capability-docs"
	supportPath := filepath.Join(repositoryRoot, "docs", "capability-support.md")
	support, err := os.ReadFile(supportPath)
	if errors.Is(err, os.ErrNotExist) {
		if _, readmeErr := os.Stat(filepath.Join(repositoryRoot, "README.md")); readmeErr == nil {
			return []Violation{{
				Rule: rule, Path: "docs/capability-support.md",
				Message: "the public capability matrix is missing",
			}}
		}
		return nil
	}
	if err != nil {
		return []Violation{{
			Rule: rule, Path: "docs/capability-support.md", Message: err.Error(),
		}}
	}
	text := string(support)
	versions := capabilityVersionRE.FindStringSubmatch(text)
	if len(versions) != 3 {
		return []Violation{{
			Rule: rule, Path: "docs/capability-support.md",
			Message: "the source and published release versions are missing or malformed",
		}}
	}

	var violations []Violation
	for _, surface := range []struct {
		path    string
		pattern *regexp.Regexp
	}{
		{path: "ui/flutter_app/pubspec.yaml", pattern: pubspecVersionRE},
		{path: "Dockerfile", pattern: dockerVersionRE},
	} {
		contents, readErr := os.ReadFile(filepath.Join(repositoryRoot, filepath.FromSlash(surface.path)))
		if readErr != nil {
			violations = append(violations, Violation{
				Rule: rule, Path: surface.path, Message: readErr.Error(),
			})
			continue
		}
		match := surface.pattern.FindSubmatch(contents)
		if len(match) != 2 || string(match[1]) != versions[1] {
			violations = append(violations, Violation{
				Rule: rule, Path: surface.path,
				Message: fmt.Sprintf("product version must match capability matrix source version %s", versions[1]),
			})
		}
	}

	releasePath := filepath.Join("docs", "releases", "v"+versions[2]+".md")
	if _, statErr := os.Stat(filepath.Join(repositoryRoot, releasePath)); statErr != nil {
		violations = append(violations, Violation{
			Rule: rule, Path: filepath.ToSlash(releasePath),
			Message: "the matrix's latest published release note does not exist",
		})
	}
	for _, readme := range []string{"README.md", "README.zh-CN.md"} {
		contents, readErr := os.ReadFile(filepath.Join(repositoryRoot, readme))
		if readErr != nil || !strings.Contains(string(contents), "docs/capability-support.md") {
			violations = append(violations, Violation{
				Rule: rule, Path: readme,
				Message: "README must link the public capability matrix",
			})
		}
	}

	rows := map[string]int{}
	for _, match := range capabilityRowRE.FindAllStringSubmatch(text, -1) {
		rows[match[1]]++
	}
	for _, capability := range []string{
		"macos-app",
		"linux-server-web",
		"remote-web-tls",
		"codex-oauth",
		"native-cli-identity-rewrite",
		"editor-acp",
		"automatic-account-failover",
	} {
		if rows[capability] != 1 {
			violations = append(violations, Violation{
				Rule: rule, Path: "docs/capability-support.md",
				Message: fmt.Sprintf("critical capability %q must have exactly one recognized status", capability),
			})
		}
	}
	return violations
}
