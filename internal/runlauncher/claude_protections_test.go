package runlauncher_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/locales"
)

// C1 (ADR 0026): when the launcher leaves stream recovery to ViberMate, an
// installed Claude Code whose stream breaks after ViberMate has answered ends
// the turn rather than sending the same turn again as a second, separately
// billed non-streaming request. The protected arms hold for the protection to
// work; the controls show that the release still falls back on its own when
// it is not told, so the instruction still matters.
//
// Each arm launches `claude -p` through the real launcher with
// CLAUDE_CODE_MAX_RETRIES=0, so the only second request a release may send
// is its own non-streaming fallback. The fixture fails the first streaming
// request that defines tools: it cuts the connection after a delta, or ends
// the stream with the proxy's own in-band error.
func TestInstalledClaudeLeavesStreamFallbackToCore(t *testing.T) {
	binary := claudeNativeBinary(t)
	catalogs, err := locales.New()
	if err != nil {
		t.Fatal(err)
	}
	notice, err := catalogs.Render(locales.EnglishUS, "cli.protections.streamingFallback", nil)
	if err != nil {
		t.Fatal(err)
	}
	for index, test := range []struct {
		name      string
		shape     claudeGrantShape
		managed   bool // the client's target is an Upstream-only authority
		failure   claudeStreamFailure
		inherited []string
		protected bool
	}{
		{name: "protected/recognized_upstream/cut", shape: grantRecognized, managed: true, failure: streamCut, protected: true},
		{name: "protected/recognized_upstream/in_band_error", shape: grantRecognized, managed: true, failure: streamInBandError, protected: true},
		// An inherited 0 keeps the release's own fallback; the launcher must
		// replace it.
		{name: "protected/recognized_replaces_inherited_zero/cut", shape: grantRecognized, managed: true, failure: streamCut,
			inherited: []string{"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=0"}, protected: true},
		{name: "protected/verified_core_owned/cut", shape: grantVerifiedCoreOwned, managed: true, failure: streamCut, protected: true},
		// The launcher must not set the variable for a recognized client on
		// Original Destination, so the client keeps its own fallback.
		{name: "control/recognized_original/cut", shape: grantRecognized, failure: streamCut},
		{name: "control/verified_client_default/cut", shape: grantVerifiedClientDefault, managed: true, failure: streamCut},
		{name: "control/verified_client_default/in_band_error", shape: grantVerifiedClientDefault, managed: true, failure: streamInBandError},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			command := []string{"claude", "-p", "Reply with the fixture text", "--output-format", "json"}
			f := newClaudeNativeFixture(t, binary, home, command, byte(0x11+index*4), test.managed, test.shape)
			f.failStream(test.failure)
			f.environment = append(f.environment, "CLAUDE_CODE_MAX_RETRIES=0")
			f.environment = append(f.environment, test.inherited...)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			var stdout, stderr strings.Builder
			code, err := f.launcher(binary, home, &stdout, &stderr).Run(ctx, runlauncher.LaunchRequest{EnvironmentID: "work", ManagedAuth: test.managed, Command: command})
			result, parsed := claudeNativeResult(stdout.String(), false)
			fallbacks := f.fallbackRequests(t)
			t.Logf("exit=%d parsed=%v is_error=%v fallback requests=%d", code, parsed, result.IsError, fallbacks)
			if err != nil {
				t.Fatalf("the launch itself failed: code=%d err=%v stderr=%s", code, err, stderr.String())
			}
			// The command says it left recovery to ViberMate exactly when it did.
			if announced := strings.Contains(stderr.String(), notice); announced != test.protected {
				t.Fatalf("the fallback notice was printed=%v, want %v: stderr=%s", announced, test.protected, stderr.String())
			}
			if test.protected {
				if fallbacks != 0 {
					t.Fatalf("the client sent the broken turn again without streaming (%d request(s)) although the launcher left recovery to ViberMate; stdout=%s stderr=%s",
						fallbacks, stdout.String(), stderr.String())
				}
				if code == 0 && (!parsed || !result.IsError) {
					t.Fatalf("the client reported success after its stream broke: stdout=%s stderr=%s", stdout.String(), stderr.String())
				}
			} else {
				if fallbacks != 1 {
					t.Fatalf("the client sent %d non-streaming fallback request(s), want exactly 1: the release may no longer fall back on its own; stdout=%s stderr=%s",
						fallbacks, stdout.String(), stderr.String())
				}
				if code != 0 || !parsed || result.IsError || !strings.Contains(result.Result, f.reply) {
					t.Fatalf("the client's own fallback did not complete the turn: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
				}
			}
			f.assertWire(t)
		})
	}
}

// fallbackRequests counts the later /v1/messages requests that resend the
// failed turn without streaming: the same messages, and stream false or
// absent. It logs every request so a resend that changed its messages is
// visible rather than silently uncounted.
func (f *claudeNativeFixture) fallbackRequests(t *testing.T) int {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	failed := -1
	for index, request := range f.messages {
		marker := ""
		if request.failed {
			marker = " (failed here)"
		}
		t.Logf("request %d: stream=%v tools=%d messages=%s%s", index, request.stream, request.tools, request.messagesDigest[:12], marker)
		if request.failed && failed < 0 {
			failed = index
		}
	}
	if failed < 0 {
		t.Fatal("no streaming request that defines tools reached the provider, so no stream was failed")
	}
	fallbacks := 0
	for _, request := range f.messages[failed+1:] {
		if !request.stream && request.messagesDigest == f.messages[failed].messagesDigest {
			fallbacks++
		}
	}
	return fallbacks
}
