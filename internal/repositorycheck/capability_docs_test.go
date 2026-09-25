package repositorycheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapabilityDocsRejectVersionAndStatusDrift(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	files := map[string]string{
		"README.md":                   "[Support](docs/capability-support.md)\n",
		"README.zh-CN.md":             "[Support](docs/capability-support.md)\n",
		"Dockerfile":                  "LABEL org.opencontainers.image.version=\"1.2.3\"\n",
		"ui/flutter_app/pubspec.yaml": "version: 1.2.3+4\n",
		"docs/releases/v1.2.2.md":     "released\n",
		"docs/capability-support.md": "Source package version: **1.2.3**. Latest published release: **v1.2.2**.\n" +
			"| Capability ID | Status | Boundary |\n" +
			"| --- | --- | --- |\n" +
			"| `macos-app` | Released | x |\n" +
			"| `linux-server-web` | Released | x |\n" +
			"| `remote-web-tls` | Released | x |\n" +
			"| `codex-oauth` | Experimental | x |\n" +
			"| `native-cli-identity-rewrite` | Unsupported | x |\n" +
			"| `editor-acp` | Branch-only | x |\n" +
			"| `automatic-account-failover` | Unsupported | x |\n",
	}
	for path, contents := range files {
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if violations := CheckCapabilityDocs(root); len(violations) != 0 {
		t.Fatalf("matching capability docs failed: %v", violations)
	}

	pubspec := filepath.Join(root, "ui", "flutter_app", "pubspec.yaml")
	if err := os.WriteFile(pubspec, []byte("version: 1.2.4+5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	violations := CheckCapabilityDocs(root)
	if len(violations) == 0 || !strings.Contains(violations[0].String(), "capability-docs") {
		t.Fatalf("version drift was not rejected: %v", violations)
	}

	if err := os.WriteFile(pubspec, []byte("version: 1.2.3+4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	supportPath := filepath.Join(root, "docs", "capability-support.md")
	broken := strings.ReplaceAll(files["docs/capability-support.md"], "Branch-only", "Planned")
	if err := os.WriteFile(supportPath, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	violations = CheckCapabilityDocs(root)
	if len(violations) == 0 || !strings.Contains(violations[0].String(), "editor-acp") {
		t.Fatalf("status drift was not rejected: %v", violations)
	}
}
