//go:build darwin || linux

package runlauncher_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

func TestACPFinalSnapshotFailurePreservesActualChildExit(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	command := []string{executable, "-test.run=^TestACPProcessFixture$"}
	control := &controlFixture{t: t, executable: executable, workspace: directory, credential: capability(0x61), proxy: capability(0x62), run: capability(0x63), expectedCommand: command, recipe: clientadapter.LaunchGeneric, recognition: clientadapter.RecognitionUnknown}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/start-acp"):
			writeControlJSON(w, http.StatusOK, acpobservation.Record{RunID: "capture-run-1", Policy: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingMetadataOnly, RetentionDays: 30}, ExpiresAtMillis: time.Now().Add(time.Hour).UnixMilli(), Snapshot: acpobservation.Snapshot{Revision: 1}})
		case strings.HasSuffix(r.URL.Path, "/observe-acp"):
			writeControlProblem(w, http.StatusServiceUnavailable)
		default:
			control.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	input, err := os.CreateTemp(directory, "input")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	exact := []byte("unchanged editor bytes\n")
	if _, err := input.Write(exact); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(directory, "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	stderr, err := os.CreateTemp(directory, "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	launcher, err := runlauncher.New(runlauncher.Config{Discovery: fixedDiscovery{session: localdiscovery.Session{Schema: localdiscovery.Schema, ProcessID: os.Getpid(), BaseURL: server.URL, InstanceID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x64}, 20)), ControlCredential: control.credential, ExpiresAt: time.Now().Add(time.Minute)}}, BaseEnvironment: []string{"VIBERMATE_TEST_ACP_PROCESS=echo", "GORACE=atexit_sleep_ms=0"}, Stdin: input, Stdout: output, Stderr: stderr, Getwd: func() (string, error) { return directory, nil }, ControlTimeout: time.Second, TerminationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := launcher.RunACP(ctx, runlauncher.ACPLaunchRequest{Command: command})
	if err != nil || code != 0 {
		t.Fatalf("optional snapshot changed actual child success: %d %v", code, err)
	}
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var result acpProcessFixtureResult
	if err := json.NewDecoder(output).Decode(&result); err != nil || !bytes.Equal(result.Input, exact) {
		t.Fatalf("forwarded bytes: %+v %v", result, err)
	}
}
