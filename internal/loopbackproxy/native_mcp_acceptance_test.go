package loopbackproxy_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// This opt-in diagnostic establishes the pinned default AuthManager wire path.
// It is not the launcher/runtime/reader acceptance matrix.
func TestExactNativeDefaultAuthManagerWire(t *testing.T) {
	binary := os.Getenv("VIBERMATE_NATIVE_CODEX160")
	if binary == "" {
		t.Skip("exact-client diagnostic requires VIBERMATE_NATIVE_CODEX160")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("absolute native executable required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	directory := t.TempDir()
	ring := loopbackproxy.NewTargetObservationRing(time.Now)
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, &nativeHandlerSettings{origin: "https://chatgpt.com", configure: func(o *loopbackproxy.Options) { o.TargetObserver = ring }})
	rootPath := filepath.Join(directory, "root.pem")
	if err := os.WriteFile(rootPath, f.proxy.authority.CertificatePEM(), 0600); err != nil {
		t.Fatal(err)
	}
	// Exact a956835 app-server/tests/common/auth_fixtures.rs:145–208 shape.
	jwt := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_plan_type":"plus","chatgpt_user_id":"synthetic-user","chatgpt_account_id":"synthetic-account"}}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte("signature"))
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "tokens": map[string]any{"id_token": jwt, "access_token": "synthetic-original-login", "refresh_token": "synthetic-never-refresh", "account_id": "synthetic-account"}, "last_refresh": time.Now().UTC()})
	if err := os.WriteFile(filepath.Join(directory, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "config.toml"), []byte("cli_auth_credentials_store = \"file\"\n[features]\napps = true\n[analytics]\nenabled = false\n[feedback]\nenabled = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var methods []string
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		if r.Method == http.MethodGet {
			reader, writer := io.Pipe()
			context.AfterFunc(r.Context(), func() { _ = writer.Close() })
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
		}
		var rpc struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &rpc); err != nil {
			return nil, err
		}
		mu.Lock()
		methods = append(methods, rpc.Method)
		mu.Unlock()
		if r.URL.Hostname() != "chatgpt.com" || r.URL.Path != "/backend-api/ps/mcp" || r.Header.Get("Authorization") != "Bearer synthetic-original-login" {
			t.Logf("fixture mismatch: host=%q path=%q originalAuthMatched=%t", r.URL.Host, r.URL.Path, r.Header.Get("Authorization") == "Bearer synthetic-original-login")
			return nil, fmt.Errorf("default authentication or target mismatch")
		}
		h := http.Header{"Content-Type": {"application/json"}, "Mcp-Session-Id": {"synthetic-session"}}
		var result any
		switch rpc.Method {
		case "initialize":
			var p struct {
				ClientInfo struct {
					Version string `json:"version"`
				} `json:"clientInfo"`
			}
			_ = json.Unmarshal(rpc.Params, &p)
			if p.ClientInfo.Version != "0.160.0" {
				return nil, fmt.Errorf("wrong initialize version")
			}
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "synthetic", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "read_file", "description": "Synthetic public tool", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "synthetic result"}}}
		default:
			return &http.Response{StatusCode: 202, Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(bytes.NewReader(encoded))}, nil
	}
	proxy := url.URL{Scheme: "http", Host: f.proxy.listener.Addr().String(), User: url.UserPassword("capture", f.credential)}
	command := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://")
	command.Dir = directory
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + directory, "CODEX_HOME=" + directory, "CODEX_CA_CERTIFICATE=" + rootPath, "HTTPS_PROXY=" + proxy.String(), "HTTP_PROXY=" + proxy.String(), "ALL_PROXY=" + proxy.String(), "NO_PROXY="}
	command.WaitDelay = time.Second
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = stdin.Close(); _ = command.Wait() })
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	send := func(id int, method string, params any) {
		t.Helper()
		if err := json.NewEncoder(stdin).Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(id int) map[string]json.RawMessage {
		t.Helper()
		for scanner.Scan() {
			var value map[string]json.RawMessage
			if json.Unmarshal(scanner.Bytes(), &value) != nil {
				continue
			}
			if string(value["id"]) == fmt.Sprint(id) {
				if value["error"] != nil {
					mu.Lock()
					observed := append([]string{}, methods...)
					mu.Unlock()
					page, _ := f.store.NativeToolRepository().Page(context.Background(), nativetool.PageRequest{Capture: f.capture, Limit: 30})
					for _, op := range page.Items {
						t.Logf("native operation kind=%s state=%s reason=%s", op.Kind, op.State, op.Reason)
					}
					t.Fatalf("app-server request %d rejected: methods=%v observations=%+v error=%s", id, observed, ring.Observations(), value["error"])
				}
				return value
			}
		}
		mu.Lock()
		observed := append([]string{}, methods...)
		mu.Unlock()
		t.Fatalf("app-server ended: methods=%v deadline=%v stderr=%s", observed, ctx.Err(), stderr.String())
		return nil
	}
	send(1, "initialize", map[string]any{"clientInfo": map[string]any{"name": "native_acceptance", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}})
	read(1)
	send(2, "thread/start", map[string]any{"cwd": directory, "model": "gpt-5.4", "approvalPolicy": "never", "sandbox": "read-only"})
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err = json.Unmarshal(read(2)["result"], &thread); err != nil || thread.Thread.ID == "" {
		t.Fatal("missing thread")
	}
	send(3, "mcpServer/tool/call", map[string]any{"threadId": thread.Thread.ID, "server": "codex_apps", "tool": "read_file", "arguments": map[string]any{"path": "synthetic"}})
	read(3)
	mu.Lock()
	observed := append([]string{}, methods...)
	mu.Unlock()
	if strings.Join(observed, ",") != "initialize,notifications/initialized,tools/list,tools/call" {
		t.Fatalf("actual native methods=%v", observed)
	}
}
