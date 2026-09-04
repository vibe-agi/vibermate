package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/serverhost"
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
