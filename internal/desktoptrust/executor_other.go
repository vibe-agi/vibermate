//go:build !darwin

package desktoptrust

import (
	"context"

	"github.com/vibe-agi/vibermate/internal/systemtrust"
)

func NewProductionCommandExecutor() (systemtrust.CommandExecutor, error) {
	return nil, systemtrust.ErrUnsupportedPlatform
}

type unsupportedCommandExecutor struct{}

func (unsupportedCommandExecutor) Execute(
	context.Context,
	systemtrust.CommandSpec,
) (systemtrust.CommandResult, error) {
	return systemtrust.CommandResult{}, systemtrust.ErrUnsupportedPlatform
}
