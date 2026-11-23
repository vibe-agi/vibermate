package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/anthropicresponses"
)

const (
	reportSchema = 1
	// calibrationDestination is where S16's calibration test is expected
	// to read the fixture; the shape is proposed by this tool.
	calibrationDestination = "internal/tokenestimate/testdata/claude-2.1.329-input-tokens.json"
)

// writeBodies writes every built body as sent, with its notices and edits,
// and records the file on the result.
func writeBodies(directory string, results []*probeResult, scrub *scrubber) error {
	bodies := filepath.Join(directory, "bodies")
	if err := os.MkdirAll(bodies, 0o700); err != nil {
		return err
	}
	for _, result := range results {
		if result.built == nil {
			continue
		}
		name := fmt.Sprintf("%02d-%s", result.Seq, result.ID)
		result.BodyFile = filepath.Join("bodies", name+".json")
		if err := scrub.writeFile(filepath.Join(directory, result.BodyFile), result.built.body); err != nil {
			return err
		}
		var notes bytes.Buffer
		fmt.Fprintf(&notes, "%s (%s)\n%s\n\nmodel %s, fixture %s, profile %s\n", result.ID, result.Question, result.Title, result.Model, result.Fixture, result.Profile)
		if result.Approximate {
			notes.WriteString("built from a stand-in reply: the live body differs where the real reply does\n")
		}
		if len(result.Edits) != 0 {
			notes.WriteString("\nedits:\n")
			for _, edit := range result.Edits {
				notes.WriteString("  " + edit + "\n")
			}
		}
		if result.Readmission != "" {
			notes.WriteString("\nre-admission of the edited body: " + result.Readmission + "\n")
		}
		notes.WriteString("\nnotices:\n")
		for _, notice := range result.built.notices {
			notes.WriteString("  " + notice + "\n")
		}
		if err := scrub.writeFile(filepath.Join(bodies, name+".notes.txt"), notes.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

// planCounts are the request counts of a plan pass.
type planCounts struct {
	always, conditional, skipped int
	tokens, refusedTokens        int
}

func countPlan(results []*probeResult) planCounts {
	var counts planCounts
	for _, result := range results {
		switch {
		case result.Skipped != "":
			counts.skipped++
			continue
		case result.Conditional != "":
			counts.conditional++
		default:
			counts.always++
		}
		if strings.HasPrefix(result.ID, "Q10.overflow") {
			counts.refusedTokens += result.EstimatedInputTokens
		} else {
			counts.tokens += result.EstimatedInputTokens
		}
	}
	return counts
}

func (counts planCounts) max() int { return counts.always + counts.conditional }

func printPlan(output io.Writer, cfg config, r *runner, directory string) {
	counts := countPlan(r.results)
	fmt.Fprintf(output, "responses-probe plan: %s revision %d, Claude Code 2.1.329 corpus, no network\n", anthropicresponses.CodecPairID, anthropicresponses.CodecRevision)
	fmt.Fprintf(output, "models: %s\n", strings.Join(cfg.models, ", "))
	mapping := r.presetMapping()
	fmt.Fprintf(output, "preset mapping: opus -> %s, sonnet -> %s, haiku -> %s\n", mapping["opus"], mapping["sonnet"], mapping["haiku"])
	if cfg.nonReasoning != "" {
		fmt.Fprintf(output, "non-reasoning model (Q6): %s\n", cfg.nonReasoning)
	}
	fmt.Fprintf(output, "output caps: %d tokens per probe, %d for replayed turns, %d for Q8\n\n", cfg.maxOutput, cfg.turnOutput, cfg.slowOutput)
	fmt.Fprintf(output, "%-3s %-40s %-4s %-14s %-6s %-12s %10s %11s  %s\n", "#", "ID", "Q", "MODEL", "METHOD", "PATH", "BODY", "EST.TOKENS", "NOTE")
	for _, result := range r.results {
		size, tokens := "-", "-"
		if result.BodyBytes != 0 {
			size = humanBytes(result.BodyBytes)
			if result.Approximate {
				size = "~" + size
			}
			tokens = fmt.Sprintf("%d", result.EstimatedInputTokens)
		}
		var notes []string
		if result.Conditional != "" {
			notes = append(notes, result.Conditional)
		}
		if result.Approximate {
			notes = append(notes, "built from a stand-in reply")
		}
		if result.Readmission != "" {
			notes = append(notes, "fails ViberMate re-admission (sent anyway)")
		}
		if result.Skipped != "" {
			notes = append(notes, "skipped: "+result.Skipped)
		}
		model := result.Model
		if model == "" {
			model = "-"
		}
		fmt.Fprintf(output, "%-3d %-40s %-4s %-14s %-6s %-12s %10s %11s  %s\n", result.Seq, result.ID, result.Question, model, result.Method, result.Path, size, tokens, strings.Join(notes, "; "))
	}
	fmt.Fprintf(output, "\nrequests: %d always, %d conditional (only when a default is refused), %d at most; budget --max-requests %d\n", counts.always, counts.conditional, counts.max(), cfg.maxRequests)
	if counts.max() > cfg.maxRequests {
		fmt.Fprintf(output, "the plan exceeds the budget: live mode will refuse to start until --max-requests is at least %d\n", counts.max())
	}
	fmt.Fprintf(output, "estimated input tokens: %d, plus %d in the Q10 overflow bodies the API should refuse before billing\n", counts.tokens, counts.refusedTokens)
	if !cfg.includeSlow && len(cfg.only) == 0 {
		fmt.Fprintln(output, "Q8 and Q9 are not planned: add --include-slow")
	}
	fmt.Fprintf(output, "bodies written to %s\n", filepath.Join(directory, "bodies"))
}

func humanBytes(size int) string {
	switch {
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(size)/(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(size)/(1<<10))
	}
	return fmt.Sprintf("%d B", size)
}

// runReport is report.json.
type runReport struct {
	Tool          string            `json:"tool"`
	Schema        int               `json:"schema"`
	StartedAt     string            `json:"started_at"`
	FinishedAt    string            `json:"finished_at"`
	BaseURL       string            `json:"base_url"`
	CodecPair     string            `json:"codec_pair"`
	CodecRevision int               `json:"codec_revision"`
	Corpus        string            `json:"corpus"`
	UserAgent     string            `json:"user_agent"`
	Models        []string          `json:"models"`
	PresetMapping map[string]string `json:"preset_mapping"`
	NonReasoning  string            `json:"non_reasoning_model,omitempty"`
	Budget        budgetReport      `json:"budget"`
	Aborted       string            `json:"aborted,omitempty"`
	FinalProfile  string            `json:"final_profile"`
	Questions     []verdict         `json:"questions"`
	Q9            *q9Report         `json:"q9,omitempty"`
	Q10           *q10Report        `json:"q10,omitempty"`
	Q11           *modelsReport     `json:"q11,omitempty"`
	Calibration   string            `json:"calibration_file,omitempty"`
	Probes        []*probeResult    `json:"probes"`
}

type budgetReport struct {
	MaxRequests      int  `json:"max_requests"`
	Sent             int  `json:"sent"`
	MaxOutputTokens  int  `json:"max_output_tokens"`
	TurnOutputTokens int  `json:"turn_output_tokens"`
	SlowOutputTokens int  `json:"slow_output_tokens"`
	OverflowTokens   int  `json:"overflow_tokens"`
	IncludeSlow      bool `json:"include_slow"`
}

// calibration is the proposed shape of the tokenestimate calibration
// fixture S16 reads: the real input_tokens of each corpus body, keyed by
// its file stem, beside the estimate at recording time.
type calibration struct {
	Schema        int                `json:"schema"`
	Note          string             `json:"note"`
	CodecPair     string             `json:"codec_pair"`
	CodecRevision int                `json:"codec_revision"`
	Corpus        string             `json:"corpus"`
	Tolerance     float64            `json:"tolerance"`
	RecordedAt    string             `json:"recorded_at"`
	Bodies        []calibrationEntry `json:"bodies"`
}

type calibrationEntry struct {
	Fixture        string `json:"fixture"`
	RequestedModel string `json:"requested_model"`
	Model          string `json:"model"`
	InputTokens    int64  `json:"input_tokens"`
	CachedTokens   int64  `json:"cached_tokens"`
	// Profile is the provider profile the body was encoded with; it differs
	// from the public profile only after a probe flipped a switch.
	Profile  string  `json:"profile"`
	Estimate int     `json:"estimate_at_recording"`
	Ratio    float64 `json:"estimate_ratio"`
}

func calibrationEntryOf(result *probeResult) (calibrationEntry, bool) {
	if !strings.HasPrefix(result.ID, "Q10.tokens.") || !accepted(result) || result.Usage == nil || result.Usage.InputTokens == nil || *result.Usage.InputTokens <= 0 {
		return calibrationEntry{}, false
	}
	entry := calibrationEntry{
		Fixture:        result.Fixture,
		RequestedModel: result.requested,
		Model:          result.Model,
		InputTokens:    *result.Usage.InputTokens,
		CachedTokens:   value(result.Usage.CachedTokens),
		Profile:        result.Profile,
		Estimate:       result.EstimatedInputTokens,
	}
	entry.Ratio = math.Round(1000*float64(entry.Estimate)/float64(entry.InputTokens)) / 1000
	return entry, true
}

func (entry calibrationEntry) within() bool {
	return math.Abs(entry.Ratio-1) <= calibrationTolerance
}

func writeReport(directory string, cfg config, r *runner, started, finished time.Time, scrub *scrubber) error {
	ran := func(id string) bool {
		for _, result := range r.results {
			if result.Question == id {
				return true
			}
		}
		return false
	}
	report := runReport{
		Tool:          "tool/responses-probe",
		Schema:        reportSchema,
		StartedAt:     started.UTC().Format(time.RFC3339),
		FinishedAt:    finished.UTC().Format(time.RFC3339),
		BaseURL:       cfg.baseURL.String(),
		CodecPair:     anthropicresponses.CodecPairID,
		CodecRevision: anthropicresponses.CodecRevision,
		Corpus:        corpusDirectory,
		UserAgent:     userAgent(),
		Models:        cfg.models,
		PresetMapping: r.presetMapping(),
		NonReasoning:  cfg.nonReasoning,
		Budget: budgetReport{
			MaxRequests: cfg.maxRequests, Sent: r.sent, MaxOutputTokens: cfg.maxOutput,
			TurnOutputTokens: cfg.turnOutput, SlowOutputTokens: cfg.slowOutput,
			OverflowTokens: cfg.overflowTokens, IncludeSlow: cfg.includeSlow,
		},
		Aborted:      r.aborted,
		FinalProfile: r.state.String(),
		Questions:    judge(r.results, r.models, ran),
		Q9:           q9Measure(r.results),
		Q10:          q10Measure(r.results),
		Q11:          r.models,
		Probes:       r.results,
	}
	var entries []calibrationEntry
	for _, result := range r.results {
		if entry, ok := calibrationEntryOf(result); ok {
			entries = append(entries, entry)
		}
	}
	if len(entries) != 0 {
		report.Calibration = "tokenestimate-calibration.json"
		fixture := calibration{
			Schema: 1,
			Note: "Proposed shape for S16's calibration test (" + calibrationDestination + "): the real input_tokens " +
				"of each corpus body as the registered path encoded it, from one live run of tool/responses-probe. " +
				"The test recomputes the estimate and keeps it within the tolerance of input_tokens.",
			CodecPair:     anthropicresponses.CodecPairID,
			CodecRevision: anthropicresponses.CodecRevision,
			Corpus:        corpusDirectory,
			Tolerance:     calibrationTolerance,
			RecordedAt:    finished.UTC().Format(time.RFC3339),
			Bodies:        entries,
		}
		encoded, err := json.MarshalIndent(fixture, "", "  ")
		if err != nil {
			return err
		}
		if err := scrub.writeFile(filepath.Join(directory, report.Calibration), append(encoded, '\n')); err != nil {
			return err
		}
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := scrub.writeFile(filepath.Join(directory, "report.json"), append(encoded, '\n')); err != nil {
		return err
	}
	return scrub.writeFile(filepath.Join(directory, "report.md"), markdown(report, entries))
}

func markdown(report runReport, entries []calibrationEntry) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "# Responses probe (S14), %s\n\n", report.FinishedAt)
	fmt.Fprintf(&out, "- Path: `%s` revision %d, bodies from `%s`\n", report.CodecPair, report.CodecRevision, report.Corpus)
	fmt.Fprintf(&out, "- Upstream: %s, User-Agent `%s`\n", report.BaseURL, report.UserAgent)
	fmt.Fprintf(&out, "- Models: %s; preset mapping opus `%s`, sonnet `%s`, haiku `%s`\n", strings.Join(report.Models, ", "), report.PresetMapping["opus"], report.PresetMapping["sonnet"], report.PresetMapping["haiku"])
	fmt.Fprintf(&out, "- Requests sent: %d (budget %d)\n", report.Budget.Sent, report.Budget.MaxRequests)
	fmt.Fprintf(&out, "- Profile after the run: `%s`\n", report.FinalProfile)
	if report.Aborted != "" {
		fmt.Fprintf(&out, "- **Aborted:** %s\n", report.Aborted)
	}
	out.WriteString("\n## Verdicts (design 18.1)\n\n| Q | Verdict | Switch to flip | Finding |\n|---|---|---|---|\n")
	for _, row := range report.Questions {
		fmt.Fprintf(&out, "| %s | %s | %s | %s |\n", row.ID, row.Verdict, cell(row.Switch), cell(row.Summary))
	}
	out.WriteString("\n## Probes\n\n| # | Probe | Model | Result | Head ms | First event ms | Longest gap ms | Input | Cached | Output | Reasoning |\n|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, result := range report.Probes {
		head, first, gap := "", "", ""
		if result.Timing != nil {
			head = fmt.Sprint(result.Timing.FirstHeaderMS)
			gap = fmt.Sprint(result.Timing.LongestGapMS)
			if result.Timing.FirstEventMS != nil {
				first = fmt.Sprint(*result.Timing.FirstEventMS)
			}
		}
		input, cached, output, reasoning := "", "", "", ""
		if usage := result.Usage; usage != nil {
			input, cached, output, reasoning = optional(usage.InputTokens), optional(usage.CachedTokens), optional(usage.OutputTokens), optional(usage.ReasoningTokens)
		}
		fmt.Fprintf(&out, "| %d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", result.Seq, result.ID, cell(result.Model), cell(result.state()), head, first, gap, input, cached, output, reasoning)
	}
	var untranslated []string
	for _, result := range report.Probes {
		if result.Translation != nil && !result.Translation.OK && !rejected(result) {
			untranslated = append(untranslated, fmt.Sprintf("| %s | %s | %s |", result.ID, cell(result.Model), cell(result.Translation.Error)))
		}
	}
	if len(untranslated) != 0 {
		out.WriteString("\n## Accepted, but not translatable\n\nThe upstream answered these, but the registered path could not turn the stream into what Claude Code receives. Each is a translation bug to fix before S19, whatever its question's verdict.\n\n| Probe | Model | ViberMate error |\n|---|---|---|\n")
		out.WriteString(strings.Join(untranslated, "\n") + "\n")
	}
	if q10 := report.Q10; q10 != nil {
		out.WriteString("\n## Q10 error forms\n\n| Probe | Status | In band | Code | Param | Numbers in message | ViberMate class | Claude Code sees |\n|---|---|---|---|---|---|---|---|\n")
		forms := append([]errorForm{}, q10.ContextLengthExceeded...)
		if q10.InvalidEncryptedContent != nil {
			forms = append(forms, *q10.InvalidEncryptedContent)
		}
		for _, form := range forms {
			class, message := "", ""
			if form.Classification != nil {
				class = form.Classification.Class
				if form.Classification.LimitTokens > 0 {
					class += fmt.Sprintf(" (window %d)", form.Classification.LimitTokens)
				}
				message = fmt.Sprintf("HTTP %d %s", form.Classification.ClientStatus, form.Classification.ClientMessage)
				if form.InBand {
					message = "event: error, " + form.Classification.ClientMessage
				}
			}
			fmt.Fprintf(&out, "| %s | %d | %t | %s | %s | %t %v | %s | %s |\n", form.Probe, form.Status, form.InBand, cell(form.Code), cell(form.Param), form.MessageHasNumbers, form.MessageNumbers, cell(class), cell(message))
		}
	}
	if len(entries) != 0 {
		fmt.Fprintf(&out, "\n## Calibration (into `%s`)\n\n| Fixture | Model | input_tokens | Estimate | Ratio |\n|---|---|---|---|---|\n", calibrationDestination)
		for _, entry := range entries {
			fmt.Fprintf(&out, "| %s | %s | %d | %d | %.3f |\n", entry.Fixture, entry.Model, entry.InputTokens, entry.Estimate, entry.Ratio)
		}
	}
	if q11 := report.Q11; q11 != nil {
		out.WriteString("\n## Q11 seed ids\n\n| Model | Listed | Preset rows | Context window |\n|---|---|---|---|\n")
		for _, seed := range q11.Seeds {
			window := "not listed"
			if seed.ContextWindow > 0 {
				window = fmt.Sprintf("%d (%s)", seed.ContextWindow, seed.WindowField)
			}
			fmt.Fprintf(&out, "| %s | %t | %s | %s |\n", seed.ID, seed.Present, cell(strings.Join(seed.Rows, ", ")), window)
		}
		if q11.Note != "" {
			fmt.Fprintf(&out, "\n%s.\n", q11.Note)
		}
	}
	return out.Bytes()
}

func optional(pointer *int64) string {
	if pointer == nil {
		return "absent"
	}
	return fmt.Sprint(*pointer)
}

func cell(value string) string {
	if value == "" {
		return "-"
	}
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(value)
}

// printSummary is the live run's closing lines.
func printSummary(output io.Writer, directory string, r *runner) {
	ran := func(id string) bool {
		for _, result := range r.results {
			if result.Question == id {
				return true
			}
		}
		return false
	}
	fmt.Fprintln(output)
	for _, row := range judge(r.results, r.models, ran) {
		line := fmt.Sprintf("%-4s %-12s %s", row.ID, row.Verdict, row.Summary)
		if row.Switch != "" {
			line += " -> flip: " + row.Switch
		}
		fmt.Fprintln(output, line)
	}
	fmt.Fprintf(output, "\n%d requests sent; report: %s\n", r.sent, filepath.Join(directory, "report.md"))
}
