package exchangecontent

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// Keep an independent copy of the legacy ordered replacements: an incorrect
// prefilter would expose credentials or change canonical text and hashes.
func TestSanitizeTextPrefilterPreservesOrderedRedaction(t *testing.T) {
	legacy := func(value string) string {
		if !utf8.ValidString(value) {
			return "[invalid text omitted]"
		}
		for _, r := range []struct {
			p *regexp.Regexp
			s string
		}{
			{unixHomePattern, "${1}~"}, {windowsHomePattern, "${1}~"}, {headerSecretPattern, "${1}: [redacted]"}, {bearerPattern, "Bearer [redacted]"}, {providerSecretPattern, "[redacted credential]"}, {urlUserInfoPattern, "${1}[redacted]@"},
		} {
			if r.p.MatchString(value) {
				value = r.p.ReplaceAllString(value, r.s)
			}
		}
		return value
	}
	for _, value := range []string{"", strings.Repeat("x", 980), "/Users/null/code /home/alice/project", `C:\Users\Alice\code`, "Bearer abcdefgh1234", "bEaReR\tabcdefgh1234", "Authorization: private Cookie=abc", "sk-ant-abcdefgh sk-abcdefghijklmnop", "https://alice:secret@example.test", "HTTP://a:b@example.test", "user:info = harmless", "\xff", "Kookie: harmless", "Bearer", "sk", "https://", "/Users", "Users"} {
		if got, want := sanitizeText(value), legacy(value); got != want {
			t.Fatalf("redaction differs: %q => %q, want %q", value, got, want)
		}
	}
}
