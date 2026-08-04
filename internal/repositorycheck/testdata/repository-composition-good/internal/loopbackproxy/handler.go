package loopbackproxy

import "github.com/vibe-agi/vibermate/internal/captureadmission"

type Handler struct {
	Admissions captureadmission.Authorizer
}
