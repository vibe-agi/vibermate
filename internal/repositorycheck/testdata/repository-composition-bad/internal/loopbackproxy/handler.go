package loopbackproxy

import "github.com/vibe-agi/vibermate/internal/capturerun"

type Handler struct {
	Runs capturerun.ProxyAuthorizer
}
