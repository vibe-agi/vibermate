package capturecontrol

import (
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

func New() (any, error) { return nil, nil }

var bypass = capturerun.CreateCommand{}
var manualBypass = manualcapture.CreateCommand{}
