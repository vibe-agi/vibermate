package clientmetadata_test

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

//go:embed request.js
var requestSource string

const sampleInstallID = "00000000-0000-4000-8000-000000000001"
const sampleUserAgent = "vibermate-client/0.0.0"

func compile(t *testing.T, source string) messagetransform.Pipeline {
	t.Helper()
	pipeline, err := messagetransform.CompilePipeline([]messagetransform.Policy{{RequestJavaScript: source}}, messagetransform.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return pipeline
}

func TestMetadataWhitelistAndUntouchedFields(t *testing.T) {
	headers := http.Header{
		"UsEr-AgEnT":    {"test-client/9.8.7 (device)", "another-client"},
		"Authorization": {"Bearer synthetic-only"}, "Cookie": {"synthetic-only"},
		"Anthropic-Version": {"2023-06-01"}, "Openai-Version": {"2020-10-01"},
		"X-Stainless-Package-Version": {"1.2.3"}, "Sec-Ch-Ua": {"untouched"},
		"Chatgpt-Account-Id": {"account-a"}, "Account-Id": {"account-a"},
		"Session-Id": {"session-a"}, "Thread-Id": {"thread-a"}, "X-Client-Request-Id": {"request-a"},
		"X-Client-Id": {"not-an-install-id"}, "X-Install-Id-Extra": {"not-a-match"},
		"Content-Type": {"application/json"}, "Accept": {"text/event-stream"},
	}
	versions := []string{"Version", "x-CLIENT-version", "X-App-Version", "X-Codex-Version"}
	installs := []string{"Install-Id", "Installation-Id", "x-INSTALL-id", "X-Installation-Id", "X-Codex-Install-Id", "X-Codex-Installation-Id"}
	for _, name := range append(append([]string{}, versions...), installs...) {
		headers[name] = []string{"original-a", "original-b"}
	}
	body := []byte("{\n  \"version\":\"9.8.7\", \"install_id\":\"private\", \"user_agent\":\"private\", \"account_id\":\"account-a\", \"large\":9007199254740993, \"tools\":[{\"name\":\"version\"}]\n}")
	input := messagetransform.RequestMessage{Method: "POST", Path: "/v1/responses", Headers: headers, Body: body}
	before := headers.Clone()
	output, err := compile(t, requestSource).NewTurn().ApplyRequest(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	expected := make(http.Header)
	for name, values := range before {
		expected[http.CanonicalHeaderKey(name)] = values
	}
	for _, name := range versions {
		expected.Set(name, "0.0.0")
	}
	for _, name := range installs {
		expected.Set(name, sampleInstallID)
	}
	expected.Set("User-Agent", sampleUserAgent)
	if !reflect.DeepEqual(output.Headers, expected) || !bytes.Equal(output.Body, body) || output.Method != input.Method || output.Path != input.Path {
		t.Fatalf("unexpected transformation: %+v", output)
	}
	if !reflect.DeepEqual(headers, before) {
		t.Fatal("caller-owned headers mutated")
	}
}

func TestMetadataMissingHeadersOnlyAddsUserAgent(t *testing.T) {
	for _, body := range []string{`{"model":"test","input":"hello"}`, "not JSON", "", "  "} {
		t.Run(fmt.Sprintf("length-%d", len(body)), func(t *testing.T) {
			output, err := compile(t, requestSource).NewTurn().ApplyRequest(context.Background(), messagetransform.RequestMessage{
				Method: "POST", Path: "/v1/messages", Body: []byte(body),
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(output.Headers) != 1 || output.Headers.Get("User-Agent") != sampleUserAgent || string(output.Body) != body {
				t.Fatalf("invented metadata or changed body: %+v", output)
			}
		})
	}
}

func TestMetadataConfigurationAndIndependentDisable(t *testing.T) {
	for _, test := range []struct{ field, old, value, header, want string }{
		{"version", `"0.0.0"`, `"1.2.3"`, "Version", "1.2.3"},
		{"installId", `"` + sampleInstallID + `"`, `"synthetic-install-b"`, "X-Install-Id", "synthetic-install-b"},
		{"userAgent", `"` + sampleUserAgent + `"`, `"test-client/1.2.3"`, "User-Agent", "test-client/1.2.3"},
		{"version", `"0.0.0"`, `null`, "Version", "original"},
		{"installId", `"` + sampleInstallID + `"`, `null`, "X-Install-Id", "original"},
		{"userAgent", `"` + sampleUserAgent + `"`, `null`, "User-Agent", "original"},
	} {
		t.Run(test.field+"/"+test.value, func(t *testing.T) {
			source := strings.Replace(requestSource, test.field+": "+test.old, test.field+": "+test.value, 1)
			if source == requestSource {
				t.Fatal("configuration fixture did not change")
			}
			output, err := compile(t, source).NewTurn().ApplyRequest(context.Background(), messagetransform.RequestMessage{
				Method: "POST", Path: "/v1/responses", Headers: http.Header{test.header: {"original"}}, Body: []byte("{}"),
			})
			if err != nil || output.Headers.Get(test.header) != test.want {
				t.Fatalf("configuration not honored: %+v, %v", output, err)
			}
		})
	}
}

func TestMetadataInvalidConfigurationFailsClosed(t *testing.T) {
	for _, field := range []struct {
		name, original string
		limit          int
	}{
		{"version", "0.0.0", 64}, {"installId", sampleInstallID, 128}, {"userAgent", sampleUserAgent, 512},
	} {
		for index, value := range []any{"", "bad\r\nInjected: yes", "bad\n", "bad\tvalue", "非ASCII", strings.Repeat("x", field.limit+1), 123, true, []string{"value"}} {
			t.Run(fmt.Sprintf("%s/%d", field.name, index), func(t *testing.T) {
				encoded, _ := json.Marshal(value)
				source := strings.Replace(requestSource, field.name+`: "`+field.original+`"`, field.name+": "+string(encoded), 1)
				_, err := compile(t, source).NewTurn().ApplyRequest(context.Background(), messagetransform.RequestMessage{
					Method: "POST", Path: "/v1/messages", Headers: http.Header{"Version": {"sensitive-original"}}, Body: []byte("{}"),
				})
				if err == nil {
					t.Fatal("invalid configuration did not stop request")
				}
				if strings.Contains(err.Error(), "sensitive-original") {
					t.Fatal("error echoed original value")
				}
			})
		}
	}
}

func TestMetadataConcurrentTurnsRetriesAndResponseIdentity(t *testing.T) {
	pipeline := compile(t, requestSource)
	for index := range 32 {
		t.Run(fmt.Sprintf("user-%d", index), func(t *testing.T) {
			t.Parallel()
			turn := pipeline.NewTurn()
			input := messagetransform.RequestMessage{Method: "POST", Path: "/v1/responses", Headers: http.Header{
				"X-Install-Id": {fmt.Sprintf("installation-%d", index)}, "Session-Id": {fmt.Sprintf("session-%d", index)},
			}, Body: []byte(fmt.Sprintf(`{"input":"user-%d"}`, index))}
			first, err := turn.ApplyRequest(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			second, err := turn.ApplyRequest(context.Background(), input)
			if err != nil || !reflect.DeepEqual(first, second) || first.Headers.Get("Session-Id") != input.Headers.Get("Session-Id") {
				t.Fatalf("retry or isolation changed: %v", err)
			}
			for _, streaming := range []bool{false, true} {
				for _, status := range []int{200, 401, 500} {
					response := messagetransform.ResponseMessage{StatusCode: status, Streaming: streaming,
						Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"version":"original","install_id":"original","delta":"vibermate-client/0.0.0"}`)}
					output, err := turn.ApplyResponse(context.Background(), response)
					if err != nil || !reflect.DeepEqual(output, response) {
						t.Fatalf("response changed: %+v, %v", output, err)
					}
				}
			}
		})
	}
}

func TestMetadataComposesWithLocalIdentityInBothOrders(t *testing.T) {
	identityRequest, err := os.ReadFile("../hide-local-identity/request.js")
	if err != nil {
		t.Fatal(err)
	}
	identityResponse, err := os.ReadFile("../hide-local-identity/response.js")
	if err != nil {
		t.Fatal(err)
	}
	identity := messagetransform.Policy{RequestJavaScript: string(identityRequest), ResponseJavaScript: string(identityResponse)}
	metadata := messagetransform.Policy{RequestJavaScript: requestSource}
	for _, policies := range [][]messagetransform.Policy{{metadata, identity}, {identity, metadata}} {
		pipeline, err := messagetransform.CompilePipeline(policies, messagetransform.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		turn, err := pipeline.NewTurnWithMetadata(messagetransform.RuntimeMetadata{
			LocalUserName: "alice", HomeDirectory: "/Users/alice", WorkspaceRoot: "/Users/alice/project", OperatingSystem: "darwin",
		})
		if err != nil {
			t.Fatal(err)
		}
		request, err := turn.ApplyRequest(context.Background(), messagetransform.RequestMessage{
			Method: "POST", Path: "/v1/chat/completions", Headers: http.Header{"Version": {"9.8.7"}, "X-Install-Id": {"installation-a"}},
			Body: []byte(`{"model":"test","messages":[{"role":"user","content":"Read /Users/alice/project/readme.md"}]}`),
		})
		if err != nil || request.Headers.Get("User-Agent") != sampleUserAgent || request.Headers.Get("Version") != "0.0.0" ||
			request.Headers.Get("X-Install-Id") != sampleInstallID || !bytes.Contains(request.Body, []byte("/__vmi1_workspace__/readme.md")) || bytes.Contains(request.Body, []byte("alice")) {
			t.Fatalf("request composition failed: %+v, %v", request, err)
		}
		response, err := turn.ApplyResponse(context.Background(), messagetransform.ResponseMessage{
			StatusCode: 200, Headers: http.Header{"Content-Type": {"application/json"}},
			Body: []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"/__vmi1_workspace__/readme.md"},"finish_reason":"stop"}]}`),
		})
		if err != nil || !bytes.Contains(response.Body, []byte("/Users/alice/project/readme.md")) {
			t.Fatalf("response composition failed: %+v, %v", response, err)
		}
	}
}
