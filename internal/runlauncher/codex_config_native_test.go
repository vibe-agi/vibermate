package runlauncher_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

// The real native config reader catches Clap dropping root configuration when
// a later command scope supplies -c. The fixture owns every network response.
func TestInstalledCodexConfigOverridesSurviveCommandScopes(t *testing.T) {
	binary := codexStableRepairBinary(t)
	for _, managed := range []bool{false, true} {
		name := "ordinary"
		if managed {
			name = "managed"
		}
		t.Run(name, func(t *testing.T) {
			home, before := codexProviderNativeHome(t, "model_provider=\"native_fixture\"\n[model_providers.native_fixture]\nname=\"Native fixture\"\nbase_url=\"https://api.openai.com/v1\"\nwire_api=\"responses\"\nrequires_openai_auth=false\n")
			command := []string{"codex", "-c", `model_reasoning_effort="low"`, "-c", `model_verbosity="low"`, "app-server", "--stdio", "-c", `model_reasoning_effort="high"`}
			fixture := newCodexProviderNativeFixture(t, binary, home, "work", "chatgpt.com:443", command, 0x31, managed)
			config := codexLauncherConfigRead(t, fixture, binary, home, managed)
			want := "native_fixture"
			if managed {
				want = "vibermate"
			}
			if config["model_provider"] != want || config["model_reasoning_effort"] != "high" || config["model_verbosity"] != "low" {
				t.Fatalf("native effective configuration lost scoped overrides: provider=%v reasoning=%v verbosity=%v", config["model_provider"], config["model_reasoning_effort"], config["model_verbosity"])
			}
			if managed {
				providers, _ := config["model_providers"].(map[string]any)
				provider, _ := providers["vibermate"].(map[string]any)
				if provider["base_url"] != "https://chatgpt.com/backend-api/codex" || provider["env_key"] != "VIBERMATE_CODEX_PROVIDER_TOKEN" || provider["requires_openai_auth"] != false {
					t.Fatalf("native effective managed provider is incomplete: %v", provider)
				}
			}
			codexProviderNativeUnchanged(t, home, before, nil)
		})
	}
}

func codexLauncherConfigRead(t *testing.T, fixture *codexProviderNativeFixture, binary, home string, managed bool) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	input, writer := io.Pipe()
	output, reader := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		code, err := fixture.launcher(binary, home, input, reader, &stderr).Run(ctx, runlauncher.LaunchRequest{EnvironmentID: environment.EnvironmentID(fixture.control.expectedEnvironment), ManagedAuth: managed, Command: fixture.control.expectedCommand})
		if err == nil && code != 0 {
			err = io.ErrUnexpectedEOF
		}
		_ = reader.CloseWithError(err)
		done <- err
	}()
	t.Cleanup(func() {
		_ = writer.Close()
		cancel()
		_ = output.Close()
		_ = input.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("native configuration reader did not terminate")
		}
	})
	lines := make(chan []byte, 8)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			select {
			case lines <- bytes.Clone(scanner.Bytes()):
			case <-ctx.Done():
				return
			}
		}
	}()
	encoder := json.NewEncoder(writer)
	rpc := func(id int, method string, params any) json.RawMessage {
		t.Helper()
		if err := encoder.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("native %s exited before response", method)
				}
				var reply struct {
					ID     int             `json:"id"`
					Result json.RawMessage `json:"result"`
					Error  json.RawMessage `json:"error"`
				}
				if json.Unmarshal(line, &reply) == nil && reply.ID == id {
					if len(reply.Error) != 0 && string(reply.Error) != "null" {
						t.Fatalf("native %s rejected synthetic request: %s", method, reply.Error)
					}
					return reply.Result
				}
			case <-ctx.Done():
				t.Fatalf("native %s timed out", method)
			}
		}
	}
	rpc(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "vibermate_config_acceptance", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}})
	if err := encoder.Encode(map[string]any{"method": "initialized"}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rpc(2, "config/read", map[string]bool{"includeLayers": false}), &result); err != nil {
		t.Fatal(err)
	}
	// Nothing in this fixture may initiate a model operation.
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.requests) != 0 {
		t.Fatal("configuration inspection sent a model request")
	}
	return result.Config
}

func TestInstalledCodexCaptureOwnsOrdinaryProxyFeatures(t *testing.T) {
	binary := codexStableRepairBinary(t)
	home, _ := codexProviderNativeHome(t, "features.respect_system_proxy=true\nfeatures.system_proxy_fallback=true\n")
	command := []string{"codex", "--enable", "respect_system_proxy", "--enable", "system_proxy_fallback", "--disable", "api_key_model_discovery", "app-server", "--stdio", "--disable", "remote_plugin", "-c", `model_reasoning_effort="high"`}
	fixture := newCodexProviderNativeFixture(t, binary, home, "work", "chatgpt.com:443", command, 0x41, true)
	config := codexLauncherConfigRead(t, fixture, binary, home, true)
	features, _ := config["features"].(map[string]any)
	for _, name := range []string{"respect_system_proxy", "system_proxy_fallback"} {
		if features[name] != false {
			t.Fatalf("ordinary %s escaped capture proxy ownership: %v", name, features[name])
		}
	}
	if features["api_key_model_discovery"] != false || features["remote_plugin"] != false {
		t.Fatal("Capture proxy guard lost unrelated native feature disables")
	}
}

func TestInstalledCodexCaptureStrictConfiguration(t *testing.T) {
	binary := codexStableRepairBinary(t)
	home, _ := codexProviderNativeHome(t, "")
	command := []string{"codex", "app-server", "--stdio", "--strict-config", "-c", `model_reasoning_effort="high"`}
	fixture := newCodexProviderNativeFixture(t, binary, home, "work", "chatgpt.com:443", command, 0x51, true)
	config := codexLauncherConfigRead(t, fixture, binary, home, true)
	if config["model_provider"] != "vibermate" {
		t.Fatal("strict native configuration lost the managed provider")
	}
}

func codexStableRepairBinary(t *testing.T) string {
	t.Helper()
	binary := codexProviderNativeBinary(t)
	command := exec.CommandContext(codexProviderNativeContext(t), binary, "--version")
	command.Env = codexProviderNativeEnvironment(t.TempDir())
	version, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(version)) != "codex-cli 0.163.0" {
		t.Skip("repair acceptance is pinned separately to 0.163.0")
	}
	return binary
}
