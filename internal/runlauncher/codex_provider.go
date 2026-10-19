package runlauncher

import (
	"slices"
	"strconv"
	"strings"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
)

const codexProviderTokenEnvironment = "VIBERMATE_CODEX_PROVIDER_TOKEN"

// prepareCodexProvider consumes a validated grant and returns a child-only
// configuration prefix and environment. The launcher adds the prefix after its
// normal transport/isolation preparation, preserving the original argv suffix.
func prepareCodexProvider(grant capturecontrol.LaunchGrant, environmentID environment.EnvironmentID, original, baseEnvironment, childEnvironment []string) ([]string, []string, error) {
	if environmentID == "" {
		return nil, nil, ErrManagedAuthEnvironmentRequired
	}
	if grant.LaunchRecipe != clientadapter.LaunchCodexResponsesHTTP {
		return nil, nil, ErrManagedAuthClientRequired
	}
	if environmentID == environment.SystemTransparentID {
		return nil, nil, ErrManagedAuthDestinationRequired
	}
	baseURL := ""
	for _, target := range []struct{ authority, base string }{
		{"chatgpt.com:443", "https://chatgpt.com/backend-api/codex"},
		{"api.openai.com:443", "https://api.openai.com/v1"},
	} {
		if slices.Contains(grant.ProtectedAuthorities, target.authority) && slices.Contains(grant.ManagedCredentialAuthorities, target.authority) {
			baseURL = target.base
			break
		}
	}
	if baseURL == "" {
		return nil, nil, ErrManagedAuthDestinationRequired
	}
	// Check both supported inputs, including duplicates, so a lower-priority base
	// cannot silently contradict the selected managed destination.
	for _, entry := range append(slices.Clone(baseEnvironment), childEnvironment...) {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (key == "CODEX_BASE_URL" || key == "OPENAI_BASE_URL") && value != "" {
			if !codexProviderBaseMatches(value, baseURL) {
				return nil, nil, ErrManagedAuthConflict
			}
		}
	}
	_, _, err := walkCodexInvocation(original, func(name, value string) error {
		switch name {
		case "--oss", "--local-provider":
			return ErrManagedAuthConflict
		case "--config":
			key, setting, ok := strings.Cut(value, "=")
			if !ok {
				return ErrManagedAuthConflict
			}
			// Conservatively reserve whole tables as well as dotted fields. Quoted
			// components and whitespace must not provide an alternate reserved spelling.
			parts := strings.Split(strings.TrimSpace(key), ".")
			for i := range parts {
				parts[i] = strings.Trim(strings.TrimSpace(parts[i]), "\"'")
			}
			switch parts[0] {
			case "model_provider":
				return ErrManagedAuthConflict
			case "model_providers":
				if len(parts) == 1 || parts[1] == "vibermate" {
					return ErrManagedAuthConflict
				}
			case "openai_base_url":
				setting = strings.TrimSpace(setting)
				if unquoted, err := strconv.Unquote(setting); err == nil {
					setting = unquoted
				} else if len(setting) >= 2 && setting[0] == '\'' && setting[len(setting)-1] == '\'' {
					setting = setting[1 : len(setting)-1]
				}
				if !codexProviderBaseMatches(setting, baseURL) {
					return ErrManagedAuthConflict
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	settings := []string{
		`model_provider="vibermate"`,
		`model_providers.vibermate.name="ViberMate"`,
		"model_providers.vibermate.base_url=" + strconv.Quote(baseURL),
		`model_providers.vibermate.wire_api="responses"`,
		`model_providers.vibermate.requires_openai_auth=false`,
		`model_providers.vibermate.env_key="VIBERMATE_CODEX_PROVIDER_TOKEN"`,
		`model_providers.vibermate.supports_websockets=false`,
	}
	arguments := make([]string, 0, len(settings)*2)
	for _, setting := range settings {
		arguments = append(arguments, "--config", setting)
	}
	environment := make([]string, 0, len(childEnvironment)+1)
	for _, entry := range childEnvironment {
		key, _, _ := strings.Cut(entry, "=")
		if key != codexProviderTokenEnvironment {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, codexProviderTokenEnvironment+"="+clientCredentialPlaceholder)
	return arguments, environment, nil
}

func codexProviderBaseMatches(raw, base string) bool {
	normalized, err := codexOrigin([]string{"CODEX_BASE_URL=" + raw})
	return err == nil && normalized == base
}
