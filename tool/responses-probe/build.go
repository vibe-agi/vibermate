package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/vibe-agi/vibermate/internal/anthropicresponses"
	"github.com/vibe-agi/vibermate/internal/cyberaccess"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// profileState is the provider profile a probe encodes with: the public
// profile with the switches earlier probes flipped (design 18.1), and
// whether the cyber finalizer runs.
type profileState struct {
	phaseOmit    bool
	summaryEmpty bool
	replayOff    bool
	toolDocsNote bool
	// noCyber leaves out access_programs.cyber, which the finalizer adds
	// for a managed API-key account on api.openai.com (F20).
	noCyber bool
}

func (state profileState) profile() openairesponses.ProviderRequestProfile {
	profile := openairesponses.PublicResponsesProfile()
	if state.phaseOmit {
		profile.AssistantPhase = openairesponses.AssistantPhaseOmit
	}
	if state.summaryEmpty {
		profile.ReplaySummary = openairesponses.ReplaySummaryEmpty
	}
	if state.replayOff {
		profile.ReplayReasoning = openairesponses.ReplayReasoningOff
	}
	if state.toolDocsNote {
		profile.ToolOutputDocuments = openairesponses.ToolOutputDocumentsTextNote
	}
	return profile
}

func (state profileState) String() string {
	choose := func(flipped bool, normal, other string) string {
		if flipped {
			return other
		}
		return normal
	}
	return strings.Join([]string{
		"AssistantPhase=" + choose(state.phaseOmit, "infer", "omit"),
		"ReplaySummary=" + choose(state.summaryEmpty, "rebuilt", "empty"),
		"ReplayReasoning=" + choose(state.replayOff, "sealed", "off"),
		"ToolOutputDocuments=" + choose(state.toolDocsNote, "input_file", "text_note"),
		"cyber=" + choose(state.noCyber, "standard", "omitted"),
	}, " ")
}

// builder encodes Claude Code bodies through the registered
// anthropic-messages-to-openai-responses path, as the Exchange does for one
// attempt: translated decode, the route's model mapping, the session
// evidence the loopback merges from X-Claude-Code-Session-Id, the provider
// encode for the official API, re-admission and the cyber finalizer.
type builder struct {
	session string
	context protocolpath.ProviderContext
	paths   map[openairesponses.ProviderRequestProfile]*protocolpath.Path
}

const (
	probeEndpoint = "target.openai.official"
	probeAccount  = "responses-probe"
)

func newBuilder(session, endpoint, account string) *builder {
	return &builder{
		session: session,
		context: protocolpath.ProviderContext{
			Target:           protocolpath.TargetOpenAIPlatform,
			ReasoningBinding: protocolpath.NewReasoningBinding(endpoint, account),
		},
		paths: map[openairesponses.ProviderRequestProfile]*protocolpath.Path{},
	}
}

func (b *builder) path(profile openairesponses.ProviderRequestProfile) (*protocolpath.Path, error) {
	if path, ok := b.paths[profile]; ok {
		return path, nil
	}
	options := anthropicresponses.DefaultOptions()
	options.Provider.Profile = profile
	path, err := anthropicresponses.NewProtocolPath(options)
	if err != nil {
		return nil, err
	}
	b.paths[profile] = path
	return path, nil
}

// built is one provider request and what produced it.
type built struct {
	path    *protocolpath.Path
	context protocolpath.ProviderContext
	state   profileState
	request protocolcore.Request
	// claude is the Claude Code body the path decoded.
	claude []byte
	body   []byte
	header http.Header
	// relative is the provider path, v1/responses.
	relative string
	notices  []string
	edits    []string
	// readmission is ViberMate's re-admission verdict on the body as sent;
	// empty when it passes.
	readmission string
	estimate    int
}

func (b *builder) build(claude []byte, model string, state profileState) (*built, error) {
	path, err := b.path(state.profile())
	if err != nil {
		return nil, err
	}
	request, decodeReport, err := path.Client().DecodeRequest(claude)
	if err != nil {
		return nil, fmt.Errorf("translated decode: %w", err)
	}
	if request, err = request.WithEffectiveModel(model); err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(request.ProtocolEvidence, func(value protocolcore.ProtocolEvidenceValue) bool {
		return value.Name == "claude.session_id"
	}) {
		request.ProtocolEvidence = append(request.ProtocolEvidence, protocolcore.ProtocolEvidenceValue{
			Name: "claude.session_id", Value: b.session,
		})
		slices.SortFunc(request.ProtocolEvidence, func(left, right protocolcore.ProtocolEvidenceValue) int {
			return strings.Compare(left.Name, right.Name)
		})
	}
	provider, encodeReport, err := path.EncodeProviderRequestFor(b.context, request, claude, nil)
	if err != nil {
		return nil, fmt.Errorf("provider encode: %w", err)
	}
	result := &built{
		path:     path,
		context:  b.context,
		state:    state,
		request:  request,
		claude:   claude,
		body:     provider.Body(),
		header:   provider.Headers().Clone(),
		relative: provider.RelativePath(),
	}
	for _, phase := range []struct {
		name   string
		report protocolcore.TranslationReport
	}{{"decode", decodeReport}, {"encode", encodeReport}} {
		for _, notice := range phase.report.Notices() {
			result.notices = append(result.notices, phase.name+" "+string(notice.Code)+" "+notice.Path)
		}
	}
	if err := path.ValidateTransformedRequest(result.body); err != nil {
		return nil, fmt.Errorf("the encoded body fails re-admission: %w", err)
	}
	if !state.noCyber {
		// The finalizer of internal/exchange/cyber_access.go for a managed
		// static-header account on api.openai.com in Standard mode.
		updated, changed, err := cyberaccess.ApplyPreference(result.body, nil, cyberaccess.Standard)
		if err != nil {
			return nil, fmt.Errorf("cyber finalizer: %w", err)
		}
		if changed {
			result.body = updated
		}
	}
	if result.estimate, err = path.CountInputTokens(b.context, request); err != nil {
		return nil, fmt.Errorf("token estimate: %w", err)
	}
	return result, nil
}

// buildEdited builds a Claude body edit by edit.
func (b *builder) buildEdited(body *claudeBody, model string, state profileState) (*built, error) {
	raw, err := body.bytes()
	if err != nil {
		return nil, err
	}
	result, err := b.build(raw, model, state)
	if err != nil {
		return nil, err
	}
	result.edits = append(result.edits, body.edits...)
	return result, nil
}

// editProvider changes the provider body where no profile switch exists
// yet (Q6, Q9), then re-admits the result and records the verdict: the
// probe still sends it, since the question is what the upstream accepts.
func (result *built) editProvider(edit string, change func(map[string]json.RawMessage) error) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(result.body, &members); err != nil {
		return err
	}
	if err := change(members); err != nil {
		return err
	}
	body, err := marshalNoEscape(members)
	if err != nil {
		return err
	}
	result.body = body
	result.edits = append(result.edits, "provider body: "+edit)
	if err := result.path.ValidateTransformedRequest(body); err != nil {
		result.readmission = err.Error()
	}
	return nil
}

// addCacheBreakpoints marks the end of the developer prompt and the end of
// the newest input message with prompt_cache_breakpoint, where Claude Code
// puts its system and last-message cache_control (follow-up X6).
func addCacheBreakpoints(members map[string]json.RawMessage) error {
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(members["input"], &input); err != nil {
		return err
	}
	marked := 0
	mark := func(item map[string]json.RawMessage) error {
		var parts []map[string]json.RawMessage
		if err := json.Unmarshal(item["content"], &parts); err != nil || len(parts) == 0 {
			return nil
		}
		parts[len(parts)-1]["prompt_cache_breakpoint"] = json.RawMessage(`{"mode":"explicit"}`)
		encoded, err := marshalNoEscape(parts)
		if err != nil {
			return err
		}
		item["content"] = encoded
		marked++
		return nil
	}
	for _, item := range input {
		if strings.Contains(string(item["role"]), "developer") {
			if err := mark(item); err != nil {
				return err
			}
			break
		}
	}
	for index := len(input) - 1; index >= 0; index-- {
		if strings.Contains(string(input[index]["type"]), "message") && !strings.Contains(string(input[index]["role"]), "assistant") {
			if err := mark(input[index]); err != nil {
				return err
			}
			break
		}
	}
	if marked == 0 {
		return errors.New("no input message to mark")
	}
	encoded, err := marshalNoEscape(input)
	if err != nil {
		return err
	}
	members["input"] = encoded
	return nil
}

// hasInputItem reports whether the provider body's input holds an item of
// the given type.
func hasInputItem(body []byte, kind string) bool {
	var wire struct {
		Input []struct {
			Type string `json:"type"`
		} `json:"input"`
	}
	if json.Unmarshal(body, &wire) != nil {
		return false
	}
	return slices.ContainsFunc(wire.Input, func(item struct {
		Type string `json:"type"`
	}) bool {
		return item.Type == kind
	})
}
