package runlauncher

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/cyberaccess"
)

// Isolation is a launch compatibility fact, never release or Root evidence.
var ErrCodexIsolationUnsupported = errors.New("this Codex invocation cannot be isolated within the current Capture")

// CodexProbeFailure reports only a locally selected reason, never executable
// paths, child output, environment values or operating-system error text.
type CodexProbeFailure struct {
	Reason string
}

func (failure *CodexProbeFailure) SafeSummary() string {
	reason := "unknown"
	switch failure.Reason {
	case "start_failed", "exit_failed", "output_limit", "timed_out":
		reason = failure.Reason
	}
	return "reason=" + reason
}

func (failure *CodexProbeFailure) Error() string {
	return "Codex capability probe failed: " + failure.SafeSummary()
}

type codexInvocationKind uint8

const (
	codexInteractive codexInvocationKind = iota
	codexSeparateProcess
	codexRemote
)

func classifyCodexInvocation(args []string) (codexInvocationKind, error) {
	kind, _, err := parseCodexInvocation(args)
	return kind, err
}

// Parse only the proven entrypoints. Unknown options fail closed: guessing their
// arity could turn a value into a command and omit required isolation. Prompt
// text and variadic image values are opaque, and -- ends option parsing.
func parseCodexInvocation(args []string) (codexInvocationKind, bool, error) {
	return walkCodexInvocation(args, nil)
}

// visit receives only parsed options, never positional prompts or option values
// interpreted as options. Policy consumers share this fail-closed grammar.
func walkCodexInvocation(args []string, visit func(name, value string) error) (codexInvocationKind, bool, error) {
	return walkCodexInvocationWithLiteral(args, visit, nil)
}

func walkCodexInvocationWithLiteral(args []string, visit func(name, value string) error, literal *int) (codexInvocationKind, bool, error) {
	kind, noDaemon := codexInteractive, false
	scope := "root"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if literal != nil {
				*literal = i
			}
			if scope == "app-server" && i+1 < len(args) {
				return 0, false, ErrCodexIsolationUnsupported
			}
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			name, value, attached := strings.Cut(arg, "=")
			if !strings.HasPrefix(arg, "--") {
				// A short cluster ends at the first value-taking option; its
				// remaining bytes are that option's value, not more flags.
				name, value, attached = "", "", false
				for n := 1; n < len(arg); n++ {
					short := arg[n]
					if short == 'h' || short == 'V' {
						continue
					}
					switch short {
					case 'c':
						name = "--config"
					case 'i':
						name = "--image"
					case 'm':
						name = "--model"
					case 'p':
						name = "--profile"
					case 's':
						name = "--sandbox"
					case 'C':
						name = "--cd"
					case 'a':
						name = "--ask-for-approval"
					case 'o':
						name = "--output-last-message"
					default:
						return 0, false, ErrCodexIsolationUnsupported
					}
					value, attached = strings.TrimPrefix(arg[n+1:], "="), n+1 < len(arg)
					break
				}
				if name == "" {
					continue
				}
			}
			if name == "--remote" || name == "--remote-auth-token-env" {
				kind = codexRemote
			}
			if name == "--no-daemon" {
				noDaemon = true
			}
			if codexFlag(name, scope) {
				if attached {
					return 0, false, ErrCodexIsolationUnsupported
				}
				if visit != nil {
					if err := visit(name, ""); err != nil {
						return 0, false, err
					}
				}
				continue
			}
			if !codexValueOption(name, scope) {
				return 0, false, ErrCodexIsolationUnsupported
			}
			if !attached {
				if i+1 == len(args) || strings.HasPrefix(args[i+1], "-") {
					return 0, false, ErrCodexIsolationUnsupported
				}
				i++
				value = args[i]
			}
			if value == "" {
				return 0, false, ErrCodexIsolationUnsupported
			}
			if name == "--cyber-access-program" {
				if _, err := cyberaccess.ParsePreference(value); err != nil {
					return 0, false, ErrCodexIsolationUnsupported
				}
			}
			if visit != nil {
				if err := visit(name, value); err != nil {
					return 0, false, err
				}
			}
			// Clap's variadic scan applies only to a separate image value.
			// An attached long/compact short value ends at this argv token.
			if name == "--image" && !attached {
				for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
				}
			}
			continue
		}
		if scope == "app-server" {
			return 0, false, ErrCodexIsolationUnsupported
		}
		// Clap still recognizes a root subcommand after a preceding prompt.
		// Only an option value or -- shields a command-looking token.
		if scope != "root" {
			continue
		}
		switch arg {
		case "exec", "e", "review":
			if kind != codexRemote {
				kind = codexSeparateProcess
			}
			scope = "exec"
			if arg == "review" {
				scope = "review"
			}
		case "app-server":
			if kind != codexRemote {
				kind = codexSeparateProcess
			}
			scope = "app-server"
		case "resume", "fork":
			scope = "session"
		case "agents", "queue", "archive", "delete", "unarchive", "remote-control", "migrate-rollouts",
			"login", "logout", "mcp", "mcp-server", "plugin", "app", "completion", "update", "doctor",
			"sandbox", "debug", "execpolicy", "apply", "a", "cloud", "cloud-tasks", "exec-server", "features", "help", "tcp-tunnel",
			"responses-api-proxy", "stdio-to-uds":
			return 0, false, ErrCodexIsolationUnsupported
		}
	}
	return kind, noDaemon, nil
}

func codexFlag(name, scope string) bool {
	switch name {
	case "--help", "--version", "--strict-config":
		return true
	case "--oss", "--search", "--full-auto", "--approve-for-me", "--dangerously-bypass-approvals-and-sandbox",
		"--dangerously-bypass-hook-trust", "--yolo", "--not-so-yolo":
		return scope != "app-server"
	case "--no-daemon", "--no-alt-screen":
		return scope == "root" || scope == "session"
	case "--worktree":
		return scope != "app-server"
	case "--last", "--all", "--include-non-interactive":
		return scope == "session" || scope == "exec" || scope == "review"
	case "--json", "--experimental-json", "--ephemeral", "--skip-git-repo-check", "--uncommitted", "--ignore-user-config", "--ignore-rules":
		return scope == "exec" || scope == "review"
	case "--stdio", "--analytics-default-enabled":
		return scope == "app-server"
	}
	return false
}

func codexValueOption(name, scope string) bool {
	switch name {
	case "--config", "--enable", "--disable", "--remote", "--remote-auth-token-env":
		return true
	case "--image", "--model", "--profile", "--sandbox", "--cd", "--add-dir", "--ask-for-approval", "--local-provider":
		return scope != "app-server"
	case "--output-last-message", "--output-schema", "--color", "--base", "--commit", "--title", "--thread-source":
		return scope == "exec" || scope == "review"
	case "--cyber-access-program":
		return scope == "exec"
	case "--listen", "--code-mode-host", "--ws-auth", "--ws-token-file", "--ws-token-sha256", "--ws-shared-secret-file",
		"--ws-issuer", "--ws-audience", "--ws-max-clock-skew-seconds":
		return scope == "app-server"
	}
	return false
}

func prepareCodexCaptureArguments(ctx context.Context, executable string, original, prepared, environment []string, legacyIndependent bool) ([]string, error) {
	kind, _, err := parseCodexInvocation(original)
	if err != nil || kind == codexRemote || len(prepared) < len(original) || !slices.Equal(prepared[len(prepared)-len(original):], original) {
		return nil, ErrCodexIsolationUnsupported
	}
	preparedKind, noDaemon, err := parseCodexInvocation(prepared)
	if err != nil || preparedKind != kind {
		return nil, ErrCodexIsolationUnsupported
	}
	if kind == codexSeparateProcess || noDaemon {
		return slices.Clone(prepared), nil
	}
	// The exact verified legacy recipe already proves Embedded startup. It
	// needs no help execution, and a CLI's self-reported version grants nothing.
	if legacyIndependent && hasVerifiedRecipeConfigPrefix(prepared, original) {
		return slices.Clone(prepared), nil
	}
	supported, err := codexSupportsNoDaemon(ctx, executable, environment)
	if err != nil {
		return nil, err
	}
	if supported {
		return append([]string{"--no-daemon"}, prepared...), nil
	}
	return nil, ErrCodexIsolationUnsupported
}

func hasVerifiedRecipeConfigPrefix(prepared, original []string) bool {
	prefixLength := len(prepared) - len(original)
	if prefixLength != 4 && prefixLength != 6 || !slices.Equal(prepared[prefixLength:], original) {
		return false
	}
	prefix := prepared[:prefixLength]
	if prefixLength == 6 {
		if prefix[0] != "--config" {
			return false
		}
		base, ok := strings.CutPrefix(prefix[1], "openai_base_url=")
		if !ok {
			return false
		}
		base, err := strconv.Unquote(base)
		if err != nil || base == "" {
			return false
		}
		if _, err = codexOrigin([]string{"CODEX_BASE_URL=" + base}); err != nil {
			return false
		}
		prefix = prefix[2:]
	}
	return slices.Equal(prefix, []string{"--config", "features.responses_websockets=false", "--disable", "enable_request_compression"})
}

const codexHelpLimit = 64 * 1024

// Both output pipes share a single budget and cancellation. Bytes are never
// included in errors or diagnostics, including failed/hostile help output.
type codexHelpOutput struct {
	mu       sync.Mutex
	data     []byte
	overflow bool
	cancel   context.CancelFunc
}

func (output *codexHelpOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	if len(data) > codexHelpLimit-len(output.data) {
		output.overflow = true
		output.cancel()
		return len(data), nil
	}
	output.data = append(output.data, data...)
	return len(data), nil
}

func codexSupportsNoDaemon(ctx context.Context, executable string, environment []string) (bool, error) {
	probeEnvironment := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		key = strings.ToUpper(key)
		_, managed := managedEnvironment[key]
		_, codex := codexManagedEnvironment[key]
		_, claude := claudeManagedEnvironment[key]
		if !managed && !codex && !claude && !strings.HasPrefix(key, "VIBERMATE_") {
			probeEnvironment = append(probeEnvironment, entry)
		}
	}
	data, err := probeCodex(ctx, executable, []string{"--help"}, probeEnvironment, "", 30*time.Second)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "--no-daemon" {
			return true, nil
		}
	}
	return false, nil
}

func probeCodex(ctx context.Context, executable string, arguments, environment []string, directory string, timeout time.Duration) ([]byte, error) {
	if ctx == nil || executable == "" {
		return nil, &CodexProbeFailure{Reason: "start_failed"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	probeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output := &codexHelpOutput{cancel: cancel}
	command := exec.CommandContext(probeContext, executable, arguments...)
	command.Env = environment
	command.Dir = directory
	command.Stdout, command.Stderr = output, output
	// nil stdin is the closed /dev/null input. The probe owns its group and
	// bounded pipe draining, so grandchildren cannot hold help open forever.
	restore := configureChild(command, 100*time.Millisecond, nil)
	defer restore()
	if err := command.Start(); err != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, &CodexProbeFailure{Reason: "start_failed"}
	}
	err := waitChild(command, 100*time.Millisecond)
	// Caller cancellation wins even if overflow or the internal deadline also
	// canceled the probe while its owned process group was being cleaned up.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if output.overflow {
		return nil, &CodexProbeFailure{Reason: "output_limit"}
	}
	if errors.Is(probeContext.Err(), context.DeadlineExceeded) {
		return nil, &CodexProbeFailure{Reason: "timed_out"}
	}
	if err != nil {
		return nil, &CodexProbeFailure{Reason: "exit_failed"}
	}
	return output.data, nil
}
