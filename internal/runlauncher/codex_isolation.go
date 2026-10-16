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
)

// Isolation is a launch compatibility fact, never release or Root evidence.
var ErrCodexIsolationUnsupported = errors.New("this Codex invocation cannot be isolated within the current Capture")

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
	kind, noDaemon := codexInteractive, false
	scope := "root"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
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
		return scope == "session" || scope == "exec"
	case "--json", "--experimental-json", "--ephemeral", "--skip-git-repo-check", "--uncommitted", "--ignore-user-config", "--ignore-rules":
		return scope == "exec"
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
	if codexSupportsNoDaemon(ctx, executable, environment) {
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

func codexSupportsNoDaemon(ctx context.Context, executable string, environment []string) bool {
	if ctx == nil || executable == "" {
		return false
	}
	probeContext, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	output := &codexHelpOutput{cancel: cancel}
	command := exec.CommandContext(probeContext, executable, "--help")
	command.Env = make([]string, 0, len(environment))
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
			command.Env = append(command.Env, entry)
		}
	}
	command.Stdout, command.Stderr = output, output
	// nil stdin is the closed /dev/null input. The probe owns its group and
	// bounded pipe draining, so grandchildren cannot hold help open forever.
	restore := configureChild(command, 100*time.Millisecond, nil)
	defer restore()
	if err := command.Start(); err != nil {
		return false
	}
	err := waitChild(command, 100*time.Millisecond)
	if err != nil || probeContext.Err() != nil || output.overflow {
		return false
	}
	for _, line := range strings.Split(string(output.data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "--no-daemon" {
			return true
		}
	}
	return false
}
