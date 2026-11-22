package productruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/responseschat"
)

// The protocol goldens characterize the four existing protocol paths over
// skeletonized real client requests (made by tool/wire-skeleton from the
// installed-client wire dumps) and scripted provider replies: the client
// decode report, the exact provider request, re-admission, the live and
// approval-held client stream bytes, tool intents, and the unary response.
// They were recorded before any shared protocol code changed, so a later
// change that moves a byte on these paths shows up as a golden diff.
//
// Keepalive frames are not part of a path: the loopback downstream writes
// them between Feed results, and its liveness tests pin their bytes.
//
// Regenerate with: go test ./internal/productruntime -run TestProtocolGolden -update
var updateProtocolGolden = flag.Bool("update", false, "rewrite the protocol golden files")

const (
	protocolGoldenRoot  = "testdata/protocolgolden"
	protocolGoldenModel = "golden-upstream-model"
	// The beta list Claude Code 2.1.329 sends on its main requests.
	protocolGoldenAnthropicBeta = "interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,prompt-caching-scope-2026-01-05,effort-2025-11-24"
)

type protocolGoldenPath struct {
	name     string
	path     *protocolpath.Path
	requests string // fixture name prefix: "claude" or "codex"
}

func protocolGoldenPaths(t *testing.T) []protocolGoldenPath {
	t.Helper()
	anthropic, err := anthropicchat.NewProtocolPath(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	messages, err := anthropicchat.NewMessagesProtocolPath(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	responses, err := responseschat.NewProtocolPath(responseschat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	passthrough, err := responseschat.NewResponsesPassthroughProtocolPath(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return []protocolGoldenPath{
		{name: anthropicchat.CodecPairID, path: anthropic, requests: "claude"},
		{name: anthropicchat.MessagesCodecPairID, path: messages, requests: "claude"},
		{name: responseschat.CodecPairID, path: responses, requests: "codex"},
		{name: responseschat.ResponsesPassthroughCodecPairID, path: passthrough, requests: "codex"},
	}
}

func TestProtocolGolden(t *testing.T) {
	requests, err := filepath.Glob(filepath.Join(protocolGoldenRoot, "requests", "*.json"))
	if err != nil || len(requests) == 0 {
		t.Fatalf("protocol golden requests: %v (%d)", err, len(requests))
	}
	for _, golden := range protocolGoldenPaths(t) {
		golden := golden
		t.Run(golden.name, func(t *testing.T) {
			ran := 0
			for _, requestPath := range requests {
				fixture := strings.TrimSuffix(filepath.Base(requestPath), ".json")
				if !strings.HasPrefix(fixture, golden.requests) {
					continue
				}
				ran++
				t.Run(fixture, func(t *testing.T) {
					body, err := os.ReadFile(requestPath)
					if err != nil {
						t.Fatal(err)
					}
					record, providerBody := runProtocolGolden(t, golden, fixture, body)
					directory := filepath.Join(protocolGoldenRoot, "golden", golden.name)
					compareProtocolGolden(t, filepath.Join(directory, fixture+".golden.json"), record)
					if providerBody != nil {
						compareProtocolGolden(t, filepath.Join(directory, fixture+".provider.json"), providerBody)
					}
				})
			}
			if ran == 0 {
				t.Fatal("no fixtures for this path")
			}
		})
	}
}

type goldenNotice []string

type goldenDecode struct {
	Error   string       `json:"error,omitempty"`
	Notices goldenNotice `json:"notices"`
}

type goldenProviderRequest struct {
	Error       string              `json:"error,omitempty"`
	Method      string              `json:"method,omitempty"`
	Path        string              `json:"relativePath,omitempty"`
	Headers     map[string][]string `json:"headers,omitempty"`
	BodyBytes   int                 `json:"bodyBytes"`
	Notices     goldenNotice        `json:"notices"`
	Readmission string              `json:"readmission,omitempty"`
}

type goldenIntent struct {
	ResponseID string `json:"responseId"`
	Ordinal    int    `json:"ordinal"`
	Kind       string `json:"kind"`
	Key        string `json:"key"`
	ItemKey    string `json:"itemKey,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	Arguments  string `json:"arguments,omitempty"`
	Input      string `json:"input,omitempty"`
}

type goldenBlock struct {
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	Refusal   string `json:"refusal,omitempty"`
	ToolName  string `json:"toolName,omitempty"`
	ToolKey   string `json:"toolKey,omitempty"`
	Extension string `json:"extension,omitempty"`
}

type goldenResponse struct {
	ID            string        `json:"id"`
	ReportedModel string        `json:"reportedModel"`
	StopReason    string        `json:"stopReason"`
	StopSequence  string        `json:"stopSequence,omitempty"`
	Blocks        []goldenBlock `json:"blocks"`
	Extensions    []string      `json:"extensions,omitempty"`
	Usage         []string      `json:"usage"`
}

type goldenStream struct {
	Error              string          `json:"error,omitempty"`
	Live               []string        `json:"live"`
	TerminalReceived   bool            `json:"terminalReceived"`
	FinishError        string          `json:"finishError,omitempty"`
	Response           *goldenResponse `json:"response,omitempty"`
	Intents            []goldenIntent  `json:"intents,omitempty"`
	Notices            goldenNotice    `json:"notices"`
	Release            string          `json:"release,omitempty"`
	ApproveError       string          `json:"approveError,omitempty"`
	RejectAfterApprove string          `json:"rejectAfterApprove,omitempty"`
}

type goldenUnary struct {
	DecodeError    string          `json:"decodeError,omitempty"`
	DecodeNotices  goldenNotice    `json:"decodeNotices"`
	Response       *goldenResponse `json:"response,omitempty"`
	EncodeError    string          `json:"encodeError,omitempty"`
	EncodeNotices  goldenNotice    `json:"encodeNotices"`
	ClientResponse string          `json:"clientResponse,omitempty"`
}

type goldenRecord struct {
	Path            string                  `json:"path"`
	Fixture         string                  `json:"fixture"`
	Decode          goldenDecode            `json:"decode"`
	ProviderRequest *goldenProviderRequest  `json:"providerRequest,omitempty"`
	Streams         map[string]goldenStream `json:"streams,omitempty"`
	Unary           map[string]goldenUnary  `json:"unary,omitempty"`
}

func runProtocolGolden(t *testing.T, golden protocolGoldenPath, fixture string, body []byte) ([]byte, []byte) {
	t.Helper()
	record := goldenRecord{Path: golden.name, Fixture: fixture}
	request, report, err := golden.path.Client().DecodeRequest(body)
	record.Decode = goldenDecode{Error: goldenErrorText(err), Notices: goldenNoticeList(report)}
	if err != nil {
		return encodeGoldenRecord(t, record), nil
	}
	mapped, err := request.WithEffectiveModel(protocolGoldenModel)
	if err != nil {
		t.Fatalf("WithEffectiveModel() error = %v", err)
	}
	headers := make(http.Header)
	if golden.requests == "claude" {
		headers.Set("Anthropic-Beta", protocolGoldenAnthropicBeta)
	}
	provider, providerReport, err := golden.path.EncodeProviderRequest(mapped, body, headers)
	encoded := &goldenProviderRequest{Error: goldenErrorText(err), Notices: goldenNoticeList(providerReport)}
	record.ProviderRequest = encoded
	if err != nil {
		return encodeGoldenRecord(t, record), nil
	}
	providerBody := provider.Body()
	encoded.Method, encoded.Path, encoded.BodyBytes = provider.Method(), provider.RelativePath(), len(providerBody)
	encoded.Headers = map[string][]string(provider.Headers())
	encoded.Readmission = "accepted"
	if err := golden.path.ValidateTransformedRequest(providerBody); err != nil {
		encoded.Readmission = "refused: " + err.Error()
	}
	record.Streams = make(map[string]goldenStream)
	record.Unary = make(map[string]goldenUnary)
	for _, reply := range protocolGoldenReplies(t, golden.path.Backend().Dialect(), providerBody) {
		if reply.stream {
			record.Streams[reply.name] = runGoldenStream(t, golden.path, mapped, reply.body)
		} else {
			record.Unary[reply.name] = runGoldenUnary(golden.path, mapped, reply.body)
		}
	}
	return encodeGoldenRecord(t, record), providerBody
}

func runGoldenStream(t *testing.T, path *protocolpath.Path, request protocolcore.Request, wire []byte) goldenStream {
	t.Helper()
	result := goldenStream{Live: []string{}}
	stream, err := path.Streaming().NewStream(request.Clone())
	if err != nil {
		result.Error = "NewStream: " + err.Error()
		return result
	}
	ctx := context.Background()
	// One Feed per server-sent event, as a well-behaved upstream delivers them.
	for _, event := range goldenSplitEvents(wire) {
		output, err := stream.Feed(ctx, event)
		if len(output) != 0 {
			result.Live = append(result.Live, string(output))
		}
		if err != nil {
			result.Error = "Feed: " + err.Error()
			return result
		}
	}
	result.TerminalReceived = stream.TerminalReceived()
	terminal, err := stream.FinishDecoded(ctx)
	if err != nil {
		result.FinishError = err.Error()
		return result
	}
	response := projectGoldenResponse(terminal.DecodedResponse())
	result.Response = &response
	result.Intents = projectGoldenIntents(terminal.ToolIntents())
	result.Notices = goldenNoticeList(terminal.TranslationReport())
	release, err := terminal.Approve()
	result.Release, result.ApproveError = string(release), goldenErrorText(err)
	result.RejectAfterApprove = "accepted"
	if err := terminal.Reject(); err != nil {
		result.RejectAfterApprove = "refused: " + err.Error()
	}
	return result
}

func runGoldenUnary(path *protocolpath.Path, request protocolcore.Request, body []byte) goldenUnary {
	result := goldenUnary{}
	response, report, err := path.Backend().DecodeResponse(request.Clone(), body)
	result.DecodeError, result.DecodeNotices = goldenErrorText(err), goldenNoticeList(report)
	if err != nil {
		return result
	}
	projected := projectGoldenResponse(response)
	result.Response = &projected
	encoded, report, err := path.EncodeClientResponse(request.Clone(), response, body)
	result.EncodeError, result.EncodeNotices = goldenErrorText(err), goldenNoticeList(report)
	result.ClientResponse = string(encoded)
	return result
}

// goldenSplitEvents cuts a wire stream after every blank line, keeping
// each event's bytes exactly as written.
func goldenSplitEvents(wire []byte) [][]byte {
	var events [][]byte
	for len(wire) != 0 {
		end := bytes.Index(wire, []byte("\n\n"))
		if end < 0 {
			events = append(events, wire)
			break
		}
		events = append(events, wire[:end+2])
		wire = wire[end+2:]
	}
	return events
}

type protocolGoldenReply struct {
	name   string
	stream bool
	body   []byte
}

// protocolGoldenReplies returns the scripted provider replies for one
// backend dialect. Chat replies name the first tool of the encoded request,
// so every request with tools is answered with a call it declared.
func protocolGoldenReplies(t *testing.T, dialect protocolspec.Dialect, providerBody []byte) []protocolGoldenReply {
	t.Helper()
	switch dialect {
	case protocolspec.DialectOpenAIChat:
		return chatGoldenReplies(t, providerBody)
	case protocolspec.DialectAnthropicMessages:
		return append(fileGoldenReplies(t, "claude-"), protocolGoldenReply{
			name: "unary-text-tool",
			body: []byte(`{"id":"msg_golden_1","type":"message","role":"assistant","model":"golden-upstream-model",` +
				`"content":[{"type":"thinking","thinking":"Golden thinking.","signature":"golden-signature"},` +
				`{"type":"text","text":"Golden reply."},` +
				`{"type":"tool_use","id":"toolu_golden_1","name":"Bash","input":{"command":"echo golden"}}],` +
				`"stop_reason":"tool_use","stop_sequence":null,` +
				`"usage":{"input_tokens":11,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":7}}`),
		})
	case protocolspec.DialectOpenAIResponses:
		return append(fileGoldenReplies(t, "codex-"),
			protocolGoldenReply{
				name: "stream-public-text", stream: true,
				body: joinGoldenEvents(t,
					`{"type":"response.created","sequence_number":0,"response":{"id":"resp_golden_1","object":"response","created_at":1,"status":"in_progress","model":"golden-upstream-model","output":[]}}`,
					`{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_golden_1","object":"response","created_at":1,"status":"in_progress","model":"golden-upstream-model","output":[]}}`,
					`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"msg_golden_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
					`{"type":"response.content_part.added","sequence_number":3,"item_id":"msg_golden_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
					`{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_golden_1","output_index":0,"content_index":0,"delta":"Golden "}`,
					`{"type":"response.output_text.delta","sequence_number":5,"item_id":"msg_golden_1","output_index":0,"content_index":0,"delta":"reply."}`,
					`{"type":"response.output_text.done","sequence_number":6,"item_id":"msg_golden_1","output_index":0,"content_index":0,"text":"Golden reply."}`,
					`{"type":"response.content_part.done","sequence_number":7,"item_id":"msg_golden_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Golden reply.","annotations":[]}}`,
					`{"type":"response.output_item.done","sequence_number":8,"output_index":0,"item":{"id":"msg_golden_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Golden reply.","annotations":[]}]}}`,
					`{"type":"response.completed","sequence_number":9,"response":{"id":"resp_golden_1","object":"response","created_at":1,"status":"completed","model":"golden-upstream-model","output":[{"id":"msg_golden_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Golden reply.","annotations":[]}]}],"usage":{"input_tokens":11,"input_tokens_details":{"cached_tokens":3},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":18}}}`,
				),
			},
			protocolGoldenReply{
				name: "unary-text-tool",
				body: []byte(`{"id":"resp_golden_2","object":"response","created_at":1,"status":"completed","model":"golden-upstream-model",` +
					`"output":[{"id":"rs_golden_1","type":"reasoning","summary":[{"type":"summary_text","text":"Golden summary."}],"encrypted_content":"golden-encrypted"},` +
					`{"id":"msg_golden_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Golden reply.","annotations":[]}]},` +
					`{"id":"fc_golden_1","type":"function_call","status":"completed","call_id":"call_golden_1","name":"exec_command","arguments":"{\"cmd\":\"echo golden\"}"}],` +
					`"usage":{"input_tokens":11,"input_tokens_details":{"cached_tokens":3},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":18}}`),
			},
		)
	default:
		t.Fatalf("no golden replies for %s", dialect)
		return nil
	}
}

func fileGoldenReplies(t *testing.T, prefix string) []protocolGoldenReply {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(protocolGoldenRoot, "replies", prefix+"*.sse"))
	if err != nil || len(names) == 0 {
		t.Fatalf("protocol golden replies %s: %v", prefix, err)
	}
	sort.Strings(names)
	replies := make([]protocolGoldenReply, 0, len(names))
	for _, name := range names {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		replies = append(replies, protocolGoldenReply{
			name: "stream-" + strings.TrimSuffix(filepath.Base(name), ".sse"), stream: true, body: body,
		})
	}
	return replies
}

func chatGoldenReplies(t *testing.T, providerBody []byte) []protocolGoldenReply {
	t.Helper()
	var encoded struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(providerBody, &encoded); err != nil {
		t.Fatalf("encoded Chat request: %v", err)
	}
	chunk := func(delta, finish string) string {
		if finish == "" {
			finish = "null"
		} else {
			finish = `"` + finish + `"`
		}
		return `{"id":"chatcmpl-golden","object":"chat.completion.chunk","created":1,"model":"golden-upstream-model","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}]}`
	}
	usage := `{"id":"chatcmpl-golden","object":"chat.completion.chunk","created":1,"model":"golden-upstream-model","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":2}}}`
	replies := []protocolGoldenReply{
		{name: "stream-text", stream: true, body: joinGoldenEvents(t,
			chunk(`{"role":"assistant","content":"","reasoning_content":"Golden thinking."}`, ""),
			chunk(`{"content":"Golden "}`, ""),
			chunk(`{"content":"reply."}`, ""),
			chunk(`{}`, "stop"),
			usage,
			"[DONE]",
		)},
		{name: "unary-text", body: []byte(`{"id":"chatcmpl-golden","object":"chat.completion","created":1,"model":"golden-upstream-model",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"Golden reply."},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":3}}}`)},
		// An empty answer: the client encoders' handling of an empty text block
		// is one of the reviewed changes the design expects later.
		{name: "stream-empty", stream: true, body: joinGoldenEvents(t,
			chunk(`{"role":"assistant","content":""}`, ""),
			chunk(`{}`, "stop"),
			usage,
			"[DONE]",
		)},
		{name: "unary-empty", body: []byte(`{"id":"chatcmpl-golden","object":"chat.completion","created":1,"model":"golden-upstream-model",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":11,"completion_tokens":0,"total_tokens":11}}`)},
	}
	if len(encoded.Tools) == 0 {
		return replies
	}
	name, err := json.Marshal(encoded.Tools[0].Function.Name)
	if err != nil {
		t.Fatal(err)
	}
	return append(replies,
		protocolGoldenReply{name: "stream-text-tool", stream: true, body: joinGoldenEvents(t,
			chunk(`{"role":"assistant","content":"Before the tool."}`, ""),
			chunk(`{"tool_calls":[{"index":0,"id":"call_golden_1","type":"function","function":{"name":`+string(name)+`,"arguments":"{\"golden\":"}}]}`, ""),
			chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"true}"}}]}`, ""),
			chunk(`{}`, "tool_calls"),
			usage,
			"[DONE]",
		)},
		protocolGoldenReply{name: "unary-text-tool", body: []byte(`{"id":"chatcmpl-golden","object":"chat.completion","created":1,"model":"golden-upstream-model",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"Before the tool.","tool_calls":[{"id":"call_golden_1","type":"function","function":{"name":` + string(name) + `,"arguments":"{\"golden\":true}"}}]},"finish_reason":"tool_calls"}],` +
			`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)},
	)
}

func joinGoldenEvents(t *testing.T, payloads ...string) []byte {
	t.Helper()
	var wire bytes.Buffer
	for _, payload := range payloads {
		if payload != "[DONE]" && !json.Valid([]byte(payload)) {
			t.Fatalf("golden event is not JSON: %s", payload)
		}
		wire.WriteString("data: " + payload + "\n\n")
	}
	return wire.Bytes()
}

func projectGoldenResponse(response protocolcore.Response) goldenResponse {
	projected := goldenResponse{
		ID: response.ID, ReportedModel: response.ReportedModel,
		StopReason: string(response.StopReason), StopSequence: response.StopSequence,
		Blocks: []goldenBlock{},
	}
	for _, block := range response.Blocks {
		view := goldenBlock{Kind: string(block.Kind), Text: block.Text, Refusal: block.Refusal}
		switch block.Kind {
		case protocolcore.BlockToolCall:
			view.ToolName, view.ToolKey = block.ToolCall.Name, goldenCallKey(block.ToolCall.Key)
		case protocolcore.BlockToolResult:
			view.ToolName, view.ToolKey = block.ToolResult.Name, goldenCallKey(block.ToolResult.Key)
		case protocolcore.BlockProviderExtension:
			view.Extension = goldenExtension(block.ProviderExtension)
		}
		projected.Blocks = append(projected.Blocks, view)
	}
	for _, extension := range response.ProviderExtensions {
		projected.Extensions = append(projected.Extensions, goldenExtension(extension))
	}
	usage := response.Usage
	for _, entry := range []struct {
		name  string
		value protocolcore.UsageValue
	}{
		{"inputUncached", usage.InputUncached}, {"cacheWrite", usage.CacheWrite},
		{"cacheRead", usage.CacheRead}, {"output", usage.Output}, {"reasoning", usage.Reasoning},
	} {
		if entry.value.Known {
			projected.Usage = append(projected.Usage, fmt.Sprintf("%s=%d (%s)", entry.name, entry.value.Tokens, entry.value.Source))
		} else {
			projected.Usage = append(projected.Usage, entry.name+"=unknown")
		}
	}
	return projected
}

func projectGoldenIntents(intents []protocolcore.ToolIntent) []goldenIntent {
	projected := make([]goldenIntent, 0, len(intents))
	for _, intent := range intents {
		view := goldenIntent{
			ResponseID: intent.ResponseID, Ordinal: intent.Ordinal,
			Kind: string(intent.Call.EffectiveKind()), Key: goldenCallKey(intent.Call.Key),
			Namespace: intent.Call.Namespace, Name: intent.Call.Name,
			Arguments: string(intent.Call.Arguments.Bytes()), Input: intent.Call.Input,
		}
		if !intent.Call.ItemKey.IsZero() {
			view.ItemKey = goldenCallKey(intent.Call.ItemKey)
		}
		projected = append(projected, view)
	}
	return projected
}

func goldenCallKey(key protocolcore.CallKey) string {
	return key.Source() + "/" + key.WireID()
}

func goldenExtension(extension protocolcore.ProviderExtension) string {
	return fmt.Sprintf("%s/%s at %s: %s", extension.Source(), extension.Kind(), extension.Path(),
		bytes.Join(extension.Fragments(), []byte(" | ")))
}

func goldenNoticeList(report protocolcore.TranslationReport) goldenNotice {
	notices := goldenNotice{}
	for _, notice := range report.Notices() {
		notices = append(notices, string(notice.Code)+" "+notice.Path)
	}
	return notices
}

func goldenErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func encodeGoldenRecord(t *testing.T, record goldenRecord) []byte {
	t.Helper()
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(record); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func compareProtocolGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	if *updateProtocolGolden {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("golden %s is missing (record it with -update): %v", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the recorded golden; first difference at byte %d\n got: %s\nwant: %s",
			name, goldenFirstDifference(got, want), goldenExcerpt(got, goldenFirstDifference(got, want)), goldenExcerpt(want, goldenFirstDifference(got, want)))
	}
}

func goldenFirstDifference(left, right []byte) int {
	limit := min(len(left), len(right))
	for index := 0; index < limit; index++ {
		if left[index] != right[index] {
			return index
		}
	}
	return limit
}

func goldenExcerpt(value []byte, at int) string {
	start, end := max(0, at-80), min(len(value), at+80)
	return string(value[start:end])
}

// The golden tree holds exactly the files this test writes, so a fixture or
// path removed from the test cannot leave a stale golden behind.
func TestProtocolGoldenTreeHasNoStrayFiles(t *testing.T) {
	if *updateProtocolGolden {
		t.Skip("goldens are being rewritten")
	}
	requests, err := filepath.Glob(filepath.Join(protocolGoldenRoot, "requests", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]bool{}
	for _, golden := range protocolGoldenPaths(t) {
		for _, request := range requests {
			fixture := strings.TrimSuffix(filepath.Base(request), ".json")
			if strings.HasPrefix(fixture, golden.requests) {
				expected[filepath.Join(golden.name, fixture+".golden.json")] = true
			}
		}
	}
	root := filepath.Join(protocolGoldenRoot, "golden")
	var stray []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(relative, ".provider.json") {
			if !expected[strings.TrimSuffix(relative, ".provider.json")+".golden.json"] {
				stray = append(stray, relative)
			}
			return nil
		}
		if !expected[relative] {
			stray = append(stray, relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stray) != 0 {
		slices.Sort(stray)
		t.Fatalf("golden files no test writes: %v", stray)
	}
}
