package loopbackproxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func nativeHandshake(t *testing.T, f *nativeFixture) net.Conn {
	t.Helper()
	secured := f.proxy.ConnectTLS(t, f.credential, "native.example:443", "native.example")
	t.Cleanup(func() { _ = secured.Close() })
	for i, body := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"codex-mcp-client","version":"0.160.0"}}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`} {
		session := "synthetic-session"
		if i == 0 {
			session = ""
		}
		response := nativeRequest(t, secured, body, session)
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 200 && response.StatusCode != 202 {
			t.Fatalf("handshake%d status%d body%s", i, response.StatusCode, data)
		}
	}
	return secured
}
func (f *nativeFixture) waitingCall(t *testing.T) nativetool.Operation {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		page, err := f.store.NativeToolRepository().Page(context.Background(), nativetool.PageRequest{Capture: f.capture, Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range page.Items {
			if op.Kind == nativetool.RequestToolsCall && op.ApprovalID != nil {
				return op
			}
		}
		time.Sleep(time.Millisecond * 5)
	}
	t.Fatal("actual Review question missing")
	return nativetool.Operation{}
}
func TestNativeHandlerReviewRejectExpiryAndUncertainty(t *testing.T) {
	for _, id := range []string{`3`, `"3"`} {
		for _, scenario := range []string{"reject", "expire", "uncertain"} {
			t.Run(scenario+id, func(t *testing.T) {
				timeout := 3 * time.Second
				if scenario == "expire" {
					timeout = 100 * time.Millisecond
				}
				f := newNativeFixtureMode(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, &nativeHandlerSettings{decisionTimeout: timeout})
				secured := nativeHandshake(t, f)
				if scenario == "uncertain" {
					f.transport.mu.Lock()
					f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
						return nil, errors.New("synthetic unknown effect")
					}
					f.transport.mu.Unlock()
				}
				done := make(chan *http.Response, 1)
				go func() {
					done <- nativeRequest(t, secured, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"callId":"synthetic-review-terminal"}}}`, id), "synthetic-session")
				}()
				call := f.waitingCall(t)
				if scenario != "expire" {
					approval, err := f.store.NativeToolRepository().GetApproval(context.Background(), *call.ApprovalID)
					if err != nil {
						t.Fatal(err)
					}
					decision := nativetool.DecisionDeny
					if scenario == "uncertain" {
						decision = nativetool.DecisionAllowOnce
					}
					if _, err = f.store.NativeToolRepository().DecideApproval(context.Background(), nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: decision, IdempotencyKey: "synthetic-" + scenario, ObservedAt: time.Now().UTC()}); err != nil {
						t.Fatal(err)
					}
				}
				response := <-done
				data, _ := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if scenario == "uncertain" {
					if response.StatusCode != 409 || !strings.Contains(string(data), "execution_uncertain") || f.transport.sends() != 4 {
						t.Fatalf("uncertainty status%d body%s sends%d", response.StatusCode, data, f.transport.sends())
					}
					return
				}
				var rpc struct {
					Version string          `json:"jsonrpc"`
					ID      json.RawMessage `json:"id"`
					Error   struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(data, &rpc); err != nil {
					t.Fatal(err)
				}
				reason := "approval_rejected"
				if scenario == "expire" {
					reason = "approval_expired"
				}
				if response.StatusCode != 200 || rpc.Version != "2.0" || string(rpc.ID) != id || rpc.Error.Code != -32003 || rpc.Error.Message != reason || f.transport.sends() != 3 {
					t.Fatalf("review terminal status%d body%s sends%d", response.StatusCode, data, f.transport.sends())
				}
			})
		}
	}
}
