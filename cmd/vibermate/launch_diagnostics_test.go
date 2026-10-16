package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientpath"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func TestRemoteRunWithoutSavedLoginRequestsRemoteLogin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr strings.Builder
	code, key := execute([]string{"run", "--server", "https://127.0.0.1:43210", "--", "/usr/bin/true"}, []string{"LANG=en_US.UTF-8"}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 || key != keyRemoteLoginRequired {
		t.Fatalf("code=%d key=%q stderr=%q", code, key, stderr.String())
	}
}

// The preflight succeeds; resource authentication can still fail after revocation.
func TestRemoteCreateRejectedSessionRequestsRemoteLogin(t *testing.T) {
	target := diagnosticRemoteServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	var stdout, stderr strings.Builder
	code, key := execute([]string{"run", "--server", target, "--", "/usr/bin/true"}, []string{"LANG=en_US.UTF-8"}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 || key != keyRemoteLoginRequired {
		t.Fatalf("code=%d key=%q stderr=%q", code, key, stderr.String())
	}
}

func TestRemoteConnectionFailureUsesServerDiagnostic(t *testing.T) {
	target := diagnosticRemoteServer(t, func(w http.ResponseWriter, r *http.Request) {
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	})
	var stdout, stderr strings.Builder
	code, key := execute([]string{"run", "--server", target, "--", "/usr/bin/true"}, []string{"LANG=en_US.UTF-8"}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 || key != keyRemoteRuntimeUnavailable {
		t.Fatalf("code=%d key=%q stderr=%q", code, key, stderr.String())
	}
}

func diagnosticRemoteServer(t *testing.T, create http.HandlerFunc) string {
	return diagnosticRemoteServerWithPreflight(t, create, nil)
}

func diagnosticRemoteServerWithPreflight(t *testing.T, create, preflight http.HandlerFunc) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	now := time.Now().UTC().Truncate(time.Millisecond)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x53}, 32))
	state, err := clientpath.DefaultRemoteStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := workspaceidentity.Open(context.Background(), filepath.Join(state, "identity"), bytes.NewReader(bytes.Repeat([]byte{0x36}, 64)), now)
	if err != nil {
		t.Fatal(err)
	}
	machineID := identity.MachineID().String()
	if err := identity.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing authenticated request")
		}
		switch r.URL.Path {
		case servercontrol.RuntimeUserCurrentSessionPath:
			if preflight != nil {
				preflight(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(servercontrol.RuntimeUserCurrentSession{Schema: servercontrol.RuntimeUserCurrentSessionSchema, InstanceID: "instance.test", APIVersion: "v1", ProductBuild: "test-build", SessionID: "login.test", MachineID: machineID, DeviceName: "fixture", User: servercontrol.RuntimeUserView{ID: "user.test", Username: "alice"}})
		case "/api/v1/capture-runs", servercontrol.RuntimeRootCAPath:
			create(w, r)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	target, err := serverconnection.ParseTarget(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, err := serverconnection.OpenLoginStore(filepath.Join(state, "login"))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := serverconnection.NewLoginCredential(serverconnection.LoginCredentialInput{Target: target, InstanceID: "instance.test", UserID: "user.test", Username: "alice", SessionID: "login.test", SessionToken: token, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(credential); err != nil {
		t.Fatal(err)
	}
	return server.URL
}

func TestRemoteCreateRejectionsExposeOnlySafeDetails(t *testing.T) {
	for _, test := range []struct {
		name                 string
		status               int
		payload, reason, key string
	}{
		{"known", 422, `{"type":"urn:vibermate:error:capture-run-create-failed","title":"BODY_SECRET","status":422,"code":"capture_run_create_failed"}`, "capture_run_create_failed", keyLaunchFailed},
		{"unknown", 403, `{"type":"urn:vibermate:error:UNKNOWN_SECRET","title":"BODY_SECRET","status":403,"code":"UNKNOWN_SECRET"}`, "unknown", keyLaunchFailed},
		{"html", 502, `<h1>BODY_SECRET</h1>`, "unknown", keyLaunchFailed},
		{"oversized", 503, strings.Repeat("BODY_SECRET", 14000), "unknown", keyLaunchFailed},
		{"trailing", 422, `{"type":"urn:vibermate:error:capture-run-create-failed","title":"BODY_SECRET","status":422,"code":"capture_run_create_failed"} {"secret":"BODY_SECRET"}`, "unknown", keyLaunchFailed},
		{"environment", 404, `{"type":"urn:vibermate:error:environment-not-found","title":"BODY_SECRET","status":404,"code":"environment_not_found"}`, "environment_not_found", keyEnvironmentMissing},
		{"target", 422, `{"type":"urn:vibermate:error:client-target-invalid","title":"BODY_SECRET","status":422,"code":"client_target_invalid"}`, "client_target_invalid", keyClientTargetInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := diagnosticRemoteServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Secret", "HEADER_SECRET")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.payload))
			})
			var stdout, stderr strings.Builder
			code, key := execute([]string{"run", "--server", target, "--", "/usr/bin/true"}, []string{"LANG=en_US.UTF-8"}, strings.NewReader(""), &stdout, &stderr)
			output := stderr.String()
			if code != 1 || key != test.key {
				t.Fatalf("code=%d key=%q stderr=%q", code, key, output)
			}
			want := fmt.Sprintf("stage=capture_create status=%d reason=%s", test.status, test.reason)
			if !strings.Contains(output, want) {
				t.Fatalf("missing safe details %q: %q", want, output)
			}
			for _, secret := range []string{"BODY_SECRET", "UNKNOWN_SECRET", "HEADER_SECRET", base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x53}, 32))} {
				if strings.Contains(output, secret) {
					t.Fatalf("leaked sentinel %q", secret)
				}
			}
			if stdout.Len() != 0 || len(output) > 1024 {
				t.Fatalf("unbounded diagnostics/stdout: %q %q", output, stdout.String())
			}
		})
	}
}

func TestRemotePreflightFailuresPreserveSafeAuthenticationStage(t *testing.T) {
	for _, test := range []struct {
		status int
		key    string
	}{{401, keyRemoteLoginRequired}, {503, keyRemoteRuntimeUnavailable}} {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			target := diagnosticRemoteServerWithPreflight(t, func(w http.ResponseWriter, r *http.Request) { t.Error("create after failed preflight") }, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte("BODY_SECRET"))
			})
			var stdout, stderr strings.Builder
			code, key := execute([]string{"run", "--server", target, "--", "/usr/bin/true"}, nil, strings.NewReader(""), &stdout, &stderr)
			want := fmt.Sprintf("stage=session_check status=%d reason=unknown", test.status)
			if code != 1 || key != test.key || !strings.Contains(stderr.String(), want) || strings.Contains(stderr.String(), "BODY_SECRET") {
				t.Fatalf("code=%d key=%q stderr=%q", code, key, stderr.String())
			}
		})
	}
}

func TestRemoteCreateCancellationKeepsInterruptOutcome(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	target := diagnosticRemoteServer(t, func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-release
	})
	var stdout, stderr strings.Builder
	code, key := executeContext(ctx, []string{"run", "--server", target, "--", "/usr/bin/true"}, nil, strings.NewReader(""), &stdout, &stderr)
	if code != 130 || key != "" {
		t.Fatalf("canceled code=%d key=%q stderr=%q", code, key, stderr.String())
	}
	if strings.Contains(stderr.String(), "control request rejected") {
		t.Fatalf("cancellation reported as rejection: %q", stderr.String())
	}
}

func TestRemoteCreateCancellationWhileReadingRejectionKeepsInterruptOutcome(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	target := diagnosticRemoteServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2048")
		w.WriteHeader(http.StatusUnauthorized)
		w.(http.Flusher).Flush()
		timer := time.AfterFunc(30*time.Millisecond, cancel)
		defer timer.Stop()
		<-release
	})
	var stdout, stderr strings.Builder
	code, key := executeContext(ctx, []string{"run", "--server", target, "--", "/usr/bin/true"}, nil, strings.NewReader(""), &stdout, &stderr)
	if code != 130 || key != "" {
		t.Fatalf("canceled response read code=%d key=%q stderr=%q", code, key, stderr.String())
	}
}
