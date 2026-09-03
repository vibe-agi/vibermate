package environment

import (
	"sort"
	"strings"
	"unicode/utf8"
)

var reservedLaunchEnvironment = map[string]struct{}{
	"ALL_PROXY":                                 {},
	"ANTHROPIC_API_KEY":                         {},
	"ANTHROPIC_AUTH_TOKEN":                      {},
	"ANTHROPIC_BASE_URL":                        {},
	"ANTHROPIC_BEDROCK_BASE_URL":                {},
	"ANTHROPIC_CUSTOM_HEADERS":                  {},
	"ANTHROPIC_FOUNDRY_BASE_URL":                {},
	"ANTHROPIC_VERTEX_BASE_URL":                 {},
	"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK": {},
	"CLAUDE_CODE_OAUTH_TOKEN":                   {},
	"CLAUDE_CODE_USE_BEDROCK":                   {},
	"CLAUDE_CODE_USE_FOUNDRY":                   {},
	"CLAUDE_CODE_USE_VERTEX":                    {},
	"CODEX_API_KEY":                             {},
	"CODEX_BASE_URL":                            {},
	"CURL_CA_BUNDLE":                            {},
	"HTTP_PROXY":                                {},
	"HTTPS_PROXY":                               {},
	"NODE_EXTRA_CA_CERTS":                       {},
	"NODE_USE_ENV_PROXY":                        {},
	"NO_PROXY":                                  {},
	"OPENAI_API_KEY":                            {},
	"OPENAI_BASE_URL":                           {},
	"OPENAI_ORGANIZATION":                       {},
	"OPENAI_ORG_ID":                             {},
	"OPENAI_PROJECT":                            {},
	"OPENAI_PROJECT_ID":                         {},
	"REQUESTS_CA_BUNDLE":                        {},
	"SSL_CERT_FILE":                             {},
}

func (policy LaunchEnvironmentPolicy) Clone() LaunchEnvironmentPolicy {
	cloned := LaunchEnvironmentPolicy{
		DeleteEnv: append([]string(nil), policy.DeleteEnv...),
	}
	if policy.SetEnv != nil {
		cloned.SetEnv = make(map[string]string, len(policy.SetEnv))
		for name, value := range policy.SetEnv {
			cloned.SetEnv[name] = value
		}
	}
	return cloned
}

func (policy LaunchEnvironmentPolicy) Validate() error {
	_, err := normalizeLaunchEnvironment(policy)
	return err
}

func normalizeLaunchEnvironment(
	policy LaunchEnvironmentPolicy,
) (LaunchEnvironmentPolicy, error) {
	if len(policy.SetEnv)+len(policy.DeleteEnv) > MaxLaunchEnvironmentRules {
		return LaunchEnvironmentPolicy{}, ErrInvalidEnvironment
	}
	result := policy.Clone()
	seen := make(map[string]struct{}, len(policy.SetEnv)+len(policy.DeleteEnv))
	totalBytes := 0
	for name, value := range result.SetEnv {
		if !validLaunchEnvironmentName(name) ||
			reservedLaunchEnvironmentName(name) ||
			len(value) > MaxEnvironmentValueBytes ||
			!utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
			return LaunchEnvironmentPolicy{}, ErrInvalidEnvironment
		}
		seen[name] = struct{}{}
		totalBytes += len(name) + len(value)
	}
	for _, name := range result.DeleteEnv {
		if !validLaunchEnvironmentName(name) || reservedLaunchEnvironmentName(name) {
			return LaunchEnvironmentPolicy{}, ErrInvalidEnvironment
		}
		if _, duplicate := seen[name]; duplicate {
			return LaunchEnvironmentPolicy{}, ErrInvalidEnvironment
		}
		seen[name] = struct{}{}
		totalBytes += len(name)
	}
	if totalBytes > MaxLaunchEnvironmentBytes {
		return LaunchEnvironmentPolicy{}, ErrInvalidEnvironment
	}
	sort.Strings(result.DeleteEnv)
	if len(result.SetEnv) == 0 {
		result.SetEnv = nil
	}
	if len(result.DeleteEnv) == 0 {
		result.DeleteEnv = nil
	}
	return result, nil
}

func validLaunchEnvironmentName(name string) bool {
	if name == "" || len(name) > MaxIDBytes {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if (character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') || character == '_' ||
			(index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

func reservedLaunchEnvironmentName(name string) bool {
	normalized := strings.ToUpper(name)
	if strings.HasPrefix(normalized, "VIBERMATE_") {
		return true
	}
	_, reserved := reservedLaunchEnvironment[normalized]
	return reserved
}
