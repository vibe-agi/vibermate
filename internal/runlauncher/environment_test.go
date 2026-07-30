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
		LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
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
		LaunchRecipe:    clientadapter.LaunchGeneric,
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

func environmentMap(environment []string) map[string]string {
	values := make(map[string]string)
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	return values
}
