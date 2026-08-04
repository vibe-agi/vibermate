package loopbackproxy

import (
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

type Handler struct {
	Runs    capturerun.ProxyAuthorizer
	Manuals manualcapture.ProxyAuthorizer
}
