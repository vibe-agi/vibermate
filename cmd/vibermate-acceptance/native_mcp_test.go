package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
)

func TestNativeClientOutputDrainsStartupWhileApprovalWaits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	frames, finished := nativeClientFrames(ctx, reader)
	written := make(chan struct{})
	go func() {
		defer writer.Close()
		defer close(written)
		for i := 0; i < 100; i++ {
			_, _ = io.WriteString(writer, "{\"method\":\"mcpStartupUpdate\",\"params\":{}}\n")
		}
		_, _ = io.WriteString(writer, "{\"id\":3,\"result\":{}}\n")
	}()
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("startup output blocked while approval consumer was busy")
	}
	frame := <-frames
	if string(frame["id"]) != "3" {
		t.Fatal("correlated response lost")
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestNativeClientOutputOverflowAndCancellationAreBounded(t *testing.T) {
	t.Run("overflow", func(t *testing.T) {
		frames, finished := nativeClientFrames(context.Background(), strings.NewReader(strings.Repeat("{\"id\":3,\"result\":{}}\n", 9)))
		select {
		case err := <-finished:
			if err == nil {
				t.Fatal("response overflow silently dropped evidence")
			}
		case <-time.After(time.Second):
			t.Fatal("overflow reader did not terminate")
		}
		count := 0
		for range frames {
			count++
		}
		if count != 8 {
			t.Fatalf("queue retained %d frames", count)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		reader, writer := io.Pipe()
		defer writer.Close()
		_, finished := nativeClientFrames(ctx, reader)
		cancel()
		_ = reader.Close()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("closed/canceled reader did not join")
		}
	})
}

func TestNativeRunnerPreservesCompleteCaseSetOnPrerequisiteFailure(t *testing.T) {
	report := filepath.Join(t.TempDir(), "report.json")
	err := executeNativeMCP(context.Background(), config{nativeMCPOnly: true, reportPath: report})
	if err == nil {
		t.Fatal("missing exact clients unexpectedly passed")
	}
	encoded, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("runner lost incomplete evidence: %v", err)
	}
	var value struct {
		Cases []struct{ Name, Client, Outcome, Reason string } `json:"cases"`
	}
	if err = json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Cases) != 46 {
		t.Fatalf("report case count=%d want46", len(value.Cases))
	}
	seen := map[string]bool{}
	for _, row := range value.Cases {
		key := row.Name + "/" + row.Client
		if seen[key] || row.Outcome != "incomplete" || row.Reason != "prerequisite_failed" {
			t.Fatalf("invalid incomplete case %s", key)
		}
		seen[key] = true
	}
}

func TestNativeInvalidConfigRetainsReportDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	c, err := parseConfig([]string{"--native-mcp-only", "--report", path})
	if err == nil || !c.nativeMCPOnly || c.reportPath != path {
		t.Fatal("native prerequisite failure lost sanitized report destination")
	}
}

func TestNativeRunnerRejectsUnverifiedExecutablesBeforeExecution(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "executed")
	path := filepath.Join(directory, "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c := config{nativeMCPOnly: true, codex145Path: path, codex159Path: path, codex160Path: path, claudePath: path, reportPath: filepath.Join(directory, "report.json")}
	if err := executeNativeMCP(context.Background(), c); err == nil {
		t.Fatal("unverified clients accepted")
	}
	encoded, err := os.ReadFile(c.reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report nativeMCPReport
	if err = json.Unmarshal(encoded, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Cases) != 46 || report.Cases[0].Reason != "client_identity_mismatch" {
		t.Fatal("runner did not verify exact client artifacts")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unverified executable was run")
	}
}

func TestNativeClientVersionProbeRejectsWrongPatch(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "client")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'codex-cli 0.159.0\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	client := nativePinnedClient{nativeMCPClientReport: nativeMCPClientReport{Version: "0.159.2"}, invocation: path}
	if err := verifyNativeClientVersions(context.Background(), []nativePinnedClient{client}, "/usr/bin/node", directory); err == nil {
		t.Fatal("wrong exact patch version accepted")
	}
}

func TestNativeReportRefusesPassedCaseWithoutSourceProvenance(t *testing.T) {
	report := newNativeMCPReport()
	report.Cases[12].Outcome = "passed"
	report.Cases[12].NativeOutbound = 5
	path := filepath.Join(t.TempDir(), "report.json")
	if err := writeNativeMCPReport(path, report); err == nil {
		t.Fatal("passed evidence written without its source SHA")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("unbound successful scenario evidence was lost")
	}
	var observed nativeMCPReport
	if err = json.Unmarshal(encoded, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.Cases[12].Outcome != "incomplete" || observed.Cases[12].NativeOutbound != 5 || observed.Cases[12].Reason != "source_provenance_unavailable" {
		t.Fatal("unbound observations were not preserved truthfully")
	}
}

func TestNativeMCPActualRemoteLauncherRuntimeSlice(t *testing.T) {
	binary := os.Getenv("VIBERMATE_NATIVE_CODEX")
	if binary == "" {
		t.Skip("requires exact Codex executable")
	}
	runNativeFixtureSlice(t, binary)
}

func TestNativeObserveCaseDriverReportsOnlyObservedOwnership(t *testing.T) {
	binary, node := os.Getenv("VIBERMATE_NATIVE_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE")
	if binary == "" || node == "" {
		t.Skip("requires exact client and pinned Node")
	}
	result, err := runNativeObserveCase(context.Background(), t.TempDir(), nativePinnedClient{nativeMCPClientReport: nativeMCPClientReport{Version: "0.160.0"}, invocation: binary}, node)
	if err != nil || result.Outcome != "passed" || result.NativeOutbound < 4 || result.ModelOutbound != 0 || !result.CaptureMatched || !result.EnvironmentMatched || result.ActivityMatched || result.Reason != "completed_native_only" {
		t.Fatalf("native-only Observe evidence=%+v err=%v", result, err)
	}
}

func TestNativeObserveCaseDriverRejectsClientLabelSubstitution(t *testing.T) {
	binary, node := os.Getenv("VIBERMATE_NATIVE_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE")
	if binary == "" || node == "" {
		t.Skip("requires exact client and pinned Node")
	}
	result, err := runNativeObserveCase(context.Background(), t.TempDir(), nativePinnedClient{nativeMCPClientReport: nativeMCPClientReport{Version: "0.159.2"}, invocation: binary}, node)
	if err == nil || result.Outcome == "passed" {
		t.Fatal("actual160 client passed under substituted159 label")
	}
}

func TestNativePolicyCasesOffAndStrict(t *testing.T) {
	binary, node := os.Getenv("VIBERMATE_NATIVE_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE")
	if binary == "" || node == "" {
		t.Skip("requires exact160 client and pinned Node")
	}
	for _, scenario := range []struct{ name, reason string }{{"off", "permission_off_zero_outbound"}, {"strict_external", "strict_external_zero_call"}} {
		t.Run(scenario.name, func(t *testing.T) {
			result, err := runNativePolicyCase(context.Background(), t.TempDir(), nativePinnedClient{nativeMCPClientReport: nativeMCPClientReport{Version: "0.160.0"}, invocation: binary}, node, scenario.name)
			if err != nil || result.Outcome != "passed" || result.Reason != scenario.reason || !result.CaptureMatched || !result.EnvironmentMatched || result.ActivityMatched || result.ModelOutbound != 0 {
				t.Fatalf("policy case=%+v err=%v", result, err)
			}
			if scenario.name == "off" && result.NativeOutbound != 0 {
				t.Fatal("OFF sent native bytes")
			}
		})
	}
}

func TestNativeReviewRejectCaseUsesAuthenticatedDecision(t *testing.T) {
	binary, node := os.Getenv("VIBERMATE_NATIVE_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE")
	if binary == "" || node == "" {
		t.Skip("requires exact160 client and pinned Node")
	}
	result, err := runNativePolicyCase(context.Background(), t.TempDir(), nativePinnedClient{nativeMCPClientReport: nativeMCPClientReport{Version: "0.160.0"}, invocation: binary}, node, "review_reject")
	if err != nil || result.Outcome != "passed" || result.Reason != "review_rejected_zero_call" || result.NativeOutbound < 3 || result.ModelOutbound != 0 || !result.CaptureMatched || !result.EnvironmentMatched || result.ActivityMatched {
		t.Fatalf("Review rejection=%+v err=%v", result, err)
	}
}

func TestNativeSession404OnceCaseDoesNotReplay(t *testing.T) {
	binary, node := os.Getenv("VIBERMATE_NATIVE_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE")
	if binary == "" || node == "" {
		t.Skip("requires exact160 client and pinned Node")
	}
	result, err := runNativePolicyCase(context.Background(), t.TempDir(), nativePinnedClient{nativeMCPClientReport: nativeMCPClientReport{Version: "0.160.0"}, invocation: binary}, node, "session404_once")
	if err != nil || result.Outcome != "passed" || result.Reason != "execution_uncertain_not_replayed" || result.ModelOutbound != 0 || !result.CaptureMatched || !result.EnvironmentMatched || result.ActivityMatched {
		t.Fatalf("404 once=%+v err=%v", result, err)
	}
}

func TestNativeSSEReconnectDoesNotRepeatToolCall(t *testing.T) {
	binary, node := os.Getenv("VIBERMATE_NATIVE_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE")
	if binary == "" || node == "" {
		t.Skip("requires exact160 client and pinned Node")
	}
	result, err := runNativePolicyCase(context.Background(), t.TempDir(), nativePinnedClient{nativeMCPClientReport: nativeMCPClientReport{Version: "0.160.0"}, invocation: binary}, node, "sse_reconnect")
	if err != nil || result.Outcome != "passed" || result.Reason != "sse_resumed_without_call_replay" {
		t.Fatalf("SSE reconnect=%+v err=%v", result, err)
	}
}

func TestNativeSSEObservePinnedClient(t *testing.T) {
	binary, node, version := os.Getenv("VIBERMATE_NATIVE_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE"), os.Getenv("VIBERMATE_NATIVE_VERSION")
	if binary == "" || node == "" || version == "" {
		t.Skip("requires an exact client, version and pinned Node; not acceptance PASS")
	}
	result, err := runNativePolicyCase(context.Background(), t.TempDir(), nativePinnedClient{nativeMCPClientReport: nativeMCPClientReport{Version: version}, invocation: binary}, node, "sse_observe", t.Logf)
	if err != nil || result.Outcome != "passed" || result.Reason != "sse_observe_only_no_review_evidence" {
		t.Fatalf("Observe-only stream=%+v err=%v", result, err)
	}
	t.Logf("Observe-only diagnostic: %+v", result)
}

func TestNativeModelTurnUsesActualManagedActivity(t *testing.T) {
	binary, node := os.Getenv("VIBERMATE_NATIVE_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE")
	if binary == "" || node == "" {
		t.Skip("requires exact160 client and pinned Node")
	}
	result, err := runNativePolicyCase(context.Background(), t.TempDir(), nativePinnedClient{nativeMCPClientReport: nativeMCPClientReport{Version: "0.160.0"}, invocation: binary}, node, "model_probe")
	if err != nil || result.Outcome != "passed" || result.Reason != "actual_model_activity" || result.ModelOutbound != 1 || !result.ActivityMatched {
		t.Fatalf("actual model=%+v err=%v", result, err)
	}
}

func TestNativeMCPContainmentBeforeAnyClient(t *testing.T) { runNativeFixtureSlice(t, "") }

func TestNativeFixtureTransportPreservesCanonicalVerifiedTLS(t *testing.T) {
	server, roots := nativeTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "chatgpt.com" || r.TLS.ServerName != "chatgpt.com" || r.URL.Path != "/backend-api/ps/mcp" {
			t.Error("canonical Host/SNI/path changed")
		}
		w.WriteHeader(202)
	}))
	transport, err := newNativeFixtureTransport(server.Listener.Addr().String(), roots)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.transport.CloseIdleConnections)
	request, _ := http.NewRequestWithContext(context.Background(), "POST", "https://chatgpt.com/backend-api/ps/mcp", nil)
	response, err := transport.RoundTrip(request, egressnetwork.DefaultPolicy())
	if err != nil {
		t.Fatalf("verified loopback canonical request failed: %v", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != 202 {
		t.Fatal("wrong fixture response")
	}
}

func TestNativeFixtureTransportRefusesUnlistedAndUnverifiedTargets(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	server, roots := nativeTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mu.Lock(); calls++; mu.Unlock(); w.WriteHeader(302) }))
	transport, err := newNativeFixtureTransport(server.Listener.Addr().String(), roots)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.transport.CloseIdleConnections()
	for _, target := range []string{"https://unlisted.invalid/backend-api/ps/mcp", "http://chatgpt.com/backend-api/ps/mcp", "https://chatgpt.com/unlisted", "https://chatgpt.com/backend-api/ps/mcp?unexpected=1", "https://chatgpt.com:444/backend-api/ps/mcp"} {
		req, _ := http.NewRequest("POST", target, nil)
		if _, err := transport.RoundTrip(req, egressnetwork.DefaultPolicy()); err == nil {
			t.Fatal("unlisted target accepted")
		}
	}
	for _, method := range []string{"POST", "DELETE"} {
		request, _ := http.NewRequest(method, "https://chatgpt.com/backend-api/wham/accounts/check", nil)
		if _, err := transport.RoundTrip(request, egressnetwork.DefaultPolicy()); err == nil {
			t.Fatal("bodyless routing path accepted another method")
		}
	}
	query, _ := http.NewRequest("GET", "https://chatgpt.com/backend-api/wham/accounts/check?unproved=1", nil)
	if _, err := transport.RoundTrip(query, egressnetwork.DefaultPolicy()); err == nil {
		t.Fatal("routing path accepted unproved query")
	}
	mu.Lock()
	before := calls
	mu.Unlock()
	if before != 0 {
		t.Fatal("unlisted target reached TLS fixture")
	}
	request, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/ps/mcp", nil)
	if _, err = transport.RoundTrip(request, egressnetwork.DefaultPolicy()); err == nil {
		t.Fatal("redirect accepted")
	}
	untrusted, err := newNativeFixtureTransport(server.Listener.Addr().String(), x509.NewCertPool())
	if err != nil {
		t.Fatal(err)
	}
	defer untrusted.transport.CloseIdleConnections()
	if _, err = untrusted.RoundTrip(request, egressnetwork.DefaultPolicy()); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
	wrongSAN := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("wrong SAN request reached handler") }))
	defer wrongSAN.Close()
	wrongRoots := x509.NewCertPool()
	wrongRoots.AddCert(wrongSAN.Certificate())
	wrong, err := newNativeFixtureTransport(wrongSAN.Listener.Addr().String(), wrongRoots)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.transport.CloseIdleConnections()
	if _, err = wrong.RoundTrip(request, egressnetwork.DefaultPolicy()); err == nil {
		t.Fatal("wrong SAN accepted")
	}
}

func TestNativeFixtureBodylessRoutingRejectsImplicitBodiesBeforeDial(t *testing.T) {
	var requests atomic.Int64
	server, roots := nativeTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(200) }))
	transport, err := newNativeFixtureTransport(server.Listener.Addr().String(), roots)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.transport.CloseIdleConnections()
	for _, kind := range []string{"implicit", "declared", "transfer-encoding"} {
		req, _ := http.NewRequest("GET", "https://chatgpt.com/backend-api/wham/accounts/check", strings.NewReader("synthetic-body"))
		if kind == "implicit" {
			req.ContentLength = 0
		}
		if kind == "transfer-encoding" {
			req.ContentLength = 0
			req.TransferEncoding = []string{"chunked"}
		}
		response, err := transport.RoundTrip(req, egressnetwork.DefaultPolicy())
		if response != nil {
			_ = response.Body.Close()
		}
		if err == nil || requests.Load() != 0 {
			t.Fatalf("%s routing body reached upstream", kind)
		}
	}
}

// Native fixture acceptance must not require or launch the user's packaged App.
func TestNativeMCPConfigAcceptsIsolatedExactClientInputs(t *testing.T) {
	directory := t.TempDir()
	args := []string{"--native-mcp-only"}
	for _, name := range []string{"codex145", "codex159", "codex160", "claude"} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("synthetic executable placeholder"), 0700); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--"+name, path)
	}
	args = append(args, "--report", filepath.Join(directory, "report.json"))
	if _, err := parseConfig(args); err != nil {
		t.Fatalf("private native acceptance configuration rejected: %v", err)
	}
}

func TestNativeMCPConfigRejectsMissingAndNonprivateInputs(t *testing.T) {
	directory := t.TempDir()
	args := []string{"--native-mcp-only"}
	for _, name := range []string{"codex145", "codex159", "codex160", "claude"} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("synthetic executable placeholder"), 0700); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--"+name, path)
	}
	args = append(args, "--report", filepath.Join(directory, "report.json"))
	for i := 1; i < len(args); i += 2 {
		t.Run(args[i], func(t *testing.T) {
			missing := append(append([]string{}, args[:i]...), args[i+2:]...)
			if _, err := parseConfig(missing); err == nil {
				t.Fatal("missing prerequisite accepted")
			}
			relative := append([]string{}, args...)
			relative[i+1] = "relative"
			if _, err := parseConfig(relative); err == nil {
				t.Fatal("relative prerequisite accepted")
			}
		})
	}
	for _, extra := range [][]string{{"--desktop-app", "/private/user.app"}, {"--anthropic-api-key-file", "/private/user.key"}, {"--keep-data"}, {"--data-dir", directory}, {"--deterministic-only"}} {
		if _, err := parseConfig(append(append([]string{}, args...), extra...)); err == nil {
			t.Fatalf("nonprivate mode accepted: %s", extra[0])
		}
	}
}

func TestNativeWrapperResolutionRejectsShadowingBeforeExecution(t *testing.T) {
	for _, prefix := range []string{"codex/bin/node_modules", "codex/node_modules", "node_modules"} {
		t.Run(prefix, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "node_modules", "@openai")
			entry := filepath.Join(root, "codex", "bin", "codex.js")
			native := filepath.Join(root, "codex-darwin-arm64", "vendor", "aarch64-apple-darwin", "bin", "codex")
			for _, path := range []string{entry, native, filepath.Join(root, prefix, "@openai", "codex-darwin-arm64", "package.json")} {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("synthetic"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := checkNativeWrapperResolution(entry, native); err == nil {
				t.Fatal("wrapper may execute a shadowing native child")
			}
		})
	}
}

func TestNativeWrapperResolutionRejectsChildRootEscape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node_modules", "@openai")
	entry := filepath.Join(root, "codex", "bin", "codex.js")
	native := filepath.Join(root, "codex-darwin-arm64", "vendor", "aarch64-apple-darwin", "bin", "codex")
	for _, path := range []string{entry, native} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(entry, []byte("synthetic"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "codex-darwin-arm64", "package.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(outside, []byte("synthetic"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, native); err != nil {
		t.Fatal(err)
	}
	if err := checkNativeWrapperResolution(entry, native); err == nil {
		t.Fatal("native child root escape accepted")
	}
}
