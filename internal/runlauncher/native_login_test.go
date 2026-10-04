package runlauncher

import (
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"slices"
	"strings"
	"testing"
)

func TestNativeLaunchPreservesOriginalLoginWithoutModelPlaceholder(t *testing.T) {
	adapter := testAdapterEvidence(clientadapter.LaunchCodexResponsesHTTP)
	grant := capturecontrol.LaunchGrant{Run: testCaptureRunView("native-login", clientadapter.RecognitionVerified, adapter), CatalogRevision: 7, LaunchRecipe: clientadapter.LaunchCodexResponsesHTTP, Recognition: clientadapter.RecognitionVerified, Adapter: adapter, ExecutablePath: "/tmp/codex", ProxyAddress: "http://127.0.0.1:43210", ProxyDelivery: capturecontrol.ProxyDeliveryLocalListener, ProxyToken: "proxy-capability", RunCapability: "run-capability", RootPEMPath: "/tmp/root.pem", ProtectedAuthorities: []string{"api.openai.com:443", "chatgpt.com:443"}, ManagedCredentialAuthorities: []string{"api.openai.com:443", "chatgpt.com:443"}}
	// Decode the proposed wire against the current consumer so RED is behavioral.
	if err := json.Unmarshal([]byte(`{"nativeToolLogin":{"origin":"https://chatgpt.com","servicePath":"/backend-api/ps/mcp","contract":"codex_legacy_2025_06_18"}}`), &grant); err != nil {
		t.Fatal(err)
	}
	env, err := buildEnvironment([]string{"CODEX_HOME=/tmp/synthetic-native-home", "CODEX_CONNECTORS_TOKEN=synthetic-connectors-token", "CUSTOM_OAUTH_TOKEN=synthetic-oauth-token", "NO_PROXY=.chatgpt.com,.openai.com,localhost"}, grant)
	if err != nil {
		t.Fatal(err)
	}
	got := environmentMap(env)
	if got["CODEX_API_KEY"] != "" {
		t.Fatal("native launch replaced original AuthManager with model placeholder")
	}
	if got["CODEX_HOME"] != "/tmp/synthetic-native-home" || got["CODEX_CONNECTORS_TOKEN"] != "synthetic-connectors-token" || got["CUSTOM_OAUTH_TOKEN"] != "synthetic-oauth-token" || got["NO_PROXY"] != "localhost" {
		t.Fatal("native original auth or protected proxy scope was lost")
	}
	for _, path := range []struct {
		name  string
		extra []string
	}{
		{"ChatGPT AuthManager", nil},
		{"configured connectors token", []string{"CODEX_CONNECTORS_TOKEN=synthetic-connectors-token"}},
		{"configured OAuth/server auth", []string{"CUSTOM_OAUTH_TOKEN=synthetic-server-auth-token"}},
	} {
		t.Run(path.name, func(t *testing.T) {
			base := append([]string{"CODEX_HOME=/tmp/synthetic-native-home", "NO_PROXY=.chatgpt.com,localhost"}, path.extra...)
			env, err := buildEnvironment(base, grant)
			if err != nil {
				t.Fatal(err)
			}
			values := environmentMap(env)
			if values["CODEX_API_KEY"] != "" || values["CODEX_HOME"] != "/tmp/synthetic-native-home" || values["NO_PROXY"] != "localhost" {
				t.Fatal("native login path was replaced")
			}
			for _, entry := range path.extra {
				key, value, _ := strings.Cut(entry, "=")
				if values[key] != value {
					t.Fatal("configured original auth input was replaced")
				}
			}
			args, err := buildChildArguments([]string{"codex", "exec", "synthetic"}, base, grant.LaunchRecipe)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(args, `openai_base_url="https://chatgpt.com/backend-api/codex"`) {
				t.Fatal("native login changed frozen model destination selection")
			}
		})
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	var decoded capturecontrol.LaunchGrant
	if err = json.Unmarshal(encoded, &decoded); err != nil || decoded.Validate() != nil || decoded.NativeToolLogin == nil {
		t.Fatal("typed launch grant wire lost native metadata")
	}
	if decoded.NativeToolLogin.Origin != "https://chatgpt.com" || decoded.NativeToolLogin.ServicePath != "/backend-api/ps/mcp" || decoded.NativeToolLogin.Contract != "codex_legacy_2025_06_18" {
		t.Fatal("native wire carried wrong service identity")
	}
	for _, view := range []capturecontrol.NativeToolLoginView{{Origin: "https://unprotected.example", ServicePath: "/backend-api/ps/mcp", Contract: "codex_legacy_2025_06_18"}, {Origin: "https://chatgpt.com", ServicePath: "/guessed", Contract: "codex_legacy_2025_06_18"}, {Origin: "https://chatgpt.com", ServicePath: "/backend-api/ps/mcp", Contract: "modern"}} {
		changed := grant
		changed.NativeToolLogin = &view
		if changed.Validate() == nil {
			t.Fatal("substituted native launch view accepted")
		}
	}
	grant.NativeToolLogin = nil
	off, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(off), "nativeToolLogin") {
		t.Fatal("OFF launch wire gained native authority field")
	}
	original, err := buildEnvironment(nil, grant)
	if err != nil {
		t.Fatal(err)
	}
	if environmentMap(original)["CODEX_API_KEY"] != "vibermate-local-proxy" {
		t.Fatal("OFF managed model launch lost original placeholder behavior")
	}
}
