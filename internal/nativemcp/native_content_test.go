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
				op, err := f.store.NativeToolRepository().Get(context.Background(), c.id)
				if err != nil || op.Result == nil || op.Arguments == nil {
					t.Fatalf("deferred descriptor %+v %v", op, err)
				}
				read := 0
				for {
					page, err := f.store.NativeToolRepository().ReadContent(context.Background(), f.capture, c.id, "tool_result", read, 128<<10)
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
