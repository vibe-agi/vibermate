package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
	"github.com/vibe-agi/vibermate/locales"
)

func TestRunCodexProbeFailureHasLocalizedSafeOutcome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	t.Setenv("HOME", t.TempDir())
	executable := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'SECRET-child-output\\n'\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	credential := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x43}, 32))
	identity := capturecontrol.ClientAdapterView{ID: "codex-cli", Revision: 1, Version: "0.160.0", CatalogRevision: 7, Source: capturecontrol.ClientAdapterSourcePrelaunchDigestCatalog, InstallShape: clientadapter.InstallNativeSingleBinary, LaunchRecipe: clientadapter.LaunchCodexResponsesHTTP}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/capture-runs":
			if r.Header.Get("Authorization") != "Bearer "+credential {
				t.Error("missing control authorization")
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(capturecontrol.LaunchGrant{
				Run:             capturecontrol.CaptureRunView{ID: "capture-run-1", ExecutableLabel: "codex", CWD: t.TempDir(), CreatedAt: now, ExpiresAt: now.Add(time.Hour), ClientAdapterState: clientadapter.StatusVerified, ClientRecognition: clientadapter.RecognitionVerified, CatalogRevision: 7, ClientAdapter: &identity},
				CatalogRevision: 7, Recognition: clientadapter.RecognitionVerified, Adapter: &capturecontrol.ClientLaunchAdapterView{ClientAdapterView: identity, StreamingFallbackPolicy: clientadapter.StreamingFallbackClientDefault},
				LaunchRecipe: clientadapter.LaunchCodexResponsesHTTP, ExecutablePath: executable, ProxyAddress: "http://" + r.Host, ProxyDelivery: capturecontrol.ProxyDeliveryLocalListener,
				ProxyToken: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 32)), RunCapability: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x45}, 32)), RootPEMPath: filepath.Join(t.TempDir(), "root.pem"),
				ProtectedAuthorities: []string{}, ManagedCredentialAuthorities: []string{},
			})
		case "/api/v1/platform/root-ca":
			_ = json.NewEncoder(w).Encode(desktopcontrol.RootCAResponse{Available: true, RootValid: true, CertificatePresent: "present", TrustDecision: "trusted"})
		case "/api/v1/capture-runs/capture-run-1/actions/finish":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("probe failure reached unexpected route %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	layout, err := runtimepath.Default()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.RuntimeDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(localdiscovery.Session{Schema: localdiscovery.Schema, InstanceID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x49}, 16)), ProcessID: os.Getpid(), BaseURL: server.URL, ControlCredential: credential, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.CLIControlRecord, payload, 0600); err != nil {
		t.Fatal(err)
	}
	catalogs, err := locales.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"en_US.UTF-8", "zh_CN.UTF-8"} {
		var stdout, stderr strings.Builder
		env := []string{"LANG=" + language, "PATH=/usr/bin:/bin"}
		code, key := execute([]string{"run", "--", executable}, env, strings.NewReader(""), &stdout, &stderr)
		if code != 1 || key != "cli.error.codexProbeFailed" || !strings.Contains(stderr.String(), "reason=exit_failed") || strings.Contains(stderr.String(), "SECRET") || stdout.Len() != 0 {
			t.Fatalf("probe diagnostic: code=%d key=%q stdout=%q stderr=%q", code, key, stdout.String(), stderr.String())
		}
		if message, err := catalogs.Render(locales.Detect(env), key, nil); err != nil || message == "" {
			t.Fatalf("localized probe outcome: %q %v", message, err)
		}
	}
}
