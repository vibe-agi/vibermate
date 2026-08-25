package serverhost_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serverhost"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

const remoteChildTarget = "VIBERMATE_REMOTE_TEST_CHILD_TARGET"

func TestMain(m *testing.M) {
	if target := os.Getenv(remoteChildTarget); target != "" {
		os.Exit(runRemoteChildFetch(target))
	}
	os.Exit(m.Run())
}

func runRemoteChildFetch(target string) int {
	proxy, err := url.Parse(os.Getenv("HTTP_PROXY"))
	if err != nil || proxy.Host == "" {
		fmt.Fprintln(os.Stderr, "remote child proxy:", err)
		return 1
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxy)},
		Timeout:   10 * time.Second,
	}
	response, err := client.Get(target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote child fetch:", err)
		return 1
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(payload) != "remote-relay-reached" {
		fmt.Fprintf(os.Stderr, "remote child response: status=%d body=%q err=%v\n", response.StatusCode, payload, err)
		return 1
	}
	return 0
}

func TestServerHostCanServeTheSameRuntimeAndManagementUIOverExplicitHTTP(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	options := serverOptions(t, root)
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	host, err := serverhost.Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, host)
	status := host.Status()
	if status.Scheme != "http" || status.TLSFingerprint != "" {
		t.Fatalf("HTTP status = %+v", status)
	}
	response, err := http.Get("http://" + status.ListenAddress + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("HTTP management status=%d body=%s", response.StatusCode, payload)
	}
}

func TestServerHostAuthenticatesServerCreatedRuntimeUserOverExplicitHTTP(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	options := serverOptions(t, root)
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	host, err := serverhost.Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, host)
	created, err := host.Runtime().RuntimeUsers().Create(
		context.Background(),
		runtimeuser.CreateCommand{
			Username: "alice", Password: []byte("test-server-password"),
		},
	)
	if err != nil {
		t.Fatalf("create Runtime User: %v", err)
	}
	response := postJSON(
		t,
		&http.Client{Timeout: 10 * time.Second},
		"http://"+host.Status().ListenAddress+servercontrol.RuntimeUserSessionPath,
		"",
		servercontrol.RuntimeUserLogin{
			Schema: servercontrol.RuntimeUserLoginSchema, Username: "alice",
			Password:   "test-server-password",
			MachineID:  "uRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6xQfEk",
			DeviceName: "Linux workstation",
		},
	)
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("login status=%d body=%s", response.StatusCode, payload)
	}
	var session servercontrol.RuntimeUserSession
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session.User.ID != string(created.ID) || session.User.Username != "alice" ||
		session.SessionToken == "" {
		t.Fatalf("login session = %#v", session)
	}
	catalog := clientadapter.BuiltInCatalog()
	createResponse := postJSON(
		t,
		&http.Client{Timeout: 10 * time.Second},
		"http://"+host.Status().ListenAddress+"/api/v1/capture-runs",
		session.SessionToken,
		capturecontrol.CreateRequest{
			EnvironmentID:  environment.SystemTransparentID.String(),
			CWD:            "/workspace/project",
			Command:        []string{"custom-agent"},
			ExecutablePath: "/opt/tools/custom-agent",
			Companion: &capturecontrol.CompanionAttestationInput{
				Detection: clientadapter.Detection{
					Status: clientadapter.StatusGeneric, Recognition: clientadapter.RecognitionUnknown,
					CatalogRevision: catalog.Revision(), CanonicalPath: "/opt/tools/custom-agent",
					ExecutableLabel: "custom-agent",
				},
				Workspace: capturecontrol.CompanionWorkspaceInput{
					MachineID:      "uRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6xQfEk",
					WorkspaceID:    "QfEkuRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6w",
					WorkspaceLabel: "project", RegistrationRevision: 1, DerivationRevision: 1,
				},
			},
		},
	)
	defer createResponse.Body.Close()
	if createResponse.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(createResponse.Body)
		t.Fatalf("create with Login Session status=%d body=%s", createResponse.StatusCode, payload)
	}
	var grant capturecontrol.LaunchGrant
	if err := json.NewDecoder(createResponse.Body).Decode(&grant); err != nil {
		t.Fatalf("decode Capture grant: %v", err)
	}
	view, err := host.Runtime().CaptureRunReader().GetRun(
		context.Background(),
		grant.Run.ID,
	)
	if err != nil {
		t.Fatalf("read attributed Capture Run: %v", err)
	}
	if view.RuntimeUserID != created.ID ||
		view.LoginSessionID != runtimeuser.LoginSessionID(session.SessionID) ||
		view.DeviceName != "Linux workstation" {
		// The raw token must never be stored; attribution uses its opaque
		// Login Session ID and the authenticated device label frozen at login.
		t.Fatalf("Capture Run attribution = %#v", view)
	}
	adminKey, err := os.ReadFile(host.Status().AdminAccessKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	adminLogin := postJSON(
		t,
		&http.Client{Timeout: 10 * time.Second},
		"http://"+host.Status().ListenAddress+servercontrol.AdminSessionPath,
		"",
		servercontrol.AdminLogin{
			Schema: servercontrol.AdminLoginSchema, AccessKey: strings.TrimSpace(string(adminKey)),
		},
	)
	defer adminLogin.Body.Close()
	if adminLogin.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(adminLogin.Body)
		t.Fatalf("admin login status=%d body=%s", adminLogin.StatusCode, payload)
	}
	var admin servercontrol.AdminSession
	if err := json.NewDecoder(adminLogin.Body).Decode(&admin); err != nil {
		t.Fatal(err)
	}
	usageRequest, err := http.NewRequest(
		http.MethodGet,
		"http://"+host.Status().ListenAddress+servercontrol.RuntimeUserUsagePath+
			"?from=2026-01-01&until=2027-01-01&timeZone=UTC",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	usageRequest.Header.Set("Authorization", "Bearer "+admin.ReadToken)
	usageResponse, err := (&http.Client{Timeout: 10 * time.Second}).Do(usageRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer usageResponse.Body.Close()
	var usage runtimeusage.Report
	if usageResponse.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(usageResponse.Body)
		t.Fatalf("usage status=%d body=%s", usageResponse.StatusCode, payload)
	}
	if err := json.NewDecoder(usageResponse.Body).Decode(&usage); err != nil {
		t.Fatal(err)
	}
	if len(usage.Users) != 1 || usage.Users[0].CaptureRuns != 1 ||
		usage.Users[0].LatestContext == nil ||
		usage.Users[0].LatestContext.DeviceName != "Linux workstation" ||
		usage.Users[0].LatestContext.WorkspaceLabel != "project" {
		t.Fatalf("remote Capture usage attribution = %#v", usage)
	}
}

func TestAttachedServerHostDoesNotOwnTheSharedProductRuntime(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	owned := serverOptions(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	runtime, err := productruntime.Start(ctx, owned.Runtime)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	attach := serverhost.DefaultAttachOptions(
		runtime,
		owned.Runtime.Paths.DataDirectory(),
		owned.Runtime.Clock,
		rand.Reader,
	)
	attach.ListenAddress = "127.0.0.1:0"
	attach.CaptureRunLifetime = 5 * time.Second
	host, err := serverhost.Attach(context.Background(), attach)
	if err != nil {
		t.Fatal(err)
	}
	status := host.Status()
	if !status.Ready || status.InstanceID != runtime.Status().InstanceID {
		t.Fatalf("attached status = %+v", status)
	}

	shutdownServer(t, host)
	if state := runtime.Status().State; state != productruntime.RuntimeStateInitialized {
		t.Fatalf("attached Host shut down shared ProductRuntime: %s", state)
	}
	shutdownContext, shutdownCancel := context.WithTimeout(
		context.Background(),
		20*time.Second,
	)
	defer shutdownCancel()
	if err := runtime.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
}

func TestServerHostExclusivelyOwnsItsDataDirectory(t *testing.T) {
	root := t.TempDir()
	first := startServer(t, root)
	defer shutdownServer(t, first)

	_, err := serverhost.Start(context.Background(), serverOptions(t, root))
	if !errors.Is(err, instanceguard.ErrAlreadyOwned) {
		t.Fatalf("second Server Host error = %v", err)
	}
}

func TestServerAdminCreatesRuntimeUserWithoutExposingPasswordMaterial(t *testing.T) {
	root := t.TempDir()
	host := startServer(t, root)
	defer shutdownServer(t, host)
	status := host.Status()
	keyPayload, err := os.ReadFile(status.AdminAccessKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	client := tlsHTTPClient(t)
	login := postJSON(
		t,
		client,
		"https://"+status.ListenAddress+servercontrol.AdminSessionPath,
		"",
		servercontrol.AdminLogin{
			Schema:    servercontrol.AdminLoginSchema,
			AccessKey: strings.TrimSpace(string(keyPayload)),
		},
	)
	defer login.Body.Close()
	if login.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(login.Body)
		t.Fatalf("admin login status=%d body=%s", login.StatusCode, payload)
	}
	var admin servercontrol.AdminSession
	if err := json.NewDecoder(login.Body).Decode(&admin); err != nil {
		t.Fatal(err)
	}
	create := postJSON(
		t,
		client,
		"https://"+status.ListenAddress+servercontrol.RuntimeUsersPath,
		admin.WriteToken,
		servercontrol.RuntimeUserCreate{
			Schema:   servercontrol.RuntimeUserCreateSchema,
			Username: "alice", Password: "test-admin-created-password",
		},
	)
	defer create.Body.Close()
	payload, err := io.ReadAll(create.Body)
	if err != nil {
		t.Fatal(err)
	}
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("Runtime User create status=%d body=%s", create.StatusCode, payload)
	}
	if bytes.Contains(payload, []byte("test-admin-created-password")) ||
		bytes.Contains(payload, []byte("argon2")) || bytes.Contains(payload, []byte("password")) {
		t.Fatalf("Runtime User response exposed password material: %s", payload)
	}
	var created servercontrol.RuntimeUserAdminView
	if err := json.Unmarshal(payload, &created); err != nil {
		t.Fatal(err)
	}
	if created.Username != "alice" || created.State != string(runtimeuser.StateActive) {
		t.Fatalf("Runtime User create = %#v", created)
	}
	listRequest, err := http.NewRequest(
		http.MethodGet,
		"https://"+status.ListenAddress+servercontrol.RuntimeUsersPath,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	listRequest.Header.Set("Authorization", "Bearer "+admin.ReadToken)
	list, err := client.Do(listRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer list.Body.Close()
	if list.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(list.Body)
		t.Fatalf("Runtime User list status=%d body=%s", list.StatusCode, body)
	}
	usageRequest, err := http.NewRequest(
		http.MethodGet,
		"https://"+status.ListenAddress+servercontrol.RuntimeUserUsagePath+
			"?from=2026-01-01&until=2027-01-01&timeZone=UTC",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	usageRequest.Header.Set("Authorization", "Bearer "+admin.ReadToken)
	usageResponse, err := client.Do(usageRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer usageResponse.Body.Close()
	if usageResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(usageResponse.Body)
		t.Fatalf("Runtime User usage status=%d body=%s", usageResponse.StatusCode, body)
	}
	var usage runtimeusage.Report
	if err := json.NewDecoder(usageResponse.Body).Decode(&usage); err != nil {
		t.Fatal(err)
	}
	if usage.Schema != runtimeusage.ReportSchema || len(usage.Users) != 1 ||
		usage.Users[0].UserID != runtimeuser.UserID(created.ID) || usage.Users[0].Turns != 0 {
		t.Fatalf("Runtime User usage = %#v", usage)
	}
	disable := sendJSON(
		t,
		client,
		http.MethodPatch,
		"https://"+status.ListenAddress+servercontrol.RuntimeUsersPath+"/"+url.PathEscape(created.ID),
		admin.WriteToken,
		servercontrol.RuntimeUserUpdate{
			Schema: servercontrol.RuntimeUserUpdateSchema,
			State:  string(runtimeuser.StateDisabled),
		},
	)
	defer disable.Body.Close()
	if disable.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(disable.Body)
		t.Fatalf("Runtime User disable status=%d body=%s", disable.StatusCode, body)
	}
	var disabled servercontrol.RuntimeUserAdminView
	if err := json.NewDecoder(disable.Body).Decode(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled.ID != created.ID || disabled.State != string(runtimeuser.StateDisabled) {
		t.Fatalf("Runtime User disable = %#v", disabled)
	}
	denied := postJSON(
		t,
		client,
		"https://"+status.ListenAddress+servercontrol.RuntimeUserSessionPath,
		"",
		servercontrol.RuntimeUserLogin{
			Schema: servercontrol.RuntimeUserLoginSchema, Username: "alice",
			Password:   "test-admin-created-password",
			MachineID:  "uRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6xQfEk",
			DeviceName: "disabled-user-test",
		},
	)
	defer denied.Body.Close()
	if denied.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(denied.Body)
		t.Fatalf("disabled Runtime User login status=%d body=%s", denied.StatusCode, body)
	}
}

func TestServerAdminReadsRuntimeUserAccessContractOverTLS(t *testing.T) {
	root := t.TempDir()
	host := startServer(t, root)
	status := host.Status()
	keyPayload, err := os.ReadFile(status.AdminAccessKeyPath)
	if err != nil {
		shutdownServer(t, host)
		t.Fatal(err)
	}
	accessKey := strings.TrimSpace(string(keyPayload))
	client := tlsHTTPClient(t)
	login := postJSON(
		t, client, "https://"+status.ListenAddress+servercontrol.AdminSessionPath, "",
		servercontrol.AdminLogin{Schema: servercontrol.AdminLoginSchema, AccessKey: accessKey},
	)
	if login.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(login.Body)
		login.Body.Close()
		shutdownServer(t, host)
		t.Fatalf("admin login status=%d body=%s", login.StatusCode, payload)
	}
	var session servercontrol.AdminSession
	if err := json.NewDecoder(login.Body).Decode(&session); err != nil {
		login.Body.Close()
		shutdownServer(t, host)
		t.Fatal(err)
	}
	login.Body.Close()
	request, err := http.NewRequest(
		http.MethodGet,
		"https://"+status.ListenAddress+servercontrol.ServerAccessPath,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+session.ReadToken)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("Server access status=%d body=%s", response.StatusCode, payload)
	}
	var access servercontrol.ServerAccess
	if err := json.NewDecoder(response.Body).Decode(&access); err != nil {
		t.Fatal(err)
	}
	if access.Transport != "https" ||
		access.Authentication != servercontrol.RuntimeUserPasswordAuthentication ||
		access.SessionPolicy != servercontrol.ReusableLoginSessionPolicy ||
		len(access.Targets) != 1 || access.Targets[0] != status.ListenAddress {
		t.Fatalf("Server access = %#v", access)
	}
}

func TestServerServesManagementUIWithoutExposingManagementAPI(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(webRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(webRoot, "index.html"), []byte("<!doctype html><title>ViberMate Web</title>"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	options := serverOptions(t, root)
	options.ManagementUIRoot = webRoot
	host, err := serverhost.Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, host)
	if !host.Status().ManagementUI {
		t.Fatalf("management UI was not published: %+v", host.Status())
	}
	client := tlsHTTPClient(t)
	response, err := client.Get("https://" + host.Status().ListenAddress + "/")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(payload, []byte("ViberMate Web")) ||
		response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("UI status=%d headers=%v body=%s", response.StatusCode, response.Header, payload)
	}
	api, err := client.Get("https://" + host.Status().ListenAddress + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer api.Body.Close()
	if api.StatusCode != http.StatusUnauthorized {
		payload, _ := io.ReadAll(api.Body)
		t.Fatalf("unauthenticated API status=%d body=%s", api.StatusCode, payload)
	}
}

func TestRemoteLauncherRunsChildWithoutLocalDesktopDaemon(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	host := startServer(t, root)
	defer shutdownServer(t, host)
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDirectory := filepath.Join(root, "client-state")
	remoteConfig := runlauncher.RemoteConfig{
		Target:         mustServerTarget(t, "https://"+host.Status().ListenAddress),
		StateDirectory: stateDirectory, DisplayName: "integration-client",
		Clock: productruntime.SystemClock{}, Random: rand.Reader,
	}
	login := loginRemoteTestUser(t, host, remoteConfig)
	if !login.FirstUse || login.TLSFingerprint == "" {
		t.Fatalf("first encrypted login trust = %#v", login)
	}
	var firstLog strings.Builder
	launcher, err := runlauncher.New(runlauncher.Config{
		Remote:          &remoteConfig,
		BaseEnvironment: []string{"PATH=/usr/bin:/bin"},
		Stdin:           strings.NewReader(""), Stdout: io.Discard, Stderr: &firstLog,
		Getwd:          func() (string, error) { return workspace, nil },
		ControlTimeout: 2 * time.Second, CreateTimeout: 10 * time.Second,
		HeartbeatInterval: 50 * time.Millisecond, TerminationTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	exitCode, err := launcher.Run(ctx, runlauncher.LaunchRequest{
		EnvironmentID: environment.SystemTransparentID,
		Command:       []string{"/bin/sh", "-c", "exit 0"},
	})
	cancel()
	if err != nil || exitCode != 0 {
		t.Fatalf("remote child exit=%d error=%v log=%s", exitCode, err, firstLog.String())
	}
}

func TestRemoteLauncherRunsOverExplicitUnencryptedHTTP(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	options := serverOptions(t, root)
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	host, err := serverhost.Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, host)
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	remoteConfig := runlauncher.RemoteConfig{
		Target:         mustServerTarget(t, host.Status().ListenAddress),
		StateDirectory: filepath.Join(root, "client-state"),
		DisplayName:    "http-client", Clock: productruntime.SystemClock{}, Random: rand.Reader,
	}
	loginRemoteTestUser(t, host, remoteConfig)
	var log strings.Builder
	launcher, err := runlauncher.New(runlauncher.Config{
		Remote:             &remoteConfig,
		BaseEnvironment:    []string{"PATH=/usr/bin:/bin"},
		Stdin:              strings.NewReader(""),
		Stdout:             io.Discard,
		Stderr:             &log,
		Getwd:              func() (string, error) { return workspace, nil },
		ControlTimeout:     2 * time.Second,
		CreateTimeout:      10 * time.Second,
		HeartbeatInterval:  50 * time.Millisecond,
		TerminationTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	exitCode, err := launcher.Run(ctx, runlauncher.LaunchRequest{
		EnvironmentID: environment.SystemTransparentID,
		Command:       []string{"/bin/sh", "-c", "exit 0"},
	})
	cancel()
	if err != nil || exitCode != 0 {
		t.Fatalf("HTTP remote child exit=%d error=%v log=%s", exitCode, err, log.String())
	}
	if !strings.Contains(log.String(), "uses unencrypted HTTP") {
		t.Fatalf("HTTP transport warning missing: %s", log.String())
	}
}

func TestRemoteLauncherRelaysChildHTTPThroughServerDataPlane(t *testing.T) {
	t.Parallel()

	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Proxy-Authorization") != "" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(writer, "remote-relay-reached")
	}))
	defer origin.Close()
	root := t.TempDir()
	host := startServer(t, root)
	defer shutdownServer(t, host)
	parsedOrigin, _ := url.Parse(origin.URL)
	rules := host.Runtime().ConnectionRules()
	current := rules.Current()
	if _, err := rules.Replace(
		context.Background(),
		current.Revision,
		[]connectionpolicy.Rule{{
			ID: "test.remote-relay-origin", Priority: 100,
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHostPort(parsedOrigin.Hostname(), mustPort(t, parsedOrigin.Port())),
		}},
		current.Mode,
	); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	remoteConfig := runlauncher.RemoteConfig{
		Target:         mustServerTarget(t, "https://"+host.Status().ListenAddress),
		StateDirectory: filepath.Join(root, "client-state"),
		DisplayName:    "relay-client", Clock: productruntime.SystemClock{}, Random: rand.Reader,
	}
	loginRemoteTestUser(t, host, remoteConfig)
	launcher, err := runlauncher.New(runlauncher.Config{
		Remote: &remoteConfig,
		BaseEnvironment: []string{
			"PATH=/usr/bin:/bin",
			remoteChildTarget + "=" + origin.URL,
		},
		Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
		Getwd:          func() (string, error) { return workspace, nil },
		ControlTimeout: 2 * time.Second, CreateTimeout: 10 * time.Second,
		HeartbeatInterval: 50 * time.Millisecond, TerminationTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	exitCode, err := launcher.Run(ctx, runlauncher.LaunchRequest{
		EnvironmentID: environment.SystemTransparentID,
		Command:       []string{os.Args[0]},
	})
	cancel()
	if err != nil || exitCode != 0 {
		t.Fatalf("relayed child exit=%d error=%v", exitCode, err)
	}
}

func loginRemoteTestUser(
	t *testing.T,
	host *serverhost.Host,
	config runlauncher.RemoteConfig,
) runlauncher.RemoteLoginResult {
	t.Helper()
	_, err := host.Runtime().RuntimeUsers().Create(
		context.Background(),
		runtimeuser.CreateCommand{
			Username: "integration-user", Password: []byte("test-integration-password"),
		},
	)
	if err != nil {
		t.Fatalf("create integration Runtime User: %v", err)
	}
	result, err := runlauncher.LoginRemote(context.Background(), runlauncher.RemoteLoginRequest{
		Config: config, Username: "integration-user",
		Password: []byte("test-integration-password"),
	})
	if err != nil {
		t.Fatalf("login integration Runtime User: %v", err)
	}
	return result
}

func startServer(t *testing.T, root string) *serverhost.Host {
	t.Helper()
	options := serverOptions(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	host, err := serverhost.Start(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func serverOptions(t *testing.T, root string) serverhost.Options {
	t.Helper()
	paths, err := productruntime.NewRuntimePaths(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	secretsFactory, err := hostsecret.NewDevelopmentFileFactory(filepath.Join(root, "secrets", "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := secretsFactory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := serverhost.DefaultOptions(productruntime.Options{
		Paths: paths, Host: hostcontract.Server(), OfflineHold: gate, Secrets: secrets,
		Approvals: toolapproval.DefaultConfig(), ExchangeHold: exchange.DefaultHoldPolicy(),
		Clock: productruntime.SystemClock{}, InstanceIDs: productruntime.NewCryptographicInstanceIDSource(),
		SecurityRandom: rand.Reader, Lifecycle: productruntime.DefaultLifecycleOptions(),
	})
	options.ListenAddress = "127.0.0.1:0"
	options.CaptureRunLifetime = 5 * time.Second
	return options
}

func shutdownServer(t *testing.T, host *serverhost.Host) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := host.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func mustServerAddress(t *testing.T, value string) serverconnection.Address {
	t.Helper()
	address, err := serverconnection.ParseAddress(value)
	if err != nil {
		t.Fatal(err)
	}
	return address
}

func mustServerTarget(t *testing.T, value string) serverconnection.Target {
	t.Helper()
	target, err := serverconnection.ParseTarget(value)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func mustPort(t *testing.T, value string) uint16 {
	t.Helper()
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		t.Fatalf("port %q is invalid", value)
	}
	return uint16(port)
}

func tlsHTTPClient(t *testing.T) *http.Client {
	t.Helper()
	transport := &http.Transport{
		Proxy:             nil,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true}, // test-only
		ForceAttemptHTTP2: false,
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

func postJSON(t *testing.T, client *http.Client, target, bearer string, input any) *http.Response {
	return sendJSON(t, client, http.MethodPost, target, bearer, input)
}

func sendJSON(t *testing.T, client *http.Client, method, target, bearer string, input any) *http.Response {
	t.Helper()
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, target, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
