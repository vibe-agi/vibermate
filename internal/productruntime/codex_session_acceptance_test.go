package productruntime

import (
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
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clienttarget"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

type task7Native struct {
	t             *testing.T
	ctx           context.Context
	cmd           *exec.Cmd
	input         io.WriteCloser
	encoder       *json.Encoder
	messages      chan json.RawMessage
	exited        chan error
	nextID        int
	notifications []json.RawMessage
	closeOnce     sync.Once
	root, label   string
	stderr        *os.File
}

func task7StartNative(t *testing.T, ctx context.Context, root, label, binary, directory string, proxy *url.URL, ca string) *task7Native {
	t.Helper()
	cmd := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://", "-c", `cli_auth_credentials_store="file"`, "-c", "analytics.enabled=false", "-c", "feedback.enabled=false", "-c", "check_for_update_on_startup=false", "-c", `model="gpt-5.3-codex"`)
	cmd.Dir = directory
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + directory, "CODEX_HOME=" + directory, "HTTP_PROXY=" + proxy.String(), "HTTPS_PROXY=" + proxy.String(), "ALL_PROXY=" + proxy.String(), "NO_PROXY=localhost,127.0.0.1", "SSL_CERT_FILE=" + ca}
	input, e := cmd.StdinPipe()
	task7Must(t, e)
	output, e := cmd.StdoutPipe()
	task7Must(t, e)
	stderr, e := os.OpenFile(filepath.Join(root, label+"-stderr.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	task7Must(t, e)
	cmd.Stderr = stderr
	n := &task7Native{t: t, ctx: ctx, cmd: cmd, input: input, encoder: json.NewEncoder(input), messages: make(chan json.RawMessage, 256), exited: make(chan error, 1), root: root, label: label, stderr: stderr}
	task7Must(t, cmd.Start())
	t.Logf("native child label=%s original_pid=%d", label, cmd.Process.Pid)
	transcript, e := os.OpenFile(filepath.Join(root, label+"-rpc.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	task7Must(t, e)
	go func() {
		defer close(n.messages)
		defer transcript.Close()
		decoder := json.NewDecoder(io.TeeReader(output, transcript))
		for {
			var message json.RawMessage
			if decoder.Decode(&message) != nil {
				return
			}
			select {
			case n.messages <- message:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { n.exited <- cmd.Wait() }()
	t.Cleanup(n.close)
	n.rpc("initialize", map[string]any{"clientInfo": map[string]string{"name": "vibermate_task7", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}})
	task7Must(t, n.encoder.Encode(map[string]any{"method": "initialized"}))
	return n
}
func (n *task7Native) close() {
	n.closeOnce.Do(func() {
		_ = n.input.Close()
		var e error
		forced := false
		select {
		case e = <-n.exited:
		case <-time.After(10 * time.Second):
			forced = true
			_ = n.cmd.Process.Kill()
			e = <-n.exited
		}
		n.stderr.Close()
		task7Write(n.t, filepath.Join(n.root, n.label+"-exit.json"), map[string]any{"pid": n.cmd.Process.Pid, "reaped": true, "forced": forced, "exit": n.cmd.ProcessState.ExitCode(), "error": fmt.Sprint(e)})
		if forced || e != nil {
			n.t.Errorf("native child %s did not close cleanly: forced=%t error=%v", n.label, forced, e)
		}
	})
}
func (n *task7Native) next() json.RawMessage {
	n.t.Helper()
	select {
	case message, ok := <-n.messages:
		if !ok {
			n.t.Fatal("native stdout ended before expected result")
		}
		return message
	case <-n.ctx.Done():
		n.t.Fatal("native notification deadline: ", n.ctx.Err())
	}
	return nil
}
func (n *task7Native) rpc(method string, params any) json.RawMessage {
	n.t.Helper()
	n.nextID++
	task7Must(n.t, n.encoder.Encode(map[string]any{"id": n.nextID, "method": method, "params": params}))
	for {
		message := n.next()
		var reply struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		task7Must(n.t, json.Unmarshal(message, &reply))
		if reply.ID != n.nextID {
			n.notifications = append(n.notifications, message)
			continue
		}
		if len(reply.Error) > 0 && string(reply.Error) != "null" {
			n.t.Fatalf("native %s error: %s", method, reply.Error)
		}
		return reply.Result
	}
}
func (n *task7Native) turn(id, text string) {
	n.t.Helper()
	result := n.rpc("turn/start", map[string]any{"threadId": id, "input": []any{map[string]any{"type": "text", "text": text}}})
	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	task7Must(n.t, json.Unmarshal(result, &started))
	if started.Turn.ID == "" {
		n.t.Fatal("native returned no turn identity")
	}
	for {
		var message json.RawMessage
		if len(n.notifications) > 0 {
			message = n.notifications[0]
			n.notifications = n.notifications[1:]
		} else {
			message = n.next()
		}
		var event struct {
			Method string `json:"method"`
			Params struct {
				ThreadID string `json:"threadId"`
				Turn     struct {
					ID, Status string
					Error      json.RawMessage
				} `json:"turn"`
			} `json:"params"`
		}
		task7Must(n.t, json.Unmarshal(message, &event))
		if strings.Contains(strings.ToLower(event.Method), "compact") {
			n.t.Fatalf("native compaction: %s", message)
		}
		if event.Method == "turn/completed" && event.Params.ThreadID == id && event.Params.Turn.ID == started.Turn.ID {
			if event.Params.Turn.Status != "completed" {
				n.t.Fatalf("native turn failed: %s", message)
			}
			n.t.Logf("successful turn/completed thread=%s turn=%s", id, started.Turn.ID)
			return
		}
	}
}

func TestInstalledCodexSameSessionContinuation(t *testing.T) {
	root := os.Getenv("TASK7_ROOT")
	if root == "" {
		t.Skip("explicit Task7 isolated acceptance entry point")
	}
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if !filepath.IsAbs(binary) {
		t.Fatal("required selected native Codex binary missing")
	}
	data, e := os.ReadFile(binary)
	task7Must(t, e)
	packageRoot := filepath.Dir(filepath.Dir(binary))
	var manifest struct{ Version, Entrypoint, Target string }
	task7Load(t, filepath.Join(packageRoot, "codex-package.json"), &manifest)
	if manifest.Version != "0.160.0" || manifest.Entrypoint != "bin/codex" || manifest.Target != "aarch64-apple-darwin" {
		t.Fatal("wrong native package identity")
	}
	task7Write(t, filepath.Join(root, "native-package-identity.json"), map[string]any{"binary": binary, "sha256": task7Hash(data), "manifest": manifest})
	directory, e := os.MkdirTemp(root, "native-session-")
	task7Must(t, e)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := task7Open(t, ctx, filepath.Join(directory, "data"))
	f.configure()
	home := filepath.Join(directory, "client")
	task7Must(t, os.Mkdir(home, 0700))
	claims := task7JSON(t, map[string]any{"email": "task7@example.invalid", "sub": "task7", "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace-A", "chatgpt_user_id": "task7", "chatgpt_plan_type": "plus"}})
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic"
	auth := task7JSON(t, map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "tokens": map[string]any{"id_token": token, "access_token": token, "refresh_token": "task7-fake-refresh", "account_id": "workspace-A"}, "last_refresh": time.Now().UTC().Format(time.RFC3339Nano)})
	task7Must(t, os.WriteFile(filepath.Join(home, "auth.json"), auth, 0600))
	ca := filepath.Join(home, "private-ca.pem")
	task7Must(t, os.WriteFile(ca, f.runtime.LocalRootCertificate().CertificatePEM(), 0600))
	var mu sync.Mutex
	var requests [][]byte
	catalogCalls := 0
	proxy := f.bind(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			mu.Lock()
			catalogCalls++
			mu.Unlock()
			http.Error(w, "private fixture disables catalog", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Errorf("unexpected generation path %s", r.URL.Path)
			http.Error(w, "path denied", 503)
			return
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-task7-provider" {
			t.Error("native generation used wrong current credentials")
			http.Error(w, "wrong credential", 401)
			return
		}
		body, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		index := len(requests)
		mu.Unlock()
		task7Must(t, os.WriteFile(filepath.Join(directory, fmt.Sprintf("upstream-%d.json", index)), body, 0600))
		text := fmt.Sprintf("native-reply-%d", index)
		item := map[string]any{"id": fmt.Sprintf("msg_native_%d", index), "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
		response := map[string]any{"id": fmt.Sprintf("resp_native_%d", index), "object": "response", "created_at": 1, "status": "completed", "model": "gpt-5.3-codex", "output": []any{item}, "usage": map[string]any{"input_tokens": 3, "input_tokens_details": map[string]int{"cached_tokens": 0}, "output_tokens": 2, "total_tokens": 5}}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []map[string]any{{"type": "response.output_item.done", "sequence_number": 1, "output_index": 0, "item": item}, {"type": "response.completed", "sequence_number": 2, "response": response}} {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], task7JSON(t, event))
		}
	}))
	grant, e := f.runtime.captureRuns.Create(ctx, capturerun.CreateCommand{CWD: home, CanonicalExecutablePath: binary, ExecutableLabel: "codex", Lifetime: 5 * time.Minute, CatalogRevision: 1})
	task7Must(t, e)
	capture, e := captureidentity.New(captureidentity.KindManagedRun, grant.Run.ID)
	task7Must(t, e)
	profile, e := clienttarget.NewProfile("codex-cli", clienttarget.EnvironmentFacts{})
	task7Must(t, e)
	_, _, e = f.runtime.assignments.CreateForLaunch(ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: "task7-full", Source: captureassignment.SourceLaunch, ClientProfile: profile})
	task7Must(t, e)
	proxy.User = url.UserPassword("capture", grant.ProxyCapability.Value())
	first := task7StartNative(t, ctx, directory, "first", binary, home, proxy, ca)
	result := first.rpc("thread/start", map[string]any{"cwd": home, "model": "gpt-5.3-codex", "approvalPolicy": "never", "sandbox": "read-only", "ephemeral": false})
	var thread struct {
		Thread struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"thread"`
	}
	task7Must(t, json.Unmarshal(result, &thread))
	id := thread.Thread.ID
	if id == "" {
		t.Fatal("native created no thread")
	}
	first.turn(id, "native-short-initial")
	first.close()
	var rollout string
	task7Must(t, filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() && strings.HasSuffix(path, id+".jsonl") {
			if rollout != "" {
				return fmt.Errorf("multiple rollouts for %s", id)
			}
			rollout = path
		}
		return nil
	}))
	if rollout == "" {
		t.Fatal("same native rollout missing")
	}
	prefix, e := os.ReadFile(rollout)
	task7Must(t, e)
	if !bytes.Contains(prefix, []byte(id)) {
		t.Fatal("rollout metadata lacks original identity")
	}
	var seeded bytes.Buffer
	seeded.Write(prefix)
	encoder := json.NewEncoder(&seeded)
	lastOrdinal := -1
	prefixDecoder := json.NewDecoder(bytes.NewReader(prefix))
	for {
		var line struct {
			Ordinal *int `json:"ordinal"`
		}
		e := prefixDecoder.Decode(&line)
		if e == io.EOF {
			break
		}
		task7Must(t, e)
		if line.Ordinal == nil || *line.Ordinal != lastOrdinal+1 {
			t.Fatal("native rollout ordinal sequence is unsupported")
		}
		lastOrdinal = *line.Ordinal
	}
	const count = 4110
	for i := 0; i < count; i++ {
		role, kind := "user", "input_text"
		if i%2 == 1 {
			role, kind = "assistant", "output_text"
		}
		task7Must(t, encoder.Encode(map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "ordinal": lastOrdinal + i + 1, "type": "response_item", "payload": map[string]any{"type": "message", "role": role, "content": []any{map[string]string{"type": kind, "text": fmt.Sprintf("task7-history-%08d", i)}}}}))
	}
	task7Must(t, os.WriteFile(rollout, seeded.Bytes(), 0600))
	second := task7StartNative(t, ctx, directory, "second", binary, home, proxy, ca)
	resumed := second.rpc("thread/resume", map[string]any{"threadId": id, "cwd": home, "model": "gpt-5.3-codex", "approvalPolicy": "never", "sandbox": "read-only"})
	var resumedThread struct {
		Thread struct{ ID, Path string } `json:"thread"`
	}
	task7Must(t, json.Unmarshal(resumed, &resumedThread))
	if resumedThread.Thread.ID != id || resumedThread.Thread.Path != rollout {
		t.Fatalf("native replaced session: id=%s path=%s", resumedThread.Thread.ID, resumedThread.Thread.Path)
	}
	const sentinel = "task7-native-final-sentinel"
	second.turn(id, sentinel)
	second.close()
	task7Must(t, f.pipeline.Drain(ctx))
	mu.Lock()
	actual := append([][]byte(nil), requests...)
	catalog := catalogCalls
	mu.Unlock()
	if len(actual) != 2 {
		t.Fatalf("generation POST count=%d want initial1+continuation1", len(actual))
	}
	var initial, continued struct {
		Input []json.RawMessage `json:"input"`
	}
	task7Must(t, json.Unmarshal(actual[0], &initial))
	task7Must(t, json.Unmarshal(actual[1], &continued))
	if len(continued.Input) <= 4096 || len(continued.Input) < len(initial.Input)+count+1 {
		t.Fatalf("truncated native history count=%d", len(continued.Input))
	}
	for i := range initial.Input {
		if !reflect.DeepEqual(task7NormalizeJSON(t, initial.Input[i]), task7NormalizeJSON(t, continued.Input[i])) {
			t.Fatalf("native changed original history prefix item %d", i)
		}
	}
	next := 0
	for _, item := range continued.Input {
		var message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(item, &message) != nil {
			continue
		}
		for _, part := range message.Content {
			if strings.HasPrefix(part.Text, "task7-history-") {
				if part.Text != fmt.Sprintf("task7-history-%08d", next) {
					t.Fatalf("native history order at %d", next)
				}
				next++
			}
		}
	}
	if next != count || !bytes.Contains(continued.Input[len(continued.Input)-1], []byte(sentinel)) {
		t.Fatalf("native complete history=%d final=%s", next, continued.Input[len(continued.Input)-1])
	}
	after, e := os.ReadFile(rollout)
	task7Must(t, e)
	if !bytes.HasPrefix(after, seeded.Bytes()) || bytes.Contains(after, []byte(`"type":"compacted"`)) {
		t.Fatal("native rollout prefix changed or compacted")
	}
	saved, e := os.ReadFile(filepath.Join(home, "auth.json"))
	task7Must(t, e)
	if !bytes.Equal(saved, auth) {
		t.Fatal("native auth file changed")
	}
	ids := task7IDs(t, filepath.Join(directory, "data"))
	if len(ids) != 2 {
		t.Fatalf("native Store exchanges=%d", len(ids))
	}
	var complete exchangecontent.Record
	for _, exchangeID := range ids {
		record, e := f.runtime.ExchangeContents().Get(ctx, exchangeID)
		task7Must(t, e)
		if record.Response != nil && record.Response.ID == "resp_native_2" {
			complete = record
		}
	}
	if complete.ExchangeID == "" || complete.Parent.CaptureRunID != grant.Run.ID || complete.Mode != environment.ContentRecordingFull || len(complete.Request.Messages) != len(continued.Input) {
		t.Fatal("native real Store missing identity/full history")
	}
	for i, m := range complete.Request.Messages {
		var item struct {
			Role    string
			Content []struct{ Text string }
		}
		task7Must(t, json.Unmarshal(continued.Input[i], &item))
		if m.Role != item.Role || len(m.Blocks) != len(item.Content) {
			t.Fatalf("Store native shape %d", i)
		}
		for j, b := range m.Blocks {
			if b.Text != item.Content[j].Text {
				t.Fatalf("Store native content %d/%d", i, j)
			}
		}
	}
	if len(complete.Response.Blocks) != 1 || complete.Response.Blocks[0].Text != "native-reply-2" || complete.Response.Usage != (exchangecontent.Usage{InputUncached: exchangecontent.UsageValue{Known: true, Tokens: 3, Source: "openai-responses"}, CacheRead: exchangecontent.UsageValue{Known: true, Tokens: 0, Source: "openai-responses"}, Output: exchangecontent.UsageValue{Known: true, Tokens: 2, Source: "openai-responses"}}) {
		t.Fatal("native response incomplete")
	}
	task7CheckPages(t, ctx, f.runtime.ExchangeContents(), complete)
	if f.diagnostics.Load() != 0 {
		t.Fatal("native recording diagnostics")
	}
	f.close()
	reopened := task7Open(t, ctx, filepath.Join(directory, "data"))
	stored, e := reopened.runtime.ExchangeContents().Get(ctx, complete.ExchangeID)
	task7Must(t, e)
	if !reflect.DeepEqual(stored, complete) {
		t.Fatal("native reopen changed full evidence")
	}
	task7CheckPages(t, ctx, reopened.runtime.ExchangeContents(), stored)
	reopened.close()
	task7CheckGraph(t, filepath.Join(directory, "data"), false)
	task7Write(t, filepath.Join(directory, "native-success.json"), map[string]any{"threadId": id, "rollout": rollout, "prefix_sha256": task7Hash(prefix), "seeded_sha256": task7Hash(seeded.Bytes()), "post_sha256": task7Hash(after), "seeded_items": count, "upstream_items": len(continued.Input), "continuation_posts": 1, "upstream_sha256": task7Hash(actual[1]), "catalog_calls": catalog, "exchange_id": complete.ExchangeID, "store_messages": len(complete.Request.Messages), "response_sha256": task7Hash([]byte(complete.Response.Blocks[0].Text)), "diagnostics": 0, "auth_unchanged": true})
}
func task7NormalizeJSON(t *testing.T, data []byte) any {
	var v any
	task7Must(t, json.Unmarshal(data, &v))
	return v
}
