package runlauncher

import (
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

func TestManagedAuthClaudeRequiresPreparedGatewayToken(t *testing.T) {
	grant := capturecontrol.LaunchGrant{LaunchRecipe: clientadapter.LaunchNodeEnvProxy, Adapter: &capturecontrol.ClientLaunchAdapterView{ClientAdapterView: capturecontrol.ClientAdapterView{ID: "claude-code"}}, ProtectedAuthorities: []string{"api.anthropic.com:443"}, ManagedCredentialAuthorities: []string{"api.anthropic.com:443"}}
	for _, child := range [][]string{nil, {"ANTHROPIC_AUTH_TOKEN=synthetic-native-token"}} {
		_, _, err := prepareManagedAuth(grant, "work", []string{"prompt"}, nil, child)
		if !errors.Is(err, ErrManagedAuthConflict) {
			t.Fatalf("native/missing gateway token accepted: %v", err)
		}
	}
}
