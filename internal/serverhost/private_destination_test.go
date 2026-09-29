package serverhost_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serverhost"
)

func TestServerProxyDeniesImplicitPrivateDestinations(t *testing.T) {
	var requests atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "private-service")
	}))
	defer origin.Close()
	options := serverOptions(t, t.TempDir())
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	host, err := serverhost.Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, host)
	grant := privateDestinationCapture(t, host)
	parsed, _ := url.Parse(origin.URL)
	for _, target := range []string{parsed.Host, net.JoinHostPort("localhost", parsed.Port())} {
		for _, method := range []string{http.MethodConnect, http.MethodGet} {
			t.Run(method+"/"+target, func(t *testing.T) {
				status := privateProxyRequest(t, host.Status().ListenAddress, grant.ProxyToken, method, target)
				want := http.StatusForbidden
				if method == http.MethodConnect && target == parsed.Host {
					// CONNECT already requires a DNS ClientOrigin, even for an
					// Owner. Cleartext IP targets and DNS targets remain in scope.
					want = http.StatusBadRequest
				}
				if status != want {
					t.Errorf("unconfigured private target = HTTP %d, want %d", status, want)
				}
			})
		}
	}
	if requests.Load() != 0 {
		t.Errorf("Server reached its private HTTP service %d times", requests.Load())
	}
	// An Owner's explicit IP+port authorization is not the default monitor
	// policy, and remains usable for deliberately configured private services.
	rules := host.Runtime().ConnectionRules()
	current := rules.Current()
	_, err = rules.Replace(context.Background(), current.Revision, []connectionpolicy.Rule{{
		ID: "review.owner-private-service", Priority: 100, Decision: connectionpolicy.DecisionAllow,
		Match: connectionpolicy.MatchExactHostPort(parsed.Hostname(), mustPort(t, parsed.Port())),
	}, {
		ID: "review.owner-private-name", Priority: 100, Decision: connectionpolicy.DecisionAllow,
		Match: connectionpolicy.MatchExactHostPort("localhost", mustPort(t, parsed.Port())),
	}}, current.Mode)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodConnect, http.MethodGet} {
		if status := privateProxyRequest(t, host.Status().ListenAddress, grant.ProxyToken, method, net.JoinHostPort("localhost", parsed.Port())); status != http.StatusOK {
			t.Errorf("Owner-authorized private target = HTTP %d, want 200", status)
		}
	}
	current = rules.Current()
	_, err = rules.Replace(context.Background(), current.Revision, []connectionpolicy.Rule{{
		ID: "review.deny-localhost", Priority: 100, Decision: connectionpolicy.DecisionDeny,
		Match: connectionpolicy.MatchExactHost("localhost"),
	}}, current.Mode)
	if err != nil {
		t.Fatal(err)
	}
	if status := privateProxyRequest(t, host.Status().ListenAddress, grant.ProxyToken, http.MethodConnect, net.JoinHostPort("localhost", parsed.Port())); status != http.StatusForbidden {
		t.Fatalf("system_transparent bypassed the explicit deny rule: HTTP %d", status)
	}
}

func privateProxyRequest(t *testing.T, proxy, token, method, authority string) int {
	t.Helper()
	connection, err := net.DialTimeout("tcp", proxy, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	target := authority
	if method == http.MethodGet {
		target = "http://" + authority + "/private"
	}
	credential := base64.StdEncoding.EncodeToString([]byte(captureadmission.ProxyUsername + ":" + token))
	_, err = fmt.Fprintf(connection, "%s %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\nConnection: close\r\n\r\n", method, target, authority, credential)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: method})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodConnect {
		_, _ = io.Copy(io.Discard, response.Body)
	}
	_ = response.Body.Close()
	return response.StatusCode
}

func privateDestinationCapture(t *testing.T, host *serverhost.Host) capturecontrol.LaunchGrant {
	t.Helper()
	ctx := context.Background()
	users := host.Runtime().RuntimeUsers()
	user, err := users.Create(ctx, runtimeuser.CreateCommand{Username: "review-member", Password: []byte("review-password")})
	if err != nil {
		t.Fatal(err)
	}
	grantAllEnvironments(t, users, user.ID)
	policy, err := runtimeuser.NewPolicy(false, []string{environment.SystemTransparentID.String()}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.SetPolicy(ctx, user.ID, policy); err != nil {
		t.Fatal(err)
	}
	base := "http://" + host.Status().ListenAddress
	client := &http.Client{Timeout: 5 * time.Second}
	login := postJSON(t, client, base+servercontrol.RuntimeUserSessionPath, "", servercontrol.RuntimeUserLogin{
		Schema: servercontrol.RuntimeUserLoginSchema, Username: user.Username, Password: "review-password",
		MachineID: "uRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6xQfEk", DeviceName: "Review device",
	})
	defer login.Body.Close()
	if login.StatusCode != http.StatusCreated {
		t.Fatalf("login: %d", login.StatusCode)
	}
	var session servercontrol.RuntimeUserSession
	if err := json.NewDecoder(login.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	created := postJSON(t, client, base+"/api/v1/capture-runs", session.SessionToken, capturecontrol.CreateRequest{
		EnvironmentID: environment.SystemTransparentID.String(), CWD: "/workspace/project",
		Command: []string{"custom-agent"}, ExecutablePath: "/opt/tools/custom-agent",
		Companion: &capturecontrol.CompanionAttestationInput{
			Detection: clientadapter.Detection{Status: clientadapter.StatusGeneric, Recognition: clientadapter.RecognitionUnknown,
				CatalogRevision: clientadapter.BuiltInCatalog().Revision(), CanonicalPath: "/opt/tools/custom-agent", ExecutableLabel: "custom-agent"},
			Workspace: capturecontrol.CompanionWorkspaceInput{MachineID: "uRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6xQfEk",
				WorkspaceID: "QfEkuRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6w", WorkspaceLabel: "project", RegistrationRevision: 1, DerivationRevision: 1},
		},
	})
	defer created.Body.Close()
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("capture: %d", created.StatusCode)
	}
	var grant capturecontrol.LaunchGrant
	if err := json.NewDecoder(created.Body).Decode(&grant); err != nil {
		t.Fatal(err)
	}
	return grant
}
