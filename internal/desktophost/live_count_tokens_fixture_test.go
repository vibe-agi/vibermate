//go:build !vibermate_native_secrets

package desktophost_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// countTokensFixturePlugin names the plugin the observation prices. It is
// built here rather than borrowed from the machine so the run says the same
// thing everywhere.
const countTokensFixturePlugin = "c0a-probe@c0a-fixture"

// countTokensSkillBody is the skill text whose token count the client asks the
// endpoint for. Its size is the point: it makes the `count_tokens` body
// kilobytes of client content, which is what may not leave.
var countTokensSkillBody = strings.Repeat(
	countTokensSkillMarker+" ",
	64,
)

// newCountTokensPluginFixture builds a local marketplace holding one plugin
// with two skills, installs it into a private HOME, and returns that HOME.
//
// This runs outside the launcher because it is setup, not the observation. It
// needs no network: a marketplace on disk installs offline.
func newCountTokensPluginFixture(t *testing.T, agentPath string) string {
	t.Helper()

	marketplace := filepath.Join(t.TempDir(), "marketplace")
	plugin := filepath.Join(marketplace, "c0a-probe")
	writeFixtureFile(
		t,
		filepath.Join(marketplace, ".claude-plugin", "marketplace.json"),
		`{"name":"c0a-fixture",`+
			`"description":"A local marketplace built for one observation.",`+
			`"owner":{"name":"vibermate"},`+
			`"plugins":[{"name":"c0a-probe","source":"./c0a-probe",`+
			`"description":"Exists so the client prices something."}]}`,
	)
	writeFixtureFile(
		t,
		filepath.Join(plugin, ".claude-plugin", "plugin.json"),
		`{"name":"c0a-probe","version":"1.0.0",`+
			`"description":"Exists so the client prices something."}`,
	)
	for _, skill := range []string{"probe-one", "probe-two"} {
		writeFixtureFile(
			t,
			filepath.Join(plugin, "skills", skill, "SKILL.md"),
			"---\nname: "+skill+"\ndescription: A fixture skill that exists "+
				"only so the client prices it.\n---\n\n# "+skill+"\n\n"+
				countTokensSkillBody+"\n",
		)
	}

	home := t.TempDir()
	for _, arguments := range [][]string{
		{"plugin", "marketplace", "add", marketplace},
		{"plugin", "install", countTokensFixturePlugin},
	} {
		command := exec.Command(agentPath, arguments...)
		command.Env = append(
			os.Environ(),
			"HOME="+home,
			// Setup must not reach anything, and must not carry a real
			// credential if it tries.
			"ANTHROPIC_BASE_URL=http://127.0.0.1:1",
			"ANTHROPIC_API_KEY=vibermate-count-tokens-placeholder",
		)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture setup %v: %v\n%s", arguments, err, out)
		}
	}
	return home
}

func writeFixtureFile(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
