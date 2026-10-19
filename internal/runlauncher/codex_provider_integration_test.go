package runlauncher_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

func TestLauncherCodexProvider(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX child fixture")
	}
	for _, tc := range []struct {
		name        string
		managed     []string
		env         environment.EnvironmentID
		generic     bool
		otherClient bool
		signer      bool
		opt         bool
		args        []string
		base        []string
		want        string
		reject      bool
	}{
		{name: "chatgpt managed", managed: []string{"chatgpt.com:443"}, env: "work", opt: true, want: "https://chatgpt.com/backend-api/codex"},
		{name: "recognized Codex signer", signer: true, managed: []string{"chatgpt.com:443"}, env: "work", opt: true, want: "https://chatgpt.com/backend-api/codex"},
		{name: "api managed", managed: []string{"api.openai.com:443"}, env: "work", opt: true, want: "https://api.openai.com/v1"},
		{name: "both prefer chatgpt", managed: []string{"api.openai.com:443", "chatgpt.com:443"}, env: "work", opt: true, want: "https://chatgpt.com/backend-api/codex"},
		{name: "protected only", env: "work", opt: true, reject: true},
		{name: "no environment", opt: true, reject: true},
		{name: "transparent", env: environment.SystemTransparentID, opt: true, reject: true},
		{name: "other client with codex recipe", env: "work", managed: []string{"chatgpt.com:443"}, otherClient: true, opt: true, reject: true},
		{name: "unrecognized executable", env: "work", generic: true, opt: true, reject: true},
		{name: "ordinary login", env: "work", managed: []string{"chatgpt.com:443"}},
		{name: "provider override", env: "work", managed: []string{"chatgpt.com:443"}, opt: true, args: []string{"-cmodel_provider=\"other\""}, reject: true},
		{name: "invalid base", env: "work", managed: []string{"chatgpt.com:443"}, opt: true, base: []string{"CODEX_BASE_URL=https://name:synthetic@chatgpt.com"}, reject: true},
		{name: "base conflict", env: "work", managed: []string{"chatgpt.com:443"}, opt: true, base: []string{"OPENAI_BASE_URL=https://other.invalid/v1"}, reject: true},
		{name: "opaque prompt and model", env: "work", managed: []string{"chatgpt.com:443"}, opt: true, args: []string{"--model", "model_provider=other", "--", "--oss", "--config=model_provider=other"}, want: "https://chatgpt.com/backend-api/codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			executable := filepath.Join(dir, "codex")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nprintf '%s\\n' '---ENV---'\n/usr/bin/env\n"), 0700); err != nil {
				t.Fatal(err)
			}
			command := append([]string{"codex", "exec", "synthetic --oss model_provider=other"}, tc.args...)
			control := &controlFixture{t: t, executable: executable, workspace: dir, rootPath: filepath.Join(dir, "root.pem"), credential: capability(0x11), proxy: capability(0x22), run: capability(0x33), expectedCommand: command, expectedEnvironment: tc.env.String(), authorities: []string{"api.openai.com:443", "chatgpt.com:443"}, recipe: clientadapter.LaunchCodexResponsesHTTP, recognition: clientadapter.RecognitionVerified,
				adapter: &capturecontrol.ClientLaunchAdapterView{ClientAdapterView: capturecontrol.ClientAdapterView{ID: "codex-cli", Revision: 1, Version: "0.145.0", CatalogRevision: 7, Source: capturecontrol.ClientAdapterSourcePrelaunchDigestCatalog, InstallShape: clientadapter.InstallNativeSingleBinary, LaunchRecipe: clientadapter.LaunchCodexResponsesHTTP}, StreamingFallbackPolicy: clientadapter.StreamingFallbackClientDefault}}
			if tc.signer {
				control.adapter = nil
				control.recognition = clientadapter.RecognitionRecognized
				control.signer = &capturecontrol.ClientSignerView{ID: "codex-cli", Revision: 1, CatalogRevision: 7, InstallShape: clientadapter.InstallNativeSingleBinary, LaunchRecipe: clientadapter.LaunchCodexResponsesHTTP, SignedPath: executable}
			}
			if tc.otherClient {
				control.adapter.ID = "fixture-other-client"
			}
			if tc.generic {
				control.recipe = clientadapter.LaunchGeneric
				control.recognition = clientadapter.RecognitionUnverified
				control.adapter = nil
				control.authorities = nil
				control.rootPath = ""
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
			base := append([]string{"PATH=/usr/bin:/bin", "CODEX_HOME=" + dir, "OPENAI_API_KEY=synthetic-native-key", "VIBERMATE_CODEX_PROVIDER_TOKEN=stale-placeholder", "NO_PROXY=chatgpt.com,api.openai.com,localhost"}, tc.base...)
			launcher, err := runlauncher.New(runlauncher.Config{Discovery: fixedDiscovery{session: localdiscovery.Session{BaseURL: server.URL, ControlCredential: control.credential}}, BaseEnvironment: base, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &stderr, Getwd: func() (string, error) { return dir, nil }, LookPath: func(string) (string, error) { return executable, nil }})
			if err != nil {
				t.Fatal(err)
			}
			request := runlauncher.LaunchRequest{EnvironmentID: tc.env, Command: command}
			request.ManagedAuth = tc.opt
			code, err := launcher.Run(context.Background(), request)
			if tc.reject {
				if err == nil || code == 0 || out.Len() != 0 || control.attachCalls != 0 {
					t.Fatalf("rejected launch started: code=%d err=%v output=%s", code, err, out.String())
				}
				var providerError *runlauncher.ManagedAuthError
				if !errors.As(err, &providerError) {
					t.Fatalf("rejection lacks provider diagnostic: %v", err)
				}
				if control.finishCalls != control.createCalls {
					t.Fatalf("created=%d finalized=%d", control.createCalls, control.finishCalls)
				}
				if tc.env == "" && control.createCalls != 0 {
					t.Fatal("missing environment created Capture")
				}
				return
			}
			if err != nil || code != 0 {
				t.Fatalf("launch code=%d err=%v", code, err)
			}
			args, env, ok := strings.Cut(out.String(), "---ENV---\n")
			if !ok {
				t.Fatal("child missing")
			}
			values := parseLines(env)
			if values["CODEX_HOME"] != dir || values["OPENAI_API_KEY"] != "synthetic-native-key" {
				t.Fatal("native home or credentials changed")
			}
			if !strings.HasSuffix(args, strings.Join(command[1:], "\n")+"\n") {
				t.Fatalf("original suffix lost: %s", args)
			}
			if tc.opt {
				for _, setting := range []string{`model_provider="vibermate"`, `model_providers.vibermate.name="ViberMate"`, `model_providers.vibermate.base_url="` + tc.want + `"`, `model_providers.vibermate.wire_api="responses"`, `model_providers.vibermate.requires_openai_auth=false`, `model_providers.vibermate.env_key="VIBERMATE_CODEX_PROVIDER_TOKEN"`, `model_providers.vibermate.supports_websockets=false`} {
					if !strings.Contains(args, setting+"\n") {
						t.Errorf("missing provider setting %s in %s", setting, args)
					}
				}
				if values["VIBERMATE_CODEX_PROVIDER_TOKEN"] != "vibermate-local-proxy" {
					t.Error("reserved child placeholder missing")
				}
				if strings.Contains(values["NO_PROXY"], "chatgpt") || strings.Contains(values["NO_PROXY"], "openai") {
					t.Error("managed destinations bypass proxy")
				}
				if !strings.Contains(stderr.String(), "managed model-service authentication") {
					t.Errorf("activation missing: %s", stderr.String())
				}
			} else if values["VIBERMATE_CODEX_PROVIDER_TOKEN"] != "stale-placeholder" || strings.Contains(args, "\nmodel_provider=") || strings.Contains(stderr.String(), "managed model-service authentication") {
				t.Fatal("ordinary launch selected provider")
			}
			if control.attachCalls != 1 || control.finishCalls != 1 {
				t.Fatal("launch lifecycle incomplete")
			}
		})
	}
}
