package runlauncher

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInstalledCodexCaptureIsolationStartupContract(t *testing.T) {
	executable := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if executable == "" {
		t.Skip("set VIBERMATE_CODEX_ACCEPTANCE to an isolated pinned Codex binary")
	}
	if !filepath.IsAbs(executable) {
		t.Fatal("Codex fixture must be absolute")
	}
	environment := []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "CODEX_HOME=" + t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	versionCommand := exec.CommandContext(ctx, executable, "--version")
	versionCommand.Env = environment
	version, err := versionCommand.Output()
	if err != nil {
		t.Fatal("isolated pinned Codex version probe failed")
	}
	// This selects the pinned fixture expectation, not authorization. Actual
	// Launcher eligibility is tested through validated grants below.
	legacy := strings.TrimSpace(string(version)) == "codex-cli 0.145.0"
	if !legacy && strings.TrimSpace(string(version)) != "codex-cli 0.159.2" && strings.TrimSpace(string(version)) != "codex-cli 0.160.0" {
		t.Fatal("startup-contract fixture must be an approved exact test version")
	}
	for _, original := range [][]string{nil, {"resume", "fixture-thread"}, {"fork", "--last"}, {"--model", "exec", "resume", "fixture-thread"},
		{"exec", "fixture prompt"}, {"review", "--base", "main"}, {"app-server", "--listen", "stdio://"}} {
		prepared, err := buildChildArguments(append([]string{"codex"}, original...), environment, "codex_responses_http")
		if err != nil {
			t.Fatal(err)
		}
		got, err := prepareCodexCaptureArguments(context.Background(), executable, original, prepared, environment, legacy)
		want := prepared
		if !legacy && (len(original) == 0 || original[0] != "exec" && original[0] != "review" && original[0] != "app-server") {
			want = append([]string{"--no-daemon"}, prepared...)
		}
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("pinned startup contract %v: %v", original, err)
		}
	}
	// Exercise native Clap as well as our consumer: attached images end at
	// their token, while spaced values remain variadic until the next option.
	for _, fixture := range []struct {
		args      []string
		usage     string
		wantError bool
	}{
		{[]string{"--image=fixture.png", "app-server", "proxy"}, "Usage: codex app-server proxy", false},
		{[]string{"-ifixture.png", "app-server", "proxy"}, "Usage: codex app-server proxy", false},
		{[]string{"-i=fixture.png", "app-server", "proxy"}, "Usage: codex app-server proxy", false},
		{[]string{"--image", "fixture.png", "app-server", "proxy"}, "Usage: codex [OPTIONS]", false},
		{[]string{"--yolo", "resume"}, "Usage: codex resume", false},
		{[]string{"--not-so-yolo", "resume"}, "Usage: codex resume", legacy},
		{[]string{"exec", "--experimental-json", "--yolo"}, "Usage: codex exec", false},
	} {
		helper := exec.CommandContext(ctx, executable, append(slices.Clone(fixture.args), "--help")...)
		helper.Env = environment
		data, err := helper.Output()
		if (err != nil) != fixture.wantError || !fixture.wantError && !strings.Contains(string(data), fixture.usage) {
			t.Fatalf("native pinned argument grammar disagrees: args=%v failed=%v", fixture.args, err != nil)
		}
	}
	t.Logf("%s: interactive/resume/fork/global-value/exec/review/foreground startup contracts passed", strings.TrimSpace(string(version)))
}

func TestCodexInvocationClass(t *testing.T) {
	for _, test := range []struct {
		args []string
		want codexInvocationKind
	}{
		{nil, codexInteractive},
		{[]string{"resume", "fixture-thread"}, codexInteractive},
		{[]string{"fork", "--last"}, codexInteractive},
		{[]string{"exec", "fixture prompt"}, codexSeparateProcess},
		{[]string{"e", "--json", "fixture prompt"}, codexSeparateProcess},
		{[]string{"review", "--base", "main"}, codexSeparateProcess},
		{[]string{"app-server"}, codexSeparateProcess},
		{[]string{"--model", "exec", "resume", "fixture-thread"}, codexInteractive},
		{[]string{"fixture-prompt", "exec", "fixture prompt"}, codexSeparateProcess},
		{[]string{"--model=exec", "resume", "fixture-thread"}, codexInteractive},
		{[]string{"-mexec", "resume", "fixture-thread"}, codexInteractive},
		{[]string{"-hmexec", "resume", "fixture-thread"}, codexInteractive},
		{[]string{"-cmodel=exec", "resume", "fixture-thread"}, codexInteractive},
		{[]string{"--", "exec"}, codexInteractive},
		{[]string{"--", "--remote", "ws://fixture"}, codexInteractive},
		{[]string{"--remote", "ws://127.0.0.1:9999"}, codexRemote},
		{[]string{"resume", "--remote=ws://fixture", "thread"}, codexRemote},
		{[]string{"--remote-auth-token-env", "exec", "resume", "thread"}, codexRemote},
		{[]string{"resume", "--model", "exec", "thread"}, codexInteractive},
		{[]string{"--image", "exec", "daemon", "--model", "fixture"}, codexInteractive},
		{[]string{"--image=exec", "exec", "prompt"}, codexSeparateProcess},
		{[]string{"-iexec", "app-server", "--stdio"}, codexSeparateProcess},
		{[]string{"-i=exec", "review", "--base", "main"}, codexSeparateProcess},
		{[]string{"--image", "fixture.png", "app-server", "proxy"}, codexInteractive},
		{[]string{"-i", "fixture.png", "app-server", "proxy"}, codexInteractive},
		{[]string{"--image=exec", "--", "app-server", "proxy"}, codexInteractive},
		{[]string{"-iexec", "daemon", "--", "prompt"}, codexInteractive},
		{[]string{"app-server", "--listen", "daemon"}, codexSeparateProcess},
		{[]string{"app-server", "--listen=proxy", "--stdio"}, codexSeparateProcess},
		{[]string{"app-server", "--config", "daemon"}, codexSeparateProcess},
		{[]string{"app-server", "--ws-token-file", "proxy"}, codexSeparateProcess},
		{[]string{"app-server", "--"}, codexSeparateProcess},
		{[]string{"exec", "resume", "--last", "prompt"}, codexSeparateProcess},
		{[]string{"exec", "--ignore-user-config", "--ignore-rules", "--json", "prompt"}, codexSeparateProcess},
		{[]string{"exec", "--worktree", "--thread-source", "daemon", "resume", "--last", "prompt"}, codexSeparateProcess},
		{[]string{"exec", "--model", "daemon", "--", "--remote"}, codexSeparateProcess},
		{[]string{"--yolo", "prompt"}, codexInteractive},
		{[]string{"resume", "--yolo", "fixture-thread"}, codexInteractive},
		{[]string{"--not-so-yolo", "fork", "--last"}, codexInteractive},
		{[]string{"exec", "--yolo", "prompt"}, codexSeparateProcess},
		{[]string{"exec", "--not-so-yolo", "prompt"}, codexSeparateProcess},
		{[]string{"exec", "--experimental-json", "resume", "--last", "prompt"}, codexSeparateProcess},
	} {
		t.Run(strings.Join(test.args, "_"), func(t *testing.T) {
			got, err := classifyCodexInvocation(test.args)
			if err != nil || got != test.want {
				t.Fatalf("class=%v err=%v, want %v", got, err, test.want)
			}
		})
	}
	for _, args := range [][]string{
		{"agents"}, {"queue", "thread", "prompt"}, {"archive", "thread"}, {"remote-control", "start"},
		{"stdio-to-uds"}, {"responses-api-proxy"}, {"cloud-tasks"}, {"execpolicy", "check"},
		{"--image=exec", "app-server", "daemon"}, {"-ifixture.png", "app-server", "proxy"},
		{"-i=fixture.png", "stdio-to-uds"}, {"-hi=fixture.png", "app-server", "proxy"},
		{"--image=fixture.png", "agents"}, {"--image=fixture.png", "app-server", "--listen=daemon", "proxy"},
		{"--no-daemon", "--image=fixture.png", "app-server", "proxy"},
		{"app-server", "daemon", "start"}, {"app-server", "proxy"},
		{"app-server", "--listen", "proxy", "daemon", "start"},
		{"app-server", "--config=daemon", "proxy"}, {"app-server", "--", "daemon", "start"},
		{"fixture-prompt", "app-server", "proxy"}, {"fixture-prompt", "agents"},
		{"app-server", "unknown-entry"}, {"--unknown", "exec"}, {"resume", "--unknown", "exec"},
		{"--model"}, {"--model", "--"}, {"--image"}, {"--no-daemon=true"},
		{"app-server", "--yolo"}, {"app-server", "--not-so-yolo"}, {"app-server", "--experimental-json"},
		{"--experimental-json", "prompt"}, {"--yolo=true"}, {"--not-so-yolo=false"}, {"exec", "--experimental-json=true"},
	} {
		if _, err := classifyCodexInvocation(args); !errors.Is(err, ErrCodexIsolationUnsupported) {
			t.Errorf("unproven %v: %v", args, err)
		}
	}
}

func TestCodexCaptureIsolationNativeAliasesPreserveOriginalSpelling(t *testing.T) {
	for _, original := range [][]string{
		{"--no-daemon", "--yolo", "prompt"}, {"resume", "--no-daemon", "--not-so-yolo", "fixture-thread"},
		{"exec", "--yolo", "--experimental-json", "prompt"}, {"exec", "--not-so-yolo", "prompt"},
	} {
		got, err := prepareCodexCaptureArguments(context.Background(), "/not/executed", original, original, nil, false)
		if err != nil || !slices.Equal(got, original) {
			t.Errorf("native alias rewritten/rejected: %v => %v (%v)", original, got, err)
		}
	}
}

func TestCodexCaptureIsolationInlineImagesPreserveIndependentArguments(t *testing.T) {
	for _, original := range [][]string{
		{"--image=fixture.png", "exec", "prompt"}, {"-ifixture.png", "app-server", "--stdio"},
		{"-i=fixture.png", "review", "--base", "main"},
	} {
		got, err := prepareCodexCaptureArguments(context.Background(), "/not/executed", original, original, nil, false)
		if err != nil || !slices.Equal(got, original) {
			t.Errorf("inline image independent argv changed: %v => %v (%v)", original, got, err)
		}
	}
}

func codexHelpFixture(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX help fixture")
	}
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexCaptureIsolationPreparesArguments(t *testing.T) {
	client := codexHelpFixture(t, "printf 'Options:\\n      --no-daemon  Run without the shared background server\\n'\n")
	for _, original := range [][]string{nil, {"resume", "thread"}, {"fork", "--last"}, {"--model", "exec", "resume", "thread"},
		{"--image=--no-daemon", "--", "prompt"}, {"--", "--no-daemon"}, {"--model=--no-daemon", "prompt"}} {
		got, err := prepareCodexCaptureArguments(context.Background(), client, original, original, nil, false)
		want := append([]string{"--no-daemon"}, original...)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%v => %v (%v), want %v", original, got, err, want)
		}
	}
	for _, original := range [][]string{{"--no-daemon", "resume", "thread"}, {"resume", "--no-daemon", "thread"}, {"exec", "prompt"}, {"app-server", "--listen", "daemon"}} {
		got, err := prepareCodexCaptureArguments(context.Background(), "/not/executed", original, original, nil, false)
		if err != nil || !slices.Equal(got, original) {
			t.Errorf("idempotent/foreground %v => %v (%v)", original, got, err)
		}
	}
	for _, original := range [][]string{{"--no-daemon", "--remote", "ws://fixture"}, {"agents"}, {"app-server", "proxy"}, {"queue", "thread"}} {
		if _, err := prepareCodexCaptureArguments(context.Background(), client, original, original, nil, true); !errors.Is(err, ErrCodexIsolationUnsupported) {
			t.Errorf("unsupported %v: %v", original, err)
		}
	}
}

func TestCodexCaptureIsolationLegacyRequiresPreparedRecipe(t *testing.T) {
	client := codexHelpFixture(t, "printf 'codex-cli 0.145.0\\nOptions:\\n'\n")
	original := []string{"resume", "thread"}
	prepared := []string{"--config", `model_provider="openai"`, "--config", `openai_base_url="https://chatgpt.com/backend-api/codex"`,
		"--config", "features.responses_websockets=false", "--disable", "enable_request_compression", "resume", "thread"}
	got, err := prepareCodexCaptureArguments(context.Background(), "/not/executed", original, prepared, nil, true)
	if err != nil || !slices.Equal(got, prepared) {
		t.Fatalf("verified legacy recipe rejected: %v %v", got, err)
	}
	for _, test := range []struct {
		prepared []string
		legacy   bool
	}{
		{prepared, false}, {original, true}, {append([]string{"--config", "foo=bar"}, original...), true},
		{prepared[2:], true}, {append(slices.Clone(prepared[:8]), "fork", "thread"), true},
	} {
		if _, err := prepareCodexCaptureArguments(context.Background(), client, original, test.prepared, nil, test.legacy); !errors.Is(err, ErrCodexIsolationUnsupported) {
			t.Errorf("legacy precondition missing: %v legacy=%v err=%v", test.prepared, test.legacy, err)
		}
	}
}

func TestCodexHelpBound(t *testing.T) {
	for _, script := range []string{
		"sleep 5\n",
		"trap '' TERM\nwhile :; do printf '012345678901234567890123456789\\n'; done\n",
		"(trap '' TERM; sleep 5) &\nprintf '      --no-daemon\\n'\nexit 0\n",
		"printf '      --no-daemon\\n'; exit 7\n",
		"printf 'example prompt contains --no-daemon but no option declaration\\n'\n",
	} {
		client := codexHelpFixture(t, script)
		start := time.Now()
		_, err := prepareCodexCaptureArguments(context.Background(), client, nil, nil, nil, false)
		if !errors.Is(err, ErrCodexIsolationUnsupported) || time.Since(start) > 2500*time.Millisecond {
			t.Errorf("unbounded/accepted help: elapsed=%v err=%v", time.Since(start), err)
		}
		if err != nil && strings.Contains(err.Error(), "example prompt") {
			t.Fatal("raw help leaked into error")
		}
	}
}

func TestCodexHelpBoundCombinedOutputAndSanitizedEnvironment(t *testing.T) {
	client := codexHelpFixture(t, `
test "$1" = --help || exit 9
test -z "$HTTP_PROXY$http_proxy$HTTPS_PROXY$ALL_PROXY$NO_PROXY$VIBERMATE_CAPTURE_RUN_ID$VIBERMATE_TOKEN$CODEX_API_KEY$OPENAI_API_KEY$CODEX_BASE_URL" || exit 8
read input && exit 7
printf '      --no-daemon\n'
`)
	env := []string{"PATH=/usr/bin:/bin", "HTTP_PROXY=secret", "http_proxy=secret", "HTTPS_PROXY=secret", "ALL_PROXY=secret", "NO_PROXY=secret",
		"VIBERMATE_CAPTURE_RUN_ID=capability", "VIBERMATE_TOKEN=secret", "CODEX_API_KEY=secret", "OPENAI_API_KEY=secret", "CODEX_BASE_URL=secret"}
	got, err := prepareCodexCaptureArguments(context.Background(), client, nil, nil, env, false)
	if err != nil || !slices.Equal(got, []string{"--no-daemon"}) {
		t.Fatalf("probe inherited capability/proxy/stdin: %v %v", got, err)
	}
	// Each stream is under 64 KiB; together they exceed the shared cap.
	client = codexHelpFixture(t, "printf '      --no-daemon\\n'\nhead -c 40000 /dev/zero\nhead -c 40000 /dev/zero >&2\n")
	if _, err := prepareCodexCaptureArguments(context.Background(), client, nil, nil, []string{"PATH=/usr/bin:/bin"}, false); !errors.Is(err, ErrCodexIsolationUnsupported) {
		t.Fatalf("combined output limit ignored: %v", err)
	}
}

func TestCodexHelpBoundReapsProbeAndPipeHoldingDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process cleanup fixture")
	}
	for _, overflow := range []bool{false, true} {
		directory := t.TempDir()
		parentPath, descendantPath := filepath.Join(directory, "parent-pid"), filepath.Join(directory, "descendant-pid")
		script := "printf '%s' $$ > " + strconv.Quote(parentPath) + "\n(trap '' TERM; sleep 20) &\nprintf '%s' $! > " + strconv.Quote(descendantPath) + "\n"
		if overflow {
			script += "trap '' TERM\nwhile :; do printf '012345678901234567890123456789\\n'; done\n"
		} else {
			script += "printf '      --no-daemon\\n'\nexit 0\n"
		}
		client := codexHelpFixture(t, script)
		start := time.Now()
		if _, err := prepareCodexCaptureArguments(context.Background(), client, nil, nil, []string{"PATH=/usr/bin:/bin"}, false); !errors.Is(err, ErrCodexIsolationUnsupported) {
			t.Fatalf("unbounded probe accepted: %v", err)
		}
		if time.Since(start) > 2500*time.Millisecond {
			t.Fatal("probe cleanup exceeded its bound")
		}
		for _, path := range []string{parentPath, descendantPath} {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil || pid <= 0 {
				t.Fatal("fixture did not record its own process")
			}
			process, err := os.FindProcess(pid)
			if err != nil {
				continue
			}
			deadline := time.Now().Add(time.Second)
			for process.Signal(syscall.Signal(0)) == nil && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if process.Signal(syscall.Signal(0)) == nil {
				_ = process.Kill()
				t.Errorf("probe-owned process survived cleanup: overflow=%v", overflow)
			}
		}
	}
}
