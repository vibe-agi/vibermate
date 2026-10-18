package desktophost

import (
	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

func Start(options any) (any, error) {
	startWithConstructors()
	return productruntime.Start(options)
}

func startWithConstructors() {
	_, _ = controlprincipal.NewAuthority()
	_, _ = capturegrant.New()
	_, _ = capturecontrol.NewManualHandler()
	_, _ = capturecontrol.New()
}
