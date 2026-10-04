package repositorycheck

import (
	"os"
	"path/filepath"
	"testing"
)

func writeAcceptanceRuleFixture(t *testing.T, root, path, source string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNativeAcceptanceOutboundAllowanceIsExactFile(t *testing.T) {
	for _, name := range []string{"native_mcp_audit.go", "native_mcp_fixture.go", "native_mcp_management.go", "native_mcp_other.go", "renamed_native_mcp_audit.go"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeAcceptanceRuleFixture(t, root, "cmd/vibermate-acceptance/"+name, "package main\nimport (\"net/http\"; \"net\")\nvar _ = http.Client{}\nvar _ = http.Transport{}\nvar _ = net.Dialer{}\n")
			violations := CheckExternalEgressGate(root)
			approved := name == "native_mcp_audit.go" || name == "native_mcp_fixture.go" || name == "native_mcp_management.go"
			if approved && len(violations) != 0 || !approved && len(violations) != 3 {
				t.Fatalf("exact allowance: %+v", violations)
			}
		})
	}
}

func TestNativeAcceptanceCompositionAllowancePreservesIssuerBoundary(t *testing.T) {
	for _, name := range []string{"native_mcp_fixture.go", "native_mcp_management.go", "renamed_native_mcp_fixture.go"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			// Real holder presence makes the production chain check nonvacuous.
			writeAcceptanceRuleFixture(t, root, "internal/serverhost/host.go", "package serverhost\nfunc Start() {}\n")
			writeAcceptanceRuleFixture(t, root, "internal/serverdaemon/daemon.go", "package serverdaemon\nimport \"github.com/vibe-agi/vibermate/internal/serverhost\"\nfunc Run() { serverhost.Start() }\n")
			path := "cmd/vibermate-acceptance/" + name
			source := "package main\nimport \"github.com/vibe-agi/vibermate/internal/serverhost\"\nfunc fixture() { serverhost.Start() }\n"
			writeAcceptanceRuleFixture(t, root, path, source)
			violations := CheckProductionCompositionBoundary(root)
			if name == "native_mcp_fixture.go" && len(violations) != 0 || name != "native_mcp_fixture.go" && (len(violations) != 1 || violations[0].Rule != "unreviewed-server-host-composition") {
				t.Fatalf("exact composition allowance: %+v", violations)
			}
			writeAcceptanceRuleFixture(t, root, path, "package main\nimport \"github.com/vibe-agi/vibermate/internal/manualcapture\"\nvar _ = manualcapture.CreateCommand{}\n")
			violations = CheckProductionCompositionBoundary(root)
			if len(violations) != 1 || violations[0].Rule != "manual-capture-create-outside-issuer" {
				t.Fatalf("issuer boundary bypassed: %+v", violations)
			}
		})
	}
}

func TestNativeSyntheticFactoryOnlyInTaggedAcceptanceComposition(t *testing.T) {
	for _, test := range []struct {
		path, tag, source string
		want              bool
	}{
		{"cmd/vibermate-acceptance/native_mcp_profiles_synthetic.go", "//go:build vibermate_acceptance_synthetic\n", `package main; import nm "github.com/vibe-agi/vibermate/internal/nativemcp"; var profiles=nm.AcceptanceSyntheticContentProfiles()`, true},
		{"cmd/vibermate-acceptance/native_mcp_profiles_synthetic.go", "", `package main; import nm "github.com/vibe-agi/vibermate/internal/nativemcp"; var profiles=nm.AcceptanceSyntheticContentProfiles()`, false},
		{"cmd/vibermate/main.go", "//go:build vibermate_acceptance_synthetic\n", `package main; import nm "github.com/vibe-agi/vibermate/internal/nativemcp"; var profiles=nm.AcceptanceSyntheticContentProfiles`, false},
		{"internal/nativemcp/other.go", "", `package nativemcp; func ordinary(){ AcceptanceSyntheticContentProfiles() }`, false},
		{"cmd/vibermate/main.go", "", `package main; import . "github.com/vibe-agi/vibermate/internal/nativemcp"; var profiles=AcceptanceSyntheticContentProfiles()`, false},
	} {
		t.Run(test.path+test.tag, func(t *testing.T) {
			root := t.TempDir()
			writeAcceptanceRuleFixture(t, root, test.path, test.tag+"\n"+test.source)
			violations := CheckProductionCompositionBoundary(root)
			if (len(violations) == 0) != test.want {
				t.Fatalf("factory composition guard=%+v", violations)
			}
		})
	}
}
