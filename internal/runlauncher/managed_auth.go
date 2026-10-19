package runlauncher

import (
	"fmt"
	"slices"
	"strings"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/locales"
)

// ManagedAuthError contains a fixed diagnostic reason, never child arguments,
// account details, environment values, or a proxy capability.
type ManagedAuthError struct{ Reason string }

func (failure *ManagedAuthError) Error() string {
	return "managed authentication launch rejected: " + failure.Reason
}

var (
	ErrManagedAuthEnvironmentRequired = &ManagedAuthError{Reason: "environmentRequired"}
	ErrManagedAuthClientRequired      = &ManagedAuthError{Reason: "clientRequired"}
	ErrManagedAuthDestinationRequired = &ManagedAuthError{Reason: "managedDestinationRequired"}
	ErrManagedAuthConflict            = &ManagedAuthError{Reason: "conflict"}
)

// prepareManagedAuth asserts the explicit managed-auth contract against a
// validated grant. The established identity and recipe must agree; neither a
// command basename nor a recipe by itself grants this policy to another client.
func prepareManagedAuth(grant capturecontrol.LaunchGrant, environmentID environment.EnvironmentID, original, baseEnvironment, childEnvironment []string) ([]string, []string, error) {
	if environmentID == "" {
		return nil, nil, ErrManagedAuthEnvironmentRequired
	}
	if environmentID == environment.SystemTransparentID {
		return nil, nil, ErrManagedAuthDestinationRequired
	}
	clientID := ""
	if grant.Adapter != nil {
		clientID = grant.Adapter.ID
	} else if grant.Signer != nil {
		clientID = grant.Signer.ID
	}
	switch {
	case clientID == "codex-cli" && grant.LaunchRecipe == clientadapter.LaunchCodexResponsesHTTP:
		return prepareCodexProvider(grant, environmentID, original, baseEnvironment, childEnvironment)
	case clientID == "claude-code" && grant.LaunchRecipe == clientadapter.LaunchNodeEnvProxy:
		// The existing target/credential helpers read the first env entry, whereas
		// child preparation resolves duplicates to the last. Reject ambiguity only
		// for this explicit mode, preserving ordinary launch behavior.
		var base string
		seen := false
		for _, entry := range baseEnvironment {
			key, value, ok := strings.Cut(entry, "=")
			if !ok || key != "ANTHROPIC_BASE_URL" {
				continue
			}
			if seen && value != base {
				return nil, nil, ErrManagedAuthConflict
			}
			base = value
			seen = true
		}
		authority, ok := clientTargetAuthority(childEnvironment, clientID)
		if !ok || !slices.Contains(grant.ProtectedAuthorities, authority) || !slices.Contains(grant.ManagedCredentialAuthorities, authority) || !usesManagedClientCredential(baseEnvironment, grant) {
			return nil, nil, ErrManagedAuthDestinationRequired
		}
		if environmentValue(childEnvironment, "ANTHROPIC_AUTH_TOKEN") != clientCredentialPlaceholder {
			return nil, nil, ErrManagedAuthConflict
		}
		// buildEnvironment already owns Claude gateway-token selection and ambient
		// credential stripping. This opt-in adds validation, not a second auth path.
		return nil, childEnvironment, nil
	default:
		return nil, nil, ErrManagedAuthClientRequired
	}
}

func (launcher *Launcher) announceManagedAuth() {
	catalogs, err := locales.New()
	if err != nil {
		return
	}
	message, err := catalogs.Render(locales.Detect(launcher.config.BaseEnvironment), "cli.managedAuth.active", nil)
	if err == nil {
		_, _ = fmt.Fprintln(launcher.config.Stderr, message)
	}
}
