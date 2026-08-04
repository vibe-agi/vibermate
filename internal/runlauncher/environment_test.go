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
		ProxyToken:      "proxy-capability",
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
		values["VIBERMATE_CAPTURE_RUN_ID"] != "run-1" {
		t.Fatalf("managed environment = %+v", values)
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
		CatalogRevision:      7,
		LaunchRecipe:         clientadapter.LaunchGeneric,
		Recognition:          clientadapter.RecognitionUnknown,
		ExecutablePath:       "/tmp/agent",
		ProxyAddress:         "http://127.0.0.1:43210",
		ProxyToken:           "proxy-capability",
		RunCapability:        "run-capability",
		ProtectedAuthorities: []string{},
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

	adapter := testAdapterEvidence(clientadapter.LaunchSSLCertFile)
	grant := capturecontrol.LaunchGrant{
		Run: testCaptureRunView(
			"run-codex",
			clientadapter.RecognitionVerified,
			adapter,
		),
		CatalogRevision: 7,
		LaunchRecipe:    clientadapter.LaunchSSLCertFile,
		Recognition:     clientadapter.RecognitionVerified,
		Adapter:         adapter,
		ExecutablePath:  "/tmp/codex.js",
		ProxyAddress:    "http://127.0.0.1:43210",
		ProxyToken:      "proxy-capability",
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

	adapter := testAdapterEvidence(clientadapter.LaunchSSLCertFile)
	base := capturecontrol.LaunchGrant{
		Run: testCaptureRunView(
			"run-invalid",
			clientadapter.RecognitionVerified,
			adapter,
		),
		CatalogRevision:      7,
		LaunchRecipe:         clientadapter.LaunchSSLCertFile,
		Recognition:          clientadapter.RecognitionVerified,
		Adapter:              adapter,
		ExecutablePath:       "/tmp/codex.js",
		ProxyAddress:         "http://127.0.0.1:43210",
		ProxyToken:           "proxy-capability",
		RunCapability:        "run-capability",
		RootPEMPath:          "/tmp/root.pem",
		ProtectedAuthorities: []string{},
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
) *capturecontrol.ClientAdapterView {
	id := "claude-code"
	shape := clientadapter.InstallNativeSingleBinary
	if recipe == clientadapter.LaunchSSLCertFile {
		id = "codex-cli"
		shape = clientadapter.InstallNPMWrapperNativeChild
	}
	return &capturecontrol.ClientAdapterView{
		ID:              id,
		Revision:        1,
		Version:         "test",
		CatalogRevision: 7,
		Source: capturecontrol.
			ClientAdapterSourcePrelaunchDigestCatalog,
		InstallShape: shape,
		LaunchRecipe: recipe,
	}
}

func testCaptureRunView(
	id string,
	recognition clientadapter.Recognition,
	adapter *capturecontrol.ClientAdapterView,
) capturecontrol.CaptureRunView {
	createdAt := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	state := clientadapter.StatusGeneric
	if adapter != nil {
		state = clientadapter.StatusVerified
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
		ClientAdapter:      adapter,
	}
}
