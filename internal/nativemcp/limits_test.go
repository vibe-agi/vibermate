package nativemcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestNativeSSEEventExactBoundaryAndBufferPressure(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			f.handshake(t)
			base := "data: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"\"}]}}\n\n"
			event := strings.Replace(base, `"text":""`, `"text":"`+strings.Repeat("x", (16<<20)-len(base)+extra)+`"`, 1)
			f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(event))}, nil
			}
			c := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
			results := make(chan Terminal, 1)
			go func() { results <- f.executor.Serve(context.Background(), c, httptest.NewRecorder()) }()
			f.approve(t, c.id)
			terminal := <-results
			if (terminal.Outcome == OutcomeCompleted) != (extra == 0) {
				t.Fatalf("event16MiB+%d %+v", extra, terminal)
			}
		})
	}
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	e.budget.mu.Lock()
	remaining := e.budget.maximum - e.budget.used
	e.budget.mu.Unlock()
	if !e.budget.acquire(remaining) {
		t.Fatal("exact shared maximum rejected")
	}
	before := f.transport.sends()
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":77,"method":"ping"}`, "synthetic-session")
	if terminal.Reason != ReasonBudgetExceeded || f.transport.sends() != before {
		t.Fatalf("overflow admitted %+v", terminal)
	}
	e.budget.release(remaining)
}

func TestNativeSixtyFourSessionsRemainIndependentAndSixtyFiveRejected(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		var id string
		start := strings.Index(string(body), `"id":`) + 5
		end := strings.Index(string(body)[start:], ",") + start
		id = string(body)[start:end]
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}, "Mcp-Session-Id": []string{"synthetic-session-" + id}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":` + id + `,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"synthetic","version":"1"}}}`))}, nil
	}
	for index := 0; index < 65; index++ {
		before := f.transport.sends()
		terminal, _ := f.serve(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, 100+index), "")
		if index < 64 {
			if terminal.Outcome != OutcomeCompleted || f.transport.sends() != before+1 {
				t.Fatalf("session%d %+v", index+1, terminal)
			}
		} else if terminal.Reason != ReasonBudgetExceeded || f.transport.sends() != before {
			t.Fatalf("session65 %+v", terminal)
		}
	}
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_sessions WHERE state='initializing'`).Scan(&count); err != nil || count != 64 {
		t.Fatalf("independent durable sessions%d %v", count, err)
	}
}

func TestNativePendingModuleBudgetReleasesAtExactBoundary(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	e := f.executor.(*executor)
	var releases []func()
	for index := 0; index < 1024; index++ {
		release, err := e.beginWork()
		if err != nil {
			t.Fatalf("pending%d %v", index+1, err)
		}
		releases = append(releases, release)
	}
	if _, err := e.beginWork(); err != errBudget {
		t.Fatalf("pending1025 %v", err)
	}
	releases[0]()
	release, err := e.beginWork()
	if err != nil {
		t.Fatal("released pending slot unusable")
	}
	release()
	for _, release := range releases {
		release()
	}
}
