package openairesponses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// reasoningHeartbeatInterval spaces the empty thinking deltas that keep a
// client's event watchdog alive while reasoning is not shown (design R9).
const reasoningHeartbeatInterval = 10 * time.Second

// NewProviderStream drives the client encoder it is given from a Responses
// event stream (design 7.1), through the shared Assembler, which owns block
// order, the approval barrier, held bytes and the terminal checks.
func (side *ProviderSide) NewProviderStream(
	providerContext protocolpath.ProviderContext,
	request protocolcore.Request,
	encoder protocolpath.ClientStreamEncoder,
) (protocolpath.Stream, error) {
	if !request.Stream {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$.stream",
			errors.New("request is not configured for streaming"),
		)
	}
	return side.newTranslatedStream(providerContext, request, encoder)
}

// NewAggregatingStream decodes the Responses stream that answers a
// Stream=false request (design 9): the same driver over an encoder that
// writes nothing, so Feed never returns bytes and FinishDecoded gives the
// whole response for the client side's unary encoder.
func (side *ProviderSide) NewAggregatingStream(
	providerContext protocolpath.ProviderContext,
	request protocolcore.Request,
) (protocolpath.Stream, error) {
	if request.Stream {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$.stream",
			errors.New("a streaming request is not aggregated"),
		)
	}
	return side.newTranslatedStream(providerContext, request, aggregatingEncoder{})
}

func (side *ProviderSide) newTranslatedStream(
	providerContext protocolpath.ProviderContext,
	request protocolcore.Request,
	encoder protocolpath.ClientStreamEncoder,
) (*translatedStream, error) {
	if err := side.admission.ValidateRequest(request); err != nil {
		return nil, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	request = request.Clone()
	assembler, err := protocolpath.NewAssembler(request, encoder, protocolpath.AssemblerOptions{})
	if err != nil {
		return nil, err
	}
	framer, err := newResponsesEventFramer(side.admission.options.MaxResponseBytes, side.admission.responseBudget())
	if err != nil {
		return nil, err
	}
	return &translatedStream{
		side:       side,
		request:    request,
		normalizer: side.newOutputNormalizer(providerContext, request),
		framer:     framer,
		assembler:  assembler,
		visible:    request.Reasoning.Display != protocolcore.ThinkingDisplayOmitted,
		items:      make(map[int]*streamItem),
		output:     make(map[int]json.RawMessage),
	}, nil
}

// aggregatingEncoder writes nothing. It has both optional capabilities, so
// the Assembler neither declares reasoning as unforwarded nor renders a
// refusal as text: the unary encoder that writes the aggregated response
// decides both.
type aggregatingEncoder struct{}

var (
	_ protocolpath.ReasoningStreamEncoder = aggregatingEncoder{}
	_ protocolpath.RefusalStreamEncoder   = aggregatingEncoder{}
)

func (aggregatingEncoder) Start(protocolpath.StreamStart) ([]byte, error)      { return nil, nil }
func (aggregatingEncoder) StartText(int) ([]byte, error)                       { return nil, nil }
func (aggregatingEncoder) AppendText(int, string) ([]byte, error)              { return nil, nil }
func (aggregatingEncoder) StopText(int) ([]byte, error)                        { return nil, nil }
func (aggregatingEncoder) ToolCall(int, protocolcore.ToolCall) ([]byte, error) { return nil, nil }
func (aggregatingEncoder) Terminal(protocolcore.Response) ([]byte, error)      { return nil, nil }
func (aggregatingEncoder) StartReasoning(int) ([]byte, error)                  { return nil, nil }
func (aggregatingEncoder) AppendReasoning(int, string) ([]byte, error)         { return nil, nil }
func (aggregatingEncoder) StopReasoning(int, protocolcore.Reasoning) ([]byte, error) {
	return nil, nil
}
func (aggregatingEncoder) StartRefusal(int) ([]byte, error)          { return nil, nil }
func (aggregatingEncoder) AppendRefusal(int, string) ([]byte, error) { return nil, nil }
func (aggregatingEncoder) StopRefusal(int) ([]byte, error)           { return nil, nil }
func (aggregatingEncoder) TranslationReport() protocolcore.TranslationReport {
	return protocolcore.TranslationReport{}
}

type streamItemKind uint8

const (
	streamItemReasoning streamItemKind = iota + 1
	streamItemMessage
	streamItemFunctionCall
	streamItemHosted
)

type reasoningSource uint8

const (
	reasoningSourceUnset reasoningSource = iota
	reasoningSourceSummary
	reasoningSourceContent
)

// streamItem is what the driver knows about one output item, keyed by its
// output_index.
type streamItem struct {
	kind  streamItemKind
	added bool
	done  bool
	// A reasoning item streams one source, summary or raw text, whichever
	// came first; part is the index of the part being streamed, -1 before
	// the first, and heartbeat the time of the last client byte while
	// reasoning is hidden.
	source    reasoningSource
	part      int
	heartbeat time.Time
	// parts are a message's content parts by content_index.
	parts map[int]*streamPart
}

type streamPart struct {
	refusal bool
	closed  bool
}

// translatedStream is the Responses stream driver of a composed path.
type translatedStream struct {
	mu sync.Mutex

	side       *ProviderSide
	request    protocolcore.Request
	normalizer outputNormalizer
	framer     *responsesEventFramer
	assembler  *protocolpath.Assembler
	visible    bool

	started   bool
	createdID string
	items     map[int]*streamItem
	// output holds every output_item.done item, the terminal's fallback
	// when its own output is empty.
	output       map[int]json.RawMessage
	notification *protocolcore.Failure
	// cutOff is the path of the first function call the stream completed
	// unfinished, which only an incomplete terminal may hold.
	cutOff   string
	terminal bool
	progress uint64
	failed   bool
	finished bool
}

func (stream *translatedStream) Feed(_ context.Context, fragment []byte) ([]byte, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.failed || stream.finished {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonStreamStateViolation,
			"$",
			errors.New("Responses stream is not writable"),
		)
	}
	events, err := stream.framer.feed(fragment)
	if err != nil {
		stream.failed = true
		return nil, err
	}
	var live clientBytes
	for _, event := range events {
		if isDoneMarker(event) {
			if !stream.terminal {
				stream.failed = true
				return live.Bytes(), streamViolation("Responses DONE marker precedes the terminal event")
			}
			continue
		}
		if stream.terminal {
			stream.failed = true
			return live.Bytes(), streamViolation("event arrived after the Responses terminal event")
		}
		var decoded streamEvent
		kind, err := stream.framer.admitDecoded(event, decoded.decode)
		if err != nil {
			stream.failed = true
			return live.Bytes(), err
		}
		// Every provider event is progress, keepalives included, so the idle
		// timer measures upstream silence rather than quiet client output.
		stream.progress++
		if err := stream.handle(kind, decoded, event.Data, &live); err != nil {
			stream.failed = true
			return live.Bytes(), err
		}
	}
	return live.Bytes(), nil
}

func streamViolation(message string) error {
	return protocolcore.NewFailure(protocolcore.ReasonStreamStateViolation, "$", errors.New(message))
}

func malformedEvent(path, message string) error {
	return protocolcore.NewFailure(protocolcore.ReasonMalformedEventStream, path, errors.New(message))
}

// streamEvent is the union of the event members the driver reads.
type streamEvent struct {
	Type         string          `json:"type"`
	OutputIndex  *int            `json:"output_index"`
	ContentIndex *int            `json:"content_index"`
	SummaryIndex *int            `json:"summary_index"`
	Delta        *string         `json:"delta"`
	Text         *string         `json:"text"`
	Refusal      *string         `json:"refusal"`
	Item         json.RawMessage `json:"item"`
	Part         json.RawMessage `json:"part"`
	Response     json.RawMessage `json:"response"`
	Error        json.RawMessage `json:"error"`
	// partial marks an event decoded by its type only.
	partial bool
}

// decode reads a payload once for the framer. An event whose members do
// not have the types this driver reads is still admitted by its type alone:
// it may be one the driver ignores, and handle refuses it otherwise.
func (event *streamEvent) decode(data []byte) string {
	if json.Unmarshal(data, event) == nil {
		return event.Type
	}
	*event = streamEvent{}
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &header) != nil {
		return ""
	}
	event.partial = true
	return header.Type
}

type streamPartWire struct {
	Type    string  `json:"type"`
	Text    *string `json:"text"`
	Refusal *string `json:"refusal"`
}

func (event streamEvent) outputIndex() (int, error) {
	if event.OutputIndex == nil || *event.OutputIndex < 0 || *event.OutputIndex >= protocolcore.MaxContentBlocks {
		return 0, malformedEvent("$.output_index", "Responses event output index is invalid")
	}
	return *event.OutputIndex, nil
}

func eventIndex(value *int, path string) (int, error) {
	if value == nil || *value < 0 || *value >= protocolcore.MaxContentBlocks {
		return 0, malformedEvent(path, "Responses event index is invalid")
	}
	return *value, nil
}

func (event streamEvent) delta() (string, error) {
	if event.Delta == nil {
		return "", malformedEvent("$.delta", "Responses delta is missing")
	}
	return *event.Delta, nil
}

func itemKey(index int) string { return strconv.Itoa(index) }

func partKey(index, content int) string { return strconv.Itoa(index) + "." + strconv.Itoa(content) }

// clientBytes collects the client bytes one Feed call releases.
type clientBytes struct {
	bytes.Buffer
}

// add appends assembler output, or returns its error.
func (live *clientBytes) add(encoded []byte, err error) error {
	if err != nil {
		return err
	}
	live.Write(encoded)
	return nil
}

func (stream *translatedStream) handle(kind string, event streamEvent, data []byte, live *clientBytes) error {
	switch kind {
	case "response.created", "response.queued", "response.in_progress",
		"response.output_item.added", "response.output_item.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_text.delta",
		"response.reasoning_text.delta", "response.content_part.added", "response.content_part.done",
		"response.output_text.delta", "response.output_text.done",
		"response.refusal.delta", "response.refusal.done",
		"response.output_text.annotation.added",
		"response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.completed", "response.incomplete", "response.failed", "error":
	default:
		// keepalive, response.metadata, codex.rate_limits, the .done events
		// of reasoning parts and any other event carry nothing to deliver:
		// output items are checked when they are added and completed.
		return nil
	}
	if event.partial {
		return malformedEvent("$", "Responses SSE event is invalid")
	}
	switch kind {
	case "response.created":
		return stream.created(event, live)
	case "response.queued", "response.in_progress":
		return nil
	case "response.failed":
		var failed struct {
			Error json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(event.Response, &failed)
		return stream.side.streamFailure(stream.request, "$.response.error", failed.Error)
	case "error":
		// A notification: it becomes the failure only if the stream ends
		// without a terminal, as the native relay treats it (F16).
		raw := json.RawMessage(data)
		if rawPresent(event.Error) {
			raw = event.Error
		}
		stream.notification = stream.side.streamFailure(stream.request, "$", raw)
		return nil
	case "response.completed", "response.incomplete":
		return stream.finishResponse(event, live)
	}
	// Every other event is output: the client stream starts before it.
	if err := stream.ensureStarted(live); err != nil {
		return err
	}
	index, err := event.outputIndex()
	if err != nil {
		return err
	}
	switch kind {
	case "response.output_item.added":
		return stream.itemAdded(index, event.Item)
	case "response.output_item.done":
		return stream.itemDone(index, event.Item, live)
	case "response.reasoning_summary_part.added":
		part, err := eventIndex(event.SummaryIndex, "$.summary_index")
		if err != nil {
			return err
		}
		return stream.reasoningPart(index, reasoningSourceSummary, part, live)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		source, partIndex, partPath := reasoningSourceSummary, event.SummaryIndex, "$.summary_index"
		if kind == "response.reasoning_text.delta" {
			source, partIndex, partPath = reasoningSourceContent, event.ContentIndex, "$.content_index"
		}
		part, err := eventIndex(partIndex, partPath)
		if err != nil {
			return err
		}
		delta, err := event.delta()
		if err != nil {
			return err
		}
		return stream.reasoningDelta(index, source, part, delta, live)
	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		// The arguments are taken whole from the completed item, as Codex
		// does; a call on its way still holds every later byte.
		if _, err := stream.item(index, streamItemFunctionCall); err != nil {
			return err
		}
		stream.assembler.ArmBarrier()
		return nil
	}
	content, err := eventIndex(event.ContentIndex, "$.content_index")
	if err != nil {
		return err
	}
	switch kind {
	case "response.content_part.added", "response.content_part.done":
		var part streamPartWire
		if err := json.Unmarshal(event.Part, &part); err != nil {
			return malformedEvent("$.part", "Responses content part is invalid")
		}
		switch part.Type {
		case "reasoning_text":
			// Raw reasoning text announced as a content part.
			if kind == "response.content_part.done" {
				return nil
			}
			return stream.reasoningPart(index, reasoningSourceContent, content, live)
		case "output_text", "refusal":
			refusal := part.Type == "refusal"
			if kind == "response.content_part.added" {
				return stream.partDelta(index, content, refusal, "", live)
			}
			final := part.Text
			if refusal {
				final = part.Refusal
			}
			if final == nil {
				return malformedEvent("$.part", "Responses content part has no final value")
			}
			return stream.partDone(index, content, refusal, *final, live)
		default:
			return unsupportedOutputItem("$.part.type", "Responses output content type is unsupported")
		}
	case "response.output_text.delta", "response.refusal.delta":
		delta, err := event.delta()
		if err != nil {
			return err
		}
		return stream.partDelta(index, content, kind == "response.refusal.delta", delta, live)
	case "response.output_text.done":
		if event.Text == nil {
			return malformedEvent("$.text", "Responses output text has no final value")
		}
		return stream.partDone(index, content, false, *event.Text, live)
	case "response.refusal.done":
		if event.Refusal == nil {
			return malformedEvent("$.refusal", "Responses refusal has no final value")
		}
		return stream.partDone(index, content, true, *event.Refusal, live)
	case "response.output_text.annotation.added":
		if _, err := stream.item(index, streamItemMessage); err != nil {
			return err
		}
		stream.assembler.Notice(protocolcore.TranslationNotice{
			Code: protocolcore.NoticeCitationsNotForwarded,
			Path: fmt.Sprintf("$.output[%d].content[%d].annotations", index, content),
		})
		return nil
	}
	return nil
}

// created starts the client stream with the provider's identity. It must be
// the stream's first output.
func (stream *translatedStream) created(event streamEvent, live *clientBytes) error {
	var response struct {
		ID        string `json:"id"`
		CreatedAt int64  `json:"created_at"`
		Model     string `json:"model"`
	}
	if err := json.Unmarshal(event.Response, &response); err != nil || response.ID == "" {
		return malformedEvent("$.response", "Responses created event is invalid")
	}
	if stream.started {
		return streamViolation("response.created arrived after the stream started")
	}
	model := response.Model
	if model == "" {
		model = stream.request.EffectiveModel
	}
	stream.createdID = response.ID
	return stream.start(protocolpath.StreamStart{
		ResponseID:    response.ID,
		CreatedAtUnix: max(0, response.CreatedAt),
		ReportedModel: model,
	}, live)
}

func (stream *translatedStream) start(start protocolpath.StreamStart, live *clientBytes) error {
	if err := live.add(stream.assembler.Start(start)); err != nil {
		return err
	}
	stream.started = true
	return nil
}

// ensureStarted begins a stream whose first event is output, as the minimal
// ChatGPT-style stream is, with a provisional client message id. The neutral
// response's id still comes from the terminal.
func (stream *translatedStream) ensureStarted(live *clientBytes) error {
	if stream.started {
		return nil
	}
	return stream.start(protocolpath.StreamStart{
		ResponseID:    stream.side.messageID(),
		ReportedModel: stream.request.EffectiveModel,
	}, live)
}

// item returns the state of output index, creating it on its first event. An
// index keeps the kind its first event gave it.
func (stream *translatedStream) item(index int, kind streamItemKind) (*streamItem, error) {
	item := stream.items[index]
	if item == nil {
		item = &streamItem{kind: kind, part: -1}
		if kind == streamItemMessage {
			item.parts = make(map[int]*streamPart)
		}
		stream.items[index] = item
		return item, nil
	}
	if item.kind != kind {
		return nil, streamViolation("Responses output item changed its type")
	}
	if item.done {
		return nil, streamViolation("event arrived for a completed output item")
	}
	return item, nil
}

// itemKind classifies an output item by its type. Items the client cannot be
// given fail closed before anything of them is delivered.
func itemKind(raw json.RawMessage, path string) (streamItemKind, error) {
	kind, err := peekType(raw)
	if err != nil {
		return 0, invalidProvider(path, err)
	}
	switch kind {
	case "reasoning":
		return streamItemReasoning, nil
	case "message":
		return streamItemMessage, nil
	case "function_call":
		return streamItemFunctionCall, nil
	case "custom_tool_call":
		return 0, unsupportedOutputItem(path+".type", "custom tool calls cannot be delivered to this client")
	default:
		if isOpaqueResponsesProviderOutputItem(kind) {
			return streamItemHosted, nil
		}
		return 0, unsupportedOutputItem(path+".type", "Responses output item type cannot be delivered to this client")
	}
}

func (stream *translatedStream) itemAdded(index int, raw json.RawMessage) error {
	kind, err := itemKind(raw, fmt.Sprintf("$.output[%d]", index))
	if err != nil {
		return err
	}
	item, err := stream.item(index, kind)
	if err != nil {
		return err
	}
	if item.added {
		return streamViolation("Responses output item was added twice")
	}
	item.added = true
	if kind == streamItemFunctionCall {
		// From the first call on, every client byte waits for the tool
		// decision (design 7.2).
		stream.assembler.ArmBarrier()
	}
	return nil
}

// reasoningPart moves a reasoning item to part of source: the first part
// opens the thinking block, a later one sends the separator the normaliser
// joins parts with. Parts of the other source are not streamed.
func (stream *translatedStream) reasoningPart(index int, source reasoningSource, part int, live *clientBytes) error {
	item, err := stream.item(index, streamItemReasoning)
	if err != nil {
		return err
	}
	if item.source == reasoningSourceUnset {
		item.source = source
	}
	if item.source != source || item.part == part {
		return nil
	}
	key := itemKey(index)
	if item.part < 0 {
		item.part = part
		item.heartbeat = stream.side.now()
		return live.add(stream.assembler.Reasoning(key, ""))
	}
	item.part = part
	// With display omitted the Assembler drops the separator as it drops
	// all reasoning text.
	return live.add(stream.assembler.Reasoning(key, reasoningPartSeparator))
}

// reasoningDelta streams reasoning text, or, while reasoning is hidden, an
// empty heartbeat at most every ten seconds.
func (stream *translatedStream) reasoningDelta(index int, source reasoningSource, part int, delta string, live *clientBytes) error {
	if err := stream.reasoningPart(index, source, part, live); err != nil {
		return err
	}
	item := stream.items[index]
	if item.source != source {
		return nil
	}
	key := itemKey(index)
	if stream.visible {
		if delta == "" {
			return nil
		}
		return live.add(stream.assembler.Reasoning(key, delta))
	}
	now := stream.side.now()
	if now.Sub(item.heartbeat) < reasoningHeartbeatInterval {
		return nil
	}
	item.heartbeat = now
	return live.add(stream.assembler.Reasoning(key, ""))
}

func (stream *translatedStream) messagePart(index, content int, refusal bool) (*streamPart, error) {
	item, err := stream.item(index, streamItemMessage)
	if err != nil {
		return nil, err
	}
	part := item.parts[content]
	if part == nil {
		part = &streamPart{refusal: refusal}
		item.parts[content] = part
	}
	if part.refusal != refusal {
		return nil, streamViolation("Responses content part changed its type")
	}
	return part, nil
}

// partDelta streams text or refusal text of a message part; an empty delta
// only opens the block.
func (stream *translatedStream) partDelta(index, content int, refusal bool, delta string, live *clientBytes) error {
	part, err := stream.messagePart(index, content, refusal)
	if err != nil {
		return err
	}
	if part.closed {
		return streamViolation("delta arrived for a completed content part")
	}
	key := partKey(index, content)
	if refusal {
		return live.add(stream.assembler.Refusal(key, delta))
	}
	return live.add(stream.assembler.Text(key, delta))
}

// partDone closes a message part with its final value, once: the done events
// of a part repeat it, and the terminal check compares every block with the
// terminal snapshot anyway.
func (stream *translatedStream) partDone(index, content int, refusal bool, final string, live *clientBytes) error {
	part, err := stream.messagePart(index, content, refusal)
	if err != nil {
		return err
	}
	if part.closed {
		return nil
	}
	part.closed = true
	key := partKey(index, content)
	if refusal {
		return live.add(stream.assembler.RefusalDone(key, final))
	}
	return live.add(stream.assembler.TextDone(key, final))
}

func (stream *translatedStream) itemDone(index int, raw json.RawMessage, live *clientBytes) error {
	if !rawPresent(raw) {
		return malformedEvent("$.item", "Responses completed output item is invalid")
	}
	if _, duplicate := stream.output[index]; duplicate {
		return protocolcore.NewFailure(
			protocolcore.ReasonStreamStateViolation,
			"$.output_index",
			errors.New("Responses completed output index is duplicated"),
		)
	}
	stream.output[index] = bytes.Clone(raw)
	return stream.completeItem(index, raw, live)
}

// completeItem delivers a completed output item through the normaliser:
// every part of it the stream has not closed yet, whole when no delta came
// (ChatGPT sends only item.done).
func (stream *translatedStream) completeItem(index int, raw json.RawMessage, live *clientBytes) error {
	path := fmt.Sprintf("$.output[%d]", index)
	kind, err := itemKind(raw, path)
	if err != nil {
		return err
	}
	item, err := stream.item(index, kind)
	if err != nil {
		return err
	}
	if kind == streamItemFunctionCall {
		stream.assembler.ArmBarrier()
	}
	normalized, err := stream.normalizer.item(raw, path)
	if err != nil {
		return err
	}
	for _, notice := range normalized.notices {
		stream.assembler.Notice(notice)
	}
	key := itemKey(index)
	switch kind {
	case streamItemReasoning:
		if err := live.add(stream.assembler.ReasoningDone(key, *normalized.blocks[0].Reasoning)); err != nil {
			return err
		}
	case streamItemFunctionCall:
		if normalized.cutOff {
			// Nothing of it reaches the client; the terminal must say the
			// response ended incomplete (finishResponse checks).
			if stream.cutOff == "" {
				stream.cutOff = path
			}
			break
		}
		if err := live.add(stream.assembler.ToolCall(key, normalized.blocks[0].ToolCall)); err != nil {
			return err
		}
	case streamItemMessage:
		for content := range item.parts {
			if content >= len(normalized.blocks) {
				return streamViolation("a streamed content part is missing from its completed message")
			}
		}
		for content, block := range normalized.blocks {
			refusal := block.Kind == protocolcore.BlockRefusal
			final := block.Text
			if refusal {
				final = block.Refusal
			}
			if err := stream.partDone(index, content, refusal, final, live); err != nil {
				return err
			}
		}
	}
	item.done = true
	return nil
}

// finishResponse decodes the terminal through the normaliser, with the
// completed items standing in for an empty output, and hands it to the
// Assembler, which checks it against every streamed block in both
// directions. An item the terminal holds but the stream never completed,
// such as a message cut off by an incomplete terminal, is completed from the
// snapshot first, so its open block closes with its final text; a function
// call cut off the same way is dropped and declared.
func (stream *translatedStream) finishResponse(event streamEvent, live *clientBytes) error {
	if !rawPresent(event.Response) {
		return malformedEvent("$.response", "Responses terminal event is invalid")
	}
	completed, err := stream.completedOutput()
	if err != nil {
		return err
	}
	response, output, notices, err := stream.normalizer.terminal(event.Response, completed)
	if err != nil {
		return err
	}
	if stream.createdID != "" && stream.createdID != response.ID {
		return streamViolation("Responses terminal names another response than response.created")
	}
	if err := stream.ensureStarted(live); err != nil {
		return err
	}
	for index, raw := range output {
		if item := stream.items[index]; item != nil && item.done {
			continue
		}
		if err := stream.completeItem(index, raw, live); err != nil {
			return err
		}
	}
	// A call the stream completed unfinished may be missing from a terminal
	// output that the normaliser checked, so it is checked here too.
	if stream.cutOff != "" && !endedIncomplete(response.StopReason) {
		return cutOffCallError(stream.cutOff)
	}
	for _, notice := range notices {
		stream.assembler.Notice(notice)
	}
	if err := stream.assembler.Terminal(response); err != nil {
		return err
	}
	stream.terminal = true
	return nil
}

func (stream *translatedStream) completedOutput() ([]json.RawMessage, error) {
	if len(stream.output) == 0 {
		return nil, nil
	}
	output := make([]json.RawMessage, len(stream.output))
	for index := range output {
		raw, present := stream.output[index]
		if !present {
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonStreamStateViolation,
				"$.output_index",
				errors.New("Responses completed output items are not contiguous"),
			)
		}
		output[index] = raw
	}
	return output, nil
}

func (stream *translatedStream) SemanticProgress() uint64 {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.progress
}

func (stream *translatedStream) TerminalReceived() bool {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.terminal && !stream.failed
}

func (stream *translatedStream) FinishDecoded(context.Context) (protocolpath.PendingTerminal, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.failed || stream.finished {
		return nil, streamViolation("Responses stream cannot be finished")
	}
	if err := stream.framer.finish(); err != nil {
		stream.failed = true
		return nil, err
	}
	if !stream.terminal {
		stream.failed = true
		if stream.notification != nil {
			return nil, stream.notification
		}
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonTruncatedEventStream,
			"$",
			errors.New("Responses stream has no terminal event"),
		)
	}
	terminal, err := stream.assembler.Finish()
	if err != nil {
		stream.failed = true
		return nil, err
	}
	stream.finished = true
	return terminal, nil
}
