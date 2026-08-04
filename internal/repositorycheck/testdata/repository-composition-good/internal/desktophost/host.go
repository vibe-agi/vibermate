package desktophost

import (
	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

func Start(options any) (any, error) {
	_, _ = controlprincipal.NewAuthority()
	_, _ = capturegrant.New()
	_, _ = capturecontrol.New()
	return productruntime.Start(options)
}
