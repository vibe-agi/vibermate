package productruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/responses"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
)

// fakeResponsesUpstream is design 15.5's fake OpenAI Responses API behind the
// private transport. It checks every request against what the translated
// path promises the upstream and answers with the reply its script picks
// from the request's content.
type fakeResponsesUpstream struct {
	t      *testing.T
	script func(request fakeResponsesRequest) fakeResponsesReply
	codec  *openairesponses.Codec

	mu       sync.Mutex
	requests []fakeResponsesRequest
	other    []string
	// abandoned counts streams whose client went away during a pause.
	abandoned atomic.Int64
}

// fakeResponsesRequest is one request the fake received.
type fakeResponsesRequest struct {
	ordinal int
	at      time.Time
	headers http.Header
	body    []byte
	shape   responsesRequestShape
}

type responsesRequestShape struct {
	Model     string   `json:"model"`
	Stream    bool     `json:"stream"`
	Store     *bool    `json:"store"`
	Include   []string `json:"include"`
	Reasoning *struct {
		Effort  string `json:"effort"`
		Summary string `json:"summary"`
	} `json:"reasoning"`
	Text *struct {
		Format *struct {
			Type string `json:"type"`
		} `json:"format"`
	} `json:"text"`
	Tools []struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		Strict *bool  `json:"strict"`
	} `json:"tools"`
	Input []responsesInputItem `json:"input"`
}

type responsesInputItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Role             string          `json:"role"`
	Phase            string          `json:"phase"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Arguments        string          `json:"arguments"`
	EncryptedContent string          `json:"encrypted_content"`
	Output           json.RawMessage `json:"output"`
	Content          json.RawMessage `json:"content"`
	Summary          []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
}

// outputs is every function_call_output of the request, by call id.
func (shape responsesRequestShape) outputs() map[string]json.RawMessage {
	outputs := map[string]json.RawMessage{}
	for _, item := range shape.Input {
		if item.Type == "function_call_output" {
			outputs[item.CallID] = item.Output
		}
	}
	return outputs
}

func (shape responsesRequestShape) tool(name string) bool {
	return slices.ContainsFunc(shape.Tools, func(tool struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		Strict *bool  `json:"strict"`
	}) bool {
		return tool.Name == name
	})
}

// main reports Claude Code's main loop request: it declares tools. The title
// and other side requests declare none.
func (shape responsesRequestShape) main() bool {
	return len(shape.Tools) != 0
}

// fakeResponsesReply is one answer: an HTTP error, or an event stream that
// may pause after one of its events.
type fakeResponsesReply struct {
	status  int
	headers map[string]string
	body    string
	events  []string
	// pause holds the stream after event pauseAfter for this long.
	pause      time.Duration
	pauseAfter int
}

func newFakeResponsesUpstream(t *testing.T, script func(fakeResponsesRequest) fakeResponsesReply) *fakeResponsesUpstream {
	t.Helper()
	codec, err := openairesponses.New(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return &fakeResponsesUpstream{t: t, script: script, codec: codec}
}

func (upstream *fakeResponsesUpstream) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
		upstream.mu.Lock()
		upstream.other = append(upstream.other, r.Method+" "+r.URL.Path)
		upstream.mu.Unlock()
		http.Error(w, "fake Responses upstream serves only POST /v1/responses", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		upstream.t.Errorf("fake Responses upstream body: %v", err)
		return
	}
	request := fakeResponsesRequest{at: time.Now(), headers: r.Header.Clone(), body: body}
	if err := json.Unmarshal(body, &request.shape); err != nil {
		upstream.t.Errorf("fake Responses upstream request: %v", err)
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	upstream.mu.Lock()
	request.ordinal = len(upstream.requests)
	upstream.requests = append(upstream.requests, request)
	upstream.mu.Unlock()
	upstream.check(request)
	reply := upstream.script(request)
	for name, value := range reply.headers {
		w.Header().Set(name, value)
	}
	if reply.status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, reply.body)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for index, payload := range reply.events {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(payload), &header); err != nil || header.Type == "" {
			upstream.t.Errorf("fake Responses event is invalid: %s", payload)
			return
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", header.Type, payload); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		if reply.pause > 0 && index == reply.pauseAfter {
			select {
			case <-time.After(reply.pause):
			case <-r.Context().Done():
				upstream.abandoned.Add(1)
				return
			}
		}
	}
}

// check holds every request to what the translated path promises the
// upstream (design 15.5).
func (upstream *fakeResponsesUpstream) check(request fakeResponsesRequest) {
	t := upstream.t
	shape := request.shape
	at := fmt.Sprintf("upstream request %d", request.ordinal)
	if !shape.Stream || shape.Store == nil || *shape.Store {
		t.Errorf("%s: stream=%v store=%v, want a stateless stream", at, shape.Stream, shape.Store)
	}
	if !slices.Contains(shape.Include, "reasoning.encrypted_content") {
		t.Errorf("%s: include = %v", at, shape.Include)
	}
	if bytes.Contains(request.body, []byte("x-anthropic-billing-header")) {
		t.Errorf("%s carries Claude Code's billing line", at)
	}
	if bytes.Contains(request.body, []byte("DeferredToolPlaceholder")) {
		t.Errorf("%s carries the deferred-tool placeholder", at)
	}
	for name := range request.headers {
		lower := strings.ToLower(name)
		for _, prefix := range []string{"x-claude-code-", "anthropic-", "x-stainless-"} {
			if strings.HasPrefix(lower, prefix) {
				t.Errorf("%s carries the client header %s", at, name)
			}
		}
	}
	if agent := request.headers.Get("User-Agent"); !strings.HasPrefix(agent, "ViberMate/") {
		t.Errorf("%s User-Agent = %q", at, agent)
	}
	if request.headers.Get("Authorization") != "Bearer "+claudeAcceptanceAccount {
		t.Errorf("%s did not carry the managed account's key", at)
	}
	outputs := shape.outputs()
	for index, item := range shape.Input {
		switch item.Type {
		case "reasoning":
			if item.ID == "" || item.EncryptedContent == "" || index+1 == len(shape.Input) {
				t.Errorf("%s: reasoning item %d is not replayable: %+v", at, index, item)
			}
		case "function_call":
			if _, answered := outputs[item.CallID]; !answered {
				t.Errorf("%s: call %s has no output", at, item.CallID)
			}
		}
	}
	var params responses.ResponseNewParams
	if err := json.Unmarshal(request.body, &params); err != nil {
		t.Errorf("%s: openai-go rejects the body: %v", at, err)
	}
	if _, _, err := upstream.codec.DecodeCompatibleClientRequest(request.body); err != nil {
		t.Errorf("%s: the compatible Responses decoder rejects the body: %v", at, err)
	}
}

func (upstream *fakeResponsesUpstream) snapshot() ([]fakeResponsesRequest, []string) {
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	return slices.Clone(upstream.requests), slices.Clone(upstream.other)
}

// responsesTurn is one scripted public-API answer.
type responsesTurn struct {
	id    string
	model string
	// reasoning, when set, opens the answer with a reasoning item.
	reasoning *responsesReasoning
	text      string
	calls     []responsesCall
	usage     responsesTurnUsage
	// failAfterText ends the stream with response.failed of this code
	// after the text, instead of completing it.
	failAfterText string
}

type responsesReasoning struct {
	id, encrypted string
	parts         []string
}

type responsesCall struct {
	id, name, arguments string
}

type responsesTurnUsage struct {
	input, cached, output, reasoning int
}

// events is the public API's full event sequence for the turn.
func (turn responsesTurn) events() []string {
	sequence := 0
	var events []string
	add := func(kind, members string) {
		events = append(events, `{"type":"response.`+kind+`","sequence_number":`+strconv.Itoa(sequence)+`,`+members+`}`)
		sequence++
	}
	object := func(status, output, usage string) string {
		return `{"id":` + strconv.Quote(turn.id) + `,"object":"response","created_at":1763700000,"status":"` + status +
			`","model":` + strconv.Quote(turn.model) + `,"output":[` + output + `],"usage":` + usage + `}`
	}
	add("created", `"response":`+object("in_progress", "", "null"))
	add("in_progress", `"response":`+object("in_progress", "", "null"))
	var items []string
	index := 0
	if reasoning := turn.reasoning; reasoning != nil {
		add("output_item.added", fmt.Sprintf(`"output_index":%d,"item":{"id":%q,"type":"reasoning","summary":[]}`, index, reasoning.id))
		var summary []string
		for part, text := range reasoning.parts {
			at := fmt.Sprintf(`"item_id":%q,"output_index":%d,"summary_index":%d`, reasoning.id, index, part)
			add("reasoning_summary_part.added", at+`,"part":{"type":"summary_text","text":""}`)
			add("reasoning_summary_text.delta", at+`,"delta":`+strconv.Quote(text))
			add("reasoning_summary_text.done", at+`,"text":`+strconv.Quote(text))
			add("reasoning_summary_part.done", at+`,"part":{"type":"summary_text","text":`+strconv.Quote(text)+`}`)
			summary = append(summary, `{"type":"summary_text","text":`+strconv.Quote(text)+`}`)
		}
		item := fmt.Sprintf(`{"id":%q,"type":"reasoning","encrypted_content":%q,"summary":[%s]}`, reasoning.id, reasoning.encrypted, strings.Join(summary, ","))
		add("output_item.done", fmt.Sprintf(`"output_index":%d,"item":%s`, index, item))
		items = append(items, item)
		index++
	}
	if turn.text != "" {
		messageID := "msg_" + turn.id
		at := fmt.Sprintf(`"item_id":%q,"output_index":%d,"content_index":0`, messageID, index)
		add("output_item.added", fmt.Sprintf(`"output_index":%d,"item":{"id":%q,"type":"message","status":"in_progress","role":"assistant","content":[]}`, index, messageID))
		add("content_part.added", at+`,"part":{"type":"output_text","annotations":[],"text":""}`)
		add("output_text.delta", at+`,"delta":`+strconv.Quote(turn.text))
		add("output_text.done", at+`,"text":`+strconv.Quote(turn.text))
		add("content_part.done", at+`,"part":{"type":"output_text","annotations":[],"text":`+strconv.Quote(turn.text)+`}`)
		item := fmt.Sprintf(`{"id":%q,"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":%s}]}`, messageID, strconv.Quote(turn.text))
		add("output_item.done", fmt.Sprintf(`"output_index":%d,"item":%s`, index, item))
		items = append(items, item)
		index++
	}
	if turn.failAfterText != "" {
		add("failed", `"response":{"id":`+strconv.Quote(turn.id)+`,"object":"response","created_at":1763700000,"status":"failed","model":`+
			strconv.Quote(turn.model)+`,"output":[],"error":{"code":`+strconv.Quote(turn.failAfterText)+`,"message":"The upstream failed."}}`)
		return events
	}
	for _, call := range turn.calls {
		itemID := "fc_" + call.id
		add("output_item.added", fmt.Sprintf(`"output_index":%d,"item":{"id":%q,"type":"function_call","status":"in_progress","arguments":"","call_id":%q,"name":%q}`, index, itemID, call.id, call.name))
		add("function_call_arguments.delta", fmt.Sprintf(`"item_id":%q,"output_index":%d,"delta":%s`, itemID, index, strconv.Quote(call.arguments)))
		add("function_call_arguments.done", fmt.Sprintf(`"item_id":%q,"output_index":%d,"arguments":%s`, itemID, index, strconv.Quote(call.arguments)))
		item := fmt.Sprintf(`{"id":%q,"type":"function_call","status":"completed","arguments":%s,"call_id":%q,"name":%q}`, itemID, strconv.Quote(call.arguments), call.id, call.name)
		add("output_item.done", fmt.Sprintf(`"output_index":%d,"item":%s`, index, item))
		items = append(items, item)
		index++
	}
	usage := fmt.Sprintf(`{"input_tokens":%d,"input_tokens_details":{"cached_tokens":%d},"output_tokens":%d,"output_tokens_details":{"reasoning_tokens":%d},"total_tokens":%d}`,
		turn.usage.input, turn.usage.cached, turn.usage.output, turn.usage.reasoning, turn.usage.input+turn.usage.output)
	add("completed", `"response":`+object("completed", strings.Join(items, ","), usage))
	return events
}

// textReply answers any request with text.
func textReply(request fakeResponsesRequest, id, text string) fakeResponsesReply {
	return fakeResponsesReply{events: responsesTurn{
		id: id, model: request.shape.Model, text: text,
		usage: responsesTurnUsage{input: 120, cached: 100, output: 7, reasoning: 2},
	}.events()}
}

// openAIErrorReply is an HTTP rejection with the OpenAI error object.
func openAIErrorReply(status int, kind, code, message string, headers map[string]string) fakeResponsesReply {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": kind, "param": nil, "code": code}})
	return fakeResponsesReply{status: status, headers: headers, body: string(body)}
}

// recordingExchanges wraps the Exchange the proxy runs: it records what each
// exchange wrote downstream, keepalives included, and what it returned, and
// can drop keepalives for a negative control.
type recordingExchanges struct {
	next           exchange.Executor
	dropKeepalives atomic.Bool

	mu        sync.Mutex
	exchanges []*recordedExchange
}

type recordedExchange struct {
	mu       sync.Mutex
	events   []downstreamEvent
	done     bool
	result   exchange.Result
	err      error
	finished time.Time
}

type downstreamEvent struct {
	at    time.Time
	kind  string // begin, write, keepalive, abort
	bytes []byte
}

func (recorder *recordingExchanges) Execute(ctx context.Context, request exchange.ClientRequest, downstream exchange.Downstream) (exchange.Result, error) {
	record := &recordedExchange{}
	recorder.mu.Lock()
	recorder.exchanges = append(recorder.exchanges, record)
	recorder.mu.Unlock()
	result, err := recorder.next.Execute(ctx, request, &recordingDownstream{next: downstream, record: record, recorder: recorder})
	record.mu.Lock()
	record.done, record.result, record.err, record.finished = true, result, err, time.Now()
	record.mu.Unlock()
	return result, err
}

func (recorder *recordingExchanges) snapshot() []*recordedExchange {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return slices.Clone(recorder.exchanges)
}

func (record *recordedExchange) snapshot() []downstreamEvent {
	record.mu.Lock()
	defer record.mu.Unlock()
	return slices.Clone(record.events)
}

// notices are the codes of the exchange's translation report.
func (record *recordedExchange) notices() []string {
	record.mu.Lock()
	defer record.mu.Unlock()
	var codes []string
	for _, notice := range record.result.Translation.Notices() {
		codes = append(codes, string(notice.Code))
	}
	return codes
}

type recordingDownstream struct {
	next     exchange.Downstream
	record   *recordedExchange
	recorder *recordingExchanges
}

func (downstream *recordingDownstream) add(kind string, value []byte) {
	downstream.record.mu.Lock()
	downstream.record.events = append(downstream.record.events, downstreamEvent{at: time.Now(), kind: kind, bytes: bytes.Clone(value)})
	downstream.record.mu.Unlock()
}

func (downstream *recordingDownstream) Begin(ctx context.Context, envelope exchange.ResponseEnvelope) error {
	downstream.add("begin", nil)
	return downstream.next.Begin(ctx, envelope)
}

func (downstream *recordingDownstream) Write(ctx context.Context, value []byte) (int, error) {
	downstream.add("write", value)
	return downstream.next.Write(ctx, value)
}

func (downstream *recordingDownstream) Keepalive(ctx context.Context) error {
	if downstream.recorder.dropKeepalives.Load() {
		downstream.add("keepalive-dropped", nil)
		return nil
	}
	downstream.add("keepalive", nil)
	return downstream.next.Keepalive(ctx)
}

func (downstream *recordingDownstream) Abort(ctx context.Context, notice exchange.FailureNotice) error {
	downstream.add("abort", nil)
	return downstream.next.Abort(ctx, notice)
}
