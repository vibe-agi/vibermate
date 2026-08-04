package sneakyhost

import (
	vmcapturecontrol "github.com/vibe-agi/vibermate/internal/capturecontrol"
	vmcapturegrant "github.com/vibe-agi/vibermate/internal/capturegrant"
	vmprincipal "github.com/vibe-agi/vibermate/internal/controlprincipal"
	vmruntime "github.com/vibe-agi/vibermate/internal/productruntime"
)

// An alias hides nothing: resolution is by import path.
func Start() {
	_, _ = vmruntime.Start(nil)
	_, _ = vmprincipal.NewAuthority()
	_, _ = vmcapturegrant.New()
	_, _ = vmcapturecontrol.New()
}

// Taking it as a value reaches the same function without a call site.
var reference = vmruntime.Start
