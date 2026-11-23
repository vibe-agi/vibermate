package runlauncher

import (
	"github.com/vibe-agi/vibermate/internal/capturescope"
	"net"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/locales"
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
	// ADR 0025: without an Environment that allows it, the user's own
	// NO_PROXY counts for nothing; only the built-in set stays off ViberMate.
	if values["NO_PROXY"] != builtinNoProxy ||
		values["no_proxy"] != values["NO_PROXY"] {
		t.Fatalf("NO_PROXY without a capture scope = %q", values["NO_PROXY"])
	}
	grant.CaptureScope = &capturescope.Policy{
		AllowUserAdditions: true,
		Rules:              []capturescope.Rule{{ID: "intranet", Kind: capturescope.KindSuffix, Value: "corp.example"}},
	}
	scoped, err := buildEnvironment(base, grant)
	if err != nil {
		t.Fatal(err)
	}
	// User entries join only within the bounds: protected authorities, public
	// addresses and a bare wildcard are dropped.
	if got := environmentMap(scoped)["NO_PROXY"]; got != builtinNoProxy+",corp.example,example.org,10.0.0.0/8" {
		t.Fatalf("NO_PROXY with a scope allowing user additions = %q", got)
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

func TestBuildEnvironmentAppliesTheFrozenLaunchEnvironmentPolicy(
	t *testing.T,
) {
	t.Parallel()
	grant := capturecontrol.LaunchGrant{
		Run: testCaptureRunView(
			"run-launch-environment",
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
		LaunchEnvironment: environment.LaunchEnvironmentPolicy{
			SetEnv: map[string]string{
				"EXISTING_SETTING": "environment-value",
				"NEW_SETTING":      "new-value",
			},
			DeleteEnv: []string{"REMOVE_SETTING"},
		},
	}
	base := []string{
		"EXISTING_SETTING=ambient-value",
		"REMOVE_SETTING=ambient-value",
		"PRESERVE_SETTING=ambient-value",
	}

	built, err := buildEnvironment(base, grant)
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMap(built)
	if values["EXISTING_SETTING"] != "environment-value" ||
		values["NEW_SETTING"] != "new-value" ||
		values["PRESERVE_SETTING"] != "ambient-value" {
		t.Fatalf("child launch environment = %+v", values)
	}
	if _, exists := values["REMOVE_SETTING"]; exists {
		t.Fatalf("deleted child variable survived: %+v", values)
	}
	if base[0] != "EXISTING_SETTING=ambient-value" {
		t.Fatal("building the child environment mutated the launcher environment")
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
			want := "vibermate-local-proxy"
			if testCase.recipe == clientadapter.LaunchCodexResponsesHTTP {
				want = "client-api-key"
			}
			if got := environmentMap(environment)[testCase.placeholderKey]; got != want {
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
		"CODEX_CA_CERTIFICATE=/tmp/ambient-codex.pem",
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
		"VST_PLANE_TOKEN=private-plane-token",
	}
	environment, err := buildEnvironment(base, grant)
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMap(environment)
	if values["CODEX_API_KEY"] != "ambient-codex-secret" ||
		values["VST_PLANE_TOKEN"] != "private-plane-token" ||
		values["UNRELATED"] != "value" ||
		values["NO_PROXY"] != builtinNoProxy {
		t.Fatalf("fixed Codex environment = %+v", values)
	}
	if runtime.GOOS == "darwin" {
		if _, exists := values["CODEX_CA_CERTIFICATE"]; exists {
			t.Fatalf("local macOS Codex replaced native trust: %+v", values)
		}
	} else if values["CODEX_CA_CERTIFICATE"] != "/tmp/root.pem" {
		t.Fatalf("fixed Codex CA = %+v", values)
	}
	for _, key := range []string{"OPENAI_BASE_URL", "CODEX_BASE_URL", "OPENAI_API_KEY", "OPENAI_ORGANIZATION", "OPENAI_PROJECT"} {
		if values[key] != environmentMap(base)[key] {
			t.Fatalf("native %s was replaced", key)
		}
	}
	for _, forbidden := range []string{
		"ALL_PROXY",
		"all_proxy",
		"SSL_CERT_FILE",
		"REQUESTS_CA_BUNDLE",
		"CURL_CA_BUNDLE",
		"NODE_EXTRA_CA_CERTS",
		"NODE_USE_ENV_PROXY",
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

// builtinNoProxy is what proxy-variable clients exclude when the Environment
// sets no capture scope.
var builtinNoProxy = capturescope.Compile(nil, nil, capturescope.Options{}).NoProxy()

func recognizedSignerGrant(
	id string,
	shape clientadapter.InstallShape,
	recipe clientadapter.LaunchRecipe,
	managed []string,
) capturecontrol.LaunchGrant {
	run := recognizedRun()
	label := "claude"
	if id == "codex-cli" {
		label = "codex"
	}
	run.ExecutableLabel = label
	return capturecontrol.LaunchGrant{
		Run:             run,
		CatalogRevision: run.CatalogRevision,
		LaunchRecipe:    recipe,
		Recognition:     clientadapter.RecognitionRecognized,
		Signer: &capturecontrol.ClientSignerView{
			ID:              id,
			Revision:        1,
			CatalogRevision: run.CatalogRevision,
			InstallShape:    shape,
			LaunchRecipe:    recipe,
			SignedPath:      "/opt/example/" + label,
		},
		ExecutablePath:               "/opt/example/" + label,
		ProxyAddress:                 "http://127.0.0.1:32123",
		ProxyDelivery:                capturecontrol.ProxyDeliveryLocalListener,
		ProxyToken:                   "proxy-capability",
		RunCapability:                "run-capability",
		RootPEMPath:                  "/tmp/root.pem",
		ProtectedAuthorities:         append([]string{}, managed...),
		ManagedCredentialAuthorities: append([]string{}, managed...),
	}
}

func verifiedAdapterGrant(
	recipe clientadapter.LaunchRecipe,
	managed []string,
) capturecontrol.LaunchGrant {
	adapter := testAdapterEvidence(recipe)
	return capturecontrol.LaunchGrant{
		Run:                          testCaptureRunView("run-verified", clientadapter.RecognitionVerified, adapter),
		CatalogRevision:              7,
		LaunchRecipe:                 recipe,
		Recognition:                  clientadapter.RecognitionVerified,
		Adapter:                      adapter,
		ExecutablePath:               "/tmp/client",
		ProxyAddress:                 "http://127.0.0.1:43210",
		ProxyDelivery:                capturecontrol.ProxyDeliveryLocalListener,
		ProxyToken:                   "proxy-capability",
		RunCapability:                "run-capability",
		RootPEMPath:                  "/tmp/root.pem",
		ProtectedAuthorities:         append([]string{}, managed...),
		ManagedCredentialAuthorities: append([]string{}, managed...),
	}
}

func genericGrant() capturecontrol.LaunchGrant {
	return capturecontrol.LaunchGrant{
		Run:                          testCaptureRunView("run-generic", clientadapter.RecognitionUnknown, nil),
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
}

type fallbackGrantCase struct {
	name  string
	grant capturecontrol.LaunchGrant
	// want is the policy while the operator switch is on, unset or unread.
	want clientadapter.StreamingFallbackPolicy
	// recognized grants read the operator switch; no other grant does.
	recognized bool
}

// fallbackGrantCases are the grant shapes the launcher's one fallback
// decision serves. A verified grant carries its adapter's policy; a
// publisher-recognized Claude Code whose own target is an Upstream-only
// authority receives core_owned from the recognized-client policy.
func fallbackGrantCases() []fallbackGrantCase {
	anthropic := []string{"api.anthropic.com:443"}
	return []fallbackGrantCase{
		{"verified core_owned, managed target",
			verifiedAdapterGrant(clientadapter.LaunchNodeEnvProxy, anthropic),
			clientadapter.StreamingFallbackCoreOwned, false},
		{"verified core_owned, unmanaged target",
			verifiedAdapterGrant(clientadapter.LaunchNodeEnvProxy, []string{}),
			clientadapter.StreamingFallbackCoreOwned, false},
		{"verified client_default",
			verifiedAdapterGrant(clientadapter.LaunchCodexResponsesHTTP, []string{}),
			clientadapter.StreamingFallbackClientDefault, false},
		{"generic", genericGrant(), clientadapter.StreamingFallbackClientDefault, false},
		{"recognized claude, managed target",
			recognizedSignerGrant("claude-code", clientadapter.InstallNativeSingleBinary, clientadapter.LaunchNodeEnvProxy, anthropic),
			clientadapter.StreamingFallbackCoreOwned, true},
		{"recognized claude, unmanaged target",
			recognizedSignerGrant("claude-code", clientadapter.InstallNativeSingleBinary, clientadapter.LaunchNodeEnvProxy, []string{}),
			clientadapter.StreamingFallbackClientDefault, true},
		{"recognized claude, wrapper shape",
			recognizedSignerGrant("claude-code", clientadapter.InstallNPMWrapperNativeChild, clientadapter.LaunchNodeEnvProxy, anthropic),
			clientadapter.StreamingFallbackClientDefault, true},
		{"recognized codex",
			recognizedSignerGrant("codex-cli", clientadapter.InstallNPMWrapperNativeChild, clientadapter.LaunchCodexResponsesHTTP, []string{}),
			clientadapter.StreamingFallbackClientDefault, true},
	}
}

// A verified grant keeps its exact release's policy, and a generic one keeps
// the client's own. A recognized grant reads the operator switch from the
// launcher's base environment: off withholds the fallback the policy would
// have given, and every other value leaves the decision as it is.
func TestStreamingFallbackDecisionFollowsTheGrant(t *testing.T) {
	t.Parallel()

	for _, testCase := range fallbackGrantCases() {
		for _, switchValue := range []string{"", "on", "off", "0", "banana"} {
			name := testCase.name + "/switch " + switchValue
			if switchValue == "" {
				name = testCase.name + "/switch unset"
			}
			t.Run(name, func(t *testing.T) {
				base := []string{
					"PATH=/usr/bin:/bin",
					"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=0",
				}
				if switchValue != "" {
					base = append(base, clientadapter.RecognizedProtectionsVariable+"="+switchValue)
				}
				if err := validateGrant(testCase.grant); err != nil {
					t.Fatalf("the fixture grant is invalid: %v", err)
				}
				state, recognized := clientadapter.ParseProtectionSwitch(switchValue)
				withheld := testCase.recognized &&
					testCase.want == clientadapter.StreamingFallbackCoreOwned &&
					state == clientadapter.ProtectionsOff
				policy := testCase.want
				if withheld {
					policy = clientadapter.StreamingFallbackClientDefault
				}
				environment, decision, err := buildChildEnvironment(base, testCase.grant)
				if err != nil {
					t.Fatal(err)
				}
				if decision.policy != policy || decision.switchedOff != withheld {
					t.Fatalf("decision = %+v, want policy %s and switchedOff %v", decision, policy, withheld)
				}
				if testCase.recognized {
					if decision.switchValue != switchValue || decision.switchUnrecognized != !recognized {
						t.Fatalf("the switch was read as %+v", decision)
					}
				} else if decision.switchValue != "" || decision.switchUnrecognized {
					t.Fatalf("a grant without a signer read the switch: %+v", decision)
				}
				want := "0"
				if policy == clientadapter.StreamingFallbackCoreOwned {
					want = "1"
				}
				if got := environmentMap(environment)[claudeDisableNonStreamingFallback]; got != want {
					t.Fatalf("%s = %q, want %q", claudeDisableNonStreamingFallback, got, want)
				}
			})
		}
	}
}

// A publisher-recognized Claude Code leaves stream recovery to ViberMate only
// when its own target is an authority whose every route is Upstream, where
// the Exchange holds the stream (ADR 0026). There the launcher drops every
// inherited casing of the variable and sets 1; everywhere else the user's own
// value reaches the client unchanged. The command says what it decided.
func TestRecognizedClaudeLeavesStreamFallbackToCoreOnlyOnUpstreamTargets(t *testing.T) {
	t.Parallel()

	anthropic := []string{"api.anthropic.com:443"}
	claude := func(managed []string) capturecontrol.LaunchGrant {
		return recognizedSignerGrant("claude-code", clientadapter.InstallNativeSingleBinary, clientadapter.LaunchNodeEnvProxy, managed)
	}
	inherited := []string{
		"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=0",
		"claude_code_disable_nonstreaming_fallback=false",
	}
	const (
		fallback     = "cli.protections.streamingFallback"
		switchedOff  = "cli.protections.switchedOff"
		unrecognized = "cli.protections.switchUnrecognized"
	)
	for _, test := range []struct {
		name   string
		grant  capturecontrol.LaunchGrant
		extra  []string
		set    bool
		notice []string
	}{
		{"recognized claude, managed target", claude(anthropic), nil, true, []string{fallback}},
		// A managed credential drops the base URL with the user's
		// credential, so the child reaches api.anthropic.com. The decision
		// follows that authority, not the one the base URL named.
		{"recognized claude, managed base URL the launcher drops", claude([]string{"gateway.example:443"}),
			[]string{"ANTHROPIC_BASE_URL=https://gateway.example"}, false, nil},
		{"recognized claude, managed base URL and a managed default", claude([]string{"api.anthropic.com:443", "gateway.example:443"}),
			[]string{"ANTHROPIC_BASE_URL=https://gateway.example"}, true, []string{fallback}},
		{"recognized claude, unmanaged target", claude([]string{}), nil, false, nil},
		{"recognized claude, base URL at an unmanaged host", claude(anthropic),
			[]string{"ANTHROPIC_BASE_URL=https://gateway.example"}, false, nil},
		{"recognized claude, switch off", claude(anthropic),
			[]string{clientadapter.RecognizedProtectionsVariable + "=off"}, false, []string{switchedOff}},
		{"recognized claude, switch 0", claude(anthropic),
			[]string{clientadapter.RecognizedProtectionsVariable + "=0"}, false, []string{switchedOff}},
		{"recognized claude, switch banana", claude(anthropic),
			[]string{clientadapter.RecognizedProtectionsVariable + "=banana"}, true, []string{unrecognized, fallback}},
		{"recognized claude, wrapper shape",
			recognizedSignerGrant("claude-code", clientadapter.InstallNPMWrapperNativeChild, clientadapter.LaunchNodeEnvProxy, anthropic),
			nil, false, nil},
		{"recognized codex",
			recognizedSignerGrant("codex-cli", clientadapter.InstallNPMWrapperNativeChild, clientadapter.LaunchCodexResponsesHTTP, []string{"api.openai.com:443"}),
			[]string{"OPENAI_BASE_URL=https://api.openai.com/v1"}, false, nil},
		{"verified client_default",
			func() capturecontrol.LaunchGrant {
				grant := verifiedAdapterGrant(clientadapter.LaunchNodeEnvProxy, anthropic)
				grant.Adapter.StreamingFallbackPolicy = clientadapter.StreamingFallbackClientDefault
				return grant
			}(), nil, false, nil},
		{"verified core_owned, unmanaged target",
			verifiedAdapterGrant(clientadapter.LaunchNodeEnvProxy, []string{}), nil, true, []string{fallback}},
	} {
		base := append(append([]string{"PATH=/usr/bin:/bin"}, inherited...), test.extra...)
		if err := validateGrant(test.grant); err != nil {
			t.Fatalf("%s: the fixture grant is invalid: %v", test.name, err)
		}
		environment, decision, err := buildChildEnvironment(base, test.grant)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		got := make(map[string]string)
		for key, value := range environmentMap(environment) {
			if strings.EqualFold(key, claudeDisableNonStreamingFallback) {
				got[key] = value
			}
		}
		want := environmentMap(inherited)
		if test.set {
			want = map[string]string{claudeDisableNonStreamingFallback: "1"}
		}
		// A recognized grant leaves recovery to Core only when the authority
		// the child itself reaches is managed.
		if test.set && test.grant.Adapter == nil {
			target := environmentMap(environment)["ANTHROPIC_BASE_URL"]
			if target == "" {
				target = "https://api.anthropic.com"
			}
			parsed, err := url.Parse(target)
			if err != nil {
				t.Fatalf("%s: child base URL %q: %v", test.name, target, err)
			}
			authority := net.JoinHostPort(parsed.Hostname(), "443")
			if parsed.Port() != "" {
				authority = parsed.Host
			}
			if !slices.Contains(test.grant.ManagedCredentialAuthorities, authority) {
				t.Fatalf("%s: the launcher set the variable although the child reaches %s, which is not managed (%v)",
					test.name, authority, test.grant.ManagedCredentialAuthorities)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("%s: fallback variables %v, want %v", test.name, got, want)
		}
		for key, value := range want {
			if got[key] != value {
				t.Fatalf("%s: fallback variables %v, want %v", test.name, got, want)
			}
		}
		_, value, _ := clientadapter.ProtectionSwitchFromEnvironment(base)
		var lines []string
		for _, key := range test.notice {
			lines = append(lines, renderedNotice(t, locales.EnglishUS, key, value))
		}
		if notice := noticeLines(t, base, decision); !slices.Equal(notice, lines) {
			t.Fatalf("%s: notice %q, want %q", test.name, notice, lines)
		}
	}
	// The unread value is reported as typed.
	base := []string{"PATH=/usr/bin:/bin", clientadapter.RecognizedProtectionsVariable + "=banana"}
	decision := decideStreamingFallback(base, claude(anthropic), true)
	if decision.policy != clientadapter.StreamingFallbackCoreOwned || !decision.switchUnrecognized ||
		decision.switchValue != "banana" || decision.switchedOff {
		t.Fatalf("switch banana decided %+v", decision)
	}
	// Switch 0 withholds the fallback, and the notice shows the value.
	base = []string{"PATH=/usr/bin:/bin", clientadapter.RecognizedProtectionsVariable + "=0"}
	decision = decideStreamingFallback(base, claude(anthropic), true)
	if notice := noticeLines(t, base, decision); decision.policy != clientadapter.StreamingFallbackClientDefault ||
		!decision.switchedOff || len(notice) != 1 ||
		!strings.Contains(notice[0], clientadapter.RecognizedProtectionsVariable+"=0)") {
		t.Fatalf("switch 0 decided %+v and noticed %q", decision, notice)
	}
}

// The base loop drops an inherited value, in any casing, exactly when the set
// branch writes the variable, because both read one decision.
func TestStreamingFallbackDecisionIsTheSameForSetAndDrop(t *testing.T) {
	t.Parallel()

	inherited := [][]string{
		nil,
		{"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=0"},
		{"claude_code_disable_nonstreaming_fallback=0"},
		{"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=", "Claude_Code_Disable_NonStreaming_Fallback=false"},
	}
	for _, testCase := range fallbackGrantCases() {
		for index, values := range inherited {
			base := append([]string{"PATH=/usr/bin:/bin"}, values...)
			environment, decision, err := buildChildEnvironment(base, testCase.grant)
			if err != nil {
				t.Fatalf("%s/%d: %v", testCase.name, index, err)
			}
			got := make(map[string]string)
			for key, value := range environmentMap(environment) {
				if strings.EqualFold(key, claudeDisableNonStreamingFallback) {
					got[key] = value
				}
			}
			want := make(map[string]string)
			if decision.policy == clientadapter.StreamingFallbackCoreOwned {
				want[claudeDisableNonStreamingFallback] = "1"
			} else {
				for key, value := range environmentMap(values) {
					want[key] = value
				}
			}
			if len(got) != len(want) {
				t.Fatalf("%s/%d: fallback variables %v, want %v", testCase.name, index, got, want)
			}
			for key, value := range want {
				if got[key] != value {
					t.Fatalf("%s/%d: fallback variables %v, want %v", testCase.name, index, got, want)
				}
			}
			for _, entry := range base {
				key, _, _ := strings.Cut(entry, "=")
				dropped := launchPolicyManagesEnvironment(key, decision.policy)
				if dropped != (decision.policy == clientadapter.StreamingFallbackCoreOwned &&
					strings.EqualFold(key, claudeDisableNonStreamingFallback)) {
					t.Fatalf("%s/%d: %s dropped = %v under %s", testCase.name, index, key, dropped, decision.policy)
				}
			}
		}
	}
}
