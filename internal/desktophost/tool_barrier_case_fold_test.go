//go:build !vibermate_native_secrets

package desktophost_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
)

// A client (JavaScript JSON.parse) reads only exact keys, while encoding/json
// matches struct fields case-insensitively and lets a later key win. Upstream
// JSON whose names differ only by case would let the proxy approve one block
// while the client executes another, so it must never reach the client.
func TestManagedStrictPolicyRejectsCaseFoldedProviderKeys(t *testing.T) {
	event := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
	streamBody := event("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":1}}}`) +
		event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_hidden","name":"bash","input":{},"TYPE":"server_tool_use"}}`) +
		event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"synthetic-only\"}"}}`) +
		event("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","STOP_REASON":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`) +
		event("message_stop", `{"type":"message_stop"}`)
	jsonBody := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"toolu_hidden","name":"bash","input":{"command":"synthetic-only"},"TYPE":"server_tool_use"}],"stop_reason":"tool_use","STOP_REASON":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":2}}`
	tools := map[string]string{
		"function": `{"name":"bash","description":"d","input_schema":{"type":"object"}}`,
		"native":   `{"type":"bash_20250124","name":"bash"}`,
	}
	for toolName, tool := range tools {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", toolName, stream), func(t *testing.T) {
				request := fmt.Sprintf(`{"model":"claude-test","max_tokens":32,"stream":%t,"messages":[{"role":"user","content":"Run"}],"tools":[%s]}`, stream, tool)
				response, mediaType, status := jsonBody, "application/json", http.StatusBadGateway
				if stream {
					// message_start is released before the ambiguous block arrives.
					response, mediaType, status = streamBody, "text/event-stream", http.StatusOK
				}
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", mediaType)
					_, _ = io.WriteString(w, response)
				}))
				defer provider.Close()
				root := t.TempDir()
				paths := newHostPaths(t, filepath.Join(root, "cache"))
				factory, err := hostsecret.NewDevelopmentFileFactory(filepath.Join(root, "private", "secrets.json"))
				if err != nil {
					t.Fatal(err)
				}
				host, _ := startManagedHost(t, paths, filepath.Join(root, "data"), factory, fixedSelfTestCatalog(t))
				defer shutdownHost(t, host)
				endpointID := createManagedEndpoint(t, host.Runtime().UpstreamEndpoints(), provider.URL)
				accountID := createManagedAccount(t, host.Runtime().ProviderAccounts(), endpointID)
				environmentID := publishManagedEnvironment(t, host, provider.URL, endpointID, accountID, func(value *environment.Environment) {
					value.PolicySet = &environment.PolicySet{ToolMode: environment.ToolPolicyStrict}
				})
				runManagedChild(t, paths, environmentID, false,
					childManagedBody+"="+request,
					childManagedForbidden+"=toolu_hidden",
					childManagedStatus+"="+strconv.Itoa(status))
				page, err := host.Runtime().Activities().ListExchanges(context.Background(), activity.PageRequest{Limit: 1, EnvironmentID: environmentID.String()})
				if err != nil || len(page.Items) != 1 {
					t.Fatalf("Activity page = %+v, %v", page, err)
				}
				if page.Items[0].ReasonCode == "" {
					t.Fatalf("ambiguous provider data was recorded as a success: %+v", page.Items[0])
				}
			})
		}
	}
}
