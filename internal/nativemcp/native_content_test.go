package nativemcp

import (
	"context"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/recording"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// Only controlled synthetic schema evidence is safe in this test registry.
// No production constructor or blanket profile is exported.
type syntheticProfiles struct{ argumentPaths, resultPaths []string }

func (profiles syntheticProfiles) Resolve(contract environment.ClientToolContract, d nativetool.CatalogDefinition) (ContentProfile, bool) {
	definition := d.Definition()
	if contract != environment.ClientToolContractCodexLegacy || definition.Name != "read_file" || definition.Description != "Synthetic public tool" || string(definition.InputSchema.Bytes()) != `{"properties":{"path":{"type":"string"}},"type":"object"}` {
		return ContentProfile{}, false
	}
	return ContentProfile{contract: contract, definition: d.Digest(), arguments: profiles.argumentPaths, result: profiles.resultPaths}, true
}
func TestNativeLargeResultTwoMiB(t *testing.T) {
	for _, transport := range []string{"json", "sse"} {
		for _, size := range []int{2 << 20, (2 << 20) + 4096} {
			t.Run(transport+"/"+fmt.Sprint(size), func(t *testing.T) {
				f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1})
				f.executor.(*executor).dependencies.ContentProfiles = syntheticProfiles{}
				f.handshake(t)
				base := `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":""}]}}`
				result := strings.Replace(base, `"text":""`, `"text":"`+strings.Repeat("x", size-len(base))+`"`, 1)
				f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
					media, wire := "application/json", result
					if transport == "sse" {
						media = "text/event-stream"
						wire = "event: message\nid: large-result\ndata: " + result + "\n\n"
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{media}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
				}
				c := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
				results := make(chan Terminal, 1)
				go func() { results <- f.executor.Serve(context.Background(), c, httptest.NewRecorder()) }()
				f.approve(t, c.id)
				terminal := <-results
				if terminal.Outcome != OutcomeCompleted {
					t.Fatalf("large result %+v", terminal)
				}
				for _, part := range []string{"tool_arguments", "tool_result"} {
					if _, err := f.readNativeContent(context.Background(), f.capture, c.id, part, 0, 128<<10); err != nil {
						t.Fatalf("retained %s missing: %v", part, err)
					}
				}
				read := 0
				for {
					page, err := f.readNativeContent(context.Background(), f.capture, c.id, "tool_result", read, 128<<10)
					if err != nil {
						t.Fatal(err)
					}
					if len(page) > 128<<10 {
						t.Fatal("deferred page exceeds bound")
					}
					read += len(page)
					if len(page) < 128<<10 {
						break
					}
				}
				if read < 2<<20-100 {
					t.Fatalf("result truncated: %d", read)
				}
			})
		}
	}
}
func TestNativeKnownSensitivePayloadSuppression(t *testing.T) {
	for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingOff, environment.ContentRecordingMetadataOnly, environment.ContentRecordingFull} {
		t.Run(string(mode), func(t *testing.T) {
			recording := environment.ContentRecordingPolicy{Mode: mode}
			if mode != environment.ContentRecordingOff {
				recording.RetentionDays = 1
			}
			f := newNativeFixture(t, recording)
			f.executor.(*executor).dependencies.ContentProfiles = syntheticProfiles{argumentPaths: []string{"/proof"}, resultPaths: []string{"/credentialId"}}
			f.handshake(t)
			f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":3,"result":{"content":[],"credentialId":"synthetic-retention-private"}}`))}, nil
			}
			exactRPC := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a","proof":"synthetic-retention-private"},"_meta":{"private":"synthetic-meta-private"}},"_meta":{"private":"synthetic-envelope-private"}}`
			c := f.captured(t, "POST", exactRPC, "synthetic-session")
			results := make(chan Terminal, 1)
			go func() { results <- f.executor.Serve(context.Background(), c, httptest.NewRecorder()) }()
			f.approve(t, c.id)
			terminal := <-results
			if terminal.Outcome != OutcomeCompleted {
				t.Fatalf("suppressed operation %+v", terminal)
			}
			op, err := f.store.NativeToolRepository().Get(context.Background(), c.id)
			if err != nil || op.Arguments != nil || op.Result != nil {
				t.Fatalf("sensitive body descriptor %+v %v", op, err)
			}
			f.requireNoContent(t, c.id, nativetool.ContentToolArguments, nativetool.ContentToolResult)
			params := `{"_meta":{"private":"synthetic-meta-private"},"arguments":{"path":"a","proof":"synthetic-retention-private"},"name":"read_file"}`
			group := fmt.Sprintf(`[{"Response":%q,"Ordinal":0,"Kind":"function","Source":"native","WireID":%q,"ItemSource":"","ItemID":"","Namespace":"","Name":"read_file","Arguments":{"path":"a","proof":"synthetic-retention-private"},"Input":""}]`, c.id.String(), c.id.String())
			assertNoSensitiveRows(t, f, []string{"synthetic-retention-private", "synthetic-meta-private", "synthetic-envelope-private", "synthetic-original-login", exactRPC, params, group})
			e := f.executor.(*executor)
			e.mu.Lock()
			for _, session := range e.scopes[f.capture.Key()].sessions {
				session.mu.Lock()
				if session.tools[c.id] != nil {
					t.Fatal("terminal sensitive tool witness retained")
				}
				session.mu.Unlock()
			}
			e.mu.Unlock()
		})
	}
}

func TestNativeSensitiveBindingAndPreviewSurviveRecordingLoss(t *testing.T) {
	for _, mode := range []string{"unavailable", "saturated"} {
		t.Run(mode, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1})
			e := f.executor.(*executor)
			e.dependencies.ContentProfiles = syntheticProfiles{argumentPaths: []string{"/proof"}}
			f.handshake(t)
			if mode == "unavailable" {
				e.dependencies.ContentRecorder = nil
			} else {
				reservation, ok := f.content.budget.TryReserve(recording.Native, 64<<20)
				if !ok {
					t.Fatal("cannot saturate")
				}
				defer reservation.Release()
			}
			f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
				return wireResponse(`{"jsonrpc":"2.0","id":3,"result":{"content":[]}}`, false), nil
			}
			call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a","proof":"required-sensitive-proof"}}}`, "synthetic-session")
			done := make(chan Terminal, 1)
			go func() { done <- e.Serve(context.Background(), call, httptest.NewRecorder()) }()
			approval := waitNativeQuestion(t, f, call.id)
			e.mu.Lock()
			witnessFound := false
			for _, session := range e.scopes[f.capture.Key()].sessions {
				session.mu.Lock()
				witnessFound = witnessFound || session.tools[call.id] != nil
				session.mu.Unlock()
			}
			e.mu.Unlock()
			if !witnessFound {
				t.Error("recording loss skipped required sensitive binding")
			}
			if _, err := e.PendingPreview(context.Background(), f.capture, call.id, approval.BindingDigest); err == nil {
				t.Error("recording loss exposed sensitive preview")
			}
			f.approve(t, call.id)
			if terminal := <-done; terminal.Outcome != OutcomeCompleted {
				t.Fatal(terminal)
			}
			f.requireNoContent(t, call.id, nativetool.ContentToolArguments, nativetool.ContentToolResult)
			assertNoSensitiveRows(t, f, []string{"required-sensitive-proof"})
		})
	}
}
