package exchange

import (
	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

type Runtime struct {
	writer access.Writer
	store  *runtimepersistence.Store
}
