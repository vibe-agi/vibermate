package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// question is one row of design 18.1.
type question struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Default  string `json:"default"`
	IfFails  string `json:"if_it_fails"`
}

var questions = []question{
	{"Q1", "Are Claude Code's real tool schemas (up to 142 KB; $schema removed; prefixItems, propertyNames, const, typeless properties, additionalProperties:{}) accepted with strict:false by the seed models?", "send as in 6.5", "SchemaNormalizer: codex_subset drops the keywords Codex never sends, with tool_schema_normalized"},
	{"Q2", "Is assistant phase accepted on replayed messages?", "AssistantPhase: infer", "AssistantPhase: omit"},
	{"Q3", "Do stream headers and response.created arrive within 2 s at effort max?", "no early envelope in stage 1", "pull T6 into stage 1 for every translated target"},
	{"Q4", "Is a reasoning item replayed with id, encrypted_content and a rebuilt summary accepted next turn under store:false?", "replay", "ReplaySummary: empty, then ReplayReasoning: off"},
	{"Q5", "Are input_file (PDF) and input_image accepted inside function_call_output?", "send", "ToolOutputDocuments: text_note"},
	{"Q6", "Is include:[\"reasoning.encrypted_content\"] accepted for a non-reasoning model?", "include when thinking is on", "IncludeEncryptedReasoning: reasoning_models"},
	{"Q7", "Is access_programs.cyber accepted on translated bodies?", "finalizer as today", "gate the finalizer to the Codex operation (cyber_access.go:48-51)"},
	{"Q8", "What is the longest silent gap between events during reasoning at xhigh/max?", "5-minute idle timers", "ProviderIdleAllowance of 7.8 when above 4 minutes"},
	{"Q9", "Cache hit rate with prompt_cache_key over a 10-turn session; does prompt_cache_breakpoint apply?", "key only", "follow-up X6"},
	{"Q10", "Forms of context_length_exceeded and invalid_encrypted_content (status or in band; code; numbers in the message); real input_tokens per corpus body", "8 as designed", "classifier rows and the calibration fixture"},
	{"Q11", "Do the seed model ids exist for API keys, and what are their API context windows?", "live list decides", "claude_model_defaults.dart, openai_context_windows.dart"},
}

const (
	verdictPass         = "pass"
	verdictFail         = "fail"
	verdictInconclusive = "inconclusive"
	verdictSkipped      = "skipped"
)

// Thresholds the design names.
const (
	// earlyEnvelope is Q3's bound on the head and the first event.
	earlyEnvelope = 2 * time.Second
	// idleAllowanceTrigger is Q8's: above it, add ProviderIdleAllowance.
	idleAllowanceTrigger = 4 * time.Minute
	// cacheShareFloor is this tool's reading of "key only" holding for Q9:
	// the design gives no number, so a mean cached share below half over
	// turns 2-10 is reported as a failure for the owner to judge.
	cacheShareFloor = 0.5
	// calibrationTolerance is S16's: the estimate within 20% of the real
	// input_tokens.
	calibrationTolerance = 0.2
)

type verdict struct {
	question
	Verdict string   `json:"verdict"`
	Switch  string   `json:"switch,omitempty"`
	Summary string   `json:"summary"`
	Probes  []string `json:"probes,omitempty"`
}

// accepted: the upstream took the body and answered it (an incomplete
// answer at the small output cap counts).
func accepted(result *probeResult) bool {
	if result == nil || result.Skipped != "" || result.Status != 200 || result.Error != nil {
		return false
	}
	return result.Cancelled || result.Terminal == "response.completed" || result.Terminal == "response.incomplete"
}

// rejected: the upstream refused the body itself, with 400 or 422 or an
// in-band failure. 401, 404, 429, 5xx and transport errors say nothing
// about the body.
func rejected(result *probeResult) bool {
	if result == nil || result.Skipped != "" {
		return false
	}
	if result.Status == 400 || result.Status == 422 {
		return true
	}
	return result.Status == 200 && result.Error != nil && result.Error.InBand
}

func (result *probeResult) errorText() string {
	if result == nil || result.Error == nil {
		return ""
	}
	parts := []string{}
	for _, part := range []struct{ name, value string }{{"code", result.Error.Code}, {"param", result.Error.Param}, {"type", result.Error.Type}} {
		if part.value != "" {
			parts = append(parts, part.name+" "+part.value)
		}
	}
	where := fmt.Sprintf("HTTP %d", result.Status)
	if result.Error.InBand {
		where = "in band (" + result.Error.Event + ")"
	}
	return where + ": " + strings.Join(parts, ", ")
}

// state describes a probe's result in a few words.
func (result *probeResult) state() string {
	switch {
	case result == nil:
		return "not run"
	case result.Skipped != "":
		return "skipped (" + result.Skipped + ")"
	case result.Method == "GET" && result.Status == 200:
		return "answered"
	case accepted(result):
		return "accepted"
	case rejected(result):
		return "refused, " + result.errorText()
	case result.Status == 0:
		return "no answer (" + result.TransportError + ")"
	case result.Error != nil:
		return result.errorText()
	}
	return fmt.Sprintf("HTTP %d without a terminal event", result.Status)
}

// paramNames reports whether a refusal's param names any of the fragments.
func paramNames(result *probeResult, fragments ...string) bool {
	if result == nil || result.Error == nil {
		return false
	}
	param := strings.ToLower(result.Error.Param + " " + result.Error.Code)
	for _, fragment := range fragments {
		if strings.Contains(param, fragment) {
			return true
		}
	}
	return false
}

type verdicts struct {
	results []*probeResult
	byID    map[string]*probeResult
	models  *modelsReport
	plan    bool
}

func judge(results []*probeResult, models *modelsReport, ran func(string) bool) []verdict {
	set := &verdicts{results: results, byID: map[string]*probeResult{}, models: models}
	for _, result := range results {
		set.byID[result.ID] = result
	}
	var out []verdict
	for _, row := range questions {
		value := verdict{question: row}
		for _, result := range results {
			if result.Question == row.ID {
				value.Probes = append(value.Probes, result.ID)
			}
		}
		if !ran(row.ID) {
			value.Verdict, value.Summary = verdictSkipped, "not part of this run"
			if row.ID == "Q8" || row.ID == "Q9" {
				value.Summary += " (add --include-slow)"
			}
			out = append(out, value)
			continue
		}
		switch row.ID {
		case "Q1":
			set.q1(&value)
		case "Q2":
			set.pairedSwitch(&value, "Q2.phase", "Q2.no-phase", []string{"phase"}, "AssistantPhase: omit")
		case "Q3":
			set.q3(&value)
		case "Q4":
			set.q4(&value)
		case "Q5":
			set.q5(&value)
		case "Q6":
			set.q6(&value)
		case "Q7":
			set.pairedSwitch(&value, "Q7.cyber", "Q7.no-cyber", []string{"access_programs", "cyber"}, "gate the cyber finalizer to the Codex operation (internal/exchange/cyber_access.go:48-51)")
		case "Q8":
			set.q8(&value)
		case "Q9":
			set.q9(&value)
		case "Q10":
			set.q10(&value)
		case "Q11":
			set.q11(&value)
		}
		out = append(out, value)
	}
	return out
}

func (value *verdict) set(outcome, summary, flip string) {
	value.Verdict, value.Summary, value.Switch = outcome, summary, flip
}

// pairedSwitch judges a default against its control: the default refused
// while the control is accepted, or a refusal whose param names the field,
// is a failure that flips the switch.
func (set *verdicts) pairedSwitch(value *verdict, main, control string, fields []string, flip string) {
	primary, alternative := set.byID[main], set.byID[control]
	switch {
	case accepted(primary):
		value.set(verdictPass, main+" accepted; "+control+" "+alternative.state(), "")
	case rejected(primary) && (accepted(alternative) || paramNames(primary, fields...)):
		value.set(verdictFail, main+" "+primary.state()+"; "+control+" "+alternative.state(), flip)
	default:
		value.set(verdictInconclusive, main+" "+primary.state()+"; "+control+" "+alternative.state(), "")
	}
}

func (set *verdicts) q1(value *verdict) {
	var passed, failed, unknown []string
	for _, result := range set.results {
		if result.Question != "Q1" {
			continue
		}
		model := result.Model
		switch {
		case accepted(result):
			passed = append(passed, model)
		case rejected(result) && (paramNames(result, "tools", "schema", "parameters") || (result.Error.Param == "" && set.acceptedOn(model, "Q1"))):
			failed = append(failed, model+" ("+result.errorText()+")")
		default:
			unknown = append(unknown, model+" ("+result.state()+")")
		}
	}
	summary := fmt.Sprintf("accepted by %s", listOrNone(passed))
	if len(failed) != 0 {
		summary += "; refused by " + strings.Join(failed, ", ")
	}
	if len(unknown) != 0 {
		summary += "; not judged: " + strings.Join(unknown, ", ")
	}
	switch {
	case len(failed) != 0:
		value.set(verdictFail, summary, "SchemaNormalizer: codex_subset (lands with this result; only SchemaNormalizerTopLevel exists today)")
	case len(passed) != 0 && len(unknown) == 0:
		value.set(verdictPass, summary, "")
	case len(passed) != 0:
		value.set(verdictPass, summary+" (pass for the models that answered)", "")
	default:
		value.set(verdictInconclusive, summary, "")
	}
}

// acceptedOn reports a body of another question accepted on model: the
// model itself takes translated requests.
func (set *verdicts) acceptedOn(model, except string) bool {
	for _, result := range set.results {
		if result.Model == model && result.Question != except && accepted(result) {
			return true
		}
	}
	return false
}

func listOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func (set *verdicts) q3(value *verdict) {
	measured := set.byID["Q3.max"]
	note := ""
	if !accepted(measured) && accepted(set.byID["Q3.xhigh"]) {
		note = " (effort max was refused: " + measured.errorText() + "; measured at xhigh)"
		measured = set.byID["Q3.xhigh"]
	}
	if !accepted(measured) || measured.Timing == nil || measured.Timing.FirstEventMS == nil {
		value.set(verdictInconclusive, "Q3.max "+measured.state()+note, "")
		return
	}
	head := time.Duration(measured.Timing.FirstHeaderMS) * time.Millisecond
	first := time.Duration(*measured.Timing.FirstEventMS) * time.Millisecond
	summary := fmt.Sprintf("%s: head after %d ms, first event %s after %d ms%s", measured.ID, head.Milliseconds(), measured.Timing.FirstEvent, first.Milliseconds(), note)
	if head <= earlyEnvelope && first <= earlyEnvelope && measured.Timing.FirstEvent == "response.created" {
		value.set(verdictPass, summary, "")
		return
	}
	value.set(verdictFail, summary, "pull T6 (early envelope) into stage 1 for every translated target")
}

func (set *verdicts) q4(value *verdict) {
	one, two := set.byID["Q4.turn-1"], set.byID["Q4.turn-2"]
	empty, off := set.byID["Q4.turn-2-empty-summary"], set.byID["Q4.turn-2-no-reasoning"]
	switch {
	case two == nil || two.Skipped != "":
		value.set(verdictInconclusive, "Q4.turn-1 "+one.state()+"; Q4.turn-2 "+two.state(), "")
	case accepted(two) && !replayedSummary(two):
		value.set(verdictPass, "the replayed reasoning item was accepted under store:false, but its summary was empty, so a rebuilt summary was not tested", "")
	case accepted(two):
		value.set(verdictPass, "the replayed reasoning item, with id, encrypted_content and a rebuilt summary, was accepted under store:false", "")
	case rejected(two) && accepted(empty):
		value.set(verdictFail, "Q4.turn-2 "+two.state()+"; with an empty summary: accepted", "ReplaySummary: empty")
	case rejected(two) && rejected(empty) && accepted(off):
		value.set(verdictFail, "Q4.turn-2 "+two.state()+"; empty summary "+empty.state()+"; without reasoning: accepted", "ReplayReasoning: off")
	default:
		value.set(verdictInconclusive, "Q4.turn-2 "+two.state()+"; empty summary "+empty.state()+"; without reasoning "+off.state(), "")
	}
}

// replayedSummary reports whether a sent body replayed a reasoning item
// with summary text.
func replayedSummary(result *probeResult) bool {
	if result.built == nil {
		return true
	}
	var wire struct {
		Input []struct {
			Type    string            `json:"type"`
			Summary []json.RawMessage `json:"summary"`
		} `json:"input"`
	}
	if json.Unmarshal(result.built.body, &wire) != nil {
		return true
	}
	for _, item := range wire.Input {
		if item.Type == "reasoning" && len(item.Summary) != 0 {
			return true
		}
	}
	return false
}

func (set *verdicts) q5(value *verdict) {
	media, note, control := set.byID["Q5.media"], set.byID["Q5.pdf-as-note"], set.byID["Q2.no-phase"]
	if accepted(set.byID["Q2.phase"]) {
		control = set.byID["Q2.phase"]
	}
	attributed := paramNames(media, "output", "input_file", "input_image", "file", "image") || accepted(control)
	switch {
	case accepted(media):
		value.set(verdictPass, "an input_image and an input_file (PDF) inside function_call_output were accepted", "")
	case rejected(media) && attributed && accepted(note):
		value.set(verdictFail, "Q5.media "+media.state()+"; with the PDF as a text note: accepted", "ToolOutputDocuments: text_note")
	case rejected(media) && attributed && rejected(note):
		value.set(verdictFail, "Q5.media "+media.state()+"; with the PDF as a note still "+note.state()+": input_image inside function_call_output is refused too, which 18.1 has no switch for", "ToolOutputDocuments: text_note, and a design decision for images in tool outputs")
	default:
		value.set(verdictInconclusive, "Q5.media "+media.state()+"; Q5.pdf-as-note "+note.state(), "")
	}
}

func (set *verdicts) q6(value *verdict) {
	full, includeOnly, neither := set.byID["Q6.full"], set.byID["Q6.include-only"], set.byID["Q6.neither"]
	switch {
	case full == nil || full.Skipped != "":
		value.set(verdictSkipped, full.state(), "")
	case accepted(full):
		value.set(verdictPass, "the body with reasoning and include was accepted by "+full.Model, "")
	case accepted(includeOnly):
		value.set(verdictPass, "include alone was accepted by "+full.Model+"; the full body was "+full.state()+" (a reasoning.effort refusal self-heals through effort_unsupported)", "")
	case rejected(includeOnly) && accepted(neither):
		value.set(verdictFail, "include alone "+includeOnly.state()+"; without include: accepted", "IncludeEncryptedReasoning: reasoning_models (lands with this result)")
	default:
		value.set(verdictInconclusive, "Q6.full "+full.state()+"; include-only "+includeOnly.state()+"; neither "+neither.state(), "")
	}
}

func (set *verdicts) q8(value *verdict) {
	var parts []string
	worst := time.Duration(-1)
	judged := true
	for _, id := range []string{"Q8.xhigh", "Q8.max"} {
		result := set.byID[id]
		if !accepted(result) || result.Timing == nil {
			judged = false
			parts = append(parts, id+" "+result.state())
			continue
		}
		gap := time.Duration(result.Timing.LongestGapMS) * time.Millisecond
		parts = append(parts, fmt.Sprintf("%s: longest gap between events %s (between %s), %d events over %s, %s",
			id, gap.Round(time.Millisecond), strings.Join(result.Timing.LongestGapAround, " and "), result.Timing.Events,
			(time.Duration(result.Timing.TotalMS)*time.Millisecond).Round(time.Millisecond), strings.TrimPrefix(result.Terminal, "response.")))
		worst = max(worst, gap)
	}
	summary := strings.Join(parts, "; ")
	switch {
	case worst > idleAllowanceTrigger:
		value.set(verdictFail, summary, "ProviderIdleAllowance and RequestOptions.ResponseIdleTimeout of design 7.8 (one more PR, S14)")
	case !judged:
		value.set(verdictInconclusive, summary, "")
	default:
		value.set(verdictPass, summary, "")
	}
}

func cachedShare(result *probeResult) (float64, bool) {
	if !accepted(result) || result.Usage == nil || result.Usage.InputTokens == nil || *result.Usage.InputTokens <= 0 {
		return 0, false
	}
	return float64(value(result.Usage.CachedTokens)) / float64(*result.Usage.InputTokens), true
}

// q9Report is the cache measurement per turn.
type q9Report struct {
	Turns           []q9Turn `json:"turns"`
	MeanShare       float64  `json:"mean_cached_share_turns_2_to_10"`
	BreakpointTurns []q9Turn `json:"breakpoint_turns,omitempty"`
	BreakpointShare float64  `json:"mean_cached_share_breakpoint_turns_2_to_3"`
}

type q9Turn struct {
	ID           string  `json:"id"`
	InputTokens  int64   `json:"input_tokens"`
	CachedTokens int64   `json:"cached_tokens"`
	Share        float64 `json:"cached_share"`
	State        string  `json:"state"`
}

func q9Measure(results []*probeResult) *q9Report {
	report := &q9Report{}
	mean := func(turns []q9Turn) float64 {
		total, count := 0.0, 0
		for index, turn := range turns {
			if index == 0 || turn.State != "accepted" {
				continue
			}
			total += turn.Share
			count++
		}
		if count == 0 {
			return 0
		}
		return total / float64(count)
	}
	for _, result := range results {
		if result.Question != "Q9" {
			continue
		}
		turn := q9Turn{ID: result.ID, State: result.state()}
		if share, ok := cachedShare(result); ok {
			turn.InputTokens, turn.CachedTokens, turn.Share = *result.Usage.InputTokens, value(result.Usage.CachedTokens), share
		}
		if strings.HasPrefix(result.ID, "Q9.breakpoint") {
			report.BreakpointTurns = append(report.BreakpointTurns, turn)
		} else {
			report.Turns = append(report.Turns, turn)
		}
	}
	report.MeanShare, report.BreakpointShare = mean(report.Turns), mean(report.BreakpointTurns)
	if len(report.Turns) == 0 {
		return nil
	}
	return report
}

func (set *verdicts) q9(value *verdict) {
	report := q9Measure(set.results)
	if report == nil {
		value.set(verdictInconclusive, "no Q9 turn ran", "")
		return
	}
	answered := 0
	for _, turn := range report.Turns {
		if turn.State == "accepted" {
			answered++
		}
	}
	breakpoint := "the breakpoint session did not run"
	if len(report.BreakpointTurns) != 0 {
		first := set.byID[report.BreakpointTurns[0].ID]
		breakpoint = fmt.Sprintf("prompt_cache_breakpoint: first turn %s, mean cached share of turns 2-3 %.0f%%", first.state(), 100*report.BreakpointShare)
	}
	summary := fmt.Sprintf("%d of %d turns answered; mean cached share of turns 2-10 %.0f%%; %s", answered, len(report.Turns), 100*report.MeanShare, breakpoint)
	switch {
	case answered < len(report.Turns):
		value.set(verdictInconclusive, summary, "")
	case report.MeanShare >= cacheShareFloor:
		value.set(verdictPass, summary, "")
	default:
		value.set(verdictFail, summary+fmt.Sprintf(" (this tool's floor is %.0f%%; the design names none)", 100*cacheShareFloor), "follow-up X6: map cache_control to prompt_cache_breakpoint")
	}
}

// q10Report collects the two error forms the classifier rows depend on.
type q10Report struct {
	ContextLengthExceeded    []errorForm `json:"context_length_exceeded"`
	InvalidEncryptedContent  *errorForm  `json:"invalid_encrypted_content,omitempty"`
	CalibrationBodies        int         `json:"calibration_bodies"`
	CalibrationWithinPercent int         `json:"calibration_within_20_percent"`
}

type errorForm struct {
	Probe             string          `json:"probe"`
	State             string          `json:"state"`
	Status            int             `json:"status,omitempty"`
	InBand            bool            `json:"in_band"`
	Code              string          `json:"code,omitempty"`
	Param             string          `json:"param,omitempty"`
	MessageHasNumbers bool            `json:"message_has_numbers"`
	MessageNumbers    []int64         `json:"message_numbers,omitempty"`
	Classification    *classification `json:"vibermate_classification,omitempty"`
}

func formOf(result *probeResult) errorForm {
	form := errorForm{Probe: result.ID, State: result.state(), Status: result.Status, Classification: result.Classification}
	if result.Error != nil {
		form.InBand, form.Code, form.Param = result.Error.InBand, result.Error.Code, result.Error.Param
		form.MessageHasNumbers, form.MessageNumbers = result.Error.MessageHasNumbers, result.Error.MessageNumbers
	}
	return form
}

func q10Measure(results []*probeResult) *q10Report {
	report := &q10Report{}
	found := false
	for _, result := range results {
		switch {
		case result.ID == "Q10.overflow" || result.ID == "Q10.overflow-max-output":
			report.ContextLengthExceeded = append(report.ContextLengthExceeded, formOf(result))
			found = true
		case result.ID == "Q10.tampered-reasoning":
			form := formOf(result)
			report.InvalidEncryptedContent = &form
			found = true
		case strings.HasPrefix(result.ID, "Q10.tokens."):
			if entry, ok := calibrationEntryOf(result); ok {
				report.CalibrationBodies++
				if entry.within() {
					report.CalibrationWithinPercent++
				}
			}
		}
	}
	if !found && report.CalibrationBodies == 0 {
		return nil
	}
	return report
}

func (set *verdicts) q10(value *verdict) {
	report := q10Measure(set.results)
	if report == nil {
		value.set(verdictInconclusive, "no Q10 probe ran", "")
		return
	}
	var parts, problems []string
	inconclusive := false
	for _, form := range report.ContextLengthExceeded {
		result := set.byID[form.Probe]
		switch {
		case accepted(result):
			inconclusive = true
			parts = append(parts, form.Probe+" was accepted: the window is larger than the filler; raise --overflow-tokens")
		case form.Classification != nil && form.Classification.Class == "context_window":
			parts = append(parts, fmt.Sprintf("%s: %s, ViberMate class context_window (window %d), Claude Code sees %q", form.Probe, result.errorText(), form.Classification.LimitTokens, form.Classification.ClientMessage))
		case form.Classification != nil:
			problems = append(problems, fmt.Sprintf("%s: %s classified %s, not context_window", form.Probe, result.errorText(), form.Classification.Class))
		default:
			inconclusive = true
			parts = append(parts, form.Probe+" "+result.state())
		}
	}
	if form := report.InvalidEncryptedContent; form != nil {
		result := set.byID[form.Probe]
		switch {
		case form.Classification != nil && form.Classification.Class == "reasoning_state":
			where := "HTTP, so Claude Code strips thinking and retries"
			if form.InBand {
				where = "in band, so it heals through the rejected-seal memory of 8.6 on the next turn"
			}
			parts = append(parts, fmt.Sprintf("%s: %s, ViberMate class reasoning_state, %s", form.Probe, result.errorText(), where))
		case accepted(result):
			inconclusive = true
			parts = append(parts, form.Probe+" was accepted with a damaged encrypted_content")
		case form.Classification != nil:
			problems = append(problems, fmt.Sprintf("%s: %s classified %s, not reasoning_state", form.Probe, result.errorText(), form.Classification.Class))
		default:
			inconclusive = true
			parts = append(parts, form.Probe+" "+result.state())
		}
	}
	parts = append(parts, fmt.Sprintf("input_tokens recorded for %d of %d corpus bodies, the estimate within 20%% for %d", report.CalibrationBodies, len(corpusFixtures), report.CalibrationWithinPercent))
	summary := strings.Join(append(problems, parts...), "; ")
	switch {
	case len(problems) != 0:
		value.set(verdictFail, summary, "classifier rows of design 8.2")
	case inconclusive:
		value.set(verdictInconclusive, summary, "")
	case report.CalibrationBodies < len(corpusFixtures):
		value.set(verdictInconclusive, summary, "")
	default:
		value.set(verdictPass, summary, "")
	}
}

func (set *verdicts) q11(value *verdict) {
	listing := set.byID["Q11.models"]
	if set.models == nil || listing == nil || listing.Status != 200 {
		value.set(verdictInconclusive, "GET /v1/models "+listing.state(), "")
		return
	}
	var present, missing []string
	for _, seed := range set.models.Seeds {
		if seed.Present {
			present = append(present, seed.ID)
		} else {
			missing = append(missing, seed.ID)
		}
	}
	windows := "no context window is listed"
	if set.models.ContextWindowsExposed {
		windows = "context windows are listed (see q11.seeds)"
	}
	summary := fmt.Sprintf("listed: %s; missing: %s; %s", listOrNone(present), listOrNone(missing), windows)
	if len(missing) == 0 {
		value.set(verdictPass, summary, "")
		return
	}
	value.set(verdictFail, summary, "claude_model_defaults.dart (drop or replace the missing candidates); openai_context_windows.dart")
}
