// Package clienttarget owns the non-secret network target selected by a
// supported Agent client before a Capture starts. It translates only explicit
// client configuration; it never identifies an upstream implementation from
// a host name, model ID, or response.
package clienttarget

import (
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/originidentity"
)

const maxEnvironmentValueBytes = 2048

var ErrInvalidTarget = errors.New("client target is invalid")

const (
	claudeClientID = "claude-code"
	codexClientID  = "codex-cli"

	defaultAnthropicOrigin = "https://api.anthropic.com"
	defaultOpenAIOrigin    = "https://api.openai.com/v1"
	defaultChatGPTOrigin   = "https://chatgpt.com/backend-api/codex"
)

// EnvironmentFacts is the complete allowlist of ambient values that can
// affect where a supported client sends model traffic. Credential values are
// intentionally absent; only the presence bits needed for Codex's documented
// default-origin choice cross the control seam.
type EnvironmentFacts struct {
	AnthropicBaseURL    string
	CodexBaseURL        string
	OpenAIBaseURL       string
	CodexAPIKeyPresent  bool
	OpenAIAPIKeyPresent bool
}

func (facts EnvironmentFacts) Validate() error {
	for _, value := range []string{
		facts.AnthropicBaseURL,
		facts.CodexBaseURL,
		facts.OpenAIBaseURL,
	} {
		if len(value) > maxEnvironmentValueBytes || !utf8.ValidString(value) ||
			strings.TrimSpace(value) != value || strings.ContainsRune(value, '\x00') {
			return ErrInvalidTarget
		}
	}
	return nil
}

// FromEnvironment keeps only target-selection facts. In particular, it never
// copies an API key, token, custom Header value, or unrelated environment
// variable into the control request.
func FromEnvironment(environment []string) EnvironmentFacts {
	values := make(map[string]string, 5)
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch key {
		case "ANTHROPIC_BASE_URL", "CODEX_BASE_URL", "OPENAI_BASE_URL",
			"CODEX_API_KEY", "OPENAI_API_KEY":
			values[key] = value
		}
	}
	return EnvironmentFacts{
		AnthropicBaseURL:    values["ANTHROPIC_BASE_URL"],
		CodexBaseURL:        values["CODEX_BASE_URL"],
		OpenAIBaseURL:       values["OPENAI_BASE_URL"],
		CodexAPIKeyPresent:  values["CODEX_API_KEY"] != "",
		OpenAIAPIKeyPresent: values["OPENAI_API_KEY"] != "",
	}
}

// Profile binds environment facts to the client identity established by the
// server-side adapter verifier. A launcher cannot turn an unknown executable
// into a protocol-aware Capture by choosing a Base URL.
type Profile struct {
	clientID string
	facts    EnvironmentFacts
}

func NewProfile(clientID string, facts EnvironmentFacts) (Profile, error) {
	if err := facts.Validate(); err != nil {
		return Profile{}, err
	}
	switch clientID {
	case claudeClientID, codexClientID:
		return Profile{clientID: clientID, facts: facts}, nil
	case "":
		return Profile{}, nil
	default:
		// A catalog may contain other clients. Until this module explicitly owns
		// their configuration contract they remain ordinary proxy traffic.
		return Profile{}, nil
	}
}

func (profile Profile) Empty() bool { return profile.clientID == "" }

// Resolve applies the frozen Environment launch overlay and returns the one
// actual origin the verified client will use plus the canonical Client Flow
// whose protocol contract parses it.
func (profile Profile) Resolve(
	set map[string]string,
	deleteKeys []string,
) (Target, bool, error) {
	if profile.Empty() {
		return Target{}, false, nil
	}
	facts := profile.facts.apply(set, deleteKeys)
	if err := facts.Validate(); err != nil {
		return Target{}, false, err
	}
	actualRaw := ""
	canonicalRaw := ""
	switch profile.clientID {
	case claudeClientID:
		actualRaw = facts.AnthropicBaseURL
		if actualRaw == "" {
			actualRaw = defaultAnthropicOrigin
		}
		canonicalRaw = defaultAnthropicOrigin
	case codexClientID:
		actualRaw = facts.CodexBaseURL
		if actualRaw == "" {
			actualRaw = facts.OpenAIBaseURL
		}
		if actualRaw == "" {
			if facts.CodexAPIKeyPresent || facts.OpenAIAPIKeyPresent {
				actualRaw = defaultOpenAIOrigin
			} else {
				actualRaw = defaultChatGPTOrigin
			}
		}
		canonicalRaw = "https://api.openai.com"
		normalized, err := parseActual(actualRaw)
		if err != nil {
			return Target{}, false, err
		}
		chatGPT, _ := originidentity.ParseProviderOrigin(defaultChatGPTOrigin)
		if normalized == chatGPT {
			canonicalRaw = "https://chatgpt.com"
		}
		canonical, err := originidentity.ParseClientOrigin(canonicalRaw)
		if err != nil {
			return Target{}, false, ErrInvalidTarget
		}
		target, err := New(normalized, canonical)
		return target, err == nil, err
	default:
		return Target{}, false, nil
	}
	actual, err := parseActual(actualRaw)
	if err != nil {
		return Target{}, false, err
	}
	canonical, err := originidentity.ParseClientOrigin(canonicalRaw)
	if err != nil {
		return Target{}, false, ErrInvalidTarget
	}
	target, err := New(actual, canonical)
	return target, err == nil, err
}

func (facts EnvironmentFacts) apply(
	set map[string]string,
	deleteKeys []string,
) EnvironmentFacts {
	for _, key := range deleteKeys {
		switch key {
		case "ANTHROPIC_BASE_URL":
			facts.AnthropicBaseURL = ""
		case "CODEX_BASE_URL":
			facts.CodexBaseURL = ""
		case "OPENAI_BASE_URL":
			facts.OpenAIBaseURL = ""
		case "CODEX_API_KEY":
			facts.CodexAPIKeyPresent = false
		case "OPENAI_API_KEY":
			facts.OpenAIAPIKeyPresent = false
		}
	}
	for key, value := range set {
		switch key {
		case "ANTHROPIC_BASE_URL":
			facts.AnthropicBaseURL = value
		case "CODEX_BASE_URL":
			facts.CodexBaseURL = value
		case "OPENAI_BASE_URL":
			facts.OpenAIBaseURL = value
		case "CODEX_API_KEY":
			facts.CodexAPIKeyPresent = value != ""
		case "OPENAI_API_KEY":
			facts.OpenAIAPIKeyPresent = value != ""
		}
	}
	return facts
}

// Target is immutable and contains no credential. Actual is the client-owned
// Original Destination; Canonical selects the Environment Client Flow whose
// protocol contract applies to the request.
type Target struct {
	actual    originidentity.ProviderOrigin
	canonical originidentity.ClientOrigin
}

func New(
	actual originidentity.ProviderOrigin,
	canonical originidentity.ClientOrigin,
) (Target, error) {
	if actual.Validate() != nil || canonical.Validate() != nil {
		return Target{}, ErrInvalidTarget
	}
	return Target{actual: actual, canonical: canonical}, nil
}

func Restore(actualRaw, canonicalRaw string) (Target, error) {
	actual, err := originidentity.ParseProviderOrigin(actualRaw)
	if err != nil {
		return Target{}, ErrInvalidTarget
	}
	canonical, err := originidentity.ParseClientOrigin(canonicalRaw)
	if err != nil {
		return Target{}, ErrInvalidTarget
	}
	return New(actual, canonical)
}

func (target Target) Available() bool {
	return target.actual.String() != "" || target.canonical.String() != ""
}

func (target Target) Validate() error {
	if !target.Available() || target.actual.Validate() != nil ||
		target.canonical.Validate() != nil {
		return ErrInvalidTarget
	}
	return nil
}

func (target Target) ActualOrigin() originidentity.ProviderOrigin  { return target.actual }
func (target Target) CanonicalOrigin() originidentity.ClientOrigin { return target.canonical }

func (target Target) MatchesTransport(origin originidentity.ProviderOrigin) bool {
	return target.Validate() == nil && origin.Validate() == nil &&
		target.actual.Scheme() == origin.Scheme() &&
		target.actual.Host() == origin.Host() &&
		target.actual.Port() == origin.Port()
}

func (target Target) ContainsPath(value string) bool {
	if target.Validate() != nil || value == "" || value[0] != '/' {
		return false
	}
	base := target.actual.BasePath()
	return base == "" || value == base || strings.HasPrefix(value, base+"/")
}

func parseActual(raw string) (originidentity.ProviderOrigin, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" {
		return originidentity.ProviderOrigin{}, ErrInvalidTarget
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if parsed.RawPath != "" {
		parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
	}
	actual, err := originidentity.ParseProviderOrigin(parsed.String())
	if err != nil {
		return originidentity.ProviderOrigin{}, ErrInvalidTarget
	}
	return actual, nil
}
