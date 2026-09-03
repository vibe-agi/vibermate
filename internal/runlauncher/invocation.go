package runlauncher

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

const (
	codexBuiltInProvider = "openai"
	codexChatGPTBaseURL  = "https://chatgpt.com/backend-api/codex"
	codexAPIBaseURL      = "https://api.openai.com/v1"
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
	settings := []string{
		// Persist Codex's built-in provider identity in new rollouts. A private
		// launch-only provider made those sessions impossible to resume outside
		// ViberMate because session_meta outlived its temporary definition.
		"model_provider=" + strconv.Quote(codexBuiltInProvider),
		"openai_base_url=" + strconv.Quote(baseURL),
		// The semantic proxy owns Responses HTTP. Codex 0.145 exposed its
		// WebSocket transport behind this feature; selecting the built-in provider
		// must not silently re-enable a wire shape the launch recipe cannot decode.
		"features.responses_websockets=false",
		// Retry/attempt ownership belongs to the frozen Environment route. If
		// Codex retries internally, those attempts cannot be selected, approved,
		// or explained independently by ViberMate.
		"request_max_retries=0",
		"stream_max_retries=0",
	}
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

// codexOrigin resolves where a Codex child will send traffic.
//
// It is the single answer for both the launch recipe and the managed-credential
// decision. Deriving it twice with different fallbacks pointed the child at
// chatgpt.com while the credential decision was made for api.openai.com — the
// ChatGPT-login case, and the common one.
func codexOrigin(environment []string) (string, error) {
	configured := environmentValue(environment, "CODEX_BASE_URL")
	if configured == "" {
		configured = environmentValue(environment, "OPENAI_BASE_URL")
	}
	if configured == "" {
		if environmentValue(environment, "CODEX_API_KEY") != "" ||
			environmentValue(environment, "OPENAI_API_KEY") != "" {
			return codexAPIBaseURL, nil
		}
		return codexChatGPTBaseURL, nil
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
