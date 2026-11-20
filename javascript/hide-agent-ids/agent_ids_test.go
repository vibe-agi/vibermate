package agentids_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

//go:embed request.js
var requestSource string

const defaultKey = "replace this with your own key text"

// Shapes copied from Claude Code 2.1.329 and Codex 0.171.0 requests, with
// synthetic values.
const (
	claudePrompt  = "2491cf85-d7d6-4e5d-81c6-41a46d940c02"
	claudeDevice  = "e0d7088cb33b8d2ca86155708c3a756349c724a3d1117efac990a9b3edab2bd4"
	claudeAccount = "6d1f3c2a-9a4e-4c11-8f0e-2b7d5a9e1c33"
	claudeSession = "32cef294-68ee-4c58-855d-1d74185f12b7"
	codexInstall  = "2f56266b-f376-4497-b53c-b1abea9e47ca"
	codexSession  = "01a1f977-87e9-7d02-b55f-3638e71b9fa4"
	codexTurn     = "01a1f977-a787-74b1-9a7e-3519450a0649"
)

func compile(t *testing.T, source string) messagetransform.Pipeline {
	t.Helper()
	pipeline, err := messagetransform.CompilePipeline([]messagetransform.Policy{{RequestJavaScript: source}}, messagetransform.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return pipeline
}

func apply(t *testing.T, source string, input messagetransform.RequestMessage) messagetransform.RequestMessage {
	t.Helper()
	output, err := compile(t, source).NewTurn().ApplyRequest(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

// standIn is the script's derivation, written independently in Go.
func standIn(key, kind, original string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(kind + "\n" + strings.ToLower(original)))
	digest := mac.Sum(nil)
	if kind == "claude.device" {
		return hex.EncodeToString(digest)[:len(original)]
	}
	id := digest[:16]
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	text := hex.EncodeToString(id)
	return text[:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]
}

func claudeBody(t *testing.T) string {
	t.Helper()
	userID, err := json.Marshal(map[string]string{"device_id": claudeDevice, "account_uuid": claudeAccount, "session_id": claudeSession})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"model": "claude-sonnet-5-5",
		"system": []map[string]any{
			{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.329.034; cc_entrypoint=cli; cch=bb47f; cc_prompt_id=" + claudePrompt + "; cc_turn_origin=sdk; cc_prompt_index=0; cc_turn_index=1;"},
			{"type": "text", "text": "Your scratchpad is /tmp/claude-1000/project/" + claudeSession + "/scratchpad.", "cache_control": map[string]string{"type": "ephemeral"}},
		},
		"messages": []map[string]any{
			{"role": "user", "content": "Write notes to /tmp/claude-1000/project/" + claudeSession + "/scratchpad/notes.md"},
		},
		"metadata":   map[string]string{"user_id": string(userID)},
		"max_tokens": 9007199254740993,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestClaudeIdentifiersBecomeStableStandIns(t *testing.T) {
	body := claudeBody(t)
	headers := http.Header{"X-Claude-Code-Session-Id": {claudeSession}, "User-Agent": {"claude-cli/2.1.329 (external, cli)"}}
	input := messagetransform.RequestMessage{Method: "POST", Path: "/v1/messages", Headers: headers, Body: []byte(body)}
	first := apply(t, requestSource, input)
	second := apply(t, requestSource, input)
	if string(first.Body) != string(second.Body) || !reflect.DeepEqual(first.Headers, second.Headers) {
		t.Fatal("the same request did not get the same stand-ins")
	}
	prompt, device := standIn(defaultKey, "claude.prompt", claudePrompt), standIn(defaultKey, "claude.device", claudeDevice)
	want := strings.NewReplacer(claudePrompt, prompt, claudeDevice, device).Replace(body)
	if string(first.Body) != want {
		t.Fatalf("body changed beyond the two identifiers:\n got %s\nwant %s", first.Body, want)
	}
	// The session ID stays: the model uses it in paths, and the provider keys
	// caching on it. The account, cch and every other byte stay too.
	for _, kept := range []string{claudeSession, claudeAccount, "cch=bb47f", "9007199254740993"} {
		if !strings.Contains(string(first.Body), kept) {
			t.Errorf("%s was changed", kept)
		}
	}
	if first.Headers.Get("X-Claude-Code-Session-Id") != claudeSession {
		t.Error("the session header was changed")
	}
}

func TestCodexInstallationIdIsReplacedInEachFieldCodexSendsIt(t *testing.T) {
	turnMetadata, _ := json.Marshal(map[string]any{"installation_id": codexInstall, "session_id": codexSession, "thread_id": codexSession, "turn_id": codexTurn})
	body, _ := json.Marshal(map[string]any{
		"model":            "gpt-5.5",
		"prompt_cache_key": codexSession,
		"input":            []map[string]any{{"type": "message", "id": "msg_" + codexTurn, "role": "user", "content": []map[string]string{{"type": "input_text", "text": "hello"}}}},
		"client_metadata": map[string]string{
			"session_id": codexSession, "turn_id": codexTurn,
			"x-codex-installation-id": codexInstall, "x-codex-turn-metadata": string(turnMetadata),
		},
	})
	headers := http.Header{
		"Session-Id": {codexSession}, "X-Codex-Turn-Metadata": {string(turnMetadata)},
		"Authorization": {"Bearer synthetic-" + codexInstall},
	}
	output := apply(t, requestSource, messagetransform.RequestMessage{Method: "POST", Path: "/backend-api/codex/responses", Headers: headers, Body: body})
	install := standIn(defaultKey, "codex.installation", codexInstall)
	if want := strings.ReplaceAll(string(body), codexInstall, install); string(output.Body) != want {
		t.Fatalf("body: got %s\nwant %s", output.Body, want)
	}
	if want := strings.ReplaceAll(string(turnMetadata), codexInstall, install); output.Headers.Get("X-Codex-Turn-Metadata") != want {
		t.Fatalf("turn metadata header: %s", output.Headers.Get("X-Codex-Turn-Metadata"))
	}
	if output.Headers.Get("Authorization") != "Bearer synthetic-"+codexInstall {
		t.Error("a credential header was rewritten")
	}
	if output.Headers.Get("Session-Id") != codexSession || !strings.Contains(string(output.Body), `"prompt_cache_key":"`+codexSession+`"`) {
		t.Error("the session ID or prompt cache key was changed")
	}
}

// Only the fields the agent fills are rewritten. A copy elsewhere, such as an
// ID the user pasted into a message, is left as it is: the README says so.
func TestOnlyTheAgentsOwnFieldsChange(t *testing.T) {
	body := strings.Replace(claudeBody(t), "Write notes to", "Prompt "+claudePrompt+" from device "+claudeDevice+": write notes to", 1)
	output := string(apply(t, requestSource, messagetransform.RequestMessage{Method: "POST", Path: "/v1/messages", Body: []byte(body)}).Body)
	for _, original := range []string{claudePrompt, claudeDevice} {
		if strings.Count(output, original) != 1 || !strings.Contains(output, "Prompt "+claudePrompt+" from device "+claudeDevice) {
			t.Fatalf("expected only the pasted copy of %s to remain: %s", original, output)
		}
	}
}

func TestStandInsFollowTheKeyAndSettings(t *testing.T) {
	body := claudeBody(t)
	input := messagetransform.RequestMessage{Method: "POST", Path: "/v1/messages", Body: []byte(body)}
	key := "a different key of my own, 2026"
	output := apply(t, strings.Replace(requestSource, `const key = "`+defaultKey+`"`, `const key = "`+key+`"`, 1), input)
	if !strings.Contains(string(output.Body), standIn(key, "claude.prompt", claudePrompt)) ||
		strings.Contains(string(output.Body), standIn(defaultKey, "claude.prompt", claudePrompt)) {
		t.Fatal("the stand-in did not follow the key")
	}
	for setting, original := range map[string]string{"claudePromptId": claudePrompt, "claudeDeviceId": claudeDevice} {
		source := strings.Replace(requestSource, setting+": true", setting+": false", 1)
		if source == requestSource {
			t.Fatalf("setting %s not found", setting)
		}
		if !strings.Contains(string(apply(t, source, input).Body), original) {
			t.Errorf("%s: false did not leave the identifier", setting)
		}
	}
	long := strings.Repeat("k", 200)
	output = apply(t, strings.Replace(requestSource, `const key = "`+defaultKey+`"`, `const key = "`+long+`"`, 1), input)
	if !strings.Contains(string(output.Body), standIn(long, "claude.prompt", claudePrompt)) {
		t.Fatal("a key longer than the HMAC block was not hashed first")
	}
}

func TestInvalidConfigurationRefusesWithoutEchoingIdentifiers(t *testing.T) {
	input := messagetransform.RequestMessage{Method: "POST", Path: "/v1/messages", Body: []byte(claudeBody(t))}
	for name, source := range map[string]string{
		"short key":     strings.Replace(requestSource, `"`+defaultKey+`"`, `"too short"`, 1),
		"long key":      strings.Replace(requestSource, `"`+defaultKey+`"`, `"`+strings.Repeat("k", 257)+`"`, 1),
		"non-ASCII key": strings.Replace(requestSource, `"`+defaultKey+`"`, `"非ASCII的密钥文本非ASCII的密钥文本"`, 1),
		"setting":       strings.Replace(requestSource, "claudeDeviceId: true", `claudeDeviceId: "yes"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compile(t, source).NewTurn().ApplyRequest(context.Background(), input)
			if err == nil {
				t.Fatal("invalid configuration was accepted")
			}
			for _, original := range []string{claudePrompt, claudeDevice} {
				if strings.Contains(err.Error(), original) {
					t.Fatal("the error repeated an identifier")
				}
			}
		})
	}
}

func TestRequestsWithoutAgentIdentifiersAreUntouched(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":       "not JSON " + claudePrompt,
		"empty":          "",
		"plain request":  `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		"id in a prompt": `{"model":"m","messages":[{"role":"user","content":"x-anthropic-billing-header: cc_prompt_id=` + claudePrompt + `"}]}`,
		"other metadata": `{"model":"m","metadata":{"user_id":"opaque-user"},"system":"You are helpful. cc_prompt_id=` + claudePrompt + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			output := apply(t, requestSource, messagetransform.RequestMessage{Method: "POST", Path: "/v1/messages", Body: []byte(body)})
			if string(output.Body) != body {
				t.Fatalf("changed: %s", output.Body)
			}
		})
	}
}

func TestLegacyClaudeUserIdForm(t *testing.T) {
	body := `{"model":"m","metadata":{"user_id":"user_` + claudeDevice + `_account_` + claudeAccount + `_session_` + claudeSession + `"}}`
	output := apply(t, requestSource, messagetransform.RequestMessage{Method: "POST", Path: "/v1/messages", Body: []byte(body)})
	if want := strings.ReplaceAll(body, claudeDevice, standIn(defaultKey, "claude.device", claudeDevice)); string(output.Body) != want {
		t.Fatalf("got %s", output.Body)
	}
}

// A long conversation near the engine's body limit must finish inside the
// two-second stage limit, or the agent's turn would fail.
func TestNearLimitConversationFinishesInTime(t *testing.T) {
	body := claudeBody(t)
	filler := strings.Repeat("x", 4000)
	var messages strings.Builder
	for messages.Len() < 15<<20 {
		if messages.Len() > 0 {
			messages.WriteByte(',')
		}
		fmt.Fprintf(&messages, `{"role":"user","content":[{"type":"text","text":%q}]}`, filler)
	}
	body = strings.Replace(body, `"messages":[`, `"messages":[`+messages.String()+",", 1)
	started := time.Now()
	output := apply(t, requestSource, messagetransform.RequestMessage{Method: "POST", Path: "/v1/messages", Body: []byte(body)})
	t.Logf("%d bytes in %v", len(body), time.Since(started).Round(time.Millisecond))
	if strings.Contains(string(output.Body), claudePrompt) || strings.Contains(string(output.Body), claudeDevice) {
		t.Fatal("identifiers survived in a long conversation")
	}
}
