package runlauncher_test

import (
	"context"
	"errors"
	"io"
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
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

func TestLocalCodexRequiresTrustedRootBeforeStartingChild(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("local Codex uses native trust on macOS")
	}
	for _, decision := range []string{"untrusted", "unknown", "trusted"} {
		t.Run(decision, func(t *testing.T) {
			directory := t.TempDir()
			marker := filepath.Join(directory, "started")
			executable := filepath.Join(directory, "codex")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\ntouch started\n"), 0700); err != nil {
				t.Fatal(err)
			}
			control := &controlFixture{
				t: t, workspace: directory, executable: executable, rootPath: filepath.Join(directory, "root.pem"),
				credential: capability(0x61), proxy: capability(0x62), run: capability(0x63),
				expectedCommand: []string{"codex"}, recipe: clientadapter.LaunchCodexResponsesHTTP,
				recognition: clientadapter.RecognitionVerified,
				adapter: &capturecontrol.ClientLaunchAdapterView{
					ClientAdapterView: capturecontrol.ClientAdapterView{
						ID: "codex-cli", Revision: 1, Version: "fixture", CatalogRevision: 7,
						Source:       capturecontrol.ClientAdapterSourcePrelaunchDigestCatalog,
						InstallShape: clientadapter.InstallNativeSingleBinary, LaunchRecipe: clientadapter.LaunchCodexResponsesHTTP,
					},
					StreamingFallbackPolicy: clientadapter.StreamingFallbackClientDefault,
				},
				rootStatus: &desktopcontrol.RootCAResponse{Available: true, RootValid: true, CertificatePresent: "present", TrustDecision: decision},
			}
			server := httptest.NewServer(control)
			defer server.Close()
			discovery := fixedDiscovery{session: localdiscovery.Session{
				Schema: localdiscovery.Schema, InstanceID: capability(0x64), ProcessID: os.Getpid(),
				BaseURL: server.URL, ControlCredential: control.credential, ExpiresAt: time.Now().Add(time.Minute),
			}}
			launcher, err := runlauncher.New(runlauncher.Config{Discovery: discovery,
				BaseEnvironment: []string{"PATH=/usr/bin:/bin"}, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
				Getwd: func() (string, error) { return directory, nil }, LookPath: func(string) (string, error) { return executable, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			code, err := launcher.Run(context.Background(), transparentLaunch("codex"))
			_, markerErr := os.Stat(marker)
			if decision == "trusted" {
				if err != nil || code != 0 || markerErr != nil || control.attachCalls != 1 {
					t.Fatalf("trusted launch: code=%d err=%v marker=%v", code, err, markerErr)
				}
			} else if !errors.Is(err, runlauncher.ErrLocalRootUntrusted) || code != 1 || !errors.Is(markerErr, os.ErrNotExist) || control.attachCalls != 0 {
				t.Fatalf("untrusted launch: code=%d err=%v marker=%v", code, err, markerErr)
			}
			if control.finishCalls != 1 {
				t.Fatalf("unused capture not finalized: %d", control.finishCalls)
			}
		})
	}
}
