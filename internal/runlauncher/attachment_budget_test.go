//go:build darwin || linux

package runlauncher_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

func TestDefaultAttachmentWaitsForBusyRuntimeWithoutStoppingChild(t *testing.T) {
	for _, createTimeout := range []time.Duration{0, time.Second} {
		t.Run(fmt.Sprintf("create=%s", createTimeout), func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			executable := filepath.Join(directory, "agent")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nread line\nprintf 'survived:%s' \"$line\"\nexit 7\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			input, send, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer send.Close()
			control := &controlFixture{
				t: t, executable: executable, workspace: directory,
				credential: capability(0x11), proxy: capability(0x22), run: capability(0x33),
				expectedCommand: []string{"agent"}, recipe: clientadapter.LaunchGeneric,
				recognition: clientadapter.RecognitionUnknown,
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/attach-process") {
					// A busy Runtime can take longer than the 5s heartbeat budget.
					// The child must remain alive until its initial attachment ends.
					timer := time.NewTimer(6 * time.Second)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-r.Context().Done():
						return
					}
					control.ServeHTTP(w, r)
					_, _ = send.WriteString("attached\n")
					return
				}
				control.ServeHTTP(w, r)
			}))
			defer server.Close()
			var output, diagnostic bytes.Buffer
			launcher, err := runlauncher.New(runlauncher.Config{
				Discovery:       fixedDiscovery{session: localdiscovery.Session{BaseURL: server.URL, ControlCredential: control.credential}},
				BaseEnvironment: []string{"PATH=/usr/bin:/bin"},
				Stdin:           input, Stdout: &output, Stderr: &diagnostic,
				CreateTimeout: createTimeout,
				Getwd:         func() (string, error) { return directory, nil },
				LookPath:      func(string) (string, error) { return executable, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			code, err := launcher.Run(ctx, transparentLaunch("agent"))
			if err != nil || code != 7 || output.String() != "survived:attached" {
				t.Fatalf("slow initial attachment stopped the child: code=%d err=%v output=%q", code, err, output.String())
			}
		})
	}
}
