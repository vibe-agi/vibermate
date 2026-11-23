package main

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
)

// presetRow is one row of the preset's model defaults (design 11.2): the
// globs it writes and the upstream candidates in preference order. The
// candidates come from Codex 0.171.0's ChatGPT catalog, so whether an API
// key can use them is what Q11 answers; S19 writes the same table into
// claude_model_defaults.dart.
type presetRow struct {
	name       string
	globs      []string
	candidates []string
}

var presetRows = []presetRow{
	{name: "opus", globs: []string{"claude-*opus*", "claude-*fable*"}, candidates: []string{"gpt-6.2-astra", "gpt-6-astra", "gpt-6.2-sol"}},
	{name: "sonnet", globs: []string{"claude-*sonnet*"}, candidates: []string{"gpt-6.2-sol", "gpt-6.1-sol", "gpt-6-sol"}},
	{name: "haiku", globs: []string{"claude-*haiku*"}, candidates: []string{"gpt-6-luna", "gpt-5.6-luna", "gpt-6.2-sol"}},
	{name: "other claude", globs: []string{"claude-*"}, candidates: []string{"gpt-6.2-sol", "gpt-6.1-sol", "gpt-6-sol"}},
}

// seedModels are the distinct candidates in table order: the default
// --models.
func seedModels() []string {
	var models []string
	for _, row := range presetRows {
		for _, candidate := range row.candidates {
			if !slices.Contains(models, candidate) {
				models = append(models, candidate)
			}
		}
	}
	return models
}

// presetRowFor resolves a Claude model as the glob contract of design 11.1
// does: among matching globs the one with the most literal bytes wins,
// ties going to the lexically smallest pattern.
func presetRowFor(model string) *presetRow {
	var best *presetRow
	bestPattern, bestLiteral := "", -1
	for index := range presetRows {
		for _, glob := range presetRows[index].globs {
			if !globMatch(glob, model) {
				continue
			}
			literal := len(strings.ReplaceAll(glob, "*", ""))
			if literal > bestLiteral || (literal == bestLiteral && glob < bestPattern) {
				best, bestPattern, bestLiteral = &presetRows[index], glob, literal
			}
		}
	}
	return best
}

// globMatch matches a pattern whose only special character is *.
func globMatch(pattern, value string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == value
	}
	if !strings.HasPrefix(value, parts[0]) {
		return false
	}
	value = value[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		at := strings.Index(value, part)
		if at < 0 {
			return false
		}
		value = value[at+len(part):]
	}
	return strings.HasSuffix(value, parts[len(parts)-1])
}

// presetMapping is the mapping a run used, by row.
func (r *runner) presetMapping() map[string]string {
	mapping := map[string]string{}
	for _, row := range presetRows {
		mapping[row.name] = r.rowModel(&row)
	}
	return mapping
}

// modelsReport is Q11's finding.
type modelsReport struct {
	Listed int `json:"listed_models"`
	// PublicGPT lists the gpt- ids the key sees, without fine-tuned ones
	// (whose ids name an organization), to choose a Q6 model from.
	PublicGPT             []string     `json:"public_gpt_ids,omitempty"`
	Seeds                 []seedReport `json:"seeds"`
	ContextWindowsExposed bool         `json:"context_windows_exposed"`
	Note                  string       `json:"note,omitempty"`
}

type seedReport struct {
	ID      string `json:"id"`
	Present bool   `json:"present"`
	// Rows names the preset rows and ranks that list this id.
	Rows []string `json:"preset_rows,omitempty"`
	// Fields are the model object's members beyond id, object, created and
	// owned_by, when they are plain values.
	Fields        map[string]any `json:"extra_fields,omitempty"`
	ContextWindow int64          `json:"context_window,omitempty"`
	WindowField   string         `json:"context_window_field,omitempty"`
}

func (report *modelsReport) windowOf(model string) int {
	if report == nil {
		return 0
	}
	for _, seed := range report.Seeds {
		if seed.ID == model {
			return int(seed.ContextWindow)
		}
	}
	return 0
}

// windowFields are member names that would carry a context window, most
// specific first.
var windowFields = []string{"context_window", "context_length", "max_context_tokens", "max_input_tokens", "input_token_limit", "max_model_len", "max_tokens"}

func (r *runner) readModels(body []byte) (*modelsReport, map[string]bool) {
	var list struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil || list.Data == nil {
		return &modelsReport{Note: "the model list could not be read; every model was tried"}, nil
	}
	report := &modelsReport{Listed: len(list.Data)}
	available := map[string]bool{}
	entries := map[string]map[string]json.RawMessage{}
	for _, entry := range list.Data {
		var id string
		if json.Unmarshal(entry["id"], &id) != nil || id == "" {
			continue
		}
		available[id] = true
		entries[id] = entry
		if strings.HasPrefix(id, "gpt-") && !strings.Contains(id, ":") && len(report.PublicGPT) < 200 {
			report.PublicGPT = append(report.PublicGPT, r.scrub.upstream(id, 80))
		}
	}
	sort.Strings(report.PublicGPT)
	wanted := append([]string{}, r.cfg.models...)
	if r.cfg.nonReasoning != "" && !slices.Contains(wanted, r.cfg.nonReasoning) {
		wanted = append(wanted, r.cfg.nonReasoning)
	}
	for _, id := range wanted {
		seed := seedReport{ID: id, Present: available[id]}
		for _, row := range presetRows {
			if rank := slices.Index(row.candidates, id); rank >= 0 {
				seed.Rows = append(seed.Rows, row.name+" #"+string(rune('1'+rank)))
			}
		}
		if id == r.cfg.nonReasoning {
			seed.Rows = append(seed.Rows, "Q6 non-reasoning model")
		}
		for name, raw := range entries[id] {
			switch name {
			case "id", "object", "created", "owned_by":
				continue
			}
			var plain any
			if json.Unmarshal(raw, &plain) != nil {
				continue
			}
			switch typed := plain.(type) {
			case string:
				plain = r.scrub.upstream(typed, 120)
			case float64, bool, nil:
			default:
				plain = "(nested value not kept)"
			}
			if seed.Fields == nil {
				seed.Fields = map[string]any{}
			}
			seed.Fields[r.scrub.upstream(name, 60)] = plain
		}
		for _, field := range windowFields {
			if number, ok := seed.Fields[field].(float64); ok && number > 0 {
				seed.ContextWindow, seed.WindowField = int64(number), field
				report.ContextWindowsExposed = true
				break
			}
		}
		report.Seeds = append(report.Seeds, seed)
	}
	if !report.ContextWindowsExposed {
		report.Note = "GET /v1/models lists no context window for these ids; fill openai_context_windows.dart from OpenAI's model pages for the API (design 11.3)"
	}
	return report, available
}
