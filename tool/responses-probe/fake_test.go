package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// canaryKey is never a real key; tests prove no output carries it.
const canaryKey = "sk-canary-0123456789abcdefghijKLMNOP"

// fakeRequest is one request the fake upstream saw.
type fakeRequest struct {
	method, path, authorization, userAgent, accept string
	body                                           map[string]any
	size                                           int
}

// fakeScenario scripts the fake upstream's answers.
type fakeScenario struct {
	listed []string
	// windows adds a context_window member to listed models.
	windows map[string]int
	// refuse returns a status and an error body for a request it refuses.
	refuse func(body map[string]any) (int, string)
	// tamperInBand answers an unknown encrypted_content in band, after the
	// response head, instead of with HTTP 400.
	tamperInBand bool
	// gap delays every event of a request at effort xhigh or max.
	gap time.Duration
	// echo puts the key in a response header, an SSE event and error
	// messages.
	echo bool
}

// fakeUpstream is an httptest Responses API: it streams the public API's
// event sequence, refuses oversized contexts and unknown encrypted content
// as the API does, and reports cache reads per prompt_cache_key.
type fakeUpstream struct {
	t        *testing.T
	scenario fakeScenario
	server   *httptest.Server

	mutex     sync.Mutex
	requests  []fakeRequest
	issued    map[string]bool
	cacheKeys map[string]bool
	responses int
}

func newFakeUpstream(t *testing.T, scenario fakeScenario) *fakeUpstream {
	t.Helper()
	fake := &fakeUpstream{t: t, scenario: scenario, issued: map[string]bool{}, cacheKeys: map[string]bool{}}
	fake.server = httptest.NewServer(fake)
	t.Cleanup(fake.server.Close)
	return fake
}

func (fake *fakeUpstream) seen() []fakeRequest {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]fakeRequest(nil), fake.requests...)
}

func (fake *fakeUpstream) echo() string {
	if fake.scenario.echo {
		return " (request key " + canaryKey + ", masked sk-cana****MNOP)"
	}
	return ""
}

func (fake *fakeUpstream) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	raw, _ := io.ReadAll(request.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	fake.mutex.Lock()
	fake.requests = append(fake.requests, fakeRequest{
		method: request.Method, path: request.URL.Path, authorization: request.Header.Get("Authorization"),
		userAgent: request.Header.Get("User-Agent"), accept: request.Header.Get("Accept"), body: body, size: len(raw),
	})
	fake.mutex.Unlock()
	if fake.scenario.echo {
		writer.Header().Set("X-Echo-Key", canaryKey)
		writer.Header().Set("X-Request-Id", "req_"+canaryKey)
	}
	if request.Header.Get("Authorization") != "Bearer "+canaryKey {
		fake.fail(writer, 401, `{"error":{"message":"Incorrect API key provided: sk-cana****MNOP.","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`)
		return
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/v1/models":
		var data []map[string]any
		for _, id := range fake.scenario.listed {
			entry := map[string]any{"id": id, "object": "model", "created": 1763700000, "owned_by": "system"}
			if window, ok := fake.scenario.windows[id]; ok {
				entry["context_window"] = window
			}
			data = append(data, entry)
		}
		data = append(data, map[string]any{"id": "ft:gpt-6-luna:secret-org::abc", "object": "model", "created": 1, "owned_by": "secret-org"})
		encoded, _ := json.Marshal(map[string]any{"object": "list", "data": data})
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Openai-Organization", "secret-org")
		_, _ = writer.Write(encoded)
	case request.Method == http.MethodPost && request.URL.Path == "/v1/responses":
		fake.respond(writer, body, len(raw))
	default:
		fake.fail(writer, 404, `{"error":{"message":"not found","type":"invalid_request_error","param":null,"code":null}}`)
	}
}

func (fake *fakeUpstream) fail(writer http.ResponseWriter, status int, body string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, body)
}

func errorBody(message, kind, param, code string) string {
	encoded, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": kind, "param": param, "code": code}})
	return string(encoded)
}

// unknownEncrypted reports a replayed reasoning item this upstream never
// issued.
func (fake *fakeUpstream) unknownEncrypted(body map[string]any) bool {
	input, _ := body["input"].([]any)
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		if item["type"] != "reasoning" {
			continue
		}
		encrypted, _ := item["encrypted_content"].(string)
		fake.mutex.Lock()
		known := fake.issued[encrypted]
		fake.mutex.Unlock()
		if !known {
			return true
		}
	}
	return false
}

func (fake *fakeUpstream) respond(writer http.ResponseWriter, body map[string]any, size int) {
	if body["stream"] != true || body["store"] != false {
		fake.fail(writer, 400, errorBody("stream and store", "invalid_request_error", "stream", "invalid_value"))
		return
	}
	model, _ := body["model"].(string)
	if size > 1<<20 {
		fake.fail(writer, 400, errorBody(fmt.Sprintf("This model's maximum context length is 400000 tokens. However, your messages resulted in %d tokens.", size/4)+fake.echo(), "invalid_request_error", "input", "context_length_exceeded"))
		return
	}
	tampered := fake.unknownEncrypted(body)
	if tampered && !fake.scenario.tamperInBand {
		fake.fail(writer, 400, errorBody("The encrypted content could not be verified."+fake.echo(), "invalid_request_error", "input", "invalid_encrypted_content"))
		return
	}
	if !tampered && fake.scenario.refuse != nil {
		if status, refusal := fake.scenario.refuse(body); status != 0 {
			fake.fail(writer, status, refusal)
			return
		}
	}
	fake.mutex.Lock()
	fake.responses++
	number := fake.responses
	cacheKey, _ := body["prompt_cache_key"].(string)
	warm := fake.cacheKeys[cacheKey]
	fake.cacheKeys[cacheKey] = true
	encrypted := fmt.Sprintf("gAAAAABfake-%04d-%s", number, strings.Repeat("Q", 40))
	fake.issued[encrypted] = true
	fake.mutex.Unlock()

	input := size / 4
	cached := 0
	if warm {
		cached = input * 9 / 10
	}
	turn := standInTurn{
		responseID: fmt.Sprintf("resp_fake_%04d", number), model: model,
		reasoning: "Fake reasoning summary.", encrypted: encrypted, text: "ok",
		usage: standInUsage{input: input, cached: cached, output: 9, reasoning: 5},
	}
	events := turn.events()
	if tampered {
		failed := map[string]any{"id": turn.responseID, "object": "response", "status": "failed", "model": model, "output": []any{},
			"error": map[string]any{"code": "invalid_encrypted_content", "message": "The encrypted content could not be verified." + fake.echo()}}
		events = [][]byte{events[0], sseEvent("response.failed", map[string]any{"type": "response.failed", "response": failed, "sequence_number": 1})}
	}
	if fake.scenario.echo {
		events = append(events[:1:1], append([][]byte{sseEvent("response.metadata", map[string]any{"type": "response.metadata", "note": canaryKey})}, events[1:]...)...)
	}
	gap := time.Duration(0)
	if reasoning, ok := body["reasoning"].(map[string]any); ok && (reasoning["effort"] == "xhigh" || reasoning["effort"] == "max") {
		gap = fake.scenario.gap
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(http.StatusOK)
	flusher, _ := writer.(http.Flusher)
	for _, event := range events {
		if gap > 0 {
			time.Sleep(gap)
		}
		_, _ = writer.Write(event)
		if flusher != nil {
			flusher.Flush()
		}
	}
}
