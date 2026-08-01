package runlauncher

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

// A client this build knows, at a version it has no evidence for, is launched
// without a trust root on purpose. It will then fail its handshake with a
// transport error that says nothing about vibermate. The person watching the
// terminal is told why, before that happens.
func TestAnUnverifiedClientIsToldWhyItWillFail(t *testing.T) {
	t.Parallel()

	var stderr strings.Builder
	launcher := &Launcher{config: Config{Stderr: &stderr}}
	launcher.warnUnverified(capturecontrol.LaunchGrant{
		Run:         capturerun.View{ExecutableLabel: "codex"},
		Recognition: clientadapter.RecognitionUnverified,
	})
	warning := stderr.String()
	if !strings.Contains(warning, "codex") {
		t.Fatalf("the warning does not name the program: %q", warning)
	}
	if !strings.Contains(warning, "trust root") {
		t.Fatalf("the warning does not say what is missing: %q", warning)
	}
}

// A recognized client and an uncatalogued program are both launched without
// surprise, and neither is warned about: one is fine, and the other never had
// a reason to expect a trust root.
func TestOnlyAnUnverifiedClientIsWarnedAbout(t *testing.T) {
	t.Parallel()

	for _, recognition := range []clientadapter.Recognition{
		clientadapter.RecognitionVerified,
		clientadapter.RecognitionUnknown,
	} {
		var stderr strings.Builder
		launcher := &Launcher{config: Config{Stderr: &stderr}}
		launcher.warnUnverified(capturecontrol.LaunchGrant{
			Run:         capturerun.View{ExecutableLabel: "agent"},
			Recognition: recognition,
		})
		if stderr.Len() != 0 {
			t.Fatalf("%q was warned about: %q", recognition, stderr.String())
		}
	}
}
