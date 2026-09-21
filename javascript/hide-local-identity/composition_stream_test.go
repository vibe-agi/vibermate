package hideidentity_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

// Exercise both shipped scripts together, sharing a compiled Pipeline but never
// Turn Context. Everything here uses synthetic identities and in-memory events.
func TestIdentityMetadataCompositionStreamingIsolation(t *testing.T) {
	metadataSource, err := os.ReadFile("../hide-client-metadata/request.js")
	if err != nil {
		t.Fatal(err)
	}
	identityPolicy := messagetransform.Policy{RequestJavaScript: requestSource, ResponseJavaScript: responseSource}
	metadataPolicy := messagetransform.Policy{RequestJavaScript: string(metadataSource)}
	for _, order := range []struct {
		name     string
		policies []messagetransform.Policy
	}{
		{"metadata-first", []messagetransform.Policy{metadataPolicy, identityPolicy}},
		{"identity-first", []messagetransform.Policy{identityPolicy, metadataPolicy}},
	} {
		pipeline, err := messagetransform.CompilePipeline(order.policies, messagetransform.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		for _, protocol := range srProtocols {
			for _, windows := range []bool{false, true} {
				for _, user := range []string{"alice", "li", "root"} {
					for session := range 2 {
						t.Run(fmt.Sprintf("%s/%s/windows_%v/%s/session_%d", order.name, protocol, windows, user, session), func(t *testing.T) {
							t.Parallel()
							meta := metadata(user)
							meta.WorkspaceRoot += fmt.Sprintf("-%d", session)
							workspace, home := workspaceAlias, homeAlias
							if windows {
								meta.HomeDirectory = `C:\Users\` + user
								meta.WorkspaceRoot = meta.HomeDirectory + fmt.Sprintf(`\work\project-%d`, session)
								meta.OperatingSystem = "windows"
								workspace, home = `C:\__vmi1_workspace__`, `C:\__vmi1_home__`
							}
							newReadyTurn := func() *messagetransform.PipelineTurn {
								t.Helper()
								turn, err := pipeline.NewTurnWithMetadata(meta)
								if err != nil {
									t.Fatal(err)
								}
								text := "username: " + user + "; " + meta.WorkspaceRoot + "; valid � 🙂"
								body := map[string]any{"model": "fixture", "stream": true}
								path := "/v1/responses"
								if protocol == "responses" {
									body["input"] = text
								} else {
									path = "/v1/chat/completions"
									if protocol == "anthropic" {
										path = "/v1/messages"
										body["max_tokens"] = 256
									}
									body["messages"] = []any{map[string]any{"role": "user", "content": text}}
								}
								request := messagetransform.RequestMessage{
									Method: http.MethodPost, Path: path, Body: marshal(t, body),
									Headers: http.Header{"Version": {"9.8.7"}, "X-Install-Id": {"synthetic-install"},
										"User-Agent": {"fixture-client/9.8.7"}, "Session-Id": {fmt.Sprintf("session-%d", session)}, "Chatgpt-Account-Id": {"account-" + user}},
								}
								masked, err := turn.ApplyRequest(context.Background(), request)
								if err != nil {
									t.Fatal(err)
								}
								if !strings.Contains(string(masked.Body), "__vmi1_workspace__") || !strings.Contains(string(masked.Body), userAlias) ||
									masked.Headers.Get("User-Agent") != "vibermate-client/0.0.0" || masked.Headers.Get("Version") != "0.0.0" ||
									masked.Headers.Get("X-Install-Id") != "00000000-0000-4000-8000-000000000001" ||
									masked.Headers.Get("Session-Id") != request.Headers.Get("Session-Id") || masked.Headers.Get("Chatgpt-Account-Id") != "account-"+user {
									t.Fatalf("combined request transformation failed: %+v", masked)
								}
								if _, err := messagetransform.RequestUserAgentOverride(request.Headers, masked.Headers); err != nil {
									t.Fatal(err)
								}
								retry, err := turn.ApplyRequest(context.Background(), request)
								if err != nil || !reflect.DeepEqual(masked, retry) {
									t.Fatalf("combined retry changed result: %v", err)
								}
								return turn
							}
							turn := newReadyTurn()
							text := workspace + "/file.txt " + userAlias + " valid � 🙂."
							tool := string(marshal(t, map[string]any{"cwd": home, "username": userAlias, "command": "read " + workspace + "/a\"b.txt", "options": []any{7, true, nil, "� 🙂"}}))
							seed := int64(session * 42) // single-rune cuts and random multi-rune cuts
							textParts, toolParts := srFragments(text, seed), srFragments(tool, seed)
							var gotText, gotTool strings.Builder
							for index := 0; index < max(len(textParts), len(toolParts)); index++ {
								if index < len(textParts) {
									gotText.WriteString(deltaText(output(t, turn, delta(protocol, textParts[index], false, 0), true), protocol, false))
								}
								if index < len(toolParts) {
									gotTool.WriteString(deltaText(output(t, turn, delta(protocol, toolParts[index], true, 1), true), protocol, true))
								}
							}
							output(t, turn, terminal(protocol), true)
							if gotText.String() != meta.WorkspaceRoot+"/file.txt "+user+" valid � 🙂." {
								t.Fatalf("text mixed sessions or lost Unicode: %q", gotText.String())
							}
							wantTool := map[string]any{"cwd": meta.HomeDirectory, "username": user, "command": "read " + meta.WorkspaceRoot + "/a\"b.txt", "options": []any{float64(7), true, nil, "� 🙂"}}
							if actual := decode(t, []byte(gotTool.String())); !reflect.DeepEqual(actual, wantTool) {
								t.Fatalf("tool arguments mixed sessions or changed values: %#v", actual)
							}
							// Composition must retain fail-closed stream termination.
							broken := newReadyTurn()
							output(t, broken, delta(protocol, `{"cwd":"unfinished`, true, 1), true)
							srAssertError(t, broken, terminal(protocol))
						})
					}
				}
			}
		}
	}
}
