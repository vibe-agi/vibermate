package desktophost_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

const (
	childMarkerEnvironment  = "VIBERMATE_TEST_CHILD_FETCH"
	childManagedEnvironment = "VIBERMATE_TEST_CHILD_MANAGED"
	childManagedFailure     = "VIBERMATE_TEST_CHILD_MANAGED_FAILURE"
	childSuccessMarker      = "reached"
)

// TestMain lets this test binary act as its own captured child, so the test
// needs no external tool and still exercises a real process through the real
// launcher.
func TestMain(m *testing.M) {
	if os.Getenv(childManagedEnvironment) != "" {
		os.Exit(runCapturedManagedRequest())
	}
	if target := os.Getenv(childMarkerEnvironment); target != "" {
		os.Exit(runCapturedChildFetch(target))
	}
	os.Exit(m.Run())
}

// runCapturedManagedRequest is the fixed test client used by the production
// composition test. It deliberately presents ambient and per-request client
// credentials: the managed route must remove every one of them and apply the
// selected ProviderAccount only at the final provider boundary.
func runCapturedManagedRequest() int {
	expectFailure := os.Getenv(childManagedFailure) != ""
	if os.Getenv("ANTHROPIC_API_KEY") == "client-ambient-secret" ||
		os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
		fmt.Fprintln(os.Stderr, "child: ambient client credentials were retained")
		return 1
	}
	proxyURL := os.Getenv("HTTPS_PROXY")
	rootPath := os.Getenv("NODE_EXTRA_CA_CERTS")
	if proxyURL == "" || rootPath == "" {
		fmt.Fprintln(os.Stderr, "child: managed launch environment is incomplete")
		return 1
	}
	parsedProxy, err := url.Parse(proxyURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child proxy URL:", err)
		return 1
	}
	rootPEM, err := os.ReadFile(rootPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child root:", err)
		return 1
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootPEM) {
		fmt.Fprintln(os.Stderr, "child: managed Root is invalid")
		return 1
	}
	transport := &http.Transport{
		Proxy:             http.ProxyURL(parsedProxy),
		ForceAttemptHTTP2: false,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		},
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequest(
		http.MethodPost,
		"https://api.anthropic.com/v1/messages",
		strings.NewReader(`{"model":"claude-test","max_tokens":32,"messages":[{"role":"user","content":"managed route"}]}`),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child request:", err)
		return 1
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer client-request-secret")
	request.Header.Set("X-Api-Key", "client-request-key")
	request.Header.Set("Api-Key", "client-request-alias")
	request.Header.Set("Cookie", "session=client-cookie")
	response, err := (&http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	}).Do(request)
	if err != nil {
		if expectFailure {
			return 0
		}
		fmt.Fprintln(os.Stderr, "child managed request:", err)
		return 1
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(response.Body)
	if expectFailure {
		if readErr == nil && response.StatusCode >= http.StatusBadRequest {
			return 0
		}
		fmt.Fprintf(
			os.Stderr,
			"child managed request unexpectedly succeeded: status=%d body=%q err=%v\n",
			response.StatusCode,
			body,
			readErr,
		)
		return 1
	}
	if readErr != nil || response.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), "managed reached") {
		fmt.Fprintf(
			os.Stderr,
			"child managed response: status=%d body=%q err=%v\n",
			response.StatusCode,
			body,
			readErr,
		)
		return 1
	}
	return 0
}

// runCapturedChildFetch fetches through the proxy variables the launcher
// exported into this process.
//
// It builds the transport from those variables explicitly rather than relying
// on Go's automatic selection, because Go unconditionally skips a proxy for a
// loopback target and the test origin is on loopback. Using the automatic path
// here would prove nothing: the child would connect directly and the test would
// pass with the proxy uninvolved. What this still proves is what belongs to
// vibermate: the launcher exported a usable proxy address and credential, and
// the proxy forwards a cleartext request to a host that is not a model API.
func runCapturedChildFetch(target string) int {
	proxyURL := os.Getenv("HTTP_PROXY")
	if proxyURL == "" {
		fmt.Fprintln(os.Stderr, "child: the launcher exported no HTTP_PROXY")
		return 1
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child proxy URL:", err)
		return 1
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(parsed),
		},
		Timeout: 10 * time.Second,
	}
	response, err := client.Get(target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child fetch:", err)
		return 1
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || !strings.Contains(string(body), childSuccessMarker) {
		fmt.Fprintln(os.Stderr, "child body:", string(body), err)
		return 1
	}
	return 0
}

// Before blind tunnelling and cleartext forwarding, every non-model host an
// Agent touched was refused, because the launcher exports the proxy to the
// whole child process tree. This proves a captured child can now reach one.
func TestCapturedChildReachesANonModelHost(t *testing.T) {
	t.Parallel()

	listener, listenErr := net.Listen("tcp4", "127.0.0.1:0")
	if listenErr != nil {
		t.Fatal(listenErr)
	}
	origin := &http.Server{
		Handler: http.HandlerFunc(func(
			writer http.ResponseWriter,
			request *http.Request,
		) {
			if request.Header.Get("Proxy-Authorization") != "" {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(writer, childSuccessMarker)
		}),
	}
	go func() { _ = origin.Serve(listener) }()
	defer origin.Close()

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	host := startHost(t, hostOptions(t, paths, filepath.Join(root, "data")))
	defer shutdownHost(t, host)
	sessionFile, err := localdiscovery.NewFile(
		paths.DiscoveryPath(),
		productruntime.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}

	// A scoped rule still overrides Monitor mode, and its exact target is
	// recorded as the reason the connection was allowed.
	probeHost, probePort := splitProbeAuthority(t, listener.Addr().String())
	rules := host.Runtime().ConnectionRules()
	if _, err := rules.Replace(
		context.Background(),
		rules.Current().Revision,
		[]connectionpolicy.Rule{{
			ID:       "test.allow-probe-origin",
			Priority: 100,
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHostPort(probeHost, probePort),
		}},
		rules.Current().Mode,
	); err != nil {
		t.Fatal(err)
	}

	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: sessionFile,
		BaseEnvironment: []string{
			"PATH=/usr/bin:/bin",
			childMarkerEnvironment + "=http://" +
				listener.Addr().String() + "/probe",
		},
		Stdin:              strings.NewReader(""),
		Stdout:             io.Discard,
		Stderr:             os.Stderr,
		HeartbeatInterval:  10 * time.Millisecond,
		ControlTimeout:     2 * time.Second,
		TerminationTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithTimeout(
		context.Background(),
		20*time.Second,
	)
	defer cancelRun()
	exitCode, err := launcher.Run(runContext, transparentLaunch(os.Args[0]))
	if err != nil || exitCode != 0 {
		t.Fatalf(
			"a captured child could not reach a non-model host: exit=%d err=%v",
			exitCode,
			err,
		)
	}

	// A successful fetch alone proves nothing: Go's default proxy function
	// skips loopback, so the child could have connected directly. The proxy
	// must show the outbound it forwarded.
	page, egressErr := host.Runtime().EgressAttempts().List(
		context.Background(),
		egressaudit.PageRequest{Limit: 20},
	)
	if egressErr != nil {
		t.Fatal(egressErr)
	}
	forwarded := false
	for _, record := range page.Items {
		if record.Attempt.Purpose() == egressaudit.PurposeBlindTunnel {
			forwarded = true
		}
	}
	if !forwarded {
		t.Fatalf(
			"the child reached the host without passing through the proxy: %+v",
			page.Items,
		)
	}
}

// splitProbeAuthority names the probe origin in the terms a rule matches on.
func splitProbeAuthority(t *testing.T, authority string) (string, uint16) {
	t.Helper()

	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		t.Fatal(err)
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return host, uint16(number)
}
