package runlauncher_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

func TestLauncherManagedAuthClaude(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX child fixture")
	}
	for _, tc := range []struct {
		name               string
		protected, managed []string
		client             string
		signer             bool
		base               []string
		opt, reject        bool
		wantToken          string
	}{
		{name: "managed default", protected: []string{"api.anthropic.com:443"}, managed: []string{"api.anthropic.com:443"}, client: "claude-code", opt: true, wantToken: "vibermate-local-proxy"},
		{name: "managed signer", protected: []string{"api.anthropic.com:443"}, managed: []string{"api.anthropic.com:443"}, client: "claude-code", signer: true, opt: true, wantToken: "vibermate-local-proxy"},
		{name: "custom base lost in existing prep", protected: []string{"claude.fixture.invalid:443"}, managed: []string{"claude.fixture.invalid:443"}, client: "claude-code", base: []string{"ANTHROPIC_BASE_URL=https://claude.fixture.invalid"}, opt: true, reject: true},
		{name: "duplicate conflicting target", protected: []string{"api.anthropic.com:443"}, managed: []string{"api.anthropic.com:443"}, client: "claude-code", base: []string{"ANTHROPIC_BASE_URL=https://api.anthropic.com", "ANTHROPIC_BASE_URL=https://other.invalid"}, opt: true, reject: true},
		{name: "reverse duplicate conflicting target", protected: []string{"api.anthropic.com:443"}, managed: []string{"api.anthropic.com:443"}, client: "claude-code", base: []string{"ANTHROPIC_BASE_URL=https://other.invalid", "ANTHROPIC_BASE_URL=https://api.anthropic.com"}, opt: true, reject: true},
		{name: "identical duplicate target", protected: []string{"api.anthropic.com:443"}, managed: []string{"api.anthropic.com:443"}, client: "claude-code", base: []string{"ANTHROPIC_BASE_URL=https://api.anthropic.com", "ANTHROPIC_BASE_URL=https://api.anthropic.com"}, opt: true, wantToken: "vibermate-local-proxy"},
		{name: "protected only", protected: []string{"api.anthropic.com:443"}, client: "claude-code", opt: true, reject: true},
		{name: "original only", client: "claude-code", opt: true, reject: true},
		{name: "managed wrong target", protected: []string{"api.anthropic.com:443", "other.invalid:443"}, managed: []string{"other.invalid:443"}, client: "claude-code", opt: true, reject: true},
		{name: "unsupported identity", protected: []string{"api.anthropic.com:443"}, managed: []string{"api.anthropic.com:443"}, client: "fixture-other-client", opt: true, reject: true},
		{name: "codex identity with node recipe", protected: []string{"api.anthropic.com:443"}, managed: []string{"api.anthropic.com:443"}, client: "codex-cli", opt: true, reject: true},
		{name: "ordinary managed launch", protected: []string{"api.anthropic.com:443"}, managed: []string{"api.anthropic.com:443"}, client: "claude-code", wantToken: "vibermate-local-proxy"},
		{name: "ordinary original launch", protected: []string{"api.anthropic.com:443"}, client: "claude-code", wantToken: "synthetic-ambient-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			executable := filepath.Join(dir, "claude")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nprintf '%s\\n' '---ENV---'\n/usr/bin/env\n"), 0700); err != nil {
				t.Fatal(err)
			}
			command := []string{"claude", "--model", "fixture-model", "synthetic prompt"}
			control := &controlFixture{t: t, executable: executable, workspace: dir, rootPath: filepath.Join(dir, "root.pem"), credential: capability(0x11), proxy: capability(0x22), run: capability(0x33), expectedCommand: command, expectedEnvironment: "work", authorities: tc.protected, recipe: clientadapter.LaunchNodeEnvProxy, recognition: clientadapter.RecognitionVerified,
				adapter: &capturecontrol.ClientLaunchAdapterView{ClientAdapterView: capturecontrol.ClientAdapterView{ID: tc.client, Revision: 1, Version: "fixture", CatalogRevision: 7, Source: capturecontrol.ClientAdapterSourcePrelaunchDigestCatalog, InstallShape: clientadapter.InstallNativeSingleBinary, LaunchRecipe: clientadapter.LaunchNodeEnvProxy}, StreamingFallbackPolicy: clientadapter.StreamingFallbackClientDefault}}
			if tc.signer {
				control.adapter = nil
				control.recognition = clientadapter.RecognitionRecognized
				control.signer = &capturecontrol.ClientSignerView{ID: tc.client, Revision: 1, CatalogRevision: 7, InstallShape: clientadapter.InstallNativeSingleBinary, LaunchRecipe: clientadapter.LaunchNodeEnvProxy, SignedPath: executable}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/capture-runs" {
					recorder := httptest.NewRecorder()
					control.ServeHTTP(recorder, r)
					var grant capturecontrol.LaunchGrant
					if err := json.Unmarshal(recorder.Body.Bytes(), &grant); err != nil {
						t.Error(err)
					}
					grant.ManagedCredentialAuthorities = append([]string{}, tc.managed...)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(recorder.Code)
					_ = json.NewEncoder(w).Encode(grant)
					return
				}
				control.ServeHTTP(w, r)
			}))
			defer server.Close()
			var out, stderr bytes.Buffer
			base := append([]string{"PATH=/usr/bin:/bin", "ANTHROPIC_API_KEY=synthetic-ambient-key", "ANTHROPIC_AUTH_TOKEN=synthetic-ambient-token", "CLAUDE_CODE_OAUTH_TOKEN=synthetic-ambient-oauth"}, tc.base...)
			launcher, err := runlauncher.New(runlauncher.Config{Discovery: fixedDiscovery{session: localdiscovery.Session{BaseURL: server.URL, ControlCredential: control.credential}}, BaseEnvironment: base, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &stderr, Getwd: func() (string, error) { return dir, nil }, LookPath: func(string) (string, error) { return executable, nil }})
			if err != nil {
				t.Fatal(err)
			}
			code, err := launcher.Run(context.Background(), runlauncher.LaunchRequest{EnvironmentID: "work", Command: command, ManagedAuth: tc.opt})
			if tc.reject {
				if err == nil || code == 0 || out.Len() != 0 || control.attachCalls != 0 || control.createCalls != 1 || control.finishCalls != 1 {
					t.Fatalf("rejected launch: code=%d err=%v create=%d attach=%d finish=%d output=%s", code, err, control.createCalls, control.attachCalls, control.finishCalls, out.String())
				}
				return
			}
			if err != nil || code != 0 {
				t.Fatalf("managed Claude rejected: %d %v", code, err)
			}
			args, env, ok := strings.Cut(out.String(), "---ENV---\n")
			if !ok {
				t.Fatal("child missing")
			}
			values := parseLines(env)
			if args != strings.Join(command[1:], "\n")+"\n" || strings.Contains(env, "VIBERMATE_CODEX_PROVIDER_TOKEN=") {
				t.Fatal("Claude got Codex config or changed argv")
			}
			if values["ANTHROPIC_AUTH_TOKEN"] != tc.wantToken {
				t.Fatalf("wrong Claude token: %q", values["ANTHROPIC_AUTH_TOKEN"])
			}
			if tc.wantToken == "vibermate-local-proxy" && (values["ANTHROPIC_API_KEY"] != "" || values["CLAUDE_CODE_OAUTH_TOKEN"] != "") {
				t.Fatal("ambient credentials survived managed launch")
			}
			if tc.wantToken != "vibermate-local-proxy" && (values["ANTHROPIC_API_KEY"] != "synthetic-ambient-key" || values["CLAUDE_CODE_OAUTH_TOKEN"] != "synthetic-ambient-oauth") {
				t.Fatal("ordinary original auth changed")
			}
			if tc.opt != strings.Contains(stderr.String(), "managed model-service authentication") {
				t.Fatalf("wrong activation notice: %s", stderr.String())
			}
			if control.attachCalls != 1 || control.finishCalls != 1 {
				t.Fatal("lifecycle incomplete")
			}
		})
	}
}
