package runlauncher

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

func TestBuildEnvironmentPinsProxyAndRemovesProtectedBypasses(t *testing.T) {
	t.Parallel()

	grant := capturecontrol.LaunchGrant{
		Run:             capturerun.View{ID: "run-1"},
		CatalogRevision: 7,
		LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
		Recognition:     clientadapter.RecognitionVerified,
		Adapter:         testAdapterEvidence(clientadapter.LaunchNodeEnvProxy),
		ExecutablePath:  "/tmp/claude",
		ProxyOrigin:     "http://127.0.0.1:43210",
		ProxyCapability: "proxy-capability",
		RunCapability:   "run-capability",
		RootPEMPath:     "/tmp/root.pem",
		ProtectedAuthorities: []string{
			"api.anthropic.com:443",
			"192.0.2.8:8443",
		},
	}
	base := []string{
		"PATH=/usr/bin:/bin",
		"HTTP_PROXY=http://untrusted.invalid",
		"NODE_EXTRA_CA_CERTS=/tmp/untrusted.pem",
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
		values["VIBERMATE_CAPTURE_RUN_ID"] != "run-1" {
		t.Fatalf("managed environment = %+v", values)
	}
	base[0] = "PATH=/mutated"
	if environmentMap(environment)["PATH"] != "/usr/bin:/bin" {
		t.Fatal("caller mutation changed the built environment")
	}
}

func TestBuildEnvironmentDoesNotTrustRootForGenericClient(t *testing.T) {
	t.Parallel()

	grant := capturecontrol.LaunchGrant{
		Run:             capturerun.View{ID: "run-2"},
		CatalogRevision: 7,
		LaunchRecipe:    clientadapter.LaunchGeneric,
		Recognition:     clientadapter.RecognitionUnknown,
		ExecutablePath:  "/tmp/agent",
		ProxyOrigin:     "http://127.0.0.1:43210",
		ProxyCapability: "proxy-capability",
		RunCapability:   "run-capability",
	}
	environment, err := buildEnvironment(
		[]string{
			"NODE_EXTRA_CA_CERTS=/tmp/inherited.pem",
			"NODE_USE_ENV_PROXY=1",
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
}

func TestBuildEnvironmentIsolatesFixedCodexInputs(t *testing.T) {
	t.Parallel()

	grant := capturecontrol.LaunchGrant{
		Run:             capturerun.View{ID: "run-codex"},
		CatalogRevision: 7,
		LaunchRecipe:    clientadapter.LaunchSSLCertFile,
		Recognition:     clientadapter.RecognitionVerified,
		Adapter:         testAdapterEvidence(clientadapter.LaunchSSLCertFile),
		ExecutablePath:  "/tmp/codex.js",
		ProxyOrigin:     "http://127.0.0.1:43210",
		ProxyCapability: "proxy-capability",
		RunCapability:   "run-capability",
		RootPEMPath:     "/tmp/root.pem",
		ProtectedAuthorities: []string{
			"api.openai.com:443",
		},
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

	base := capturecontrol.LaunchGrant{
		Run:             capturerun.View{ID: "run-invalid"},
		CatalogRevision: 7,
		LaunchRecipe:    clientadapter.LaunchSSLCertFile,
		Recognition:     clientadapter.RecognitionVerified,
		Adapter:         testAdapterEvidence(clientadapter.LaunchSSLCertFile),
		ExecutablePath:  "/tmp/codex.js",
		ProxyOrigin:     "http://127.0.0.1:43210",
		ProxyCapability: "proxy-capability",
		RunCapability:   "run-capability",
		RootPEMPath:     "/tmp/root.pem",
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
) *clientadapter.Evidence {
	id := "claude-code"
	shape := clientadapter.InstallNativeSingleBinary
	features := clientadapter.Feature(0)
	if recipe == clientadapter.LaunchSSLCertFile {
		id = "codex-cli"
		shape = clientadapter.InstallNPMWrapperNativeChild
		features = clientadapter.FeatureResponsesWebSocketHTTPFallback
	}
	return &clientadapter.Evidence{
		ID:              id,
		Revision:        1,
		Version:         "test",
		CatalogRevision: 7,
		InstallShape:    shape,
		ReleaseSHA256:   strings.Repeat("c", 64),
		LaunchRecipe:    recipe,
		Features:        features,
	}
}
