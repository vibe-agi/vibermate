//go:build !vibermate_native_secrets

package desktophost_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
)

// An upstream content block the proxy does not model can still be work the
// client executes. Strict policy stops it; Observe releases it unchanged.
func TestManagedRouteAppliesToolPolicyToUnmodelledBlocks(t *testing.T) {
	event := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
	streamBody := event("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":1}}}`) +
		event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"browser_action_use","id":"bau_hidden","name":"browser","input":{}}}`) +
		event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"future_delta","value":"navigate"}}`) +
		event("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`) +
		event("message_stop", `{"type":"message_stop"}`)
	jsonBody := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"browser_action_use","id":"bau_hidden","name":"browser","input":{"url":"https://example.com"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":2}}`
	for _, mode := range []environment.ToolPolicyMode{environment.ToolPolicyStrict, environment.ToolPolicyObserve} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", mode, stream), func(t *testing.T) {
				request := fmt.Sprintf(`{"model":"claude-test","max_tokens":32,"stream":%t,"messages":[{"role":"user","content":"browse"}]}`, stream)
				response, mediaType := jsonBody, "application/json"
				if stream {
					response, mediaType = streamBody, "text/event-stream"
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
					value.PolicySet = &environment.PolicySet{ToolMode: mode}
				})
				if mode == environment.ToolPolicyObserve {
					runManagedChild(t, paths, environmentID, false,
						childManagedBody+"="+request, childManagedResponse+"="+response, childManagedStatus+"=200")
					return
				}
				status := http.StatusBadGateway
				if stream {
					status = http.StatusOK
				}
				runManagedChild(t, paths, environmentID, false,
					childManagedBody+"="+request, childManagedForbidden+"=bau_hidden", childManagedStatus+"="+strconv.Itoa(status))
			})
		}
	}
}
