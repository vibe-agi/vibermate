package protocolpath

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"hash"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

const (
	// DefaultAssemblerMaxHeldBytes bounds the client bytes held behind the
	// tool-approval barrier, as the Chat driver's held suffix.
	DefaultAssemblerMaxHeldBytes = 8 << 20
	// DefaultAssemblerMaxInterleaveBytes bounds deltas buffered for keys whose
	// block cannot open yet.
	DefaultAssemblerMaxInterleaveBytes = 1 << 20
)

type AssemblerOptions struct {
	MaxHeldBytes       int // zero means DefaultAssemblerMaxHeldBytes
	MaxInterleaveBytes int // zero means DefaultAssemblerMaxInterleaveBytes
}

type assembledKind uint8

const (
	assembledText assembledKind = iota + 1
	assembledRefusal
	assembledReasoning
	assembledToolCall
)

// assembledBlock is one block of the assembled response, in the order its
// first event arrived. A reasoning block the client encoder cannot carry has
// no client index.
type assembledBlock struct {
	key       string
	kind      assembledKind
	index     int
	forwarded bool
	open      bool
	closed    bool
	// Running digest and length of the text the client received, so the
	// final value is checked without a second copy of streamed text.
	streamed      hash.Hash
	streamedBytes int
	digest        [32]byte
	length        int
	reasoning     reasoningFacts
	call          protocolcore.ToolCall
}

// reasoningFacts identify a completed reasoning value without retaining it.
type reasoningFacts struct {
	text       [32]byte
	textBytes  int
	textSource protocolcore.ReasoningTextSource
	redacted   bool
	seal       [32]byte
	sealed     bool
}

// writeString hashes a string through a small buffer: SHA-256 has no
// WriteString, and converting a long final text to bytes would copy it.
func writeString(digest hash.Hash, value string) {
	var chunk [4096]byte
	for value != "" {
		count := copy(chunk[:], value)
		_, _ = digest.Write(chunk[:count])
		value = value[count:]
	}
}

func sumString(value string) [32]byte {
	digest := sha256.New()
	writeString(digest, value)
	var sum [32]byte
	copy(sum[:], digest.Sum(nil))
	return sum
}

func factsOf(reasoning protocolcore.Reasoning) reasoningFacts {
	source := reasoning.TextSource
	if source == protocolcore.ReasoningTextSummary {
		source = ""
	}
	facts := reasoningFacts{
		text: sumString(reasoning.Text), textBytes: len(reasoning.Text),
		textSource: source, redacted: reasoning.Redacted, sealed: !reasoning.Seal.IsZero(),
	}
	if facts.sealed {
		facts.seal = protocolcore.SealDigest(reasoning.Seal)
	}
	return facts
}

type assemblerEventKind uint8

const (
	eventText assemblerEventKind = iota + 1
	eventRefusal
	eventTextDone
	eventReasoning
	eventReasoningDone
	eventToolCall
)

type assemblerEvent struct {
	kind      assemblerEventKind
	key       string
	text      string
	reasoning protocolcore.Reasoning
	call      protocolcore.ToolCall
}

func (event assemblerEvent) interleaveBytes() int {
	switch event.kind {
	case eventToolCall:
		return 0
	case eventReasoningDone:
		return len(event.reasoning.Text) + len(event.reasoning.Seal.Opaque)
	default:
		return len(event.text)
	}
}

func (event assemblerEvent) heldBytes() int {
	if event.kind != eventToolCall {
		return 0
	}
	return event.call.Arguments.ByteLen() + len(event.call.Name) + len(event.call.Input)
}

// Assembler is the stream state machine a provider stream drives, written
// once for every composed path. It turns provider-facing events into client
// bytes through the client encoder it wraps and enforces, in one place:
//
//  1. one open client block at a time, with contiguous indexes from 0;
//     events for another key are buffered, bounded, and replayed in arrival
//     order when the open block closes;
//  2. the approval barrier: from ArmBarrier or the first ToolCall every later
//     client byte, the terminal included, is held for the tool decision;
//  3. text equality without a second copy: each streamed block keeps a
//     running digest, a final value repairs a missing suffix, and the
//     terminal must match every block;
//  4. a two-way tool check: terminal tool calls equal the streamed ones;
//  5. an empty response still gives the client one empty text block;
//  6. refusals use the encoder's refusal capability or become text;
//  7. reasoning uses the encoder's reasoning capability or stays only in the
//     terminal, and its text is never shown when display is omitted;
//  8. every provider event advances SemanticProgress.
//
// Methods return the client bytes that are safe to write now.
type Assembler struct {
	mu sync.Mutex

	encoder   ClientStreamEncoder
	reasoner  ReasoningStreamEncoder
	refuser   RefusalStreamEncoder
	visible   bool
	maxHeld   int
	maxBuffer int

	started   bool
	barrier   bool
	held      bytes.Buffer
	blocks    map[string]*assembledBlock
	order     []*assembledBlock
	open      *assembledBlock
	nextIndex int
	calls     map[protocolcore.CallKey]struct{}

	pending       []assemblerEvent
	pendingBytes  int
	pendingCalled int

	terminal *protocolcore.Response
	finished bool
	failed   bool
	notices  []protocolcore.TranslationNotice

	progress atomic.Uint64
}

func NewAssembler(request protocolcore.Request, encoder ClientStreamEncoder, options AssemblerOptions) (*Assembler, error) {
	if encoder == nil {
		return nil, errors.New("client stream encoder is required")
	}
	if options.MaxHeldBytes < 0 || options.MaxInterleaveBytes < 0 {
		return nil, errors.New("assembler limits cannot be negative")
	}
	if options.MaxHeldBytes == 0 {
		options.MaxHeldBytes = DefaultAssemblerMaxHeldBytes
	}
	if options.MaxInterleaveBytes == 0 {
		options.MaxInterleaveBytes = DefaultAssemblerMaxInterleaveBytes
	}
	assembler := &Assembler{
		encoder:   encoder,
		visible:   request.Reasoning.Display != protocolcore.ThinkingDisplayOmitted,
		maxHeld:   options.MaxHeldBytes,
		maxBuffer: options.MaxInterleaveBytes,
		blocks:    make(map[string]*assembledBlock),
		calls:     make(map[protocolcore.CallKey]struct{}),
	}
	assembler.reasoner, _ = encoder.(ReasoningStreamEncoder)
	assembler.refuser, _ = encoder.(RefusalStreamEncoder)
	return assembler, nil
}

// SemanticProgress counts provider events, keepalives included, so an idle
// timer measures upstream silence rather than quiet client output.
func (assembler *Assembler) SemanticProgress() uint64 {
	return assembler.progress.Load()
}

// Progress records a provider event that produces no client bytes.
func (assembler *Assembler) Progress() {
	assembler.progress.Add(1)
}

// Notice adds a translation notice to the terminal's report, once.
func (assembler *Assembler) Notice(notice protocolcore.TranslationNotice) {
	assembler.mu.Lock()
	defer assembler.mu.Unlock()
	assembler.notice(notice.Code, notice.Path)
}

func (assembler *Assembler) notice(code protocolcore.NoticeCode, path string) {
	notice := protocolcore.TranslationNotice{Code: code, Path: path}
	if !slices.Contains(assembler.notices, notice) {
		assembler.notices = append(assembler.notices, notice)
	}
}

// Start begins the client stream. Later calls are no-ops, so a stream may be
// started provisionally before the provider's own start event arrives.
func (assembler *Assembler) Start(start StreamStart) ([]byte, error) {
	assembler.mu.Lock()
	defer assembler.mu.Unlock()
	assembler.progress.Add(1)
	if err := assembler.usable(); err != nil {
		return nil, err
	}
	if assembler.started {
		return nil, nil
	}
	encoded, err := assembler.encoder.Start(start)
	if err != nil {
		return nil, assembler.fail(err)
	}
	assembler.started = true
	var live bytes.Buffer
	if err := assembler.write(&live, encoded); err != nil {
		return nil, assembler.fail(err)
	}
	return live.Bytes(), nil
}

func (assembler *Assembler) Text(key, delta string) ([]byte, error) {
	return assembler.submit(assemblerEvent{kind: eventText, key: key, text: delta})
}

func (assembler *Assembler) Refusal(key, delta string) ([]byte, error) {
	return assembler.submit(assemblerEvent{kind: eventRefusal, key: key, text: delta})
}

// TextDone closes a text or refusal block with its final value: a missing
// suffix of the streamed text is emitted, a block that streamed nothing is
// emitted whole, and streamed text that is not a prefix of final fails.
func (assembler *Assembler) TextDone(key, final string) ([]byte, error) {
	return assembler.submit(assemblerEvent{kind: eventTextDone, key: key, text: final})
}

// Reasoning streams reasoning text. An empty delta opens the block, or is a
// heartbeat on an open one; with display omitted text is never forwarded.
func (assembler *Assembler) Reasoning(key, delta string) ([]byte, error) {
	return assembler.submit(assemblerEvent{kind: eventReasoning, key: key, text: delta})
}

// ReasoningDone closes a reasoning block with its complete value, seal
// included.
func (assembler *Assembler) ReasoningDone(key string, value protocolcore.Reasoning) ([]byte, error) {
	return assembler.submit(assemblerEvent{kind: eventReasoningDone, key: key, reasoning: value})
}

// ArmBarrier holds every later client byte for the tool decision. A provider
// calls it as soon as it knows a tool call is coming.
func (assembler *Assembler) ArmBarrier() {
	assembler.mu.Lock()
	defer assembler.mu.Unlock()
	assembler.progress.Add(1)
	assembler.barrier = true
}

// ToolCall adds one complete tool call. It arms the barrier at once, even
// when the call itself waits for an open block.
func (assembler *Assembler) ToolCall(key string, call protocolcore.ToolCall) ([]byte, error) {
	return assembler.submit(assemblerEvent{kind: eventToolCall, key: key, call: call.Clone()})
}

func (assembler *Assembler) usable() error {
	switch {
	case assembler.failed:
		return assemblerViolation("stream assembler already failed")
	case assembler.finished:
		return assemblerViolation("stream assembler already finished")
	case assembler.terminal != nil:
		return assemblerViolation("event arrived after the terminal")
	}
	return nil
}

func (assembler *Assembler) fail(err error) error {
	assembler.failed = true
	return err
}

func (assembler *Assembler) submit(event assemblerEvent) ([]byte, error) {
	assembler.mu.Lock()
	defer assembler.mu.Unlock()
	assembler.progress.Add(1)
	if err := assembler.usable(); err != nil {
		return nil, err
	}
	if !assembler.started {
		return nil, assembler.fail(assemblerViolation("content arrived before the stream started"))
	}
	if event.kind == eventToolCall {
		if err := event.call.Validate(); err != nil {
			return nil, assembler.fail(protocolcore.NewFailure(protocolcore.ReasonInvalidProviderResponse, "$.tool_call", err))
		}
		if _, duplicate := assembler.calls[event.call.Key]; duplicate {
			return nil, assembler.fail(assemblerViolation("tool call identity is duplicated"))
		}
		assembler.calls[event.call.Key] = struct{}{}
		// The barrier holds from the call's arrival, not from its replay: a
		// call that waits for an open block must not let the rest of that
		// block reach the client before the tool decision.
		assembler.barrier = true
	}
	if event.kind == eventReasoningDone {
		if err := event.reasoning.Validate(); err != nil {
			return nil, assembler.fail(protocolcore.NewFailure(protocolcore.ReasonInvalidProviderResponse, "$.reasoning", err))
		}
	}
	var live bytes.Buffer
	if assembler.mustWait(event) {
		if err := assembler.buffer(event); err != nil {
			return nil, assembler.fail(err)
		}
		return nil, nil
	}
	if err := assembler.apply(event, &live); err != nil {
		return nil, assembler.fail(err)
	}
	if err := assembler.drain(&live); err != nil {
		return nil, assembler.fail(err)
	}
	return live.Bytes(), nil
}

// forwardsAsBlock reports an event that occupies a client block.
func (assembler *Assembler) forwardsAsBlock(event assemblerEvent) bool {
	if event.kind == eventReasoning || event.kind == eventReasoningDone {
		return assembler.reasoner != nil
	}
	return true
}

func (assembler *Assembler) mustWait(event assemblerEvent) bool {
	return assembler.forwardsAsBlock(event) && assembler.open != nil && assembler.open.key != event.key
}

func (assembler *Assembler) buffer(event assemblerEvent) error {
	assembler.pendingBytes += event.interleaveBytes()
	if assembler.pendingBytes > assembler.maxBuffer {
		return protocolcore.NewFailure(protocolcore.ReasonStreamLimitExceeded, "$",
			errors.New("interleaved stream content exceeds the buffer limit"))
	}
	// A waiting tool call is held once replayed, so it is charged to the held
	// bound rather than to the interleave buffer.
	assembler.pendingCalled += event.heldBytes()
	if assembler.held.Len()+assembler.pendingCalled > assembler.maxHeld {
		return protocolcore.NewFailure(protocolcore.ReasonStreamLimitExceeded, "$",
			errors.New("held stream content exceeds the configured limit"))
	}
	assembler.pending = append(assembler.pending, event)
	return nil
}

// drain replays buffered events in arrival order: events for the open block
// apply at once, and every close restarts from the oldest waiting event.
func (assembler *Assembler) drain(live *bytes.Buffer) error {
	for index := 0; index < len(assembler.pending); {
		event := assembler.pending[index]
		if assembler.mustWait(event) {
			index++
			continue
		}
		assembler.pending = slices.Delete(assembler.pending, index, index+1)
		assembler.pendingBytes -= event.interleaveBytes()
		assembler.pendingCalled -= event.heldBytes()
		if err := assembler.apply(event, live); err != nil {
			return err
		}
		if assembler.open == nil {
			index = 0
		}
	}
	return nil
}

// write sends encoder bytes to the client now, or holds them once the
// barrier is armed.
func (assembler *Assembler) write(live *bytes.Buffer, encoded []byte) error {
	if !assembler.barrier {
		live.Write(encoded)
		return nil
	}
	if assembler.held.Len()+len(encoded) > assembler.maxHeld {
		return protocolcore.NewFailure(protocolcore.ReasonStreamLimitExceeded, "$",
			errors.New("held stream content exceeds the configured limit"))
	}
	assembler.held.Write(encoded)
	return nil
}

func (assembler *Assembler) block(event assemblerEvent) (*assembledBlock, error) {
	kind := assembledText
	switch event.kind {
	case eventRefusal:
		kind = assembledRefusal
	case eventReasoning, eventReasoningDone:
		kind = assembledReasoning
	case eventToolCall:
		kind = assembledToolCall
	}
	block := assembler.blocks[event.key]
	if block == nil {
		block = &assembledBlock{key: event.key, kind: kind, index: -1, streamed: sha256.New()}
		assembler.blocks[event.key] = block
		assembler.order = append(assembler.order, block)
		return block, nil
	}
	if block.closed {
		return nil, assemblerViolation("event arrived for a closed block")
	}
	textual := func(kind assembledKind) bool { return kind == assembledText || kind == assembledRefusal }
	if block.kind != kind && !(event.kind == eventTextDone && textual(block.kind)) {
		return nil, assemblerViolation("event kind changed within a block")
	}
	return block, nil
}

func (assembler *Assembler) apply(event assemblerEvent, live *bytes.Buffer) error {
	block, err := assembler.block(event)
	if err != nil {
		return err
	}
	switch event.kind {
	case eventToolCall:
		return assembler.applyToolCall(block, event.call, live)
	case eventReasoning, eventReasoningDone:
		if assembler.reasoner == nil {
			return assembler.applyUnforwardedReasoning(block, event)
		}
		return assembler.applyReasoning(block, event, live)
	default:
		return assembler.applyText(block, event, live)
	}
}

func (assembler *Assembler) openBlock(block *assembledBlock, live *bytes.Buffer) error {
	block.index = assembler.nextIndex
	assembler.nextIndex++
	block.forwarded, block.open = true, true
	assembler.open = block
	var encoded []byte
	var err error
	switch {
	case block.kind == assembledReasoning:
		encoded, err = assembler.reasoner.StartReasoning(block.index)
	case block.kind == assembledRefusal && assembler.refuser != nil:
		encoded, err = assembler.refuser.StartRefusal(block.index)
	default:
		encoded, err = assembler.encoder.StartText(block.index)
	}
	if err != nil {
		return err
	}
	return assembler.write(live, encoded)
}

func (assembler *Assembler) appendText(block *assembledBlock, text string, live *bytes.Buffer) error {
	var encoded []byte
	var err error
	switch {
	case block.kind == assembledReasoning:
		encoded, err = assembler.reasoner.AppendReasoning(block.index, text)
	case block.kind == assembledRefusal && assembler.refuser != nil:
		encoded, err = assembler.refuser.AppendRefusal(block.index, text)
	default:
		encoded, err = assembler.encoder.AppendText(block.index, text)
	}
	if err != nil {
		return err
	}
	return assembler.write(live, encoded)
}

// stream records visible text in the block's running digest and sends it.
func (assembler *Assembler) stream(block *assembledBlock, text string, live *bytes.Buffer) error {
	writeString(block.streamed, text)
	block.streamedBytes += len(text)
	return assembler.appendText(block, text, live)
}

// repair checks that the streamed text is a prefix of final and sends the
// rest.
func (assembler *Assembler) repair(block *assembledBlock, final string, live *bytes.Buffer) error {
	if block.streamedBytes > len(final) {
		return assemblerViolation("streamed text is longer than its final value")
	}
	prefix := sumString(final[:block.streamedBytes])
	if !bytes.Equal(prefix[:], block.streamed.Sum(nil)) {
		return assemblerViolation("streamed text is not a prefix of its final value")
	}
	if suffix := final[block.streamedBytes:]; suffix != "" {
		if err := assembler.stream(block, suffix, live); err != nil {
			return err
		}
	}
	return nil
}

func (assembler *Assembler) applyText(block *assembledBlock, event assemblerEvent, live *bytes.Buffer) error {
	if block.kind == assembledRefusal && assembler.refuser == nil {
		assembler.notice(protocolcore.NoticeRefusalRenderedAsText, "$")
	}
	if !block.open {
		if err := assembler.openBlock(block, live); err != nil {
			return err
		}
	}
	if event.kind != eventTextDone {
		if event.text == "" {
			return nil
		}
		return assembler.stream(block, event.text, live)
	}
	if err := assembler.repair(block, event.text, live); err != nil {
		return err
	}
	var encoded []byte
	var err error
	if block.kind == assembledRefusal && assembler.refuser != nil {
		encoded, err = assembler.refuser.StopRefusal(block.index)
	} else {
		encoded, err = assembler.encoder.StopText(block.index)
	}
	if err != nil {
		return err
	}
	assembler.close(block)
	// After the repair the running digest covers exactly the final text.
	copy(block.digest[:], block.streamed.Sum(nil))
	block.length = block.streamedBytes
	return assembler.write(live, encoded)
}

func (assembler *Assembler) applyReasoning(block *assembledBlock, event assemblerEvent, live *bytes.Buffer) error {
	wasOpen := block.open
	if !wasOpen {
		if err := assembler.openBlock(block, live); err != nil {
			return err
		}
	}
	if event.kind == eventReasoning {
		switch {
		case event.text == "" && wasOpen:
			// A heartbeat: an empty delta keeps the client's event watchdog
			// alive while reasoning is not shown.
			return assembler.appendText(block, "", live)
		case event.text == "" || !assembler.visible:
			return nil
		default:
			return assembler.stream(block, event.text, live)
		}
	}
	if assembler.visible {
		if err := assembler.repair(block, event.reasoning.Text, live); err != nil {
			return err
		}
	}
	encoded, err := assembler.reasoner.StopReasoning(block.index, event.reasoning)
	if err != nil {
		return err
	}
	assembler.close(block)
	block.reasoning = factsOf(event.reasoning)
	return assembler.write(live, encoded)
}

// applyUnforwardedReasoning keeps reasoning only in the terminal when the
// client encoder has no reasoning capability.
func (assembler *Assembler) applyUnforwardedReasoning(block *assembledBlock, event assemblerEvent) error {
	assembler.notice(protocolcore.NoticeReasoningContentNotForwarded, "$")
	if event.kind == eventReasoningDone {
		block.closed = true
		block.reasoning = factsOf(event.reasoning)
	}
	return nil
}

func (assembler *Assembler) applyToolCall(block *assembledBlock, call protocolcore.ToolCall, live *bytes.Buffer) error {
	assembler.barrier = true
	block.index = assembler.nextIndex
	assembler.nextIndex++
	block.forwarded, block.closed, block.call = true, true, call
	encoded, err := assembler.encoder.ToolCall(block.index, call)
	if err != nil {
		return err
	}
	return assembler.write(live, encoded)
}

func (assembler *Assembler) close(block *assembledBlock) {
	block.open, block.closed = false, true
	if assembler.open == block {
		assembler.open = nil
	}
}

// Terminal records the provider's terminal response. Every streamed block
// must match it, in order, and every terminal block must have been streamed.
// It returns no bytes: terminal bytes always leave through Finish's release.
func (assembler *Assembler) Terminal(response protocolcore.Response) error {
	assembler.mu.Lock()
	defer assembler.mu.Unlock()
	assembler.progress.Add(1)
	if err := assembler.usable(); err != nil {
		return err
	}
	if !assembler.started {
		return assembler.fail(assemblerViolation("terminal arrived before the stream started"))
	}
	if assembler.open != nil || len(assembler.pending) != 0 {
		return assembler.fail(assemblerViolation("terminal arrived while a block was open"))
	}
	if err := assembler.matchTerminal(response); err != nil {
		return assembler.fail(err)
	}
	cloned := response.Clone()
	assembler.terminal = &cloned
	return nil
}

func (assembler *Assembler) matchTerminal(response protocolcore.Response) error {
	var terminal []protocolcore.ContentBlock
	for _, block := range response.Blocks {
		switch block.Kind {
		case protocolcore.BlockText, protocolcore.BlockRefusal, protocolcore.BlockReasoning, protocolcore.BlockToolCall:
			terminal = append(terminal, block)
		}
	}
	if len(terminal) != len(assembler.order) {
		return assemblerViolation("terminal blocks differ from the streamed blocks")
	}
	for index, block := range assembler.order {
		if !block.closed {
			return assemblerViolation("a streamed block was never completed")
		}
		if !block.matches(terminal[index]) {
			if block.kind == assembledToolCall || terminal[index].Kind == protocolcore.BlockToolCall {
				return protocolcore.NewFailure(protocolcore.ReasonToolCallIncomplete, "$.output",
					errors.New("terminal tool calls differ from the streamed tool calls"))
			}
			return assemblerViolation("terminal content differs from the streamed content")
		}
	}
	return nil
}

func (block *assembledBlock) matches(terminal protocolcore.ContentBlock) bool {
	text := func(value string) bool {
		return len(value) == block.length && sumString(value) == block.digest
	}
	switch block.kind {
	case assembledText:
		return terminal.Kind == protocolcore.BlockText && text(terminal.Text)
	case assembledRefusal:
		return terminal.Kind == protocolcore.BlockRefusal && text(terminal.Refusal)
	case assembledReasoning:
		return terminal.Kind == protocolcore.BlockReasoning && terminal.Reasoning != nil &&
			factsOf(*terminal.Reasoning) == block.reasoning
	case assembledToolCall:
		return terminal.Kind == protocolcore.BlockToolCall && sameToolCall(block.call, terminal.ToolCall)
	default:
		return false
	}
}

func sameToolCall(left, right protocolcore.ToolCall) bool {
	return left.EffectiveKind() == right.EffectiveKind() && left.Key == right.Key &&
		left.ItemKey == right.ItemKey && left.Namespace == right.Namespace &&
		left.Name == right.Name && left.Input == right.Input &&
		bytes.Equal(left.Arguments.Bytes(), right.Arguments.Bytes())
}

// Finish ends the stream: the release holds every byte after the barrier, an
// empty text block when the client received no block at all, and the
// terminal events. Intents carry the terminal's response ID.
func (assembler *Assembler) Finish() (PendingTerminal, error) {
	assembler.mu.Lock()
	defer assembler.mu.Unlock()
	if assembler.failed || assembler.finished {
		return nil, assemblerViolation("stream assembler cannot finish twice or after a failure")
	}
	if assembler.terminal == nil {
		assembler.failed = true
		return nil, protocolcore.NewFailure(protocolcore.ReasonTruncatedEventStream, "$",
			errors.New("provider terminal was not received"))
	}
	response := assembler.terminal.Clone()
	var release bytes.Buffer
	release.Write(assembler.held.Bytes())
	forwarded := false
	for _, block := range assembler.order {
		forwarded = forwarded || block.forwarded
	}
	if !forwarded {
		index := assembler.nextIndex
		assembler.nextIndex++
		for _, step := range []func() ([]byte, error){
			func() ([]byte, error) { return assembler.encoder.StartText(index) },
			func() ([]byte, error) { return assembler.encoder.StopText(index) },
		} {
			encoded, err := step()
			if err != nil {
				assembler.failed = true
				return nil, err
			}
			release.Write(encoded)
		}
		if !slices.ContainsFunc(response.Blocks, func(block protocolcore.ContentBlock) bool {
			return block.Kind == protocolcore.BlockReasoning
		}) {
			response.Blocks = append(response.Blocks, protocolcore.ContentBlock{Kind: protocolcore.BlockText})
		}
	}
	encoded, err := assembler.encoder.Terminal(response)
	if err != nil {
		assembler.failed = true
		return nil, err
	}
	release.Write(encoded)
	intents := make([]protocolcore.ToolIntent, 0, len(assembler.calls))
	for _, block := range assembler.order {
		if block.kind == assembledToolCall {
			intents = append(intents, protocolcore.ToolIntent{ResponseID: response.ID, Ordinal: len(intents), Call: block.call.Clone()})
		}
	}
	report := protocolcore.NewTranslationReport(assembler.notices...).Merge(assembler.encoder.TranslationReport())
	assembler.finished = true
	return NewPendingTerminal(release.Bytes(), response, intents, report), nil
}

func assemblerViolation(message string) error {
	return protocolcore.NewFailure(protocolcore.ReasonStreamStateViolation, "$", errors.New(message))
}
