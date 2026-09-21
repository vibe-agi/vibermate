package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/serverhost"
	"github.com/vibe-agi/vibermate/internal/serveridentity"
)

func TestParseArgumentsRequiresExplicitHostPathsAndPipe(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		nil,
		{"--app-cache-dir=/tmp/cache"},
		{"--app-cache-dir=/tmp/cache", "--data-dir=/tmp/data"},
		{
			"--app-cache-dir=/tmp/cache",
			"--data-dir=/tmp/data",
			"--webview-origin=https://example.com",
			"--bootstrap-fd=1",
			"--parent-lifetime-fd=0",
		},
		{
			"--app-cache-dir=/tmp/cache",
			"--data-dir=/tmp/data",
			"--webview-origin=vibermate://desktop",
			"--bootstrap-fd=1",
		},
		{
			"--app-cache-dir=/tmp/cache",
			"--data-dir=/tmp/data",
			"--webview-origin=vibermate://desktop",
			"--bootstrap-fd=1",
			"--parent-lifetime-fd=2",
		},
		{
			"--app-cache-dir=/tmp/cache",
			"--data-dir=/tmp/data",
			"--webview-origin=tauri://localhost",
			"--bootstrap-fd=1",
			"--parent-lifetime-fd=0",
		},
		{
			"--app-cache-dir=/tmp/cache",
			"--data-dir=/tmp/data",
			"--webview-origin=http://127.0.0.1:1420",
			"--bootstrap-fd=1",
			"--parent-lifetime-fd=0",
		},
		{"--unknown=value"},
	} {
		if _, _, err := parseArguments(arguments); err == nil {
			t.Fatalf("parseArguments(%v) succeeded", arguments)
		}
	}
}

func TestPackagedManagementUIRootRequiresTheClosedAppResource(t *testing.T) {
	t.Parallel()

	contents := filepath.Join(t.TempDir(), "ViberMate.app", "Contents")
	executable := filepath.Join(contents, "MacOS", "vibermated")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := packagedManagementUIRoot(executable); err == nil {
		t.Fatal("packaged daemon accepted a missing Web UI")
	}
	root := filepath.Join(contents, "Resources", "vibermate-web")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "index.html"),
		[]byte("<!doctype html>"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	resolved, err := packagedManagementUIRoot(executable)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != root {
		t.Fatalf("Web UI root = %q, want %q", resolved, root)
	}

	standalone := filepath.Join(t.TempDir(), "vibermated")
	if root, err := packagedManagementUIRoot(standalone); err != nil || root != "" {
		t.Fatalf("standalone Web UI root = %q, error = %v", root, err)
	}
}

func TestAdjacentServerManagementUIRootIsOptionalButClosedWhenPresent(t *testing.T) {
	t.Parallel()

	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(bin, "vibermated")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if root, err := adjacentServerManagementUIRoot(executable); err != nil || root != "" {
		t.Fatalf("missing adjacent Web UI root = %q, error = %v", root, err)
	}

	root := filepath.Join(bin, "vibermate-web")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := adjacentServerManagementUIRoot(executable); err == nil {
		t.Fatal("adjacent Web UI without index.html was accepted")
	}
	if err := os.WriteFile(
		filepath.Join(root, "index.html"),
		[]byte("<!doctype html>"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	resolved, err := adjacentServerManagementUIRoot(executable)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != root {
		t.Fatalf("adjacent Web UI root = %q, want %q", resolved, root)
	}
}

func TestParseCommandConfigAcceptsFlutterDesktopOrigin(t *testing.T) {
	t.Parallel()

	config, err := parseCommandConfig([]string{
		"--app-cache-dir=/tmp/cache",
		"--data-dir=/tmp/data",
		"--webview-origin=vibermate://desktop",
		"--bootstrap-fd=1",
		"--parent-lifetime-fd=0",
		"--remote-server-listen=127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("parseCommandConfig() error = %v", err)
	}
	if config.webviewOrigin != "vibermate://desktop" {
		t.Fatalf("webview origin = %q", config.webviewOrigin)
	}
	if config.remoteServerListenAddress != "127.0.0.1:0" {
		t.Fatalf("remote Server listen address = %q", config.remoteServerListenAddress)
	}
}

func TestParseCommandConfigDefaultsDesktopRemoteServerToLoopback(t *testing.T) {
	t.Parallel()

	config, err := parseCommandConfig([]string{
		"--app-cache-dir=/tmp/cache",
		"--data-dir=/tmp/data",
		"--webview-origin=vibermate://desktop",
		"--bootstrap-fd=1",
		"--parent-lifetime-fd=0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.remoteServerListenAddress != "127.0.0.1:9666" {
		t.Fatalf(
			"default remote Server listen address = %q, want loopback",
			config.remoteServerListenAddress,
		)
	}
}

func TestParseArgumentsRejectsPlaintextDesktopRemoteServerOutsideLoopback(t *testing.T) {
	t.Parallel()

	if _, _, err := parseArguments([]string{
		"--app-cache-dir=/tmp/cache",
		"--data-dir=/tmp/data",
		"--webview-origin=vibermate://desktop",
		"--bootstrap-fd=1",
		"--parent-lifetime-fd=0",
		"--remote-server-listen=0.0.0.0:9666",
	}); err == nil {
		t.Fatal("Desktop remote Server accepted plaintext non-loopback exposure")
	}
}

func TestParseArgumentsRejectsInvalidRemoteServerListenAddress(t *testing.T) {
	t.Parallel()

	base := []string{
		"--app-cache-dir=/tmp/cache",
		"--data-dir=/tmp/data",
		"--webview-origin=vibermate://desktop",
		"--bootstrap-fd=1",
		"--parent-lifetime-fd=0",
	}
	for _, address := range []string{"", "127.0.0.1", ":9666", "127.0.0.1:70000"} {
		arguments := append(
			append([]string(nil), base...),
			"--remote-server-listen="+address,
		)
		if _, _, err := parseArguments(arguments); err == nil {
			t.Fatalf("remote Server listen address %q was accepted", address)
		}
	}
}

func TestParseServerArgumentsAcceptsHeadlessHTTPConfiguration(t *testing.T) {
	t.Parallel()

	data := filepath.Join(t.TempDir(), "runtime-data")
	config, err := parseServerArguments([]string{
		"--listen", "0.0.0.0:9666",
		"--data-dir=" + data,
		"--web-root", filepath.Join(data, "web"),
		"--transport", "http",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.listenAddress != "0.0.0.0:9666" ||
		config.dataDirectory != data ||
		config.webRoot != filepath.Join(data, "web") ||
		config.transport.Mode != serverhost.TransportHTTP {
		t.Fatalf("config = %+v", config)
	}
}

func TestParseServerArgumentsAcceptsAnExplicitAccessAddress(t *testing.T) {
	t.Parallel()
	config, err := parseServerArguments([]string{
		"--transport", "private_ca_tls",
		"--access-address", "runtime.example.com:443",
		"--tls-hosts", "runtime.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.accessAddress != "runtime.example.com:443" ||
		config.transport.Mode != serverhost.TransportPrivateCATLS {
		t.Fatalf("config = %+v", config)
	}
}

func TestParseServerArgumentsBuildsAutomaticHTTPSFromOnePublicAddress(t *testing.T) {
	t.Parallel()
	config, err := parseServerArguments([]string{
		"--listen", "0.0.0.0:9666",
		"--access-address", "runtime.example.com:443",
		"--transport", "automatic_tls",
		"--acme-agree-terms",
		"--acme-email", "owner@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := config.transport.Automatic
	if config.transport.Mode != serverhost.TransportAutomaticTLS ||
		policy.ServerName != "runtime.example.com" ||
		policy.Challenge != serveridentity.AutomaticChallengeHTTP01 ||
		policy.HTTPChallengePort != 80 || !policy.TermsAgreed {
		t.Fatalf("automatic config = %+v", config)
	}

	tlsALPN, err := parseServerArguments([]string{
		"--access-address=runtime.example.com:443",
		"--transport=automatic_tls",
		"--acme-challenge=tls_alpn_01",
		"--acme-agree-terms=true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tlsALPN.transport.Automatic.Challenge !=
		serveridentity.AutomaticChallengeTLSALPN01 ||
		tlsALPN.transport.Automatic.HTTPChallengePort != 0 {
		t.Fatalf("TLS-ALPN config = %+v", tlsALPN)
	}
}

func TestParseServerArgumentsRejectsUnsafeAutomaticHTTPS(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][]string{
		{"--transport", "automatic_tls", "--acme-agree-terms"},
		{"--transport", "automatic_tls", "--access-address", "runtime.example.com:443"},
		{"--transport", "automatic_tls", "--access-address", "192.0.2.10:443", "--acme-agree-terms"},
		{"--transport", "automatic_tls", "--access-address", "runtime.example.com:443", "--acme-agree-terms", "--acme-challenge", "tls_alpn_01", "--acme-http-port", "8080"},
	} {
		if _, err := parseServerArguments(arguments); err == nil {
			t.Fatalf("unsafe automatic HTTPS was accepted: %v", arguments)
		}
	}
}

func TestParseServerArgumentsDefaultsHeadlessHTTPToLoopback(t *testing.T) {
	t.Parallel()

	config, err := parseServerArguments(nil)
	if err != nil {
		t.Fatal(err)
	}
	if config.listenAddress != "127.0.0.1:9666" ||
		config.transport.Mode != serverhost.TransportHTTP {
		t.Fatalf("default Server config = %+v, want loopback HTTP", config)
	}
}

func TestParseServerArgumentsRequiresExplicitHTTPForNonLoopback(t *testing.T) {
	t.Parallel()

	if _, err := parseServerArguments([]string{
		"--listen", "0.0.0.0:9666",
	}); err == nil {
		t.Fatal("implicit plaintext non-loopback Server exposure was accepted")
	}
}

func TestParseServerArgumentsRequiresBothCertificateFilesForManagedTLS(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	certificate := filepath.Join(root, "server.crt")
	privateKey := filepath.Join(root, "server.key")
	config, err := parseServerArguments([]string{
		"--transport", "tls_files",
		"--tls-cert", certificate,
		"--tls-key", privateKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.transport.Mode != serverhost.TransportTLSFiles ||
		config.transport.CertificateFile != certificate ||
		config.transport.PrivateKeyFile != privateKey {
		t.Fatalf("config = %+v", config)
	}
	if _, err := parseServerArguments([]string{
		"--transport", "tls_files", "--tls-cert", certificate,
	}); err == nil {
		t.Fatal("tls_files without a private key was accepted")
	}
}

func TestParseServerArgumentsAcceptsExplicitCertificateHosts(t *testing.T) {
	t.Parallel()
	config, err := parseServerArguments([]string{
		"--transport", "self_signed_tls", "--tls-hosts", "192.168.1.20,vibermate.example.test",
	})
	if err != nil || len(config.transport.TLSHosts) != 2 {
		t.Fatalf("configured hosts were not accepted: %v", err)
	}
	for _, args := range [][]string{
		{"--tls-hosts", "192.168.1.20"},
		{"--transport", "self_signed_tls", "--tls-hosts", "https://192.168.1.20:9667"},
		{"--transport", "self_signed_tls", "--tls-hosts", "0.0.0.0"},
		{"--transport", "self_signed_tls", "--tls-hosts", "192.168.1.20,"},
	} {
		if _, err := parseServerArguments(args); err == nil {
			t.Fatalf("invalid certificate host configuration was accepted: %v", args)
		}
	}
}

func TestParseServerArgumentsRejectsRemovedClientAdmissionFlag(t *testing.T) {
	t.Parallel()

	if _, err := parseServerArguments([]string{
		"--client-admission", "no_review",
	}); err == nil {
		t.Fatal("unsupported admission policy was accepted")
	}
}

func TestParseRecoveryKeyArgumentsUsesTheServerDataDirectory(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "server-data")
	for _, arguments := range [][]string{{"--data-dir", root}, {"--data-dir=" + root}} {
		resolved, err := parseRecoveryKeyArguments(arguments)
		if err != nil {
			t.Fatal(err)
		}
		if resolved != root {
			t.Fatalf("recovery data directory = %q, want %q", resolved, root)
		}
	}
	for _, arguments := range [][]string{
		{"--listen", "127.0.0.1:9666"},
		{"--data-dir", root, "--data-dir", root},
		{"--data-dir", "relative"},
	} {
		if _, err := parseRecoveryKeyArguments(arguments); err == nil {
			t.Fatalf("parseRecoveryKeyArguments(%v) succeeded", arguments)
		}
	}
}

func TestServerHelpCoversTheThreeRemoteCertificatePaths(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"private_ca_tls",
		"automatic_tls",
		"ca-certificate",
		"docs/deployment.md",
	} {
		if !strings.Contains(serverHelp, value) {
			t.Fatalf("server help does not contain %q", value)
		}
	}
}

func TestParseCACertificateArgumentsUsesTheServerDataDirectory(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "server-data")
	for _, arguments := range [][]string{{"--data-dir", root}, {"--data-dir=" + root}} {
		resolved, err := parseServerDataDirectoryArguments(arguments, "ca-certificate")
		if err != nil {
			t.Fatal(err)
		}
		if resolved != root {
			t.Fatalf("CA data directory = %q, want %q", resolved, root)
		}
	}
}
