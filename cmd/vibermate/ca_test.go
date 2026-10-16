package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCACommandDetectsPlatformAndKeepsConsentExplicit(t *testing.T) {
	for _, args := range [][]string{
		{"install", "--server", "https://runtime.example:9666"},
		{"status", "--server", "http://host.docker.internal:9666"},
		{"download", "--server", "https://runtime.example:9666", "--out", "root.crt"},
		{"install", "--server", "http://host.docker.internal:9666", "--yes", "--fingerprint", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	} {
		if _, err := parseCA(args); err != nil {
			t.Fatalf("valid CA command: %v: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"install", "--server", "http://host.docker.internal:9666", "--yes"},
		{"install", "--server", "https://runtime.example:9666", "--platform", "linux"},
		{"download", "--server", "https://runtime.example:9666"},
		{"status", "--server", "https://runtime.example:9666", "--yes"},
		{"install", "--server", "https://runtime.example:9666", "--fingerprint", "****************************************************************"},
	} {
		if _, err := parseCA(args); err == nil {
			t.Fatalf("invalid CA command accepted: %v", args)
		}
	}
}

func TestCARejectsHostileResponsesWithoutPrintingPayload(t *testing.T) {
	for _, stage := range []string{"session", "resource"} {
		t.Run(stage, func(t *testing.T) {
			const sentinel = "ca-secret-server-field"
			resource := func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"`+sentinel+`":true}`) }
			preflight := http.HandlerFunc(nil)
			if stage == "session" {
				preflight = resource
			}
			target := diagnosticRemoteServerWithPreflight(t, resource, preflight)
			out := filepath.Join(t.TempDir(), "root.pem")
			var stdout, stderr strings.Builder
			code, key := executeCA(context.Background(), []string{"download", "--server", target, "--out", out}, strings.NewReader(""), &stdout, &stderr)
			if code != 1 || key != "cli.error.caFailed" {
				t.Fatalf("code=%d key=%s", code, key)
			}
			if strings.Contains(stdout.String()+stderr.String(), sentinel) {
				t.Fatalf("server payload leaked: %s%s", &stdout, &stderr)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("failed response created CA download: %v", err)
			}
		})
	}
}

func TestCARetainsSafeRejectionMetadata(t *testing.T) {
	target := diagnosticRemoteServerWithPreflight(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"type":"urn:vibermate:error:control-principal-unauthorized","title":"ca-secret-title","status":401,"code":"control_principal_unauthorized"}`)
	})
	var stdout, stderr strings.Builder
	code, key := executeCA(context.Background(), []string{"status", "--server", target}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 || key != "cli.error.caDownloadDenied" || !strings.Contains(stderr.String(), "stage=session_check status=401 reason=control_principal_unauthorized") || strings.Contains(stderr.String(), "ca-secret-title") {
		t.Fatalf("code=%d key=%s stderr=%s", code, key, &stderr)
	}
}
