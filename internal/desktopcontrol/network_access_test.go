package desktopcontrol_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

func TestNetworkAccessCopiedValidatedAndAuthenticated(t *testing.T) {
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	metadata := &desktopcontrol.NetworkAccess{SOCKSAddress: "[::1]:1080", VLESSAvailable: true}
	options := desktopcontrol.Options{NetworkAccess: metadata, Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime, Clock: desktopcontrol.SystemClock{}}
	app, err := desktopcontrol.New(options)
	if err != nil {
		t.Fatal(err)
	}
	metadata.SOCKSAddress, metadata.VLESSAvailable = "127.0.0.1:1081", false
	auth, err := desktopcontrol.NewAuthenticator(desktopcontrol.CapabilityGrant{ReadToken: capability(0x23), WriteToken: capability(0x24), ExpiresAt: time.Now().Add(time.Minute)}, desktopcontrol.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	const authority = "127.0.0.1:43137"
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{Authority: authority, AllowedOrigins: []string{"vibermate://desktop"}, Authenticator: auth, Application: app, Bootstrap: emptyBootstrap(), CLIControl: http.NotFoundHandler(), ManualCaptures: rejectingManualCaptureHandler{}, DesktopPrincipal: desktopManualPrincipal(t)})
	if err != nil {
		t.Fatal(err)
	}
	response := doRequest(t, router, authority, http.MethodGet, "/api/v1/status", capability(0x23), nil)
	var body map[string]any
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
		t.Fatalf("status=%d", response.Code)
	}
	access, ok := body["networkAccess"].(map[string]any)
	if !ok || access["socksAddress"] != "[::1]:1080" || access["vlessAvailable"] != true {
		t.Fatalf("caller mutation changed access: %v", access)
	}
	unauth := doRequest(t, router, authority, http.MethodGet, "/api/v1/status", "", nil)
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", unauth.Code)
	}
	for _, value := range []string{"0.0.0.0:1080", "[::]:1080", "127.0.0.1:0", "http://127.0.0.1:1080", "user@host:1080", "host:1080/path", "HOST:1080", "[0:0:0:0:0:0:0:1]:1080"} {
		options.NetworkAccess = &desktopcontrol.NetworkAccess{SOCKSAddress: value}
		if _, err := desktopcontrol.New(options); err == nil {
			t.Fatalf("invalid/noncanonical address %q accepted", value)
		}
	}
}
