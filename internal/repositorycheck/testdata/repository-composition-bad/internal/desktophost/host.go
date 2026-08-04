package desktophost

import (
	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

// Start no longer starts the runtime; a decoy does.
func Start(options any) (any, error) { return nil, nil }

func unused() {
	_, _ = productruntime.Start(nil)
	_, _ = controlprincipal.NewAuthority()
	_, _ = capturegrant.New()
	_, _ = capturecontrol.NewManualHandler()
	_, _ = capturecontrol.New()
}
