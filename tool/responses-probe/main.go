// Command responses-probe runs the live probe gate S14 of the design for
// Claude Code on OpenAI Responses: probes Q1-Q11 of its section 18.1, each
// sent once to the real API with one key, and the data S14 records (the
// error forms of section 8, the real input_tokens per corpus body for the
// tokenestimate calibration fixture, the seed model ids and their windows).
//
// Every body comes from ViberMate's own code: a sanitized Claude Code
// 2.1.329 request from the committed corpus, decoded by the registered
// anthropic-messages-to-openai-responses path and encoded for the official
// API with PublicResponsesProfile, exactly as the Exchange would send it.
// A probe changes only what its question needs (an output cap, an effort, a
// profile switch, a synthetic filler).
//
// Usage:
//
//	responses-probe [plan] [flags]            offline: print and write the plan
//	responses-probe [live] --live [flags]     send it; needs --key-file or OPENAI_API_KEY
//
// See README.md beside this file.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

type config struct {
	live           bool
	keyFile        string
	baseURL        *url.URL
	models         []string
	nonReasoning   string
	maxRequests    int
	maxOutput      int
	turnOutput     int
	slowOutput     int
	overflowTokens int
	includeSlow    bool
	only           map[string]bool
	out            string
	repo           string
	timeout        time.Duration
	verbose        bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr, time.Now))
}

func userAgent() string { return wireprofile.ViberMateUserAgent() }

var errUsage = errors.New("usage")

func parseConfig(args []string, stderr io.Writer, now time.Time) (config, error) {
	cfg := config{}
	flags := flag.NewFlagSet("responses-probe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.BoolVar(&cfg.live, "live", false, "send the probes to the API (without it nothing leaves the machine)")
	flags.StringVar(&cfg.keyFile, "key-file", "", "file holding the API key, mode 0600 or stricter (else OPENAI_API_KEY)")
	baseURL := flags.String("base-url", "https://api.openai.com", "API origin")
	models := flags.String("models", strings.Join(seedModels(), ","), "comma-separated upstream models: the preset's candidates of design 11.2")
	flags.StringVar(&cfg.nonReasoning, "non-reasoning-model", "", "a model without reasoning for Q6 (Q6 is skipped without one)")
	flags.IntVar(&cfg.maxRequests, "max-requests", 40, "refuse to start, and stop sending, beyond this many requests")
	flags.IntVar(&cfg.maxOutput, "max-output-tokens", 64, "output cap of the acceptance probes (sent as max_output_tokens)")
	flags.IntVar(&cfg.turnOutput, "turn-output-tokens", 2048, "output cap of turns whose reply is replayed (Q4 turn one, Q9)")
	flags.IntVar(&cfg.slowOutput, "slow-output-tokens", 24000, "output cap of the Q8 reasoning probes (below 32000, or Claude's cap is not sent)")
	flags.IntVar(&cfg.overflowTokens, "overflow-tokens", 1500000, "tokens of synthetic filler in the Q10 oversized context")
	flags.BoolVar(&cfg.includeSlow, "include-slow", false, "also run Q8 (long reasoning) and Q9 (10-turn cache session)")
	only := flags.String("only", "", "run only these questions, e.g. Q6,Q10 (Q11 always runs first)")
	flags.StringVar(&cfg.out, "out", "", "output directory (default: a new directory under the system temp directory)")
	flags.StringVar(&cfg.repo, "repo", "", "repository root (default: found above the working directory)")
	flags.DurationVar(&cfg.timeout, "timeout", 30*time.Minute, "per-request limit")
	flags.BoolVar(&cfg.verbose, "verbose", false, "log each request and response head to stderr (Authorization is never logged)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: responses-probe [plan|live] [flags]\n\nplan (the default) builds every body offline; live needs --live and a key.")
		flags.PrintDefaults()
	}
	mode := ""
	if len(args) != 0 && !strings.HasPrefix(args[0], "-") {
		mode, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return cfg, errUsage
	}
	if flags.NArg() != 0 {
		return cfg, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	switch mode {
	case "":
	case "plan":
		if cfg.live {
			return cfg, errors.New("plan mode never sends; drop --live or use live mode")
		}
	case "live":
		if !cfg.live {
			return cfg, errors.New("live mode sends real, billed requests: add --live to confirm")
		}
	default:
		return cfg, fmt.Errorf("unknown mode %q: use plan or live", mode)
	}
	var err error
	if cfg.baseURL, err = validBaseURL(*baseURL); err != nil {
		return cfg, err
	}
	for _, model := range strings.Split(*models, ",") {
		if model = strings.TrimSpace(model); model != "" {
			cfg.models = append(cfg.models, model)
		}
	}
	if len(cfg.models) == 0 {
		return cfg, errors.New("--models is empty")
	}
	for _, model := range append(append([]string{}, cfg.models...), cfg.nonReasoning) {
		if strings.ContainsAny(model, " /?#\"\\") {
			return cfg, fmt.Errorf("model id %q is not an id", model)
		}
	}
	if *only != "" {
		cfg.only = map[string]bool{}
		for _, id := range strings.Split(*only, ",") {
			id = strings.ToUpper(strings.TrimSpace(id))
			known := false
			for _, row := range questions {
				known = known || row.ID == id
			}
			if !known {
				return cfg, fmt.Errorf("--only names %q, which is not Q1-Q11", id)
			}
			cfg.only[id] = true
		}
	}
	switch {
	case cfg.maxRequests < 1:
		return cfg, errors.New("--max-requests must be positive")
	case cfg.maxOutput < 16 || cfg.maxOutput >= 32000:
		return cfg, errors.New("--max-output-tokens must be 16 to 31999")
	case cfg.turnOutput < 16 || cfg.turnOutput >= 32000:
		return cfg, errors.New("--turn-output-tokens must be 16 to 31999")
	case cfg.slowOutput < 16 || cfg.slowOutput >= 32000:
		return cfg, errors.New("--slow-output-tokens must be 16 to 31999: Claude's max_tokens of 32000 or more is not sent at all")
	case cfg.overflowTokens < 1000 || cfg.overflowTokens > 3500000:
		return cfg, errors.New("--overflow-tokens must be 1000 to 3500000 (the client body limit is 16 MiB)")
	case cfg.timeout <= 0:
		return cfg, errors.New("--timeout must be positive")
	}
	if cfg.out == "" {
		cfg.out = filepath.Join(os.TempDir(), "vibermate-responses-probe", now.UTC().Format("20060102T150405Z"))
	}
	return cfg, nil
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, now func() time.Time) int {
	started := now()
	cfg, err := parseConfig(args, stderr, started)
	if err != nil {
		if !errors.Is(err, errUsage) {
			fmt.Fprintln(stderr, "responses-probe:", err)
		}
		return 2
	}
	var key apiKey
	if cfg.live {
		if key, err = loadKey(cfg.keyFile, getenv); err != nil {
			fmt.Fprintln(stderr, "responses-probe:", err)
			return 2
		}
	}
	scrub := newScrubber(key)
	stdout, stderr = scrub.writer(stdout), scrub.writer(stderr)
	if err := probe(ctx, cfg, key, scrub, stdout, stderr, started, now); err != nil {
		fmt.Fprintln(stderr, "responses-probe:", scrub.string(err.Error()))
		return 1
	}
	return 0
}

func probe(ctx context.Context, cfg config, key apiKey, scrub *scrubber, stdout, stderr io.Writer, started time.Time, now func() time.Time) error {
	repository := cfg.repo
	if repository == "" {
		working, err := os.Getwd()
		if err != nil {
			return err
		}
		if repository, err = findRepository(working); err != nil {
			return err
		}
	}
	loaded, err := loadCorpus(repository)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.out, 0o700); err != nil {
		return err
	}
	session := "responses-probe-" + started.UTC().Format("20060102T150405Z")
	planModels := append([]string{}, cfg.models...)
	if cfg.nonReasoning != "" {
		planModels = append(planModels, cfg.nonReasoning)
	}
	// The plan pass runs every probe, conditional ones included, against
	// an offline stand-in: it is the plan mode output and, before a live
	// run, the request count the budget is checked against.
	plan := newRunner(cfg, loaded, &planSender{models: planModels}, scrub, true, session, nil)
	plan.run(ctx)
	if !cfg.live {
		if err := writeBodies(cfg.out, plan.results, scrub); err != nil {
			return err
		}
		if err := writePlan(cfg.out, plan, scrub); err != nil {
			return err
		}
		printPlan(stdout, cfg, plan, cfg.out)
		return nil
	}
	if counts := countPlan(plan.results); counts.max() > cfg.maxRequests {
		return fmt.Errorf("the plan needs up to %d requests (%d always, %d conditional) but --max-requests is %d; raise it or drop --include-slow; nothing was sent",
			counts.max(), counts.always, counts.conditional, cfg.maxRequests)
	}
	var verbose io.Writer
	if cfg.verbose {
		verbose = stderr
	}
	fmt.Fprintf(stdout, "responses-probe live: %s, up to %d requests, writing %s\n", cfg.baseURL.Redacted(), countPlan(plan.results).max(), cfg.out)
	live := newRunner(cfg, loaded, newLiveSender(cfg.baseURL, key, scrub, verbose), scrub, false, session, stdout)
	live.run(ctx)
	if err := writeBodies(cfg.out, live.results, scrub); err != nil {
		return err
	}
	if err := writeReport(cfg.out, cfg, live, started, now(), scrub); err != nil {
		return err
	}
	printSummary(stdout, cfg.out, live)
	if live.aborted != "" {
		return errors.New(live.aborted)
	}
	if ctx.Err() != nil {
		return errors.New("interrupted: the report covers the probes sent before the interrupt")
	}
	return nil
}

func writePlan(directory string, plan *runner, scrub *scrubber) error {
	counts := countPlan(plan.results)
	encoded, err := marshalIndentNoEscape(map[string]any{
		"tool":                   "tool/responses-probe",
		"mode":                   "plan",
		"requests_always":        counts.always,
		"requests_conditional":   counts.conditional,
		"requests_at_most":       counts.max(),
		"estimated_input_tokens": counts.tokens,
		"overflow_input_tokens":  counts.refusedTokens,
		"preset_mapping":         plan.presetMapping(),
		"probes":                 plan.results,
	})
	if err != nil {
		return err
	}
	return scrub.writeFile(filepath.Join(directory, "plan.json"), encoded)
}

func marshalIndentNoEscape(value any) ([]byte, error) {
	compact, err := marshalNoEscape(value)
	if err != nil {
		return nil, err
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, compact, "", "  "); err != nil {
		return nil, err
	}
	return append(indented.Bytes(), '\n'), nil
}
