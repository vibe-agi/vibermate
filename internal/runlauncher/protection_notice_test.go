package runlauncher

import (
	"slices"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/locales"
)

// noticeLines is what announceProtections writes for one decision, one line
// per entry.
func noticeLines(t *testing.T, base []string, decision streamingFallbackDecision) []string {
	t.Helper()
	var stderr strings.Builder
	launcher := &Launcher{config: Config{Stderr: &stderr, BaseEnvironment: base}}
	launcher.announceProtections(decision)
	if stderr.Len() == 0 {
		return nil
	}
	text := stderr.String()
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("the notice does not end its line: %q", text)
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func renderedNotice(t *testing.T, locale locales.Locale, key, value string) string {
	t.Helper()
	catalogs, err := locales.New()
	if err != nil {
		t.Fatal(err)
	}
	var parameters map[string]string
	if key != "cli.protections.streamingFallback" {
		parameters = map[string]string{"variable": clientadapter.RecognizedProtectionsVariable, "value": value}
	}
	message, err := catalogs.Render(locale, key, parameters)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

// The command says only what the launcher itself set for the client (ADR
// 0026): the fallback instruction when it set it, the operator switch when it
// withheld it, and a switch value it could not read, in that order and in the
// terminal's language. It never names a protection the Runtime applies.
func TestProtectionNoticeNamesOnlyWhatTheLauncherSet(t *testing.T) {
	t.Parallel()

	const (
		fallback     = "cli.protections.streamingFallback"
		switchedOff  = "cli.protections.switchedOff"
		unrecognized = "cli.protections.switchUnrecognized"
	)
	coreOwned := clientadapter.StreamingFallbackCoreOwned
	clientDefault := clientadapter.StreamingFallbackClientDefault
	type entry struct{ key, value string }
	for _, test := range []struct {
		name     string
		decision streamingFallbackDecision
		want     []entry
	}{
		{"core_owned", streamingFallbackDecision{policy: coreOwned},
			[]entry{{fallback, ""}}},
		{"core_owned with an unreadable switch",
			streamingFallbackDecision{policy: coreOwned, switchValue: "banana", switchUnrecognized: true},
			[]entry{{unrecognized, "banana"}, {fallback, ""}}},
		{"switched off", streamingFallbackDecision{policy: clientDefault, switchedOff: true, switchValue: "0"},
			[]entry{{switchedOff, "0"}}},
		{"switched off with spaces", streamingFallbackDecision{policy: clientDefault, switchedOff: true, switchValue: " Off "},
			[]entry{{switchedOff, `" Off "`}}},
		{"an unreadable switch alone",
			streamingFallbackDecision{policy: clientDefault, switchValue: "of\x1bf", switchUnrecognized: true},
			[]entry{{unrecognized, `"of\x1bf"`}}},
		{"client_default", streamingFallbackDecision{policy: clientDefault}, nil},
		{"client_default with the switch read as on",
			streamingFallbackDecision{policy: clientDefault, switchValue: "on"}, nil},
	} {
		for _, language := range []struct {
			locale locales.Locale
			base   []string
		}{
			{locales.EnglishUS, []string{"LANG=en_US.UTF-8"}},
			{locales.SimplifiedChinese, []string{"LANG=zh_CN.UTF-8"}},
		} {
			got := noticeLines(t, language.base, test.decision)
			var want []string
			for _, entry := range test.want {
				want = append(want, renderedNotice(t, language.locale, entry.key, entry.value))
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%s (%s): notice %q, want %q", test.name, language.locale, got, want)
			}
		}
	}

	// The lines say what they are about in both languages.
	english := noticeLines(t, nil, streamingFallbackDecision{policy: coreOwned})
	if len(english) != 1 || !strings.Contains(english[0], "non-streaming retry is off") ||
		!strings.Contains(english[0], "separately billed") {
		t.Fatalf("English fallback notice = %q", english)
	}
	chinese := noticeLines(t, []string{"LC_ALL=zh_CN.UTF-8"}, streamingFallbackDecision{policy: clientDefault, switchedOff: true, switchValue: "off"})
	if len(chinese) != 1 || !strings.Contains(chinese[0], clientadapter.RecognizedProtectionsVariable+"=off") ||
		!strings.Contains(chinese[0], "\u975e\u6d41\u5f0f\u91cd\u8bd5") {
		t.Fatalf("Chinese switched-off notice = %q", chinese)
	}
}

// What the launcher prints follows from the one fallback decision it makes
// for the grant. A recognized Claude Code on an Upstream-only target, and a
// verified release that leaves recovery to ViberMate on every target, as its
// exact rule has always set the variable, say so; nothing is printed for a
// client that keeps its own retry, and a switch value the launcher cannot
// read is reported for any signer grant, since only those read it.
func TestProtectionNoticeFollowsTheLaunchDecision(t *testing.T) {
	t.Parallel()

	anthropic := []string{"api.anthropic.com:443"}
	for _, test := range []struct {
		name  string
		grant capturecontrol.LaunchGrant
		base  []string
		want  []string
	}{
		{"verified core_owned, managed target",
			verifiedAdapterGrant(clientadapter.LaunchNodeEnvProxy, anthropic), nil,
			[]string{"cli.protections.streamingFallback"}},
		{"verified core_owned, unmanaged target",
			verifiedAdapterGrant(clientadapter.LaunchNodeEnvProxy, []string{}), nil,
			[]string{"cli.protections.streamingFallback"}},
		{"verified core_owned ignores the switch",
			verifiedAdapterGrant(clientadapter.LaunchNodeEnvProxy, anthropic),
			[]string{clientadapter.RecognizedProtectionsVariable + "=banana"},
			[]string{"cli.protections.streamingFallback"}},
		{"verified client_default",
			verifiedAdapterGrant(clientadapter.LaunchCodexResponsesHTTP, []string{}), nil, nil},
		{"generic", genericGrant(), []string{clientadapter.RecognizedProtectionsVariable + "=banana"}, nil},
		{"recognized codex",
			recognizedSignerGrant("codex-cli", clientadapter.InstallNPMWrapperNativeChild, clientadapter.LaunchCodexResponsesHTTP, []string{}),
			nil, nil},
		{"recognized codex with an unreadable switch",
			recognizedSignerGrant("codex-cli", clientadapter.InstallNPMWrapperNativeChild, clientadapter.LaunchCodexResponsesHTTP, []string{}),
			[]string{clientadapter.RecognizedProtectionsVariable + "=banana"},
			[]string{"cli.protections.switchUnrecognized"}},
		{"recognized claude, managed target",
			recognizedSignerGrant("claude-code", clientadapter.InstallNativeSingleBinary, clientadapter.LaunchNodeEnvProxy, anthropic),
			nil, []string{"cli.protections.streamingFallback"}},
		{"recognized claude, managed target, switch off",
			recognizedSignerGrant("claude-code", clientadapter.InstallNativeSingleBinary, clientadapter.LaunchNodeEnvProxy, anthropic),
			[]string{clientadapter.RecognizedProtectionsVariable + "=off"},
			[]string{"cli.protections.switchedOff"}},
		{"recognized claude, managed target, unreadable switch",
			recognizedSignerGrant("claude-code", clientadapter.InstallNativeSingleBinary, clientadapter.LaunchNodeEnvProxy, anthropic),
			[]string{clientadapter.RecognizedProtectionsVariable + "=banana"},
			[]string{"cli.protections.switchUnrecognized", "cli.protections.streamingFallback"}},
		{"recognized claude, unmanaged target",
			recognizedSignerGrant("claude-code", clientadapter.InstallNativeSingleBinary, clientadapter.LaunchNodeEnvProxy, []string{}),
			nil, nil},
		{"recognized claude, unmanaged target, switch off",
			recognizedSignerGrant("claude-code", clientadapter.InstallNativeSingleBinary, clientadapter.LaunchNodeEnvProxy, []string{}),
			[]string{clientadapter.RecognizedProtectionsVariable + "=off"}, nil},
	} {
		base := append([]string{"PATH=/usr/bin:/bin"}, test.base...)
		if err := validateGrant(test.grant); err != nil {
			t.Fatalf("%s: the fixture grant is invalid: %v", test.name, err)
		}
		decision := decideStreamingFallback(base, test.grant, usesManagedClientCredential(base, test.grant))
		got := noticeLines(t, base, decision)
		var want []string
		for _, key := range test.want {
			_, value, _ := clientadapter.ProtectionSwitchFromEnvironment(base)
			want = append(want, renderedNotice(t, locales.EnglishUS, key, value))
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s: notice %q, want %q (decision %+v)", test.name, got, want, decision)
		}
	}
}
