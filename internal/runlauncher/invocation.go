package runlauncher

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

// buildChildArguments is the launch-invocation seam. CaptureRun creation keeps
// the command the person entered as launch evidence; this function derives the
// actual child arguments required by a verified client recipe without leaking
// recipe details into the CLI or control-plane callers.
func buildChildArguments(
	command []string,
	baseEnvironment []string,
	recipe clientadapter.LaunchRecipe,
) ([]string, error) {
	if len(command) == 0 || command[0] == "" || !recipe.Valid() {
		return nil, errors.New("CaptureRun child invocation is invalid")
	}
	arguments := append([]string(nil), command[1:]...)
	if recipe != clientadapter.LaunchCodexResponsesHTTP {
		return arguments, nil
	}
	baseURL, err := codexOrigin(baseEnvironment)
	if err != nil {
		return nil, err
	}
	// Preserve native provider and stored-login selection. Only explicit base
	// inputs are translated; transport compatibility does not select auth.
	var settings []string
	if baseURL != "" {
		settings = append(settings, "openai_base_url="+strconv.Quote(baseURL))
	}
	// Older clients honor this switch. Current built-in providers still
	// negotiate WebSockets; the proxy returns 426 and Codex falls back to HTTP.
	settings = append(settings, "features.responses_websockets=false")
	// Preserve the client's retry policy. Each new HTTP request is a new
	// Exchange; the Runtime's commit ledger still bounds its own resends.
	result := make([]string, 0, len(settings)*2+2+len(arguments))
	for _, setting := range settings {
		result = append(result, "--config", setting)
	}
	// The semantic Responses path decodes the client request before routing it.
	// Codex request compression is optional, so keep the verified launch recipe
	// on the uncompressed wire shape that the protocol decoder owns. Raw HTTP
	// evidence still retains the exact bytes that Codex sends.
	result = append(result, "--disable", "enable_request_compression")
	return append(result, arguments...), nil
}

// environmentValue reads one variable from a prepared child environment.
//
// The match is exact. POSIX environment variables are case-sensitive and each
// client reads one exact spelling, so accepting another would let a variable the
// client ignores decide the configuration ViberMate writes for that client to
// obey. A Windows launcher, whose environment is case-insensitive, will need its
// own lookup rather than a relaxation of this one.
func environmentValue(environment []string, name string) string {
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == name {
			return value
		}
	}
	return ""
}

// codexOrigin returns only an explicit supported base URL. Native defaults
// depend on Codex-owned persisted login and provider configuration.
func codexOrigin(environment []string) (string, error) {
	configured := environmentValue(environment, "CODEX_BASE_URL")
	if configured == "" {
		configured = environmentValue(environment, "OPENAI_BASE_URL")
	}
	if configured == "" {
		return "", nil
	}
	parsed, err := url.Parse(configured)
	if err != nil ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") ||
		parsed.User != nil || parsed.Hostname() == "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Codex base URL is invalid")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if parsed.RawPath != "" {
		parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
	}
	return parsed.String(), nil
}
