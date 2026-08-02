package sneakyhost

import vmruntime "github.com/vibe-agi/vibermate/internal/productruntime"

// An alias hides nothing: resolution is by import path.
func Start() { _, _ = vmruntime.Start(nil) }

// Taking it as a value reaches the same function without a call site.
var reference = vmruntime.Start
