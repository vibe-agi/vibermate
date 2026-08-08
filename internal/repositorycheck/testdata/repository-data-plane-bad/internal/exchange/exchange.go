package exchange

import (
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

type Runtime struct {
	manager *environment.Manager
	store   *runtimepersistence.Store
}
