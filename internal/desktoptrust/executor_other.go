//go:build !darwin

package desktoptrust

import (
	"github.com/vibe-agi/vibermate/internal/systemtrust"
)

func NewProductionCommandExecutor() (systemtrust.CommandExecutor, error) {
	return nil, systemtrust.ErrUnsupportedPlatform
}
