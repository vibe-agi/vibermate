package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

// probeResult is one planned request and, in live mode, what came back.
type probeResult struct {
	Seq         int      `json:"seq"`
	ID          string   `json:"id"`
	Question    string   `json:"question"`
	Title       string   `json:"title"`
	Model       string   `json:"model,omitempty"`
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Fixture     string   `json:"fixture,omitempty"`
	Conditional string   `json:"conditional,omitempty"`
	Slow        bool     `json:"slow,omitempty"`
	Edits       []string `json:"edits,omitempty"`
	Profile     string   `json:"profile,omitempty"`
	BodyBytes   int      `json:"body_bytes,omitempty"`
	BodyFile    string   `json:"body_file,omitempty"`
	// EstimatedInputTokens is ViberMate's own count_tokens estimate of the
	// body (design 10).
	EstimatedInputTokens int `json:"estimated_input_tokens,omitempty"`
	// Approximate marks a plan body built from a stand-in reply.
	Approximate bool   `json:"approximate,omitempty"`
	Readmission string `json:"readmission_error,omitempty"`
	Skipped     string `json:"skipped,omitempty"`

	Status           int               `json:"status,omitempty"`
	TransportError   string            `json:"transport_error,omitempty"`
	Cancelled        bool              `json:"cancelled_after_first_event,omitempty"`
	Timing           *timing           `json:"timing,omitempty"`
	Terminal         string            `json:"terminal,omitempty"`
	ResponseStatus   string            `json:"response_status,omitempty"`
	IncompleteReason string            `json:"incomplete_reason,omitempty"`
	Error            *upstreamError    `json:"error,omitempty"`
	Classification   *classification   `json:"vibermate_classification,omitempty"`
	Usage            *usageReport      `json:"usage,omitempty"`
	Translation      *translation      `json:"translation,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`

	built        *built
	exchange     *exchangeRecord
	clientStream []byte
	requested    string
}

// spec names a probe before it is built.
type spec struct {
	id, question, title, fixture, model, conditional string
	slow, cancelOnAccept                             bool
}

type runner struct {
	cfg      config
	corpus   *corpus
	builder  *builder
	sender   sender
	scrub    *scrubber
	plan     bool
	session  string
	progress io.Writer

	state     profileState
	available map[string]bool
	models    *modelsReport
	results   []*probeResult
	sent      int
	aborted   string
	// turnTwo is Q4's replayed turn, whose real seal Q10 tampers with.
	turnTwo []byte
	// dependent marks bodies built from an earlier reply; in a plan that
	// reply is a stand-in.
	dependent bool
}

func newRunner(cfg config, loaded *corpus, send sender, scrub *scrubber, plan bool, session string, progress io.Writer) *runner {
	return &runner{
		cfg:      cfg,
		corpus:   loaded,
		builder:  newBuilder(session, probeEndpoint, probeAccount),
		sender:   send,
		scrub:    scrub,
		plan:     plan,
		session:  session,
		progress: progress,
	}
}

// run executes the probes in an order where each switch a probe flips is
// in force for the probes after it, as the fix would be.
func (r *runner) run(ctx context.Context) {
	r.q11(ctx)
	if r.aborted != "" {
		return
	}
	steps := []struct {
		question string
		run      func(context.Context)
	}{
		{"Q7", r.q7}, {"Q2", r.q2}, {"Q1", r.q1}, {"Q5", r.q5}, {"Q6", r.q6},
		{"Q3", r.q3}, {"Q4", r.q4}, {"Q10", r.q10}, {"Q8", r.q8}, {"Q9", r.q9},
	}
	for _, step := range steps {
		if r.wants(step.question) {
			step.run(ctx)
		}
	}
}

func (r *runner) wants(question string) bool {
	if len(r.cfg.only) != 0 {
		return r.cfg.only[question]
	}
	return r.cfg.includeSlow || (question != "Q8" && question != "Q9")
}

// runs decides a conditional probe: a plan builds every one for review.
func (r *runner) runs(condition bool) bool { return r.plan || condition }

func (r *runner) usable(model string) bool { return r.available == nil || r.available[model] }

// mapModel maps a Claude model as the preset does (design 11.2): the row's
// first candidate among --models that the key's model list holds.
func (r *runner) mapModel(requested string) string { return r.rowModel(presetRowFor(requested)) }

// rowModel is a row's first candidate among --models that the key's model
// list holds; without one, the first usable model of --models.
func (r *runner) rowModel(row *presetRow) string {
	if row != nil {
		for _, candidate := range row.candidates {
			if slices.Contains(r.cfg.models, candidate) && r.usable(candidate) {
				return candidate
			}
		}
	}
	for _, model := range r.cfg.models {
		if r.usable(model) {
			return model
		}
	}
	return r.cfg.models[0]
}

func (r *runner) primary() string { return r.mapModel("claude-opus-5-5") }

func (r *runner) add(s spec, method, relative string) *probeResult {
	result := &probeResult{
		Seq:         len(r.results) + 1,
		ID:          s.id,
		Question:    s.question,
		Title:       s.title,
		Model:       s.model,
		Method:      method,
		Path:        relative,
		Fixture:     s.fixture,
		Conditional: s.conditional,
		Slow:        s.slow,
		Approximate: r.plan && r.dependent,
	}
	r.results = append(r.results, result)
	return result
}

func (r *runner) skip(s spec, reason string) *probeResult {
	result := r.add(s, http.MethodPost, "v1/responses")
	result.Skipped = reason
	r.report(result)
	return result
}

// post sends a built Responses request.
func (r *runner) post(ctx context.Context, s spec, b *built, buildErr error) *probeResult {
	if buildErr != nil {
		return r.skip(s, "the body could not be built: "+r.scrub.upstream(buildErr.Error(), 300))
	}
	result := r.add(s, http.MethodPost, b.relative)
	result.built = b
	result.Edits = b.edits
	result.Profile = b.state.String()
	result.BodyBytes = len(b.body)
	result.EstimatedInputTokens = b.estimate
	result.Readmission = b.readmission
	r.exchange(ctx, result, outbound{
		method: http.MethodPost, relative: b.relative, header: b.header, body: b.body,
		timeout: r.cfg.timeout, cancelOnAccept: s.cancelOnAccept,
	})
	return result
}

func (r *runner) exchange(ctx context.Context, result *probeResult, request outbound) {
	switch {
	case ctx.Err() != nil:
		result.Skipped = "interrupted before sending"
	case !r.plan && r.cfg.maxRequests > 0 && r.sent >= r.cfg.maxRequests:
		result.Skipped = fmt.Sprintf("request budget reached (--max-requests %d)", r.cfg.maxRequests)
	default:
		r.sent++
		record := r.sender.send(ctx, request)
		if !r.plan {
			r.observe(ctx, result, record)
		} else {
			result.exchange = record
			if result.built != nil && record.status == http.StatusOK {
				// The plan still drives the registered path's stream
				// decoder over the stand-in, so dependent bodies are what
				// the replay would build.
				r.readEvents(result, record)
				r.translate(ctx, result, record)
				result.Terminal, result.Usage, result.Translation = "", nil, nil
			}
		}
	}
	r.report(result)
}

// report prints a progress line in live mode.
func (r *runner) report(result *probeResult) {
	if r.plan || r.progress == nil {
		return
	}
	fmt.Fprintf(r.progress, "[%02d] %-40s %s\n", result.Seq, result.ID, result.line())
}

func (result *probeResult) line() string {
	switch {
	case result.Skipped != "":
		return "skipped: " + result.Skipped
	case result.TransportError != "" && result.Status == 0:
		return "no answer: " + result.TransportError
	}
	parts := []string{fmt.Sprintf("HTTP %d", result.Status)}
	if result.Terminal != "" {
		parts = append(parts, strings.TrimPrefix(result.Terminal, "response."))
	}
	if result.Error != nil {
		parts = append(parts, "error "+strings.Trim(result.Error.Code+" "+result.Error.Param, " "))
	}
	if result.Timing != nil {
		parts = append(parts, fmt.Sprintf("head %dms", result.Timing.FirstHeaderMS))
		if result.Timing.FirstEventMS != nil {
			parts = append(parts, fmt.Sprintf("first event %dms", *result.Timing.FirstEventMS))
		}
		parts = append(parts, fmt.Sprintf("total %.1fs", float64(result.Timing.TotalMS)/1000))
	}
	if usage := result.Usage; usage != nil && usage.InputTokens != nil {
		parts = append(parts, fmt.Sprintf("in %d cached %d out %d", *usage.InputTokens, value(usage.CachedTokens), value(usage.OutputTokens)))
	}
	if result.Translation != nil && !result.Translation.OK {
		parts = append(parts, "translation failed")
	}
	return strings.Join(parts, ", ")
}

func value(pointer *int64) int64 {
	if pointer == nil {
		return 0
	}
	return *pointer
}

// prepare loads a corpus body, caps its output, applies edit and builds it.
func (r *runner) prepare(name string, outputTokens int, model string, state profileState, edit func(*claudeBody) error) (*built, error) {
	body, err := parseClaudeBody(r.corpus.body(name))
	if err != nil {
		return nil, err
	}
	if outputTokens > 0 {
		if err := body.capOutput(outputTokens); err != nil {
			return nil, err
		}
	}
	if edit != nil {
		if err := edit(body); err != nil {
			return nil, err
		}
	}
	return r.builder.buildEdited(body, model, state)
}

// sendCorpus builds s.fixture for s.model and sends it.
func (r *runner) sendCorpus(ctx context.Context, s spec, outputTokens int, state profileState, edit func(*claudeBody) error) *probeResult {
	b, err := r.prepare(s.fixture, outputTokens, s.model, state, edit)
	return r.post(ctx, s, b, err)
}

// Q11: the seed ids for API keys, and their context windows if listed.
func (r *runner) q11(ctx context.Context) {
	result := r.add(spec{
		id: "Q11.models", question: "Q11",
		title: "GET /v1/models: which seed ids exist for this key, and whether context windows are listed",
	}, http.MethodGet, "v1/models")
	r.exchange(ctx, result, outbound{
		method: http.MethodGet, relative: "v1/models",
		header: http.Header{"Accept": {"application/json"}}, timeout: r.cfg.timeout,
	})
	record := result.exchange
	if record == nil {
		return
	}
	switch {
	case record.status == http.StatusOK:
		r.models, r.available = r.readModels(record.body)
		if r.available != nil && !slices.ContainsFunc(r.cfg.models, r.usable) {
			r.aborted = "GET /v1/models lists none of --models, so every probe would go to a model this key cannot use; " +
				"nothing else was sent. Choose models from q11.public_gpt_ids in report.json and pass them with --models"
		}
	case record.status == http.StatusUnauthorized || record.status == http.StatusForbidden:
		r.aborted = fmt.Sprintf("GET /v1/models answered %d: the key was refused, so nothing else was sent", record.status)
	}
}

// Q7: is access_programs.cyber accepted on translated bodies?
func (r *runner) q7(ctx context.Context) {
	model := r.primary()
	with := r.sendCorpus(ctx, spec{
		id: "Q7.cyber", question: "Q7", fixture: fixtureMain, model: model,
		title: "a main request with access_programs.cyber, as the finalizer writes it for a managed API-key account",
	}, r.cfg.maxOutput, r.state, nil)
	without := r.state
	without.noCyber = true
	control := r.sendCorpus(ctx, spec{
		id: "Q7.no-cyber", question: "Q7", fixture: fixtureMain, model: model,
		title: "the same body without access_programs",
	}, r.cfg.maxOutput, without, nil)
	if !r.plan && rejected(with) && accepted(control) {
		r.state.noCyber = true
	}
}

// Q2: is assistant phase accepted on replayed messages?
func (r *runner) q2(ctx context.Context) {
	model := r.primary()
	with := r.sendCorpus(ctx, spec{
		id: "Q2.phase", question: "Q2", fixture: fixtureToolText, model: model,
		title: "replayed assistant text and calls with phase inferred (text-only tool results)",
	}, r.cfg.maxOutput, r.state, nil)
	omit := r.state
	omit.phaseOmit = true
	control := r.sendCorpus(ctx, spec{
		id: "Q2.no-phase", question: "Q2", fixture: fixtureToolText, model: model,
		title: "the same body with AssistantPhase: omit",
	}, r.cfg.maxOutput, omit, nil)
	if !r.plan && rejected(with) && accepted(control) {
		r.state.phaseOmit = true
	}
}

// Q1: are Claude Code's real tool schemas accepted with strict:false?
func (r *runner) q1(ctx context.Context) {
	for _, model := range r.cfg.models {
		s := spec{
			id: "Q1." + model, question: "Q1", fixture: fixtureLargestTools, model: model,
			title: "Claude Code's largest tool set (28 tools) with strict:false",
		}
		if !r.usable(model) {
			r.skip(s, "not listed by GET /v1/models for this key")
			continue
		}
		r.sendCorpus(ctx, s, r.cfg.maxOutput, r.state, nil)
	}
}

// Q5: are input_file (PDF) and input_image accepted in function_call_output?
func (r *runner) q5(ctx context.Context) {
	model := r.primary()
	media := r.sendCorpus(ctx, spec{
		id: "Q5.media", question: "Q5", fixture: fixtureToolMedia, model: model,
		title: "tool results holding an input_image and an input_file (PDF) inside function_call_output",
	}, r.cfg.maxOutput, r.state, nil)
	if !r.runs(rejected(media)) {
		return
	}
	note := r.state
	note.toolDocsNote = true
	fallback := r.sendCorpus(ctx, spec{
		id: "Q5.pdf-as-note", question: "Q5", fixture: fixtureToolMedia, model: model,
		conditional: "only when Q5.media is refused",
		title:       "the same body with ToolOutputDocuments: text_note (image kept, PDF as a note)",
	}, r.cfg.maxOutput, note, nil)
	if !r.plan && accepted(fallback) {
		r.state.toolDocsNote = true
	}
}

// Q6: is include:["reasoning.encrypted_content"] accepted for a
// non-reasoning model?
func (r *runner) q6(ctx context.Context) {
	model := r.cfg.nonReasoning
	s := spec{
		id: "Q6.full", question: "Q6", fixture: fixtureMain, model: model,
		title: "a main request, as ViberMate encodes it, sent to a non-reasoning model",
	}
	if model == "" {
		r.skip(s, "no non-reasoning model given: the design names none; pass --non-reasoning-model <id> (pick one from Q11's public ids)")
		return
	}
	if !r.usable(model) {
		r.skip(s, "not listed by GET /v1/models for this key")
		return
	}
	full := r.sendCorpus(ctx, s, r.cfg.maxOutput, r.state, nil)
	if !r.runs(rejected(full)) {
		return
	}
	edited := func(edit string, names ...string) (*built, error) {
		b, err := r.prepare(fixtureMain, r.cfg.maxOutput, model, r.state, nil)
		if err != nil {
			return nil, err
		}
		return b, b.editProvider(edit, func(members map[string]json.RawMessage) error {
			for _, name := range names {
				delete(members, name)
			}
			return nil
		})
	}
	b, err := edited("reasoning removed, include kept", "reasoning")
	includeOnly := r.post(ctx, spec{
		id: "Q6.include-only", question: "Q6", fixture: fixtureMain, model: model,
		conditional: "only when Q6.full is refused",
		title:       "the same body without reasoning, include kept: include alone",
	}, b, err)
	if !r.runs(rejected(includeOnly)) {
		return
	}
	b, err = edited("reasoning and include removed", "reasoning", "include")
	r.post(ctx, spec{
		id: "Q6.neither", question: "Q6", fixture: fixtureMain, model: model,
		conditional: "only when Q6.include-only is refused",
		title:       "the same body without reasoning and include: the control",
	}, b, err)
}

// Q3: do the head and response.created arrive within 2 s at effort max?
func (r *runner) q3(ctx context.Context) {
	model := r.primary()
	effort := func(value string) func(*claudeBody) error {
		return func(body *claudeBody) error { return body.setEffort(value) }
	}
	maximum := r.sendCorpus(ctx, spec{
		id: "Q3.max", question: "Q3", fixture: fixtureMain, model: model,
		title: "a main request at effort max: time to the response head and to response.created",
	}, r.cfg.maxOutput, r.state, effort("max"))
	if !r.runs(rejected(maximum)) {
		return
	}
	r.sendCorpus(ctx, spec{
		id: "Q3.xhigh", question: "Q3", fixture: fixtureMain, model: model,
		conditional: "only when Q3.max is refused",
		title:       "the same request at effort xhigh",
	}, r.cfg.maxOutput, r.state, effort("xhigh"))
}

// Q4: is a reasoning item replayed with id, encrypted_content and a rebuilt
// summary accepted next turn under store:false?
func (r *runner) q4(ctx context.Context) {
	model := r.primary()
	first, err := r.prepare(fixtureToolTurnOne, r.cfg.turnOutput, model, r.state, nil)
	one := r.post(ctx, spec{
		id: "Q4.turn-1", question: "Q4", fixture: fixtureToolTurnOne, model: model,
		title: "turn one of a tool round, with room for reasoning and a reply",
	}, first, err)
	second := spec{
		id: "Q4.turn-2", question: "Q4", fixture: fixtureToolTurnOne, model: model,
		title: "turn two: Claude Code's replay of turn one, reasoning item included (rebuilt summary)",
	}
	next, reason := r.replay(one, "Reply with the single word ok.")
	if next == nil {
		r.skip(second, reason)
		return
	}
	if err := next.capOutput(r.cfg.maxOutput); err != nil {
		r.skip(second, err.Error())
		return
	}
	r.turnTwo, _ = next.bytes()
	r.dependent = true
	defer func() { r.dependent = false }()
	b, err := r.builder.buildEdited(next, model, r.state)
	if err == nil && !hasInputItem(b.body, "reasoning") {
		r.skip(second, "the path replayed no reasoning item (see its notices), so turn two would test nothing")
		return
	}
	two := r.post(ctx, second, b, err)
	if !r.runs(rejected(two)) {
		return
	}
	empty := r.state
	empty.summaryEmpty = true
	b, err = r.builder.buildEdited(next, model, empty)
	emptyResult := r.post(ctx, spec{
		id: "Q4.turn-2-empty-summary", question: "Q4", fixture: fixtureToolTurnOne, model: model,
		conditional: "only when Q4.turn-2 is refused",
		title:       "turn two with ReplaySummary: empty",
	}, b, err)
	if !r.plan && accepted(emptyResult) {
		r.state.summaryEmpty = true
		return
	}
	if !r.runs(rejected(emptyResult)) {
		return
	}
	off := r.state
	off.replayOff = true
	b, err = r.builder.buildEdited(next, model, off)
	offResult := r.post(ctx, spec{
		id: "Q4.turn-2-no-reasoning", question: "Q4", fixture: fixtureToolTurnOne, model: model,
		conditional: "only when Q4.turn-2-empty-summary is refused",
		title:       "turn two with ReplayReasoning: off",
	}, b, err)
	if !r.plan && accepted(offResult) {
		r.state.replayOff = true
	}
}

// replay builds the turn Claude Code sends after a translated reply.
func (r *runner) replay(previous *probeResult, prompt string) (*claudeBody, string) {
	if previous.built == nil || (!r.plan && !accepted(previous)) {
		return nil, previous.ID + " was not answered, so there is nothing to replay"
	}
	if previous.clientStream == nil {
		return nil, previous.ID + "'s stream did not translate, so Claude Code would have nothing to replay"
	}
	blocks, err := assistantBlocks(previous.clientStream)
	if err != nil {
		return nil, "the translated reply cannot be replayed: " + err.Error()
	}
	if previous.Question == "Q4" && !replayableReasoning(blocks) {
		return nil, previous.ID + " gave no reasoning item with encrypted_content followed by text or a call; raise --turn-output-tokens"
	}
	next, err := nextTurn(previous.built.claude, blocks, prompt, "the previous reply replayed, a tool_result per call, then: "+prompt)
	if err != nil {
		return nil, err.Error()
	}
	return next, ""
}

// Q10: the forms of context_length_exceeded and invalid_encrypted_content,
// and the real input_tokens per corpus body.
func (r *runner) q10(ctx context.Context) {
	for _, fixture := range corpusFixtures {
		requested := requestedModel(r.corpus.body(fixture))
		model := r.mapModel(requested)
		result := r.sendCorpus(ctx, spec{
			id: "Q10.tokens." + fixture, question: "Q10", fixture: fixture, model: model,
			title: "corpus body as ViberMate encodes it: real input_tokens for the calibration fixture",
		}, r.cfg.maxOutput, r.state, nil)
		result.requested = requested
	}

	model := r.primary()
	tokens := r.cfg.overflowTokens
	if window := r.models.windowOf(model); window > 0 && window+window/10 > tokens {
		tokens = window + window/10
	}
	filler := overflowText(tokens)
	overflow := func(body *claudeBody) error {
		return body.appendUserText(filler, fmt.Sprintf("synthetic filler of at least %d tokens appended to the user turn", tokens))
	}
	r.sendCorpus(ctx, spec{
		id: "Q10.overflow", question: "Q10", fixture: fixtureMain, model: model, cancelOnAccept: true,
		title: "an oversized context without max_output_tokens (Claude Code sends max_tokens 128000)",
	}, 0, r.state, overflow)
	r.sendCorpus(ctx, spec{
		id: "Q10.overflow-max-output", question: "Q10", fixture: fixtureMain, model: model, cancelOnAccept: true,
		title: "the same oversized context with max_output_tokens sent",
	}, r.cfg.maxOutput, r.state, overflow)

	s := spec{
		id: "Q10.tampered-reasoning", question: "Q10", fixture: fixtureToolTurnOne, model: model,
		title: "a replayed reasoning item whose encrypted_content was tampered with",
	}
	var body *claudeBody
	if r.turnTwo != nil {
		parsed, err := parseClaudeBody(r.turnTwo)
		if err == nil {
			if count, err := tamperSeals(parsed); err == nil && count > 0 {
				body = parsed
				r.dependent = true
				defer func() { r.dependent = false }()
			}
		}
	}
	if body == nil {
		first, err := parseClaudeBody(r.corpus.body(fixtureToolTurnOne))
		if err == nil {
			err = first.capOutput(r.cfg.maxOutput)
		}
		var raw []byte
		if err == nil {
			raw, err = first.bytes()
		}
		if err == nil {
			body, err = syntheticSealTurn(raw, model, r.builder.context.ReasoningBinding)
		}
		if err != nil {
			r.skip(s, err.Error())
			return
		}
		s.title += " (no real reasoning item was available: the seal payload is synthetic)"
	}
	// The default replay profile, so the damaged item is sent even if Q4
	// turned replay off.
	state := r.state
	state.replayOff = false
	b, err := r.builder.buildEdited(body, model, state)
	if err == nil && !hasInputItem(b.body, "reasoning") {
		r.skip(s, "the path replayed no reasoning item, so there is nothing to tamper with")
		return
	}
	r.post(ctx, s, b, err)
}

func requestedModel(body []byte) string {
	var envelope struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &envelope)
	return envelope.Model
}

// Q8: the longest silent gap between events while reasoning at xhigh and
// max.
func (r *runner) q8(ctx context.Context) {
	model := r.primary()
	for _, effort := range []string{"xhigh", "max"} {
		edit := func(body *claudeBody) error {
			if err := body.replaceLastUserText(reasoningTask, "the user prompt replaced by a synthetic long-reasoning task"); err != nil {
				return err
			}
			if err := body.set("tool_choice", map[string]string{"type": "none"}, "tool_choice none, so the model reasons instead of calling a tool"); err != nil {
				return err
			}
			return body.setEffort(effort)
		}
		r.sendCorpus(ctx, spec{
			id: "Q8." + effort, question: "Q8", fixture: fixtureMain, model: model, slow: true,
			title: "long reasoning at effort " + effort + ": the longest gap between events",
		}, r.cfg.slowOutput, r.state, edit)
	}
}

// Q9: cache hits with prompt_cache_key over a 10-turn session, and whether
// prompt_cache_breakpoint applies.
func (r *runner) q9(ctx context.Context) {
	r.session9(ctx, "Q9.turn", 10, nil)
	r.session9(ctx, "Q9.breakpoint-turn", 3, addCacheBreakpoints)
}

func (r *runner) session9(ctx context.Context, prefix string, turns int, providerEdit func(map[string]json.RawMessage) error) {
	model := r.primary()
	// Each session has its own prompt_cache_key.
	sessionBuilder := newBuilder(r.session+"-"+strings.ToLower(strings.ReplaceAll(prefix, ".", "-")), probeEndpoint, probeAccount)
	body, err := parseClaudeBody(r.corpus.body(fixtureMain))
	if err == nil {
		err = body.capOutput(r.cfg.turnOutput)
	}
	if err == nil {
		// Low effort keeps each short reply within the turn cap, so the
		// session reaches ten turns; caching does not depend on effort.
		err = body.setEffort("low")
	}
	stopped := ""
	if err != nil {
		stopped = err.Error()
	}
	defer func() { r.dependent = false }()
	for turn := 1; turn <= turns; turn++ {
		s := spec{
			id: fmt.Sprintf("%s-%02d", prefix, turn), question: "Q9", fixture: fixtureMain, model: model, slow: true,
			title: fmt.Sprintf("turn %d of a %d-turn session on one prompt_cache_key", turn, turns),
		}
		if providerEdit != nil {
			s.title += ", prompt_cache_breakpoint on the developer prompt and the newest message"
		}
		if stopped != "" {
			r.skip(s, stopped)
			continue
		}
		b, err := sessionBuilder.buildEdited(body, model, r.state)
		if err == nil && providerEdit != nil {
			err = b.editProvider("prompt_cache_breakpoint {mode: explicit} on the developer prompt's last part and the newest message's last part", providerEdit)
		}
		result := r.post(ctx, s, b, err)
		if turn == turns {
			break
		}
		next, reason := r.replay(result, fmt.Sprintf("Probe turn %d of %d: reply with the single word ok.", turn+1, turns))
		if next == nil {
			stopped = "the session stopped: " + reason
			continue
		}
		body = next
		r.dependent = true
	}
}
