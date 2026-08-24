package runlauncher

import (
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

func TestBuildEnvironmentPinsProxyAndRemovesProtectedBypasses(t *testing.T) {
	t.Parallel()

	adapter := testAdapterEvidence(clientadapter.LaunchNodeEnvProxy)
	grant := capturecontrol.LaunchGrant{
		Run: testCaptureRunView(
			"run-1",
			clientadapter.RecognitionVerified,
			adapter,
		),
		CatalogRevision: 7,
		LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
		Recognition:     clientadapter.RecognitionVerified,
		Adapter:         adapter,
		ExecutablePath:  "/tmp/claude",
		ProxyAddress:    "http://127.0.0.1:43210",
		ProxyDelivery:   capturecontrol.ProxyDeliveryLocalListener,
		ProxyToken:      "proxy-capability",
		RunCapability:   "run-capability",
		RootPEMPath:     "/tmp/root.pem",
		ProtectedAuthorities: []string{
			"api.anthropic.com:443",
			"ambient.invalid:443",
			"192.0.2.8:8443",
		},
		ManagedCredentialAuthorities: []string{"ambient.invalid:443"},
	}
	base := []string{
		"PATH=/usr/bin:/bin",
		"UNRELATED=value",
		"HTTP_PROXY=http://untrusted.invalid",
		"NODE_EXTRA_CA_CERTS=/tmp/untrusted.pem",
		"ANTHROPIC_API_KEY=ambient-api-key",
		"Anthropic_Auth_Token=mixed-case-ambient-auth-token",
		"ANTHROPIC_AUTH_TOKEN=ambient-auth-token",
		"CLAUDE_CODE_OAUTH_TOKEN=ambient-oauth-token",
		"ANTHROPIC_BASE_URL=https://ambient.invalid",
		"ANTHROPIC_BEDROCK_BASE_URL=https://bedrock.invalid",
		"ANTHROPIC_CUSTOM_HEADERS=x-ambient: secret",
		"ANTHROPIC_FOUNDRY_BASE_URL=https://foundry.invalid",
		"ANTHROPIC_VERTEX_BASE_URL=https://vertex.invalid",
		"CLAUDE_CODE_USE_BEDROCK=1",
		"CLAUDE_CODE_USE_FOUNDRY=1",
		"CLAUDE_CODE_USE_VERTEX=1",
		"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=0",
		"CLAUDE_CONFIG_DIR=/tmp/client-state",
		"VIBERMATE_CONNECTION=office",
		"VIBERMATE_CREDENTIAL_FILE=/tmp/control-credential",
		"VIBERMATE_TOKEN=raw-control-secret",
		"VIBERMATE_CONTROL_CREDENTIAL=raw-control-secret",
		"VIBERMATE_ENROLLMENT_TOKEN=one-time-secret",
		"VIBERMATE_ADMIN_TOKEN=admin-secret",
		"VIBERMATE_DISCOVERY_PATH=/tmp/private-discovery",
		"NO_PROXY=localhost,.anthropic.com,example.org,192.0.2.8:8443,10.0.0.0/8,*",
		"no_proxy=localhost,api.anthropic.com:443",
	}
	environment, err := buildEnvironment(base, grant)
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMap(environment)
	proxy := "http://capture:proxy-capability@127.0.0.1:43210"
	for _, name := range []string{
		"HTTP_PROXY",
		"HTTPS_PROXY",
		"http_proxy",
		"https_proxy",
	} {
		if values[name] != proxy {
			t.Fatalf("%s = %q", name, values[name])
		}
	}
	if values["NO_PROXY"] != "localhost,example.org" ||
		values["no_proxy"] != values["NO_PROXY"] {
		t.Fatalf("filtered NO_PROXY = %q", values["NO_PROXY"])
	}
	if values["NODE_EXTRA_CA_CERTS"] != "/tmp/root.pem" ||
		values["NODE_USE_ENV_PROXY"] != "1" ||
		values["ANTHROPIC_AUTH_TOKEN"] != "vibermate-local-proxy" ||
		values[claudeDisableNonStreamingFallback] != "1" ||
		values["CLAUDE_CONFIG_DIR"] != "/tmp/client-state" ||
		values["UNRELATED"] != "value" ||
		values["VIBERMATE_CAPTURE_RUN_ID"] != "run-1" {
		t.Fatalf("managed environment = %+v", values)
	}
	for _, forbidden := range []string{
		"ANTHROPIC_API_KEY",
		"Anthropic_Auth_Token",
		"CLAUDE_CODE_OAUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_BEDROCK_BASE_URL",
		"ANTHROPIC_CUSTOM_HEADERS",
		"ANTHROPIC_FOUNDRY_BASE_URL",
		"ANTHROPIC_VERTEX_BASE_URL",
		"CLAUDE_CODE_USE_BEDROCK",
		"CLAUDE_CODE_USE_FOUNDRY",
		"CLAUDE_CODE_USE_VERTEX",
	} {
		if _, exists := values[forbidden]; exists {
			t.Fatalf("fixed Claude inherited conflicting %s", forbidden)
		}
	}
	for _, forbidden := range []string{
		"VIBERMATE_CONNECTION",
		"VIBERMATE_CREDENTIAL_FILE",
		"VIBERMATE_TOKEN",
		"VIBERMATE_CONTROL_CREDENTIAL",
		"VIBERMATE_ENROLLMENT_TOKEN",
		"VIBERMATE_ADMIN_TOKEN",
		"VIBERMATE_DISCOVERY_PATH",
	} {
		if _, exists := values[forbidden]; exists {
			t.Fatalf("captured child inherited %s", forbidden)
		}
	}
	base[0] = "PATH=/mutated"
	if environmentMap(environment)["PATH"] != "/usr/bin:/bin" {
		t.Fatal("caller mutation changed the built environment")
	}
}

func TestBuildEnvironmentDoesNotTrustRootForGenericClient(t *testing.T) {
	t.Parallel()

	grant := capturecontrol.LaunchGrant{
		Run: testCaptureRunView(
			"run-2",
			clientadapter.RecognitionUnknown,
			nil,
		),
		CatalogRevision:              7,
		LaunchRecipe:                 clientadapter.LaunchGeneric,
		Recognition:                  clientadapter.RecognitionUnknown,
		ExecutablePath:               "/tmp/agent",
		ProxyAddress:                 "http://127.0.0.1:43210",
		ProxyDelivery:                capturecontrol.ProxyDeliveryLocalListener,
		ProxyToken:                   "proxy-capability",
		RunCapability:                "run-capability",
		ProtectedAuthorities:         []string{},
		ManagedCredentialAuthorities: []string{},
	}
	environment, err := buildEnvironment(
		[]string{
			"NODE_EXTRA_CA_CERTS=/tmp/inherited.pem",
			"NODE_USE_ENV_PROXY=1",
			"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=0",
		},
		grant,
	)
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMap(environment)
	if _, exists := values["NODE_EXTRA_CA_CERTS"]; exists {
		t.Fatal("generic client inherited adapter-specific Root trust")
	}
	if _, exists := values["NODE_USE_ENV_PROXY"]; exists {
		t.Fatal("generic client inherited adapter-specific proxy behavior")
	}
	if values[claudeDisableNonStreamingFallback] != "0" {
		t.Fatal("generic client did not keep its own streaming fallback policy")
	}
}

func TestBuildEnvironmentPreservesClientAuthOutsideManagedAuthority(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name        string
		recipe      clientadapter.LaunchRecipe
		base        []string
		want        map[string]string
		managedHost string
	}{
		{
			name:   "Claude",
			recipe: clientadapter.LaunchNodeEnvProxy,
			base: []string{
				"ANTHROPIC_API_KEY=client-api-key",
				"CLAUDE_CODE_OAUTH_TOKEN=client-oauth-token",
			},
			want: map[string]string{
				"ANTHROPIC_API_KEY":       "client-api-key",
				"CLAUDE_CODE_OAUTH_TOKEN": "client-oauth-token",
			},
			managedHost: "managed.example:443",
		},
		{
			name:   "Codex",
			recipe: clientadapter.LaunchCodexResponsesHTTP,
			base: []string{
				"CODEX_API_KEY=client-api-key",
				"OPENAI_BASE_URL=https://api.openai.com/v1",
			},
			want: map[string]string{
				"CODEX_API_KEY":   "client-api-key",
				"OPENAI_BASE_URL": "https://api.openai.com/v1",
			},
			managedHost: "managed.example:443",
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			adapter := testAdapterEvidence(testCase.recipe)
			grant := capturecontrol.LaunchGrant{
				Run: testCaptureRunView(
					"run-client-auth",
					clientadapter.RecognitionVerified,
					adapter,
				),
				CatalogRevision:              7,
				LaunchRecipe:                 testCase.recipe,
				Recognition:                  clientadapter.RecognitionVerified,
				Adapter:                      adapter,
				ExecutablePath:               "/tmp/agent",
				ProxyAddress:                 "http://127.0.0.1:43210",
				ProxyDelivery:                capturecontrol.ProxyDeliveryLocalListener,
				ProxyToken:                   "proxy-capability",
				RunCapability:                "run-capability",
				RootPEMPath:                  "/tmp/root.pem",
				ProtectedAuthorities:         []string{testCase.managedHost},
				ManagedCredentialAuthorities: []string{testCase.managedHost},
			}
			environment, err := buildEnvironment(testCase.base, grant)
			if err != nil {
				t.Fatal(err)
			}
			values := environmentMap(environment)
			for key, want := range testCase.want {
				if values[key] != want {
					t.Fatalf("%s = %q, want %q", key, values[key], want)
				}
			}
		})
	}
}

func TestBuildEnvironmentBootstrapsDefaultManagedClientOrigins(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name             string
		recipe           clientadapter.LaunchRecipe
		managedAuthority string
		ambientKey       string
		placeholderKey   string
	}{
		{
			name:             "Claude",
			recipe:           clientadapter.LaunchNodeEnvProxy,
			managedAuthority: "api.anthropic.com:443",
			ambientKey:       "ANTHROPIC_API_KEY=client-api-key",
			placeholderKey:   "ANTHROPIC_AUTH_TOKEN",
		},
		{
			name:             "Codex",
			recipe:           clientadapter.LaunchCodexResponsesHTTP,
			managedAuthority: "api.openai.com:443",
			ambientKey:       "CODEX_API_KEY=client-api-key",
			placeholderKey:   "CODEX_API_KEY",
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			adapter := testAdapterEvidence(testCase.recipe)
			grant := capturecontrol.LaunchGrant{
				Run: testCaptureRunView(
					"run-managed-default",
					clientadapter.RecognitionVerified,
					adapter,
				),
				CatalogRevision:              7,
				LaunchRecipe:                 testCase.recipe,
				Recognition:                  clientadapter.RecognitionVerified,
				Adapter:                      adapter,
				ExecutablePath:               "/tmp/agent",
				ProxyAddress:                 "http://127.0.0.1:43210",
				ProxyDelivery:                capturecontrol.ProxyDeliveryLocalListener,
				ProxyToken:                   "proxy-capability",
				RunCapability:                "run-capability",
				RootPEMPath:                  "/tmp/root.pem",
				ProtectedAuthorities:         []string{testCase.managedAuthority},
				ManagedCredentialAuthorities: []string{testCase.managedAuthority},
			}
			environment, err := buildEnvironment(
				[]string{testCase.ambientKey},
				grant,
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := environmentMap(environment)[testCase.placeholderKey]; got != "vibermate-local-proxy" {
				t.Fatalf("%s = %q", testCase.placeholderKey, got)
			}
		})
	}
}

func TestBuildEnvironmentIsolatesFixedCodexInputs(t *testing.T) {
	t.Parallel()

	adapter := testAdapterEvidence(clientadapter.LaunchCodexResponsesHTTP)
	grant := capturecontrol.LaunchGrant{
		Run: testCaptureRunView(
			"run-codex",
			clientadapter.RecognitionVerified,
			adapter,
		),
		CatalogRevision: 7,
		LaunchRecipe:    clientadapter.LaunchCodexResponsesHTTP,
		Recognition:     clientadapter.RecognitionVerified,
		Adapter:         adapter,
		ExecutablePath:  "/tmp/codex.js",
		ProxyAddress:    "http://127.0.0.1:43210",
		ProxyDelivery:   capturecontrol.ProxyDeliveryLocalListener,
		ProxyToken:      "proxy-capability",
		RunCapability:   "run-capability",
		RootPEMPath:     "/tmp/root.pem",
		ProtectedAuthorities: []string{
			"api.openai.com:443",
			"ambient.invalid:443",
		},
		ManagedCredentialAuthorities: []string{"ambient.invalid:443"},
	}
	base := []string{
		"PATH=/usr/bin:/bin",
		"UNRELATED=value",
		"HTTP_PROXY=http://ambient.invalid",
		"ALL_PROXY=socks5://ambient.invalid",
		"all_proxy=socks5://ambient.invalid",
		"NO_PROXY=.openai.com,localhost",
		"SSL_CERT_FILE=/tmp/ambient-ssl.pem",
		"REQUESTS_CA_BUNDLE=/tmp/ambient-requests.pem",
		"CURL_CA_BUNDLE=/tmp/ambient-curl.pem",
		"NODE_EXTRA_CA_CERTS=/tmp/ambient-node.pem",
		"NODE_USE_ENV_PROXY=1",
		"OPENAI_BASE_URL=https://ambient.invalid/v1",
		"CODEX_BASE_URL=https://ambient.invalid/v1",
		"OPENAI_API_KEY=ambient-openai-secret",
		"CODEX_API_KEY=ambient-codex-secret",
		"OPENAI_ORGANIZATION=ambient-org",
		"OPENAI_PROJECT=ambient-project",
	}
	environment, err := buildEnvironment(base, grant)
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMap(environment)
	if values["SSL_CERT_FILE"] != "/tmp/root.pem" ||
		values["CODEX_API_KEY"] != "vibermate-local-proxy" ||
		values["UNRELATED"] != "value" ||
		values["NO_PROXY"] != "localhost" {
		t.Fatalf("fixed Codex environment = %+v", values)
	}
	for _, forbidden := range []string{
		"ALL_PROXY",
		"all_proxy",
		"REQUESTS_CA_BUNDLE",
		"CURL_CA_BUNDLE",
		"NODE_EXTRA_CA_CERTS",
		"NODE_USE_ENV_PROXY",
		"OPENAI_BASE_URL",
		"CODEX_BASE_URL",
		"OPENAI_API_KEY",
		"OPENAI_ORGANIZATION",
		"OPENAI_PROJECT",
	} {
		if _, exists := values[forbidden]; exists {
			t.Fatalf(
				"fixed Codex inherited conflicting %s=%q",
				forbidden,
				values[forbidden],
			)
		}
	}
}

func TestBuildEnvironmentRejectsUnboundAdapterRecipes(t *testing.T) {
	t.Parallel()

	adapter := testAdapterEvidence(clientadapter.LaunchCodexResponsesHTTP)
	base := capturecontrol.LaunchGrant{
		Run: testCaptureRunView(
			"run-invalid",
			clientadapter.RecognitionVerified,
			adapter,
		),
		CatalogRevision:              7,
		LaunchRecipe:                 clientadapter.LaunchCodexResponsesHTTP,
		Recognition:                  clientadapter.RecognitionVerified,
		Adapter:                      adapter,
		ExecutablePath:               "/tmp/codex.js",
		ProxyAddress:                 "http://127.0.0.1:43210",
		ProxyDelivery:                capturecontrol.ProxyDeliveryLocalListener,
		ProxyToken:                   "proxy-capability",
		RunCapability:                "run-capability",
		RootPEMPath:                  "/tmp/root.pem",
		ProtectedAuthorities:         []string{},
		ManagedCredentialAuthorities: []string{},
	}
	for _, test := range []struct {
		name   string
		mutate func(*capturecontrol.LaunchGrant)
	}{
		{
			name: "missing evidence",
			mutate: func(grant *capturecontrol.LaunchGrant) {
				grant.Adapter = nil
			},
		},
		{
			name: "catalog mismatch",
			mutate: func(grant *capturecontrol.LaunchGrant) {
				grant.Adapter.CatalogRevision = 8
			},
		},
		{
			name: "recipe mismatch",
			mutate: func(grant *capturecontrol.LaunchGrant) {
				grant.Adapter.LaunchRecipe =
					clientadapter.LaunchNodeEnvProxy
			},
		},
		{
			name: "generic with evidence",
			mutate: func(grant *capturecontrol.LaunchGrant) {
				grant.LaunchRecipe = clientadapter.LaunchGeneric
				grant.RootPEMPath = ""
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			grant := base
			evidence := *base.Adapter
			grant.Adapter = &evidence
			test.mutate(&grant)
			if _, err := buildEnvironment(nil, grant); err == nil {
				t.Fatal("inconsistent launch grant was accepted")
			}
		})
	}
}

func environmentMap(environment []string) map[string]string {
	values := make(map[string]string)
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	return values
}

func testAdapterEvidence(
	recipe clientadapter.LaunchRecipe,
) *capturecontrol.ClientLaunchAdapterView {
	id := "claude-code"
	shape := clientadapter.InstallNativeSingleBinary
	fallbackPolicy := clientadapter.StreamingFallbackCoreOwned
	if recipe == clientadapter.LaunchCodexResponsesHTTP {
		id = "codex-cli"
		shape = clientadapter.InstallNPMWrapperNativeChild
		fallbackPolicy = clientadapter.StreamingFallbackClientDefault
	}
	return &capturecontrol.ClientLaunchAdapterView{
		ClientAdapterView: capturecontrol.ClientAdapterView{
			ID:              id,
			Revision:        1,
			Version:         "test",
			CatalogRevision: 7,
			Source: capturecontrol.
				ClientAdapterSourcePrelaunchDigestCatalog,
			InstallShape: shape,
			LaunchRecipe: recipe,
		},
		StreamingFallbackPolicy: fallbackPolicy,
	}
}

func testCaptureRunView(
	id string,
	recognition clientadapter.Recognition,
	adapter *capturecontrol.ClientLaunchAdapterView,
) capturecontrol.CaptureRunView {
	createdAt := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	state := clientadapter.StatusGeneric
	if adapter != nil {
		state = clientadapter.StatusVerified
	}
	var runAdapter *capturecontrol.ClientAdapterView
	if adapter != nil {
		identity := adapter.ClientAdapterView
		runAdapter = &identity
	}
	return capturecontrol.CaptureRunView{
		ID:                 id,
		ExecutableLabel:    "agent",
		CWD:                "/tmp",
		CreatedAt:          createdAt,
		ExpiresAt:          createdAt.Add(time.Minute),
		ClientAdapterState: state,
		ClientRecognition:  recognition,
		CatalogRevision:    7,
		ClientAdapter:      runAdapter,
	}
}
